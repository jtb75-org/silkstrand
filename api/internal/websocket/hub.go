package websocket

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// AllowedOrigins configures which origins are permitted for WebSocket upgrades.
// If empty, all origins are allowed (dev mode only).
var AllowedOrigins []string

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		if len(AllowedOrigins) == 0 {
			return true // dev mode: allow all
		}
		origin := r.Header.Get("Origin")
		// Non-browser clients (our CLI agent, HTTP libraries) don't send
		// an Origin header. Allow them — agent-key auth is the real gate
		// on this endpoint. The Origin check exists only to defend
		// browsers against cross-site WebSocket hijacking.
		if origin == "" {
			return true
		}
		for _, allowed := range AllowedOrigins {
			if origin == allowed {
				return true
			}
		}
		return false
	},
}

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 30 * time.Second
	maxMessageSize = 1024 * 1024 // 1MB
)

// Message represents a message sent over the WebSocket.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Hub manages active agent WebSocket connections.
type Hub struct {
	mu    sync.RWMutex
	conns map[string]*websocket.Conn // agent_id → connection

	// probeWaiters holds per-probe_id channels that probe HTTP handlers
	// block on until the agent replies with a probe_result. Buffered 1 so
	// the read loop never blocks if the handler has already given up.
	probeMu      sync.Mutex
	probeWaiters map[string]chan ProbeResultPayload

	// OnMessage is called when an agent sends a message.
	OnMessage func(agentID string, msg Message)
}

func NewHub() *Hub {
	return &Hub{
		conns:        make(map[string]*websocket.Conn),
		probeWaiters: make(map[string]chan ProbeResultPayload),
	}
}

// RegisterProbeWaiter reserves a channel keyed by probe_id. The caller
// must call ReleaseProbeWaiter when done (defer it).
func (h *Hub) RegisterProbeWaiter(probeID string) chan ProbeResultPayload {
	ch := make(chan ProbeResultPayload, 1)
	h.probeMu.Lock()
	h.probeWaiters[probeID] = ch
	h.probeMu.Unlock()
	return ch
}

// ReleaseProbeWaiter drops the waiter for probe_id.
func (h *Hub) ReleaseProbeWaiter(probeID string) {
	h.probeMu.Lock()
	delete(h.probeWaiters, probeID)
	h.probeMu.Unlock()
}

// DeliverProbeResult forwards a probe_result message to the waiting handler.
// Silently drops the result if no handler is waiting (timeout case).
func (h *Hub) DeliverProbeResult(result ProbeResultPayload) {
	h.probeMu.Lock()
	ch, ok := h.probeWaiters[result.ProbeID]
	h.probeMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- result:
	default:
	}
}

// HandleConnection upgrades an HTTP request to WebSocket and manages the connection.
func (h *Hub) HandleConnection(w http.ResponseWriter, r *http.Request, agentID string) error {
	return h.HandleConnectionWithHook(w, r, agentID, nil)
}

// HandleConnectionWithHook upgrades an HTTP request to WebSocket and invokes
// onConnect after the connection is registered and Send(agentID) can deliver.
func (h *Hub) HandleConnectionWithHook(w http.ResponseWriter, r *http.Request, agentID string, onConnect func()) error {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return fmt.Errorf("upgrading to websocket: %w", err)
	}

	h.register(agentID, conn)
	defer h.unregister(agentID)
	if onConnect != nil {
		onConnect()
	}

	conn.SetReadLimit(maxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	// Start ping ticker
	go h.pingLoop(agentID, conn)

	// Read loop
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("agent disconnected unexpectedly", "agent_id", agentID, "error", err)
			}
			return nil
		}

		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			slog.Warn("invalid message from agent", "agent_id", agentID, "error", err)
			continue
		}

		slog.Debug("message from agent", "agent_id", agentID, "type", msg.Type)

		if h.OnMessage != nil {
			h.OnMessage(agentID, msg)
		}
	}
}

// Send sends a message to a specific agent.
func (h *Hub) Send(agentID string, msg Message) error {
	h.mu.RLock()
	conn, ok := h.conns[agentID]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("agent %s not connected", agentID)
	}

	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	return conn.WriteJSON(msg)
}

// IsConnected returns whether an agent is currently connected.
func (h *Hub) IsConnected(agentID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[agentID]
	return ok
}

// ConnectedAgents returns a list of currently connected agent IDs.
func (h *Hub) ConnectedAgents() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.conns))
	for id := range h.conns {
		ids = append(ids, id)
	}
	return ids
}

func (h *Hub) register(agentID string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Close existing connection if any
	if existing, ok := h.conns[agentID]; ok {
		existing.Close()
	}
	h.conns[agentID] = conn
	slog.Info("agent registered", "agent_id", agentID, "total_agents", len(h.conns))
}

func (h *Hub) unregister(agentID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if conn, ok := h.conns[agentID]; ok {
		conn.Close()
		delete(h.conns, agentID)
	}
	slog.Info("agent unregistered", "agent_id", agentID, "total_agents", len(h.conns))
}

func (h *Hub) pingLoop(agentID string, conn *websocket.Conn) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for range ticker.C {
		h.mu.RLock()
		current, ok := h.conns[agentID]
		h.mu.RUnlock()

		if !ok || current != conn {
			return
		}

		_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
		if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
			return
		}
	}
}
