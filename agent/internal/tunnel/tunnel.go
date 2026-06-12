package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jtb75/silkstrand/agent/internal/nettls"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	heartbeatEvery = 30 * time.Second
	maxMessageSize = 1024 * 1024 // 1MB

	sendChSize = 64

	backoffInitial = 1 * time.Second
	backoffMax     = 60 * time.Second
)

// Tunnel manages the WebSocket connection to the SilkStrand API server.
type Tunnel struct {
	apiURL   string
	agentID  string
	agentKey string

	// OnDirective is called when a scan directive is received from the server.
	OnDirective func(DirectivePayload)
	// OnUpgrade is called when the server asks the agent to upgrade its binary.
	OnUpgrade func(UpgradePayload)
	// OnProbe is called when the server asks for a target connectivity test.
	OnProbe func(ProbePayload)
	// OnCredentialTest is called when the server asks to test a credential
	// source using the agent's local network (e.g. on-prem Vault).
	OnCredentialTest func(CredentialTestPayload)

	// InContainer is reported in every heartbeat so the server/UI can offer
	// image-recreate instead of in-place upgrade (ADR 013 follow-up). Set once
	// at startup before the tunnel runs.
	InContainer bool

	conn   *websocket.Conn
	sendCh chan Message
	mu     sync.Mutex
}

// New creates a new Tunnel.
func New(apiURL, agentID, agentKey string) *Tunnel {
	return &Tunnel{
		apiURL:   apiURL,
		agentID:  agentID,
		agentKey: agentKey,
		sendCh:   make(chan Message, sendChSize),
	}
}

// Send enqueues a message for sending to the server. Non-blocking; drops the
// message and logs a warning if the send buffer is full.
func (t *Tunnel) Send(msg Message) {
	select {
	case t.sendCh <- msg:
	default:
		slog.Warn("send buffer full, dropping message", "type", msg.Type)
	}
}

// Run connects to the API server and processes messages. It reconnects with
// exponential backoff on any connection error. It blocks until ctx is cancelled.
func (t *Tunnel) Run(ctx context.Context, version string) {
	startedAt := time.Now()
	backoff := backoffInitial

	for {
		if ctx.Err() != nil {
			return
		}

		err := t.connect(ctx, version, startedAt)
		if ctx.Err() != nil {
			return
		}

		if err != nil {
			slog.Warn("connection lost", "error", err, "reconnect_in", backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, backoffMax)
	}
}

func (t *Tunnel) connect(ctx context.Context, version string, startedAt time.Time) error {
	url := fmt.Sprintf("%s/ws/agent?agent_id=%s", t.apiURL, t.agentID)

	header := http.Header{}
	header.Set("Authorization", "Bearer "+t.agentKey)

	slog.Info("connecting to server", "url", url)

	conn, _, err := nettls.WSDialer().DialContext(ctx, url, header)
	if err != nil {
		return fmt.Errorf("dialing server: %w", err)
	}

	t.mu.Lock()
	t.conn = conn
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.conn = nil
		t.mu.Unlock()
		conn.Close()
	}()

	slog.Info("connected to server")

	// Reset backoff on successful connection (caller handles this via return nil path)
	conn.SetReadLimit(maxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	// The server (Hub) sends pings; gorilla's default ping handler writes
	// back a pong but does NOT extend the read deadline, so without this
	// override the connection times out after pongWait every cycle.
	defaultPing := conn.PingHandler()
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		return defaultPing(appData)
	})

	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	errCh := make(chan error, 3)

	// Read loop
	go func() {
		errCh <- t.readLoop(connCtx, conn)
	}()

	// Write loop
	go func() {
		errCh <- t.writeLoop(connCtx, conn)
	}()

	// Heartbeat loop
	go func() {
		errCh <- t.heartbeatLoop(connCtx, version, startedAt)
	}()

	// Wait for first error or context cancellation
	select {
	case err := <-errCh:
		connCancel()
		return err
	case <-connCtx.Done():
		return connCtx.Err()
	}
}

func (t *Tunnel) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("reading message: %w", err)
		}

		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			slog.Warn("invalid message from server", "error", err)
			continue
		}

		slog.Debug("received message", "type", msg.Type)

		switch msg.Type {
		case TypeDirective:
			var directive DirectivePayload
			if err := json.Unmarshal(msg.Payload, &directive); err != nil {
				slog.Error("invalid directive payload", "error", err)
				continue
			}
			if t.OnDirective != nil {
				t.OnDirective(directive)
			}
		case TypeUpgrade:
			var up UpgradePayload
			if err := json.Unmarshal(msg.Payload, &up); err != nil {
				slog.Error("invalid upgrade payload", "error", err)
				continue
			}
			if t.OnUpgrade != nil {
				t.OnUpgrade(up)
			}
		case TypeProbe:
			var p ProbePayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				slog.Error("invalid probe payload", "error", err)
				continue
			}
			if t.OnProbe != nil {
				t.OnProbe(p)
			}
		case TypeCredentialTest:
			var ct CredentialTestPayload
			if err := json.Unmarshal(msg.Payload, &ct); err != nil {
				slog.Error("invalid credential_test payload", "error", err)
				continue
			}
			if t.OnCredentialTest != nil {
				t.OnCredentialTest(ct)
			}
		default:
			slog.Debug("unhandled message type", "type", msg.Type)
		}
	}
}

func (t *Tunnel) writeLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		select {
		case <-ctx.Done():
			// Send close message before returning
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(writeWait),
			)
			return ctx.Err()
		case msg := <-t.sendCh:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteJSON(msg); err != nil {
				return fmt.Errorf("writing message: %w", err)
			}
		}
	}
}

func (t *Tunnel) heartbeatLoop(ctx context.Context, version string, startedAt time.Time) error {
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()

	// Send initial heartbeat immediately
	t.sendHeartbeat(version, startedAt)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			t.sendHeartbeat(version, startedAt)
		}
	}
}

func (t *Tunnel) sendHeartbeat(version string, startedAt time.Time) {
	payload := HeartbeatPayload{
		Version:       version,
		UptimeSeconds: int64(time.Since(startedAt).Seconds()),
		InContainer:   t.InContainer,
	}
	data, _ := json.Marshal(payload)
	t.Send(Message{Type: TypeHeartbeat, Payload: data})
}
