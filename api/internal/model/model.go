// Package model holds the domain types shared between handlers and the
// store. Post-P1 of the asset-first refactor (ADR 006 / ADR 007) the
// types split cleanly into three cohorts:
//
//   - platform primitives (Tenant, Agent, Bundle, Target, CredentialSource)
//     — survive the refactor unchanged except for the narrowed target type
//     set (CIDR / network_range only).
//   - recon + compliance model (Asset, AssetEndpoint, Finding, Scan,
//     ScanDefinition) — the new asset-first shape.
//   - collections / rules / notifications — the unifying predicate +
//     dispatch surface; ScanResult / DiscoveredAsset / AssetSet /
//     OneShotScan / NotificationChannel are deleted.
package model

import (
	"encoding/json"
	"time"
)

// ----- Platform primitives ---------------------------------------

type Tenant struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	Config    json.RawMessage `json:"config"`
	CreatedAt time.Time       `json:"created_at"`
}

const (
	TenantStatusActive    = "active"
	TenantStatusSuspended = "suspended"
	TenantStatusInactive  = "inactive"
)

type DCStats struct {
	TenantCount int `json:"tenant_count"`
	AgentCount  int `json:"agent_count"`
	ScanCount   int `json:"scan_count"`
}

type CreateTenantRequest struct {
	Name string `json:"name"`
}

type UpdateTenantRequest struct {
	Name   *string         `json:"name,omitempty"`
	Status *string         `json:"status,omitempty"`
	Config json.RawMessage `json:"config,omitempty"`
}

type Agent struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenant_id"`
	Name          string     `json:"name"`
	Zone          *string    `json:"zone,omitempty"` // ADR 013 D10: site label for overlap heuristic
	Status        string     `json:"status"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
	Version       *string    `json:"version,omitempty"`
	InContainer   *bool      `json:"in_container"` // ADR 013 follow-up: nil = unknown; true = container (image-recreate); false = binary (in-place)
	KeyHash       string     `json:"-"`
	NextKeyHash   *string    `json:"-"`
	KeyRotatedAt  *time.Time `json:"key_rotated_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type CreateAgentRequest struct {
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	// ADR 013 D5: auto-discover intent copied from the install token at
	// bootstrap; consumed (fire-once) on the agent's first allowlist snapshot.
	AutoDiscoverPending bool    `json:"-"`
	DiscoverCron        *string `json:"-"`
	// ADR 013 D10: normalized zone slug copied from the install token.
	Zone *string `json:"-"`
}

// AgentLogEvent is a persisted agent log line. Partition key is occurred_at.
type AgentLogEvent struct {
	ID         string          `json:"id"`
	AgentID    string          `json:"agent_id"`
	ScanID     *string         `json:"scan_id,omitempty"`
	Level      string          `json:"level"`
	Msg        string          `json:"msg"`
	Attrs      json.RawMessage `json:"attrs"`
	OccurredAt time.Time       `json:"occurred_at"`
}

