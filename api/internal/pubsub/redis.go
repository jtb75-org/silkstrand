package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// PubSub wraps Redis pub/sub for agent directive delivery and scan events.
type PubSub struct {
	client *redis.Client
}

func New(redisURL string) (*PubSub, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parsing redis URL: %w", err)
	}

	client := redis.NewClient(opts)

	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("pinging redis: %w", err)
	}

	return &PubSub{client: client}, nil
}

func (ps *PubSub) Close() error {
	return ps.client.Close()
}

func (ps *PubSub) Ping(ctx context.Context) error {
	return ps.client.Ping(ctx).Err()
}

// Directive is a scan directive sent to an agent.
type Directive struct {
	ScanID           string `json:"scan_id"`
	ScanType         string `json:"scan_type,omitempty"` // empty == "compliance" (back-compat)
	BundleID         string `json:"bundle_id"`
	BundleVersion    string `json:"bundle_version,omitempty"`
	TargetID         string `json:"target_id"`
	AssetEndpointID  string `json:"asset_endpoint_id,omitempty"` // set for collection/endpoint-scoped scans
	TenantID         string `json:"tenant_id,omitempty"`         // for credential resolution via mappings
	ChunkID          string `json:"chunk_id,omitempty"`
	ChunkIndex       int    `json:"chunk_index,omitempty"`
	ChunkTotal       int    `json:"chunk_total,omitempty"`
	TargetType       string `json:"target_type,omitempty"`
	TargetIdentifier string `json:"target_identifier,omitempty"`
}

// PublishDirective sends a scan directive to a specific agent's channel.
func (ps *PubSub) PublishDirective(ctx context.Context, agentID string, directive Directive) error {
	channel := fmt.Sprintf("agent:%s:directives", agentID)
	data, err := json.Marshal(directive)
	if err != nil {
		return fmt.Errorf("marshaling directive: %w", err)
	}

	if err := ps.client.Publish(ctx, channel, data).Err(); err != nil {
		return fmt.Errorf("publishing directive: %w", err)
	}

	slog.Info("published directive", "agent_id", agentID, "scan_id", directive.ScanID)
	return nil
}

// SubscribeDirectives subscribes to directives for a specific agent.
// The callback is invoked for each directive received.
func (ps *PubSub) SubscribeDirectives(ctx context.Context, agentID string, callback func(Directive)) error {
	return ps.SubscribeDirectivesReady(ctx, agentID, nil, callback)
}

func (ps *PubSub) SubscribeDirectivesReady(ctx context.Context, agentID string, ready chan<- struct{}, callback func(Directive)) error {
	channel := fmt.Sprintf("agent:%s:directives", agentID)
	sub := ps.client.Subscribe(ctx, channel)
	defer sub.Close()
	if _, err := sub.Receive(ctx); err != nil {
		if ready != nil {
			close(ready)
		}
		return fmt.Errorf("registering directive subscription: %w", err)
	}
	if ready != nil {
		close(ready)
	}

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}

			var directive Directive
			if err := json.Unmarshal([]byte(msg.Payload), &directive); err != nil {
				slog.Warn("invalid directive payload", "error", err)
				continue
			}

			callback(directive)
		}
	}
}

// PublishScanProgress publishes a scan status update for real-time UI.
func (ps *PubSub) PublishScanProgress(ctx context.Context, scanID string, status string) error {
	channel := fmt.Sprintf("scan:%s:progress", scanID)
	data, _ := json.Marshal(map[string]string{"scan_id": scanID, "status": status})
	return ps.client.Publish(ctx, channel, data).Err()
}

// --- Probes (request / response across multiple API instances) ---
//
// Cloud Run scales the API horizontally. An agent's WebSocket pins to one
// instance, but a probe HTTP request can land on any instance. Probes
// therefore route through Redis like directives do, and probe responses
// come back over a per-probe-id channel that the originating instance
// subscribes to BEFORE publishing the request (pub/sub has no durability;
// late subscribers miss the reply).

// PublishUpgrade sends an upgrade directive to whichever instance owns
// the agent's WSS. Payload is the websocket.UpgradePayload JSON bytes.
// Fire-and-forget — the agent restarts itself on success and reconnects,
// which is the implicit acknowledgement.
func (ps *PubSub) PublishUpgrade(ctx context.Context, agentID string, payload []byte) error {
	channel := fmt.Sprintf("agent:%s:upgrades", agentID)
	if err := ps.client.Publish(ctx, channel, payload).Err(); err != nil {
		return fmt.Errorf("publishing upgrade: %w", err)
	}
	return nil
}

// SubscribeUpgrades subscribes to upgrade directives for an agent. Each
// WSS connection handler runs one of these alongside SubscribeDirectives
// and SubscribeProbes.
func (ps *PubSub) SubscribeUpgrades(ctx context.Context, agentID string, callback func(payload []byte)) error {
	channel := fmt.Sprintf("agent:%s:upgrades", agentID)
	sub := ps.client.Subscribe(ctx, channel)
	defer sub.Close()

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			callback([]byte(msg.Payload))
		}
	}
}

