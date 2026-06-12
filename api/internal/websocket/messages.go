package websocket

import "encoding/json"

// Message type constants for the agent WebSocket protocol.
const (
	TypeDirective          = "directive"
	TypeScanStarted        = "scan_started"
	TypeScanResults        = "scan_results"
	TypeScanError          = "scan_error"
	TypeHeartbeat          = "heartbeat"
	TypeUpgrade            = "upgrade"
	TypeProbe              = "probe"
	TypeProbeResult        = "probe_result"
	TypeAssetDiscovered    = "asset_discovered"    // ADR 003 R1a
	TypeDiscoveryCompleted = "discovery_completed" // ADR 003 R1a
	TypeAllowlistSnapshot  = "allowlist_snapshot"  // ADR 003 D11
	TypeAgentLog               = "agent_log"               // ADR 008 — slog records streamed from agent
	TypeCredentialTest         = "credential_test"         // server → agent: test a credential source via agent
	TypeCredentialTestResult   = "credential_test_result"  // agent → server: result of credential test
	TypeFactsCollected         = "facts_collected"         // ADR 011 — agent → server: raw collector facts
)

// AllowlistSnapshotPayload is the agent's most recently loaded scan
// allowlist (ADR 003 D11). Informational only — the agent remains the
// policy authority. Server persists to agent_allowlists.
type AllowlistSnapshotPayload struct {
	Hash         string   `json:"hash"`
	Allow        []string `json:"allow"`
	Deny         []string `json:"deny,omitempty"`
	RateLimitPPS int      `json:"rate_limit_pps,omitempty"`
}

// UpgradePayload tells the agent to fetch a new binary and restart.
type UpgradePayload struct {
	Version          string            `json:"version"`
	BaseURL          string            `json:"base_url"`
	SHA256ByPlatform map[string]string `json:"sha256_by_platform"`
}

// ProbePayload validates target connectivity without running a scan.
type ProbePayload struct {
	ProbeID          string          `json:"probe_id"`
	TargetType       string          `json:"target_type"`
	TargetIdentifier string          `json:"target_identifier"`
	TargetConfig     json.RawMessage `json:"target_config"`
	Credentials      json.RawMessage `json:"credentials,omitempty"`
}