type Bundle struct {
	ID           string    `json:"id"`
	TenantID     *string   `json:"tenant_id,omitempty"`
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	Framework    string    `json:"framework"`
	TargetType   string    `json:"target_type"`
	Engine       *string   `json:"engine,omitempty"`
	ControlCount int       `json:"control_count"`
	GCSPath      *string   `json:"gcs_path,omitempty"`
	Signature    *string   `json:"signature,omitempty"`
	TarballHash  *string   `json:"tarball_hash,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// BundleControl is per-control metadata extracted from control.yaml files
// inside a bundle tarball. Populated at upload time. See ADR 010 D11.
type BundleControl struct {
	BundleID       string          `json:"bundle_id"`
	ControlID      string          `json:"control_id"`
	Name           string          `json:"name"`
	Severity       *string         `json:"severity,omitempty"`
	Section        *string         `json:"section,omitempty"`
	Engine         string          `json:"engine"`
	EngineVersions json.RawMessage `json:"engine_versions"`
	Tags           json.RawMessage `json:"tags"`
}

type Target struct {
	ID                 string          `json:"id"`
	TenantID           string          `json:"tenant_id"`
	AgentID            *string         `json:"agent_id,omitempty"`
	Type               string          `json:"type"`
	Identifier         string          `json:"identifier"`
	Config             json.RawMessage `json:"config"`
	Environment        *string         `json:"environment,omitempty"`
	CredentialSourceID *string         `json:"credential_source_id,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// CredentialSource is the pluggable resolver binding from a target to a
// credential. See ADR 004. Migration 017 extends the type set to cover
// notification channels + vault integrations.
type CredentialSource struct {
	ID        string          `json:"id"`
	TenantID  string          `json:"tenant_id"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Config    json.RawMessage `json:"config"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

const (
	CredentialSourceTypeStatic            = "static"
	CredentialSourceTypeSlack             = "slack"
	CredentialSourceTypeWebhook           = "webhook"
	CredentialSourceTypeEmail             = "email"
	CredentialSourceTypePagerDuty         = "pagerduty"
	CredentialSourceTypeAWSSecretsManager = "aws_secrets_manager"
	CredentialSourceTypeHashiCorpVault    = "hashicorp_vault"
	CredentialSourceTypeCyberArk          = "cyberark"
)

type StaticCredentialConfig struct {
	Type          string `json:"type"`
	EncryptedData string `json:"encrypted_data"`
}

type CreateTargetRequest struct {
	AgentID     *string         `json:"agent_id,omitempty"`
	Type        string          `json:"type"`
	Identifier  string          `json:"identifier"`
	Config      json.RawMessage `json:"config,omitempty"`
	Environment string          `json:"environment,omitempty"`
}

type UpdateTargetRequest struct {
	AgentID     *string         `json:"agent_id,omitempty"`
	Type        *string         `json:"type,omitempty"`
	Identifier  *string         `json:"identifier,omitempty"`
	Config      json.RawMessage `json:"config,omitempty"`
	Environment *string         `json:"environment,omitempty"`
}

// Target types — post ADR 006 D8 the tenant-facing `targets` table is
// narrowed to CIDR / network_range. The per-engine constants (postgres,
// mssql, etc.) survive on the wire for compatibility with bundles and
// asset_endpoints.service values but are no longer valid for Target.Type.
const (
	TargetTypeCIDR         = "cidr"
	TargetTypeNetworkRange = "network_range"
)

// IsValidTargetType enforces the narrowed set from ADR 006 D8.
func IsValidTargetType(t string) bool {
	switch t {
	case TargetTypeCIDR, TargetTypeNetworkRange:
		return true
	}
	return false
}

// Engine identifiers, used as asset_endpoints.service values and as
// bundle.target_type. Kept here because handlers that translate
// asset → target + bundle still need them in scope.
const (
	EngineTypePostgreSQL       = "postgresql"
	EngineTypeAuroraPostgreSQL = "aurora_postgresql"
	EngineTypeMSSQL            = "mssql"
	EngineTypeMongoDB          = "mongodb"
	EngineTypeMySQL            = "mysql"
	EngineTypeAuroraMySQL      = "aurora_mysql"
)

// ----- Recon model (ADR 006 D2) ----------------------------------

// Asset is a host-level identity row in the inventory. See ADR 006 D2.
type Asset struct {
	ID           string          `json:"id"`
	TenantID     string          `json:"tenant_id"`
	PrimaryIP    *string         `json:"primary_ip,omitempty"`
	Hostname     *string         `json:"hostname,omitempty"`
	Fingerprint  json.RawMessage `json:"fingerprint"`
	ResourceType string          `json:"resource_type"`
	Source       string          `json:"source"`
	Environment  *string         `json:"environment,omitempty"`
	FirstSeen    time.Time       `json:"first_seen"`
	LastSeen     time.Time       `json:"last_seen"`
	CreatedAt    time.Time       `json:"created_at"`
}

const (
	ResourceTypeHost          = "host"
	ResourceTypeContainer     = "container"
	ResourceTypeCloudResource = "cloud_resource"
	// ResourceTypeHTTPService is a virtual host / website, identity-keyed on its
	// name so vhosts sharing an ingress IP stay distinct (ADR 014 D1).
	ResourceTypeHTTPService = "http_service"

	AssetSourceManual     = "manual"
	AssetSourceDiscovered = "discovered"

	// Per-endpoint allowlist classification (carried forward from ADR 003 D11).
	AllowlistStatusAllowlisted = "allowlisted"
	AllowlistStatusOutOfPolicy = "out_of_policy"
	AllowlistStatusUnknown     = "unknown"
)

// AssetEndpoint is a (port, protocol) on an Asset. See ADR 006 D2.
type AssetEndpoint struct {
	ID                 string          `json:"id"`
	AssetID            string          `json:"asset_id"`
	Port               int             `json:"port"`
	Protocol           string          `json:"protocol"`
	Service            *string         `json:"service,omitempty"`
	Version            *string         `json:"version,omitempty"`
	Technologies       json.RawMessage `json:"technologies"`
	ComplianceStatus   *string         `json:"compliance_status,omitempty"`
	AllowlistStatus    *string         `json:"allowlist_status,omitempty"`
	AllowlistCheckedAt *time.Time      `json:"allowlist_checked_at,omitempty"`
	LastScanID         *string         `json:"last_scan_id,omitempty"`
	MissedScanCount    int             `json:"missed_scan_count"`
	Metadata           json.RawMessage `json:"metadata"`
	FirstSeen          time.Time       `json:"first_seen"`
	LastSeen           time.Time       `json:"last_seen"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// AssetDiscoverySource is a row in asset_discovery_sources: one per
// discovery event (ADR 006 D9).
type AssetDiscoverySource struct {
	AssetID      string    `json:"asset_id"`
	TargetID     *string   `json:"target_id,omitempty"`
	AgentID      *string   `json:"agent_id,omitempty"`
	ScanID       *string   `json:"scan_id,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// AssetEvent is one append-only change log row. FK now points at
// asset_endpoints(id) per ADR 006 D4.
type AssetEvent struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenant_id"`
	AssetID    string          `json:"asset_id"` // asset_endpoints(id) logically
	ScanID     *string         `json:"scan_id,omitempty"`
	EventType  string          `json:"event_type"`
	Severity   *string         `json:"severity,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
}

const (
	AssetEventNewAsset        = "new_asset"
	AssetEventAssetGone       = "asset_gone"
	AssetEventAssetReappeared = "asset_reappeared"
	AssetEventNewCVE          = "new_cve"
	AssetEventCVEResolved     = "cve_resolved"
	AssetEventVersionChanged  = "version_changed"
	AssetEventPortOpened      = "port_opened"
	AssetEventPortClosed      = "port_closed"
	AssetEventCompliancePass  = "compliance_pass"
	AssetEventComplianceFail  = "compliance_fail"
)

// ----- Collections (ADR 006 D5) ----------------------------------

type Collection struct {
	ID                string          `json:"id"`
	TenantID          string          `json:"tenant_id"`
	Name              string          `json:"name"`
	Description       *string         `json:"description,omitempty"`
	Scope             string          `json:"scope"`
	Predicate         json.RawMessage `json:"predicate"`
	IsDashboardWidget bool            `json:"is_dashboard_widget"`
	WidgetKind        *string         `json:"widget_kind,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	CreatedBy         *string         `json:"created_by,omitempty"`
}

const (
	CollectionScopeAsset    = "asset"
	CollectionScopeEndpoint = "endpoint"
	CollectionScopeFinding  = "finding"
)

// ----- Rules (ADR 006 D6) ----------------------------------------

type CorrelationRule struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id"`
	Name            string          `json:"name"`
	Version         int             `json:"version"`
	Enabled         bool            `json:"enabled"`
	Trigger         string          `json:"trigger"`
	EventTypeFilter *string         `json:"event_type_filter,omitempty"`
	Body            json.RawMessage `json:"body"`
	CreatedAt       time.Time       `json:"created_at"`
	CreatedBy       *string         `json:"created_by,omitempty"`
}

const (
	RuleTriggerAssetDiscovered = "asset_discovered"
	RuleTriggerAssetEvent      = "asset_event"
	RuleTriggerFinding         = "finding"
)

// ----- Notifications (ADR 006 roadmap P6 — deliveries only) ------

// NotificationDelivery is one append-only audit row. The channel config
// lives in credential_sources (type=slack|webhook|...); channel_source_id
// is the FK to that row.
type NotificationDelivery struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id"`
	ChannelSourceID string          `json:"channel_source_id"`
	RuleID          *string         `json:"rule_id,omitempty"`
	EventID         *string         `json:"event_id,omitempty"`
	Severity        *string         `json:"severity,omitempty"`
	Status          string          `json:"status"`
	Attempt         int             `json:"attempt"`
	ResponseCode    *int            `json:"response_code,omitempty"`
	Error           *string         `json:"error,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	DispatchedAt    time.Time       `json:"dispatched_at"`
}

const (
	DeliveryStatusPending  = "pending"
	DeliveryStatusSent     = "sent"
	DeliveryStatusFailed   = "failed"
	DeliveryStatusRetrying = "retrying"
)

// ----- Scans + Scan Definitions (ADR 007) ------------------------

// Scan is execution history. Pre-refactor Scan.Results was populated
// by scan_results rows; post-refactor findings are the terminal surface
// and Results is gone — ScanResults was a separate table that no longer
// exists.
type Scan struct {
	ID               string     `json:"id"`
	TenantID         string     `json:"tenant_id"`
	ScanDefinitionID *string    `json:"scan_definition_id,omitempty"`
	AgentID          *string    `json:"agent_id,omitempty"`
	TargetID         *string    `json:"target_id,omitempty"`
	AssetEndpointID  *string    `json:"asset_endpoint_id,omitempty"`
	BundleID         *string    `json:"bundle_id,omitempty"`
	ScanType         string     `json:"scan_type"`
	Status           string     `json:"status"`
	ErrorMessage     *string    `json:"error_message,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

const (
	ScanStatusQueued    = "queued"
	ScanStatusPending   = "pending"
	ScanStatusRunning   = "running"
	ScanStatusCompleted = "completed"
	ScanStatusFailed    = "failed"

	ScanTypeCompliance = "compliance"
	ScanTypeDiscovery  = "discovery"

	// DiscoveryBundleID is the well-known UUID for the global discovery
	// bundle seeded by migration 015. Discovery scans reference it so
	// the scan row always has a valid bundle_id FK.
	DiscoveryBundleID = "11111111-1111-1111-1111-111111111111"
)

type CreateScanRequest struct {
	TargetID string `json:"target_id,omitempty"`
	BundleID string `json:"bundle_id,omitempty"`
	ScanType string `json:"scan_type,omitempty"`
}

// ScanDefinition is the stored configuration for a scheduled or
// manually-triggered scan. See ADR 007 D3.
type ScanDefinition struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id"`
	Name            string     `json:"name"`
	Kind            string     `json:"kind"` // compliance | discovery
	BundleID        *string    `json:"bundle_id,omitempty"`
	ScopeKind       string     `json:"scope_kind"` // asset_endpoint | collection | cidr
	AssetEndpointID *string    `json:"asset_endpoint_id,omitempty"`
	CollectionID    *string    `json:"collection_id,omitempty"`
	CIDR            *string    `json:"cidr,omitempty"`
	AgentID         *string    `json:"agent_id,omitempty"`
	Schedule        *string    `json:"schedule,omitempty"`
	Enabled         bool       `json:"enabled"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	LastRunStatus   *string    `json:"last_run_status,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CreatedBy       *string    `json:"created_by,omitempty"`
}

