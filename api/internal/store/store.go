// Package store defines the data access interface. All tenant-scoped
// methods read the tenant id from context (see TenantID). Non-tenant-scoped
// methods are called out explicitly in the godoc for each method.
//
// This file is the P1 asset-first refactor shape: the recon pipeline
// model (DiscoveredAsset / AssetSet / OneShotScan / NotificationChannel)
// is gone; Asset / AssetEndpoint / Collection / Finding / ScanDefinition
// are its successors. Real implementations live in postgres.go; methods
// that return errNotImplemented are P2+ work per the execution plan.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jtb75/silkstrand/api/internal/model"
)

// ErrNotImplemented is returned from store methods that are part of the
// asset-first surface but whose implementations land in later phases
// (P2: ingest + rules, P3: findings + scheduler, P4: collections query).
var ErrNotImplemented = errors.New("store method not implemented in P1")

// Store defines the data access interface.
type Store interface {
	// --- Platform ---------------------------------------------------

	// Targets (CIDR / network_range only post-ADR-006 D8).
	ListTargets(ctx context.Context) ([]model.Target, error)
	GetTarget(ctx context.Context, id string) (*model.Target, error)
	GetTargetByID(ctx context.Context, id string) (*model.Target, error) // not tenant-scoped
	CreateTarget(ctx context.Context, req model.CreateTargetRequest) (*model.Target, error)
	UpdateTarget(ctx context.Context, id string, req model.UpdateTargetRequest) (*model.Target, error)
	DeleteTarget(ctx context.Context, id string) error
	// UpsertTargetByCIDR looks up (or creates) a targets row matching the
	// tenant + CIDR tuple, returning its id. Used by the scheduler's
	// CIDR-scope dispatch to materialize a target row so the agent
	// receives a directive with target_type='cidr' + identifier=<cidr>.
	// Relies on the partial unique index from migration 019.
	UpsertTargetByCIDR(ctx context.Context, tenantID, cidr string, agentID *string, environment string) (string, error)

	// Agents
	GetAgent(ctx context.Context, id string) (*model.Agent, error)
	GetAgentByID(ctx context.Context, id string) (*model.Agent, error)
	CreateAgent(ctx context.Context, req model.CreateAgentRequest) (*model.Agent, string, error)
	UpdateAgentStatus(ctx context.Context, id, status string) error
	UpdateAgentHeartbeat(ctx context.Context, id, version string, inContainer *bool) error
	RotateAgentKey(ctx context.Context, id string) (string, error)
	PromoteAgentKey(ctx context.Context, id string) error
	ListAgents(ctx context.Context) ([]model.Agent, error)
	ListAllAgents(ctx context.Context) ([]model.Agent, error)
	DeleteAgent(ctx context.Context, id string) error

	// Install tokens
	CreateInstallToken(ctx context.Context, in CreateInstallTokenInput) error
	ConsumeInstallToken(ctx context.Context, tokenHash []byte, agentID string) (*ConsumedInstallToken, error)
	// ClaimAutoDiscover atomically clears an agent's pending auto-discover
	// flag and returns whether it was set (+ the recurring cron, if any).
	// Fire-once: a second caller sees pending=false (ADR 013 D5).
	ClaimAutoDiscover(ctx context.Context, agentID string) (pending bool, cron *string, err error)

	// Bundles
	GetBundle(ctx context.Context, id string) (*model.Bundle, error)
	ListBundlesForTenant(ctx context.Context, tenantID string) ([]model.Bundle, error)
	UpsertBundle(ctx context.Context, b model.Bundle) (*model.Bundle, error)
	ReplaceBundleControls(ctx context.Context, bundleID string, controls []model.BundleControl) error
	ListBundleControls(ctx context.Context, bundleID string) ([]model.BundleControl, error)
	ListControls(ctx context.Context, tenantID string, filter ControlFilter) ([]ControlRow, int, error)

	// Tenants (internal)
	CreateTenant(ctx context.Context, name string) (*model.Tenant, error)
	ListAllTenants(ctx context.Context) ([]model.Tenant, error)
	GetTenantByID(ctx context.Context, id string) (*model.Tenant, error)
	UpdateTenantStatus(ctx context.Context, id, status string) error
	UpdateTenantConfig(ctx context.Context, id string, config json.RawMessage) error
	UpdateTenantName(ctx context.Context, id, name string) error

	// Stats
	GetDCStats(ctx context.Context) (*model.DCStats, error)

	// Health
	Ping(ctx context.Context) error

	// --- Credential sources (ADR 004 C0) ----------------------------

	CreateCredentialSource(ctx context.Context, tenantID, name, srcType string, config json.RawMessage) (string, error)
	GetCredentialSource(ctx context.Context, id string) (*model.CredentialSource, error)
	GetCredentialSourceByTarget(ctx context.Context, targetID string) (*model.CredentialSource, error)
	ListCredentialSources(ctx context.Context, tenantID string) ([]model.CredentialSource, error)
	UpdateCredentialSource(ctx context.Context, id, name string, config json.RawMessage) error
	DeleteCredentialSource(ctx context.Context, id string) error

	// ResolveCredentialForEndpoint finds a credential_source mapped to any
	// collection that contains the given endpoint. Used by the scheduler
	// and forwardDirective to attach credentials to compliance scans
	// dispatched via collection fan-out (no per-target binding required).
	ResolveCredentialForEndpoint(ctx context.Context, tenantID, endpointID string) (*model.CredentialSource, error)

	// Credential mappings (ADR 006 P6 — collection/asset/endpoint ↔ credential_source).
	ListCredentialMappings(ctx context.Context, tenantID string) ([]model.CredentialMapping, error)
	GetCredentialMapping(ctx context.Context, id string) (*model.CredentialMapping, error)
	CreateCredentialMapping(ctx context.Context, in CreateCredentialMappingInput) (*model.CredentialMapping, error)
	DeleteCredentialMapping(ctx context.Context, id string) error
	CountMappingsForSource(ctx context.Context, sourceID string) (int, error)
	SetTargetCredentialSource(ctx context.Context, targetID, sourceID string) error
	ClearTargetCredentialSource(ctx context.Context, targetID string) error
	UpsertStaticCredentialSource(ctx context.Context, tenantID, targetID, credType string, encryptedData []byte) error
	GetStaticCredentialForTarget(ctx context.Context, targetID string) ([]byte, string, error)
	HasCredentialForTarget(ctx context.Context, targetID string) (bool, string, error)
	DeleteCredentialForTarget(ctx context.Context, tenantID, targetID string) error

	// --- Scans (execution history, ADR 007 D3) ----------------------
	//
	// The ad-hoc CreateScan surface survives as an internal helper for
	// the scheduler (P3) and the existing `POST /api/v1/scans` route.
	// P1 ships a working write path because the Scan handler still
	// lives (it returns 501 on all non-bundle routes; the list/get
	// surface otherwise compiles empty).

	ListScans(ctx context.Context) ([]model.Scan, error)
	GetScan(ctx context.Context, id string) (*model.Scan, error)
	GetScanByID(ctx context.Context, id string) (*model.Scan, error) // not tenant-scoped
	CreateScan(ctx context.Context, req model.CreateScanRequest) (*model.Scan, error)
	UpdateScanStatus(ctx context.Context, id, status string) error
	FailScan(ctx context.Context, id, reason string) error
	DeleteScan(ctx context.Context, id string) error
	FailRunningScansForAgent(ctx context.Context, agentID string) (int, error)
	AgentHasRunningScan(ctx context.Context, agentID string) (bool, error)
	AgentHasRunningScanExcluding(ctx context.Context, agentID, excludeScanID string) (bool, error)
	OldestQueuedScanForAgent(ctx context.Context, agentID string) (*model.Scan, error)
	FailStaleQueuedScans(ctx context.Context, maxAge time.Duration) (int, error)
	CreateScanChunks(ctx context.Context, chunks []CreateScanChunkInput) error
	ClaimNextScanChunk(ctx context.Context, scanID, agentID string) (*model.ScanChunk, error)
	AckScanChunkStarted(ctx context.Context, chunkID string) error
	ResetScanChunkToPending(ctx context.Context, chunkID string) error
	CompleteScanChunk(ctx context.Context, chunkID string, assetsFound, hostsScanned int) (bool, error)
	FailScanChunk(ctx context.Context, chunkID, reason string) (bool, error)
	// UpdateScanChunkStage records the chunk's latest recon stage, returning
	// true only when it changed AND the chunk is still running under the given
	// scan — so a late asset_discovered batch for an already completed/failed
	// chunk can't rewrite current_stage or emit a misleading running update
	// (callers emit stage_progress on transition, not per batch). See #387.
	UpdateScanChunkStage(ctx context.Context, scanID, chunkID, stage string) (bool, error)
	// ListScanChunks returns a scan's chunks ordered by chunk_index for the
	// scan-detail (drawer) initial state. Tenant-scoped via TenantID(ctx).
	ListScanChunks(ctx context.Context, scanID string) ([]model.ScanChunk, error)
	ResetRunningScanChunksForAgent(ctx context.Context, agentID string) ([]string, error)
	ResetUnackedScanChunks(ctx context.Context, maxAge time.Duration) ([]string, error)
	ResetStaleRunningScanChunks(ctx context.Context, maxAge time.Duration) ([]string, error)
	FailAbandonedChunkedScans(ctx context.Context, maxAge time.Duration) (int, error)
	ActiveChunkedParentsWithoutRunningChunk(ctx context.Context, limit int) ([]ChunkedParent, error)
	ScanChunkSummary(ctx context.Context, scanID string) (*ScanChunkSummary, error)

	// --- Assets + endpoints (ADR 006 D2) ----------------------------
	//
	// P1 ships minimal working impls for the read side + a write path
	// that ingest (P2) will call. The field set is narrow until P2.

	UpsertAsset(ctx context.Context, in UpsertAssetInput) (*model.Asset, error)
	UpsertAssetEndpoint(ctx context.Context, in UpsertAssetEndpointInput) (*model.AssetEndpoint, error)
	// ListHTTPServiceHostnames returns the tenant's imported http_service
	// hostnames — the scan targets for a dns_list-scoped definition (ADR 014 D4).
	ListHTTPServiceHostnames(ctx context.Context, tenantID string) ([]string, error)
	GetAssetByID(ctx context.Context, id string) (*model.Asset, error)
	ListAssets(ctx context.Context, filter AssetFilter) (items []model.Asset, total int, err error)
	ListAssetEndpoints(ctx context.Context, filter AssetEndpointFilter) ([]AssetEndpointRow, int, error)

	// --- Collections (ADR 006 D5) -----------------------------------

	CreateCollection(ctx context.Context, c model.Collection) (*model.Collection, error)
	GetCollection(ctx context.Context, id string) (*model.Collection, error)
	ListCollections(ctx context.Context) ([]model.Collection, error)
	UpdateCollection(ctx context.Context, c model.Collection) (*model.Collection, error)
	DeleteCollection(ctx context.Context, id string) error

	// --- P2 ingest surface ------------------------------------------
	//
	// Discovery ingest path uses these in addition to UpsertAsset /
	// UpsertAssetEndpoint above. AppendAssetEvents is partition-aware
	// via the occurred_at column. RecordDiscoverySource logs provenance
	// per ADR 006 D9.
	AppendAssetEvents(ctx context.Context, events []model.AssetEvent) error
	RecordDiscoverySource(ctx context.Context, in DiscoverySourceInput) error
	ListEndpointsForAsset(ctx context.Context, assetID string) ([]model.AssetEndpoint, error)
	UpdateEndpointAllowlistStatus(ctx context.Context, endpointID, status string) error

	// Rule-engine loader.
	ListActiveRulesForTrigger(ctx context.Context, tenantID, trigger string) ([]model.CorrelationRule, error)

	// Correlation-rule CRUD (ADR 006 D6; P4 wires the collection-aware
	// matcher). Tenant-scoped.
	ListCorrelationRules(ctx context.Context) ([]model.CorrelationRule, error)
	GetCorrelationRule(ctx context.Context, id string) (*model.CorrelationRule, error)
	CreateCorrelationRule(ctx context.Context, r model.CorrelationRule) (*model.CorrelationRule, error)
	UpdateCorrelationRule(ctx context.Context, r model.CorrelationRule) (*model.CorrelationRule, error)
	DeleteCorrelationRule(ctx context.Context, id string) error

	// P4 read surface for preview / members / coverage roll-ups.
	ListAllAssetsTenant(ctx context.Context) ([]model.Asset, error)
	ListEndpointsForAssetTenant(ctx context.Context, assetID string) ([]model.AssetEndpoint, error)
	GetAssetEndpointByID(ctx context.Context, endpointID string) (*model.AssetEndpoint, *model.Asset, error)
	ListAllEndpointViewsTenant(ctx context.Context) ([]EndpointRow, error)
	ListAllFindingsTenant(ctx context.Context) ([]model.Finding, error)
	ListFindingsForEndpoint(ctx context.Context, endpointID string) ([]model.Finding, error)

	// Coverage helpers.
	CountEndpointsByAsset(ctx context.Context) (map[string]int, error)
	EndpointsWithScanDefinitionByAsset(ctx context.Context) (map[string]map[string]struct{}, error)
	LastScanAtByAsset(ctx context.Context) (map[string]time.Time, error)
	NextScanAtByAsset(ctx context.Context) (map[string]time.Time, error)
	FindingsSeverityByEndpoint(ctx context.Context) (map[string]map[string]int, error)
	CollectionsWithCredentialMappings(ctx context.Context) ([]model.Collection, error)

	// Agent allowlist snapshot (ADR 003 D11 — restored in migration 018).
	UpsertAgentAllowlist(ctx context.Context, in AgentAllowlistInput) (changed bool, err error)
	GetAgentAllowlist(ctx context.Context, agentID string) (*AgentAllowlistSnapshot, error)

	// Notification deliveries (ADR 006 P6 — channel_source_id → credential_sources).
	InsertNotificationDelivery(ctx context.Context, d model.NotificationDelivery) error

	// --- Agent log events (persisted console lines) --------------------

	InsertAgentLogEvent(ctx context.Context, in AgentLogEventInput) error
	ListAgentLogEvents(ctx context.Context, agentID string, f AgentLogFilter) ([]model.AgentLogEvent, int, error)
	DeleteOldAgentLogs(ctx context.Context, maxAge time.Duration) (int, error)

	// --- Findings (ADR 007 D1/D2/D7) --------------------------------

	UpsertFinding(ctx context.Context, in UpsertFindingInput) (*model.Finding, error)
	ListFindings(ctx context.Context, f FindingFilter) ([]model.Finding, error)
	GetFindingByID(ctx context.Context, id string) (*model.Finding, error)
	SetFindingStatus(ctx context.Context, id, status string) error

	// --- Scan definitions (ADR 007 D3/D4/D5) ------------------------

	CreateScanDefinition(ctx context.Context, in model.ScanDefinition) (*model.ScanDefinition, error)
	GetScanDefinition(ctx context.Context, id string) (*model.ScanDefinition, error)
	ListScanDefinitions(ctx context.Context) ([]model.ScanDefinition, error)
	UpdateScanDefinition(ctx context.Context, in model.ScanDefinition) (*model.ScanDefinition, error)
	DeleteScanDefinition(ctx context.Context, id string) error
	SetScanDefinitionEnabled(ctx context.Context, id string, enabled bool, nextRunAt *time.Time) error
	SetScanDefinitionNextRun(ctx context.Context, id string, nextRunAt *time.Time) error
	SetScanDefinitionLastRun(ctx context.Context, id string, at time.Time, status string) error
	ClaimDueScanDefinitions(ctx context.Context, now time.Time, nextFn func(schedule string, from time.Time) (time.Time, error), limit int) ([]model.ScanDefinition, error)

	// CreateScanForDefinition creates a `scans` row bound to a scan
	// definition. It returns a pending scan; the caller publishes the
	// directive after create succeeds.
	CreateScanForDefinition(ctx context.Context, in CreateScanForDefinitionInput) (*model.Scan, error)

	// --- Compliance profiles (ADR 010 D9 — Level 3A) ----------------

	ListComplianceProfiles(ctx context.Context) ([]model.ComplianceProfile, error)
	GetComplianceProfile(ctx context.Context, id string) (*model.ComplianceProfile, error)
	CreateComplianceProfile(ctx context.Context, p model.ComplianceProfile) (*model.ComplianceProfile, error)
	UpdateComplianceProfile(ctx context.Context, id string, p model.ComplianceProfile) (*model.ComplianceProfile, error)
	DeleteComplianceProfile(ctx context.Context, id string) error
	SetProfileControls(ctx context.Context, profileID string, controlIDs []string) error
	GetProfileControls(ctx context.Context, profileID string) ([]string, error)
	PublishProfile(ctx context.Context, profileID string, bundleID string) error

	// Collection membership resolution — used for finding filters and
	// scheduler dispatch. Returns endpoint ids for scope=endpoint/finding
	// collections, asset ids for scope=asset.
	CollectionEndpointIDs(ctx context.Context, collectionID string) ([]string, error)

	// --- Collected facts (ADR 011 D4) --------------------------------

	InsertCollectedFacts(ctx context.Context, f model.CollectedFacts) error
	GetCollectedFactsByScan(ctx context.Context, scanID string) ([]model.CollectedFacts, error)
	GetLatestFactsForEndpoint(ctx context.Context, endpointID string) (*model.CollectedFacts, error)
	DeleteOldCollectedFacts(ctx context.Context, maxAge time.Duration) (int, error)

	// --- Tenant policies (ADR 011 D9) --------------------------------

	ListTenantPolicies(ctx context.Context) ([]model.TenantPolicy, error)
	GetTenantPolicy(ctx context.Context, id string) (*model.TenantPolicy, error)
	CreateTenantPolicy(ctx context.Context, p model.TenantPolicy) (*model.TenantPolicy, error)
	UpdateTenantPolicy(ctx context.Context, id string, p model.TenantPolicy) (*model.TenantPolicy, error)
	DeleteTenantPolicy(ctx context.Context, id string) error
	ListTenantPoliciesForTenant(ctx context.Context, tenantID string) ([]model.TenantPolicy, error)
}