// PublishProbe sends a probe request to whichever instance owns the
// agent's WSS. Payload is the websocket.ProbePayload JSON bytes.
func (ps *PubSub) PublishProbe(ctx context.Context, agentID string, payload []byte) error {
	channel := fmt.Sprintf("agent:%s:probes", agentID)
	if err := ps.client.Publish(ctx, channel, payload).Err(); err != nil {
		return fmt.Errorf("publishing probe: %w", err)
	}
	return nil
}

// SubscribeProbes subscribes to probe requests for an agent. Each WSS
// connection handler runs one of these alongside SubscribeDirectives.
func (ps *PubSub) SubscribeProbes(ctx context.Context, agentID string, callback func(payload []byte)) error {
	channel := fmt.Sprintf("agent:%s:probes", agentID)
	sub := ps.client.Subscribe(ctx, channel)
	defer sub.Close()

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			callback([]byte(msg.Payload))
		}
	}
}

// PublishCredentialTest sends a credential test request to whichever
// instance owns the agent's WSS. Payload is the
// websocket.CredentialTestPayload JSON bytes.
func (ps *PubSub) PublishCredentialTest(ctx context.Context, agentID string, payload []byte) error {
	channel := fmt.Sprintf("agent:%s:credential-tests", agentID)
	if err := ps.client.Publish(ctx, channel, payload).Err(); err != nil {
		return fmt.Errorf("publishing credential test: %w", err)
	}
	return nil
}

// SubscribeCredentialTests subscribes to credential test requests for an
// agent. Each WSS connection handler runs one of these alongside the
// other subscribe goroutines.
func (ps *PubSub) SubscribeCredentialTests(ctx context.Context, agentID string, callback func(payload []byte)) error {
	channel := fmt.Sprintf("agent:%s:credential-tests", agentID)
	sub := ps.client.Subscribe(ctx, channel)
	defer sub.Close()

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			callback([]byte(msg.Payload))
		}
	}
}

// PublishCredentialTestResult is called by the WSS handler when an agent
// replies with a credential_test_result message. The originating HTTP
// handler listens on credential_test:<id>:result.
func (ps *PubSub) PublishCredentialTestResult(ctx context.Context, testID string, payload []byte) error {
	channel := fmt.Sprintf("credential_test:%s:result", testID)
	if err := ps.client.Publish(ctx, channel, payload).Err(); err != nil {
		return fmt.Errorf("publishing credential test result: %w", err)
	}
	return nil
}

// AwaitCredentialTestResult subscribes to the result channel for testID,
// then runs `then` (which should publish the test request) and waits up
// to timeout for the agent's reply. Subscribe-before-publish is mandatory.
func (ps *PubSub) AwaitCredentialTestResult(ctx context.Context, testID string, timeout time.Duration, then func() error) ([]byte, error) {
	channel := fmt.Sprintf("credential_test:%s:result", testID)
	sub := ps.client.Subscribe(ctx, channel)
	defer sub.Close()

	if _, err := sub.Receive(ctx); err != nil {
		return nil, fmt.Errorf("registering credential test result subscription: %w", err)
	}

	if err := then(); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-sub.Channel():
		if !ok {
			return nil, fmt.Errorf("credential test result channel closed")
		}
		return []byte(msg.Payload), nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("agent did not reply in time")
	}
}

// PublishProbeResult is called by the WSS handler when an agent replies
// with a probe_result message. The originating HTTP handler is listening
// on probe:<id>:result.
func (ps *PubSub) PublishProbeResult(ctx context.Context, probeID string, payload []byte) error {
	channel := fmt.Sprintf("probe:%s:result", probeID)
	if err := ps.client.Publish(ctx, channel, payload).Err(); err != nil {
		return fmt.Errorf("publishing probe result: %w", err)
	}
	return nil
}

// AwaitProbeResult subscribes to the result channel for probe_id, then
// runs `then` (which should publish the probe request) and waits up to
// timeout for the agent's reply. Subscribe-before-publish is mandatory:
// pub/sub doesn't queue, so a publish before the subscriber is connected
// is dropped. Returns the raw payload bytes or an error.
func (ps *PubSub) AwaitProbeResult(ctx context.Context, probeID string, timeout time.Duration, then func() error) ([]byte, error) {
	channel := fmt.Sprintf("probe:%s:result", probeID)
	sub := ps.client.Subscribe(ctx, channel)
	defer sub.Close()

	// Confirm the subscription is registered server-side before letting
	// the caller publish — go-redis's Subscribe is async otherwise.
	if _, err := sub.Receive(ctx); err != nil {
		return nil, fmt.Errorf("registering probe result subscription: %w", err)
	}

	if err := then(); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-sub.Channel():
		if !ok {
			return nil, fmt.Errorf("probe result channel closed")
		}
		return []byte(msg.Payload), nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("agent did not reply in time")
	}
}