const (
	ScanDefinitionKindCompliance = "compliance"
	ScanDefinitionKindDiscovery  = "discovery"

	ScanDefinitionScopeAssetEndpoint = "asset_endpoint"
	ScanDefinitionScopeCollection    = "collection"
	ScanDefinitionScopeCIDR          = "cidr"
	// ScanDefinitionScopeAgentAllowlist scopes a discovery scan to whatever
	// the agent currently reports in its scan-allowlist snapshot (ADR 013 D4).
	// Resolved at dispatch time; requires agent_id.
	ScanDefinitionScopeAgentAllowlist = "agent_allowlist"
)

// ----- Findings (ADR 007 D1) -------------------------------------

type Finding struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id"`
	AssetEndpointID string          `json:"asset_endpoint_id"`
	ScanID          *string         `json:"scan_id,omitempty"`
	SourceKind      string          `json:"source_kind"`
	Source          string          `json:"source"`
	SourceID        *string         `json:"source_id,omitempty"`
	CVEID           *string         `json:"cve_id,omitempty"`
	Severity        *string         `json:"severity,omitempty"`
	Title           string          `json:"title"`
	Status          string          `json:"status"`
	Evidence        json.RawMessage `json:"evidence"`
	Remediation     *string         `json:"remediation,omitempty"`
	FirstSeen       time.Time       `json:"first_seen"`
	LastSeen        time.Time       `json:"last_seen"`
	ResolvedAt      *time.Time      `json:"resolved_at,omitempty"`
}