// --- Helper types passed through the store boundary ------------------

// UpsertAssetInput is what the discovery ingest path (P2) will hand to
// UpsertAsset. Kept narrow in P1 — P2 extends as ingest requires.
type UpsertAssetInput struct {
	TenantID     string
	PrimaryIP    string // "" → leave nil
	Hostname     string
	ResourceType string // empty defaults to 'host'
	Source       string // 'manual' | 'discovered'
	Environment  *string
	Fingerprint  json.RawMessage
}

// UpsertAssetEndpointInput mirrors UpsertAssetInput for asset_endpoints.
type UpsertAssetEndpointInput struct {
	AssetID         string
	Port            int
	Protocol        string // empty defaults to 'tcp'
	Service         string
	Version         string
	Technologies    json.RawMessage
	AllowlistStatus *string
}

// DiscoverySourceInput records provenance for a single discovery event
// (ADR 006 D9). ScanID is optional so manual asset creation can also
// log a source row later.
type DiscoverySourceInput struct {
	AssetID  string
	TargetID *string
	AgentID  *string
	ScanID   *string
}

// AgentAllowlistInput is the agent's reported scan-policy snapshot.
// Matches websocket.AllowlistSnapshotPayload field-for-field.
type AgentAllowlistInput struct {
	AgentID      string
	Hash         string
	Allow        []string
	Deny         []string
	RateLimitPPS int
}