// ProbeResultPayload is the agent's reply to a probe.
type ProbeResultPayload struct {
	ProbeID string `json:"probe_id"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// CredentialResolverConfig tells the agent to resolve credentials
// locally (e.g. from an on-prem Vault) instead of receiving
// pre-resolved plaintext from the server.
type CredentialResolverConfig struct {
	Type   string          `json:"type"`   // "hashicorp_vault"
	Config json.RawMessage `json:"config"` // resolver-specific config
}

// DirectivePayload is sent from server to agent with scan instructions.
type DirectivePayload struct {
	ScanID             string                    `json:"scan_id"`
	ScanType           string                    `json:"scan_type,omitempty"` // "compliance" (default) | "discovery"
	BundleID           string                    `json:"bundle_id"`
	BundleName         string                    `json:"bundle_name"`
	BundleVersion      string                    `json:"bundle_version"`
	BundleURL          string                    `json:"bundle_url,omitempty"`
	TargetID           string                    `json:"target_id"`
	TargetType         string                    `json:"target_type"`
	TargetIdentifier   string                    `json:"target_identifier"`
	TargetConfig       json.RawMessage           `json:"target_config"`
	Credentials        json.RawMessage           `json:"credentials,omitempty"`          // empty for discovery; set when server resolves
	CredentialResolver *CredentialResolverConfig  `json:"credential_resolver,omitempty"`  // set for agent-side resolution (on-prem vault)
}

// AssetDiscoveredPayload is sent from agent to server during a discovery
// scan, carrying a batch of discovered (ip, port, service, ...) tuples.
// The agent emits these incrementally per ADR 003 D9 — process inline,
// don't wait for discovery_completed.
type AssetDiscoveredPayload struct {
	ScanID   string                  `json:"scan_id"`
	BatchSeq int                     `json:"batch_seq,omitempty"`
	Stage    string                  `json:"stage,omitempty"` // naabu|httpx|nuclei
	Assets   []DiscoveredAssetUpsert `json:"assets"`
}

// DiscoveredAssetUpsert is one normalized asset finding the agent
// streams up. JSONB-shaped fields are pass-through.
type DiscoveredAssetUpsert struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	// ResourceType is "http_service" when the agent probed this by name
	// (SNI/Host); the server then keys the asset on the hostname rather than the
	// shared ingress IP (ADR 014 D3). Empty → default host (IP-keyed).
	ResourceType string          `json:"resource_type,omitempty"`
	Hostname     string          `json:"hostname,omitempty"`
	Service      string          `json:"service,omitempty"`
	Version      string          `json:"version,omitempty"`
	Technologies json.RawMessage `json:"technologies,omitempty"`
	CVEs         json.RawMessage `json:"cves,omitempty"`
	ObservedAt   string          `json:"observed_at"` // RFC3339; agent's clock
}

// DiscoveryCompletedPayload is sent from agent to server when a
// discovery scan finishes successfully.
type DiscoveryCompletedPayload struct {
	ScanID       string `json:"scan_id"`
	AssetsFound  int    `json:"assets_found"`
	HostsScanned int    `json:"hosts_scanned"`
}

// ScanStartedPayload is sent from agent to server when scan execution begins.
type ScanStartedPayload struct {
	ScanID string `json:"scan_id"`
}

// ScanErrorPayload is sent from agent to server when scan execution fails.
type ScanErrorPayload struct {
	ScanID string `json:"scan_id"`
	Error  string `json:"error"`
}

// HeartbeatPayload is sent periodically from agent to server.
type HeartbeatPayload struct {
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	// InContainer reports whether the agent runs inside a container, where
	// in-place self-upgrade is unsupported (ADR 013 follow-up). Pointer so an
	// agent predating this field (omits it) is distinguishable from one that
	// reports false — the former leaves the stored value unknown (NULL).
	InContainer *bool `json:"in_container"`
}

// CredentialTestPayload is sent from server to agent to test a credential
// source (e.g. HashiCorp Vault) using the agent's local network.
type CredentialTestPayload struct {
	TestID       string          `json:"test_id"`
	ResolverType string          `json:"resolver_type"` // "hashicorp_vault"
	Config       json.RawMessage `json:"config"`        // resolver-specific config
}

// CredentialTestResultPayload is the agent's reply to a credential_test.
type CredentialTestResultPayload struct {
	TestID     string `json:"test_id"`
	Success    bool   `json:"success"`
	Username   string `json:"username,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// FactsCollectedPayload is sent from agent to server with raw facts
// gathered by a collector binary (ADR 011 D5).
type FactsCollectedPayload struct {
	ScanID      string          `json:"scan_id"`
	CollectorID string          `json:"collector_id"`
	Facts       json.RawMessage `json:"facts"`
}

// NewDirectiveMessage creates a Message containing an enriched scan directive.
func NewDirectiveMessage(scanID, scanType, bundleID, bundleName, bundleVersion, bundleURL, targetID, targetType, targetIdentifier string, targetConfig, credentials json.RawMessage, resolver *CredentialResolverConfig) Message {
	payload := DirectivePayload{
		ScanID:             scanID,
		ScanType:           scanType,
		BundleID:           bundleID,
		BundleName:         bundleName,
		BundleVersion:      bundleVersion,
		BundleURL:          bundleURL,
		TargetID:           targetID,
		TargetType:         targetType,
		TargetIdentifier:   targetIdentifier,
		TargetConfig:       targetConfig,
		Credentials:        credentials,
		CredentialResolver: resolver,
	}
	data, _ := json.Marshal(payload)
	return Message{Type: TypeDirective, Payload: data}
}