const (
	FindingSourceKindNetworkVuln       = "network_vuln"
	FindingSourceKindNetworkCompliance = "network_compliance"
	FindingSourceKindBundleCompliance  = "bundle_compliance"

	FindingStatusOpen       = "open"
	FindingStatusResolved   = "resolved"
	FindingStatusSuppressed = "suppressed"
)

// ----- Credential mappings + asset relationships (placeholders) --

// Credential mapping scope kinds.
const (
	MappingScopeCollection    = "collection"
	MappingScopeAssetEndpoint = "asset_endpoint"
	MappingScopeAsset         = "asset"
)

type CredentialMapping struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenant_id"`
	ScopeKind          string    `json:"scope_kind"`
	CollectionID       *string   `json:"collection_id,omitempty"`
	AssetEndpointID    *string   `json:"asset_endpoint_id,omitempty"`
	AssetID            *string   `json:"asset_id,omitempty"`
	CredentialSourceID string    `json:"credential_source_id"`
	CreatedAt          time.Time `json:"created_at"`
}

// ----- Compliance profiles (ADR 010 D9 — Level 3A) ---------------

type ComplianceProfile struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	Name          string    `json:"name"`
	Description   *string   `json:"description,omitempty"`
	BaseFramework *string   `json:"base_framework,omitempty"`
	Status        string    `json:"status"`
	Version       int       `json:"version"`
	BundleID      *string   `json:"bundle_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	CreatedBy     *string   `json:"created_by,omitempty"`
	ControlCount  int       `json:"control_count"`
}

const (
	ProfileStatusDraft     = "draft"
	ProfileStatusPublished = "published"
)

// ----- Collected facts (ADR 011 D4) ------------------------------

// CollectedFacts stores raw facts collected by an agent-side collector
// before policy evaluation. 30-day retention, partitioned by collected_at.
type CollectedFacts struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id"`
	AssetEndpointID string          `json:"asset_endpoint_id"`
	ScanID          string          `json:"scan_id"`
	CollectorID     string          `json:"collector_id"`
	Facts           json.RawMessage `json:"facts"`
	CollectedAt     time.Time       `json:"collected_at"`
}

// ----- Tenant policies (ADR 011 D9) ------------------------------

// TenantPolicy is a tenant-scoped compliance policy rule (Rego source).
// Origin is 'derived' (copy of a builtin) or 'custom' (from scratch).
type TenantPolicy struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenant_id"`
	ControlID   string          `json:"control_id"`
	Origin      string          `json:"origin"`
	BasedOn     *string         `json:"based_on,omitempty"`
	Name        string          `json:"name"`
	Severity    string          `json:"severity"`
	RegoSource  string          `json:"rego_source"`
	CollectorID string          `json:"collector_id"`
	FactKeys    json.RawMessage `json:"fact_keys"`
	Frameworks  json.RawMessage `json:"frameworks"`
	Tags        json.RawMessage `json:"tags"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

const (
	TenantPolicyOriginDerived = "derived"
	TenantPolicyOriginCustom  = "custom"
)

// ----- Agent statuses (unchanged) --------------------------------

const (
	AgentStatusConnected    = "connected"
	AgentStatusDisconnected = "disconnected"
)