// AgentAllowlistSnapshot is the stored row, returned for UI display
// and for rediscovery-free re-evaluation when the snapshot changes.
type AgentAllowlistSnapshot struct {
	AgentID      string    `json:"agent_id"`
	Hash         string    `json:"snapshot_hash"`
	Allow        []string  `json:"allow"`
	Deny         []string  `json:"deny"`
	RateLimitPPS int       `json:"rate_limit_pps"`
	ReportedAt   time.Time `json:"reported_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// EndpointRow is the flattened (asset, endpoint) pair served to the
// P4 collection-preview path when scope=endpoint. Matches the shape
// the rule engine uses (asset + endpoint columns in one struct).
type EndpointRow struct {
	Asset    model.Asset
	Endpoint model.AssetEndpoint
}

// UpsertFindingInput is the write-through shape for the ADR 007 D2
// ingest paths (nuclei hits and compliance bundle results). Upsert key
// is (asset_endpoint_id, source_kind, source, source_id).
type UpsertFindingInput struct {
	TenantID        string
	AssetEndpointID string
	ScanID          *string
	SourceKind      string
	Source          string
	SourceID        string
	CVEID           *string
	Severity        *string
	Title           string
	Status          string
	Evidence        json.RawMessage
	Remediation     *string
}

// FindingFilter is the parsed query for ListFindings.
type FindingFilter struct {
	SourceKind      string
	Source          string
	Severity        string
	Status          string
	AssetEndpointID string
	CollectionID    string
	CVEID           string
	Since           *time.Time
	Until           *time.Time
	Limit           int
}

// CreateScanForDefinitionInput is the store-level input for materializing
// a scans row from a definition dispatch (scheduler, manual execute,
// or run_scan_definition rule action).
type CreateScanForDefinitionInput struct {
	TenantID         string
	ScanDefinitionID string
	AgentID          *string
	TargetID         *string
	AssetEndpointID  *string
	BundleID         *string
	ScanType         string
}

// CreateScanChunkInput is the durable unit used by chunked discovery scans.
type CreateScanChunkInput struct {
	ScanID           string
	TenantID         string
	AgentID          *string
	ChunkIndex       int
	TargetType       string
	TargetIdentifier string
	IPStart          *string
	IPEnd            *string
	IPCount          int
}

type ScanChunkSummary struct {
	Total     int
	Pending   int
	Running   int
	Completed int
	Failed    int
}

type ChunkedParent struct {
	ScanID  string
	AgentID string
}

// CreateInstallTokenInput carries a new install token plus the ADR 013 D5
// auto-discover intent (copied onto the agent at bootstrap). DiscoverCron is
// nil for a one-shot (on-connect only) discovery.
type CreateInstallTokenInput struct {
	TenantID     string
	TokenHash    []byte
	ExpiresAt    time.Time
	CreatedBy    string
	AutoDiscover bool
	DiscoverCron *string
	Zone         *string // ADR 013 D10: normalized zone slug (nil if unset)
}

// ConsumedInstallToken is the record returned when an install token is
// redeemed at bootstrap — the tenant plus the auto-discover intent to stamp
// onto the new agent.
type ConsumedInstallToken struct {
	TenantID     string
	AutoDiscover bool
	DiscoverCron *string
	Zone         *string // ADR 013 D10
}

// CreateCredentialMappingInput carries the three-way scope for a new
// credential mapping. Exactly one of CollectionID, AssetEndpointID, or
// AssetID must be non-nil; ScopeKind must match.
type CreateCredentialMappingInput struct {
	TenantID           string
	ScopeKind          string
	CollectionID       *string
	AssetEndpointID    *string
	AssetID            *string
	CredentialSourceID string
}

// AssetFilter is the parsed query for ListAssets.
type AssetFilter struct {
	Source      string
	Environment string
	Q           string
	Page        int
	PageSize    int
}

// AssetEndpointFilter is the parsed query for ListAssetEndpoints.
type AssetEndpointFilter struct {
	Service  string
	Port     int
	Source   string
	Q        string
	Page     int
	PageSize int
}

// AssetEndpointRow is the flattened shape returned by ListAssetEndpoints,
// joining asset_endpoints with the parent asset for the list view.
type AssetEndpointRow struct {
	ID            string
	AssetID       string
	Host          string // hostname or IP
	IP            string
	Port          int
	Protocol      string
	Service       *string
	Version       *string
	Technologies  json.RawMessage
	FindingsCount int
	LastSeen      time.Time
}

// AgentLogEventInput is what handleAgentLog passes to InsertAgentLogEvent.
type AgentLogEventInput struct {
	TenantID string
	AgentID  string
	ScanID   string // "" → NULL
	Level    string
	Msg      string
	Attrs    json.RawMessage
}

// AgentLogFilter controls the query for ListAgentLogEvents.
type AgentLogFilter struct {
	Since  *time.Time
	Until  *time.Time
	Level  string
	ScanID string
	Limit  int
	Order  string // "asc" or "desc"
}

// ControlFilter is the parsed query for ListControls.
type ControlFilter struct {
	Framework string
	Engine    string
	Severity  string
	Tag       string
	Q         string
	Page      int
	PageSize  int
}

// ControlRow is one (control, bundle) pair returned from the DB.
// The handler groups these by ControlID to build the nested response.
type ControlRow struct {
	ControlID      string
	Name           string
	Severity       *string
	Engine         string
	EngineVersions json.RawMessage
	Tags           json.RawMessage
	Section        *string
	BundleID       string
	BundleName     string
}

// --- Context plumbing ------------------------------------------------

type contextKey string

const TenantIDKey contextKey = "tenant_id"

// TenantID extracts the tenant ID from the context.
func TenantID(ctx context.Context) string {
	v, _ := ctx.Value(TenantIDKey).(string)
	return v
}

// WithTenantID returns a new context with the tenant ID set.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, TenantIDKey, tenantID)
}
