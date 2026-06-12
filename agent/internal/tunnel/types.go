package tunnel

import "encoding/json"

// Message is the envelope for all WebSocket messages between agent and server.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Message type constants matching the API's websocket protocol.
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
	TypeAllowlistSnapshot      = "allowlist_snapshot"      // ADR 003 D11 — agent → server informational
	TypeCredentialTest         = "credential_test"         // server → agent: test a credential source
	TypeCredentialTestResult   = "credential_test_result"  // agent → server: result of credential test
	TypeFactsCollected         = "facts_collected"         // ADR 011 — agent → server: raw collector facts
)

// AllowlistSnapshotPayload is the agent's most recently reported
// scan allowlist (D11). Informational only — the agent remains the
// policy authority. Server uses it to tag discovered_assets with a
// display status so the UI can surface/gate promote.
type AllowlistSnapshotPayload struct {
	Hash         string   `json:"hash"`
	Allow        []string `json:"allow"`
	Deny         []string `json:"deny,omitempty"`
	RateLimitPPS int      `json:"rate_limit_pps,omitempty"`
}

// ProbePayload is sent from server to agent to validate target connectivity
// without running a scan.
type ProbePayload struct {
	ProbeID          string          `json:"probe_id"`
	TargetType       string          `json:"target_type"`
	TargetIdentifier string          `json:"target_identifier"`
	TargetConfig     json.RawMessage `json:"target_config"`
	Credentials      json.RawMessage `json:"credentials,omitempty"`
}

// ProbeResultPayload is the agent's reply.
type ProbeResultPayload struct {
	ProbeID string `json:"probe_id"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Detail  string `json:"detail,omitempty"` // e.g. "PostgreSQL 16.13"
}

// UpgradePayload instructs the agent to download a new binary version,
// swap in place, and exit so the service manager restarts it.
type UpgradePayload struct {
	Version          string            `json:"version"`  // e.g. "v0.1.4" or "latest"
	BaseURL          string            `json:"base_url"` // e.g. https://downloads.silkstrand.io/agent
	SHA256ByPlatform map[string]string `json:"sha256_by_platform"`
	// Keys are "<os>-<arch>": linux-amd64, darwin-arm64, etc.
}

// CredentialResolverConfig tells the agent to resolve credentials
// locally (e.g. from an on-prem Vault) instead of receiving
// pre-resolved plaintext from the server.
type CredentialResolverConfig struct {
	Type   string          `json:"type"`   // "hashicorp_vault"
	Config json.RawMessage `json:"config"` // resolver-specific config
}

// DirectivePayload is received from the server with scan instructions.
type DirectivePayload struct {
	ScanID             string                    `json:"scan_id"`
	ScanType           string                    `json:"scan_type,omitempty"` // "compliance" (default) | "discovery"
	BundleID           string                    `json:"bundle_id"`
	BundleName         string                    `json:"bundle_name"`
	BundleVersion      string                    `json:"bundle_version"`
	BundleURL          string                    `json:"bundle_url,omitempty"` // HTTPS URL to a .tar.gz; agent fetches if not cached
	TargetID           string                    `json:"target_id"`
	TargetType         string                    `json:"target_type"`
	TargetIdentifier   string                    `json:"target_identifier"`
	TargetConfig       json.RawMessage           `json:"target_config"`
	Credentials        json.RawMessage           `json:"credentials,omitempty"`          // set when server resolves (static, aws_secrets_manager)
	CredentialResolver *CredentialResolverConfig  `json:"credential_resolver,omitempty"`  // set for agent-side resolution (on-prem vault)
}

// AssetDiscoveredPayload is sent during a discovery scan with a batch of
// findings. Process inline server-side per ADR 003 D9.
type AssetDiscoveredPayload struct {
	ScanID   string                  `json:"scan_id"`
	BatchSeq int                     `json:"batch_seq,omitempty"`
	Stage    string                  `json:"stage,omitempty"` // naabu|httpx|nuclei
	Assets   []DiscoveredAssetUpsert `json:"assets"`
}

// DiscoveredAssetUpsert is one normalized asset finding.
type DiscoveredAssetUpsert struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	// ResourceType is "http_service" for a vhost probed by name (SNI/Host) so
	// the server keys it on the hostname, not the shared ingress IP (ADR 014
	// D3). Empty → the default host (IP-keyed) behavior.
	ResourceType string          `json:"resource_type,omitempty"`
	Hostname     string          `json:"hostname,omitempty"`
	Service      string          `json:"service,omitempty"`
	Version      string          `json:"version,omitempty"`
	Technologies json.RawMessage `json:"technologies,omitempty"`
	CVEs         json.RawMessage `json:"cves,omitempty"`
	ObservedAt   string          `json:"observed_at"`
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

// DiscoveryCompletedPayload is the terminal message for a discovery scan.
type DiscoveryCompletedPayload struct {
	ScanID       string `json:"scan_id"`
	AssetsFound  int    `json:"assets_found"`
	HostsScanned int    `json:"hosts_scanned"`
}

// ScanStartedPayload is sent to the server when scan execution begins.
type ScanStartedPayload struct {
	ScanID string `json:"scan_id"`
}

// ScanResultsPayload is sent to the server with completed scan results.
// When Partial is true, the server writes findings but keeps the scan
// status as running — more results are coming. When false (or absent for
// backwards compatibility with legacy agents), the scan is marked completed.
type ScanResultsPayload struct {
	ScanID  string          `json:"scan_id"`
	Results json.RawMessage `json:"results"`
	Partial bool            `json:"partial,omitempty"`
}

// ScanErrorPayload is sent to the server when scan execution fails.
type ScanErrorPayload struct {
	ScanID string `json:"scan_id"`
	Error  string `json:"error"`
}

// HeartbeatPayload is sent periodically to the server.
type HeartbeatPayload struct {
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	// InContainer tells the server this agent runs in a container, where the
	// in-place self-upgrade path is unsupported (ADR 013 follow-up).
	InContainer bool `json:"in_container"`
}
