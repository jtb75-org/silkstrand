package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/pubsub"
	"github.com/jtb75/silkstrand/api/internal/store"
)

// fakeStore implements the bits of store.Store that the scheduler touches.
// Only the methods under test need real behavior; the rest are stubs so
// the type satisfies the interface.
type fakeStore struct {
	store.Store
	claimed       []model.ScanDefinition
	nextRunAt     map[string]time.Time
	createCalls   int
	createErr     error
	lastRun       map[string]string
	cidrUpserts   []cidrUpsert
	createInputs  []store.CreateScanForDefinitionInput
	allowlistSnap *store.AgentAllowlistSnapshot
	httpNames     []string
	chunks        []store.CreateScanChunkInput
	resetScanIDs  []string
	scans         map[string]*model.Scan
	summaries     []*store.ScanChunkSummary
	claims        []*model.ScanChunk
	statusUpdates []scanStatusUpdate
	resetChunks   []string
	parents       []store.ChunkedParent
	unackedResets []string
}

type scanStatusUpdate struct {
	scanID string
	status string
}

type fakePublisher struct {
	directives []pubsub.Directive
	err        error
}

func (p *fakePublisher) PublishDirective(ctx context.Context, agentID string, directive pubsub.Directive) error {
	if p.err != nil {
		return p.err
	}
	p.directives = append(p.directives, directive)
	return nil
}

func (f *fakeStore) GetAgentAllowlist(ctx context.Context, agentID string) (*store.AgentAllowlistSnapshot, error) {
	return f.allowlistSnap, nil
}

func (f *fakeStore) ListHTTPServiceHostnames(ctx context.Context, tenantID string) ([]string, error) {
	return f.httpNames, nil
}

type cidrUpsert struct {
	TenantID    string
	CIDR        string
	AgentID     *string
	Environment string
}

func (f *fakeStore) ClaimDueScanDefinitions(ctx context.Context, now time.Time, next func(string, time.Time) (time.Time, error), limit int) ([]model.ScanDefinition, error) {
	// Simulate the SQL path: compute next_run_at for each claimed row via `next`.
	if f.nextRunAt == nil {
		f.nextRunAt = map[string]time.Time{}
	}
	for _, d := range f.claimed {
		s := ""
		if d.Schedule != nil {
			s = *d.Schedule
		}
		n, err := next(s, now)
		if err == nil {
			f.nextRunAt[d.ID] = n
		}
	}
	out := f.claimed
	f.claimed = nil
	return out, nil
}

func (f *fakeStore) CreateScanForDefinition(ctx context.Context, in store.CreateScanForDefinitionInput) (*model.Scan, error) {
	f.createCalls++
	f.createInputs = append(f.createInputs, in)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &model.Scan{ID: "scan-1", TenantID: in.TenantID, ScanDefinitionID: &in.ScanDefinitionID}, nil
}

func (f *fakeStore) UpsertTargetByCIDR(ctx context.Context, tenantID, cidr string, agentID *string, environment string) (string, error) {
	f.cidrUpserts = append(f.cidrUpserts, cidrUpsert{
		TenantID: tenantID, CIDR: cidr, AgentID: agentID, Environment: environment,
	})
	return "target-cidr-1", nil
}

func (f *fakeStore) CollectionEndpointIDs(ctx context.Context, id string) ([]string, error) {
	return []string{"ep-1"}, nil
}

func (f *fakeStore) AgentHasRunningScan(ctx context.Context, agentID string) (bool, error) {
	return false, nil
}

func (f *fakeStore) AgentHasRunningScanExcluding(ctx context.Context, agentID, excludeScanID string) (bool, error) {
	return false, nil
}

func (f *fakeStore) UpdateScanStatus(ctx context.Context, scanID, status string) error {
	f.statusUpdates = append(f.statusUpdates, scanStatusUpdate{scanID: scanID, status: status})
	return nil
}

func (f *fakeStore) CreateScanChunks(ctx context.Context, chunks []store.CreateScanChunkInput) error {
	f.chunks = append(f.chunks, chunks...)
	return nil
}

func (f *fakeStore) FailStaleQueuedScans(ctx context.Context, maxAge time.Duration) (int, error) {
	return 0, nil
}

func (f *fakeStore) ResetStaleRunningScanChunks(ctx context.Context, maxAge time.Duration) ([]string, error) {
	return nil, nil
}

func (f *fakeStore) ResetUnackedScanChunks(ctx context.Context, maxAge time.Duration) ([]string, error) {
	return f.unackedResets, nil
}

func (f *fakeStore) FailAbandonedChunkedScans(ctx context.Context, maxAge time.Duration) (int, error) {
	return 0, nil
}

func (f *fakeStore) ActiveChunkedParentsWithoutRunningChunk(ctx context.Context, limit int) ([]store.ChunkedParent, error) {
	out := f.parents
	f.parents = nil
	return out, nil
}

func (f *fakeStore) DeleteOldAgentLogs(ctx context.Context, maxAge time.Duration) (int, error) {
	return 0, nil
}

func (f *fakeStore) DeleteOldCollectedFacts(ctx context.Context, maxAge time.Duration) (int, error) {
	return 0, nil
}

func (f *fakeStore) OldestQueuedScanForAgent(ctx context.Context, agentID string) (*model.Scan, error) {
	return nil, nil
}

func (f *fakeStore) ResetRunningScanChunksForAgent(ctx context.Context, agentID string) ([]string, error) {
	return f.resetScanIDs, nil
}

func (f *fakeStore) ScanChunkSummary(ctx context.Context, scanID string) (*store.ScanChunkSummary, error) {
	if len(f.summaries) == 0 {
		return &store.ScanChunkSummary{}, nil
	}
	next := f.summaries[0]
	f.summaries = f.summaries[1:]
	return next, nil
}

func (f *fakeStore) ClaimNextScanChunk(ctx context.Context, scanID, agentID string) (*model.ScanChunk, error) {
	if len(f.claims) == 0 {
		return nil, nil
	}
	next := f.claims[0]
	f.claims = f.claims[1:]
	return next, nil
}

func (f *fakeStore) AckScanChunkStarted(ctx context.Context, chunkID string) error {
	return nil
}

func (f *fakeStore) ResetScanChunkToPending(ctx context.Context, chunkID string) error {
	f.resetChunks = append(f.resetChunks, chunkID)
	return nil
}

func (f *fakeStore) CompleteScanChunk(ctx context.Context, chunkID string, assetsFound, hostsScanned int) (bool, error) {
	return true, nil
}

func (f *fakeStore) FailScanChunk(ctx context.Context, chunkID, reason string) (bool, error) {
	return true, nil
}

func (f *fakeStore) GetScanByID(ctx context.Context, id string) (*model.Scan, error) {
	if f.scans == nil {
		return nil, nil
	}
	return f.scans[id], nil
}

func (f *fakeStore) SetScanDefinitionLastRun(ctx context.Context, id string, at time.Time, status string) error {
	if f.lastRun == nil {
		f.lastRun = map[string]string{}
	}
	f.lastRun[id] = status
	return nil
}

// TestTickCrashRecovery — if dispatch (CreateScanForDefinition) fails,
// next_run_at has still been advanced inside ClaimDueScanDefinitions
// so the definition does not wedge in a perpetually-due state, and
// last_run_status records the failure. This matches ADR 007 D4's
// "lose a tick, not a definition" invariant.
func TestTickCrashRecovery(t *testing.T) {
	cron := "*/5 * * * *"
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	endpointID := "ep-1"
	def := model.ScanDefinition{
		ID:              "def-1",
		TenantID:        "t-1",
		Kind:            model.ScanDefinitionKindCompliance,
		ScopeKind:       model.ScanDefinitionScopeAssetEndpoint,
		AssetEndpointID: &endpointID,
		Schedule:        &cron,
		Enabled:         true,
		NextRunAt:       &now,
	}
	f := &fakeStore{
		claimed:   []model.ScanDefinition{def},
		createErr: errors.New("boom"),
	}
	s := &Scheduler{D: Dispatcher{Store: f}, Interval: time.Minute}
	s.Tick(context.Background())

	if f.createCalls != 1 {
		t.Fatalf("CreateScanForDefinition calls: got %d want 1", f.createCalls)
	}
	gotNext, ok := f.nextRunAt["def-1"]
	if !ok {
		t.Fatal("next_run_at never advanced; scheduler would re-fire forever")
	}
	if !gotNext.After(now) {
		t.Errorf("next_run_at=%v did not advance past now=%v", gotNext, now)
	}
	if got := f.lastRun["def-1"]; got != "failed" {
		t.Errorf("last_run_status: got %q want 'failed'", got)
	}
}

// TestExecuteAgentAllowlistScope verifies an agent_allowlist-scope definition
// resolves the agent's reported allowlist snapshot and dispatches one scan per
// allow entry, as-is, against the agent (ADR 013 D4).
func TestExecuteAgentAllowlistScope(t *testing.T) {
	agent := "agent-1"
	bundle := "bundle-discovery"
	def := model.ScanDefinition{
		ID:        "def-al",
		TenantID:  "t-1",
		Kind:      model.ScanDefinitionKindDiscovery,
		ScopeKind: model.ScanDefinitionScopeAgentAllowlist,
		AgentID:   &agent,
		BundleID:  &bundle,
		Enabled:   true,
	}
	f := &fakeStore{
		allowlistSnap: &store.AgentAllowlistSnapshot{
			AgentID: agent,
			Hash:    "h1",
			// Mixed forms + a blank entry that must be skipped.
			Allow: []string{"10.0.0.0/24", "192.168.5.10", " ", "host.example.com"},
		},
	}
	d := Dispatcher{Store: f}
	if err := d.Execute(context.Background(), def); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// 3 real entries -> one parent scan with 3 chunks (blank skipped).
	if len(f.cidrUpserts) != 0 {
		t.Fatalf("UpsertTargetByCIDR calls: got %d want 0", len(f.cidrUpserts))
	}
	if f.createCalls != 1 {
		t.Fatalf("CreateScanForDefinition calls: got %d want 1", f.createCalls)
	}
	if len(f.chunks) != 3 {
		t.Fatalf("CreateScanChunks count: got %d want 3", len(f.chunks))
	}
	got := []string{f.chunks[0].TargetIdentifier, f.chunks[1].TargetIdentifier, f.chunks[2].TargetIdentifier}
	want := []string{"10.0.0.0/24", "192.168.5.10", "host.example.com"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %q want %q", i, got[i], want[i])
		}
		if f.chunks[i].AgentID == nil || *f.chunks[i].AgentID != agent {
			t.Errorf("entry %d agent: got %v want %q", i, f.chunks[i].AgentID, agent)
		}
	}
}

func TestExecuteDNSListScope(t *testing.T) {
	agent := "agent-1"
	def := model.ScanDefinition{
		ID:        "def-dns",
		TenantID:  "t-1",
		Kind:      model.ScanDefinitionKindDiscovery,
		ScopeKind: model.ScanDefinitionScopeDNSList,
		AgentID:   &agent,
		Enabled:   true,
	}
	f := &fakeStore{httpNames: []string{"app.example.com", "api.example.com"}}
	d := Dispatcher{Store: f}
	if err := d.Execute(context.Background(), def); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// One parent scan with one chunk per imported name.
	if len(f.cidrUpserts) != 0 || f.createCalls != 1 {
		t.Fatalf("got upserts=%d createCalls=%d, want 0/1", len(f.cidrUpserts), f.createCalls)
	}
	if len(f.chunks) != 2 {
		t.Fatalf("CreateScanChunks count: got %d want 2", len(f.chunks))
	}
	want := []string{"app.example.com", "api.example.com"}
	for i, w := range want {
		if f.chunks[i].TargetIdentifier != w {
			t.Errorf("dispatch %d: got %q want %q", i, f.chunks[i].TargetIdentifier, w)
		}
		if f.chunks[i].AgentID == nil || *f.chunks[i].AgentID != agent {
			t.Errorf("dispatch %d agent: got %v want %q", i, f.chunks[i].AgentID, agent)
		}
	}
}

// TestExecuteDNSListScopeBlocksOnEmpty: nothing imported must block, not scan nothing.
func TestExecuteDNSListScopeBlocksOnEmpty(t *testing.T) {
	agent := "agent-1"
	def := model.ScanDefinition{
		ID: "def-dns-empty", TenantID: "t-1",
		Kind: model.ScanDefinitionKindDiscovery, ScopeKind: model.ScanDefinitionScopeDNSList,
		AgentID: &agent, Enabled: true,
	}
	f := &fakeStore{httpNames: nil}
	d := Dispatcher{Store: f}
	if err := d.Execute(context.Background(), def); err == nil {
		t.Fatal("expected a blocking error, got nil")
	}
	if f.createCalls != 0 || len(f.cidrUpserts) != 0 {
		t.Errorf("must not dispatch: createCalls=%d upserts=%d", f.createCalls, len(f.cidrUpserts))
	}
}

func TestChunkedDiscoveryResumeFlow(t *testing.T) {
	agent := "agent-1"
	scanID := "scan-1"
	chunkID := "chunk-2"
	cases := []struct {
		name string
	}{
		{"disconnect mid-chunk resets redispatches and parent completes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{
				resetScanIDs: []string{scanID},
				scans: map[string]*model.Scan{
					scanID: {
						ID:       scanID,
						TenantID: "tenant-1",
						AgentID:  &agent,
						BundleID: strPtr("bundle-discovery"),
						ScanType: model.ScanTypeDiscovery,
						Status:   model.ScanStatusRunning,
					},
				},
				summaries: []*store.ScanChunkSummary{
					{Total: 2, Pending: 1, Completed: 1},
					{Total: 2, Completed: 2},
				},
				claims: []*model.ScanChunk{
					{
						ID:               chunkID,
						ScanID:           scanID,
						TenantID:         "tenant-1",
						AgentID:          &agent,
						ChunkIndex:       1,
						TargetType:       model.TargetTypeCIDR,
						TargetIdentifier: "10.0.4.0/22",
					},
					nil,
				},
			}
			pub := &fakePublisher{}
			d := Dispatcher{Store: f, PubSub: pub}

			reset, err := f.ResetRunningScanChunksForAgent(context.Background(), agent)
			if err != nil {
				t.Fatalf("ResetRunningScanChunksForAgent: %v", err)
			}
			if len(reset) != 1 || reset[0] != scanID {
				t.Fatalf("reset scan IDs = %v, want [%s]", reset, scanID)
			}
			published, err := d.DispatchNextChunk(context.Background(), agent, scanID)
			if err != nil {
				t.Fatalf("DispatchNextChunk after reset: %v", err)
			}
			if !published {
				t.Fatal("DispatchNextChunk after reset did not publish")
			}
			if len(pub.directives) != 1 {
				t.Fatalf("published directives = %d, want 1", len(pub.directives))
			}
			got := pub.directives[0]
			if got.ScanID != scanID || got.ChunkID != chunkID || got.ChunkIndex != 1 || got.ChunkTotal != 2 {
				t.Fatalf("directive chunk metadata = scan:%q chunk:%q idx:%d total:%d",
					got.ScanID, got.ChunkID, got.ChunkIndex, got.ChunkTotal)
			}
			if got.TargetIdentifier != "10.0.4.0/22" {
				t.Fatalf("directive target = %q, want 10.0.4.0/22", got.TargetIdentifier)
			}

			published, err = d.DispatchNextChunk(context.Background(), agent, scanID)
			if err != nil {
				t.Fatalf("DispatchNextChunk after completion: %v", err)
			}
			if published {
				t.Fatal("DispatchNextChunk after all chunks complete unexpectedly published")
			}
			if len(f.statusUpdates) != 1 {
				t.Fatalf("status updates = %d, want 1", len(f.statusUpdates))
			}
			if f.statusUpdates[0].scanID != scanID || f.statusUpdates[0].status != model.ScanStatusCompleted {
				t.Fatalf("status update = %+v, want completed for %s", f.statusUpdates[0], scanID)
			}
		})
	}
}

func TestDispatchNextChunkResetsClaimOnPublishError(t *testing.T) {
	agent := "agent-1"
	scanID := "scan-1"
	chunkID := "chunk-1"
	f := &fakeStore{
		scans: map[string]*model.Scan{
			scanID: {ID: scanID, TenantID: "tenant-1", AgentID: &agent, ScanType: model.ScanTypeDiscovery, Status: model.ScanStatusRunning},
		},
		summaries: []*store.ScanChunkSummary{{Total: 1, Pending: 1}},
		claims: []*model.ScanChunk{{
			ID:               chunkID,
			ScanID:           scanID,
			TenantID:         "tenant-1",
			AgentID:          &agent,
			TargetType:       model.TargetTypeCIDR,
			TargetIdentifier: "10.0.0.0/24",
		}},
	}
	d := Dispatcher{Store: f, PubSub: &fakePublisher{err: errors.New("publish down")}}
	if _, err := d.DispatchNextChunk(context.Background(), agent, scanID); err == nil {
		t.Fatal("DispatchNextChunk expected publish error, got nil")
	}
	if len(f.resetChunks) != 1 || f.resetChunks[0] != chunkID {
		t.Fatalf("reset chunks = %v, want [%s]", f.resetChunks, chunkID)
	}
}

func TestTickReconcilesActiveChunkedParentWithoutRunningChunk(t *testing.T) {
	agent := "agent-1"
	scanID := "scan-1"
	chunkID := "chunk-2"
	f := &fakeStore{
		parents: []store.ChunkedParent{{ScanID: scanID, AgentID: agent}},
		scans: map[string]*model.Scan{
			scanID: {ID: scanID, TenantID: "tenant-1", AgentID: &agent, ScanType: model.ScanTypeDiscovery, Status: model.ScanStatusRunning},
		},
		summaries: []*store.ScanChunkSummary{{Total: 2, Pending: 1, Completed: 1}},
		claims: []*model.ScanChunk{{
			ID:               chunkID,
			ScanID:           scanID,
			TenantID:         "tenant-1",
			AgentID:          &agent,
			ChunkIndex:       1,
			TargetType:       model.TargetTypeCIDR,
			TargetIdentifier: "10.0.4.0/22",
		}},
	}
	pub := &fakePublisher{}
	s := &Scheduler{D: Dispatcher{Store: f, PubSub: pub}, Interval: time.Minute}
	s.Tick(context.Background())
	if len(pub.directives) != 1 {
		t.Fatalf("published directives = %d, want 1", len(pub.directives))
	}
	if got := pub.directives[0]; got.ScanID != scanID || got.ChunkID != chunkID {
		t.Fatalf("published directive = %+v, want scan %s chunk %s", got, scanID, chunkID)
	}
}

func TestTickResetsUnackedChunksBeforeReconcile(t *testing.T) {
	agent := "agent-1"
	scanID := "scan-1"
	chunkID := "chunk-1"
	f := &fakeStore{
		unackedResets: []string{scanID},
		parents:       []store.ChunkedParent{{ScanID: scanID, AgentID: agent}},
		scans: map[string]*model.Scan{
			scanID: {ID: scanID, TenantID: "tenant-1", AgentID: &agent, ScanType: model.ScanTypeDiscovery, Status: model.ScanStatusRunning},
		},
		summaries: []*store.ScanChunkSummary{{Total: 1, Pending: 1}},
		claims: []*model.ScanChunk{{
			ID:               chunkID,
			ScanID:           scanID,
			TenantID:         "tenant-1",
			AgentID:          &agent,
			TargetType:       model.TargetTypeCIDR,
			TargetIdentifier: "10.0.0.0/24",
		}},
	}
	pub := &fakePublisher{}
	s := &Scheduler{D: Dispatcher{Store: f, PubSub: pub}, Interval: time.Minute}
	s.Tick(context.Background())
	if len(pub.directives) != 1 || pub.directives[0].ChunkID != chunkID {
		t.Fatalf("published directives = %+v, want chunk %s", pub.directives, chunkID)
	}
}

func TestDispatchNextChunkDoesNotTerminalizeFailedParent(t *testing.T) {
	agent := "agent-1"
	scanID := "scan-1"
	f := &fakeStore{
		scans: map[string]*model.Scan{
			scanID: {ID: scanID, TenantID: "tenant-1", AgentID: &agent, ScanType: model.ScanTypeDiscovery, Status: model.ScanStatusFailed},
		},
		summaries: []*store.ScanChunkSummary{{Total: 1, Completed: 1}},
		claims:    []*model.ScanChunk{nil},
	}
	d := Dispatcher{Store: f, PubSub: &fakePublisher{}}
	published, err := d.DispatchNextChunk(context.Background(), agent, scanID)
	if err != nil {
		t.Fatalf("DispatchNextChunk: %v", err)
	}
	if published {
		t.Fatal("DispatchNextChunk unexpectedly published")
	}
	if len(f.statusUpdates) != 0 {
		t.Fatalf("status updates = %+v, want none", f.statusUpdates)
	}
}

// TestExecuteAgentAllowlistScopeBlocksOnMissingSnapshot verifies the fail-safe:
// no snapshot (or empty) must block the run, never dispatch a broad scan.
func TestExecuteAgentAllowlistScopeBlocksOnMissingSnapshot(t *testing.T) {
	agent := "agent-1"
	def := model.ScanDefinition{
		ID:        "def-al-empty",
		TenantID:  "t-1",
		Kind:      model.ScanDefinitionKindDiscovery,
		ScopeKind: model.ScanDefinitionScopeAgentAllowlist,
		AgentID:   &agent,
		Enabled:   true,
	}
	for _, tc := range []struct {
		name string
		snap *store.AgentAllowlistSnapshot
	}{
		{"nil snapshot", nil},
		{"empty allow", &store.AgentAllowlistSnapshot{AgentID: agent, Allow: []string{" "}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{allowlistSnap: tc.snap}
			d := Dispatcher{Store: f}
			err := d.Execute(context.Background(), def)
			if err == nil {
				t.Fatal("expected a blocking error, got nil")
			}
			if f.createCalls != 0 || len(f.cidrUpserts) != 0 {
				t.Errorf("must not dispatch: createCalls=%d upserts=%d", f.createCalls, len(f.cidrUpserts))
			}
		})
	}
}

// TestExecuteCIDRScope verifies CIDR-scope scan_definitions materialize
// a targets row via UpsertTargetByCIDR and dispatch a scan referencing
// that target_id. Without this wiring the CIDR-scope branch silently
// skipped, which was the pre-fix bug this PR closes.
func TestExecuteCIDRScope(t *testing.T) {
	cidr := "192.168.0.0/24"
	agent := "agent-1"
	bundle := "bundle-discovery"
	def := model.ScanDefinition{
		ID:        "def-cidr",
		TenantID:  "t-1",
		Kind:      model.ScanDefinitionKindDiscovery,
		ScopeKind: model.ScanDefinitionScopeCIDR,
		CIDR:      &cidr,
		AgentID:   &agent,
		BundleID:  &bundle,
		Enabled:   true,
	}
	f := &fakeStore{}
	// PubSub=nil means dispatchOne creates the scan row but skips
	// PublishDirective — that's fine for this test; we're checking the
	// store side of the wiring. The agent-connected path is exercised
	// by the e2e smoketest.
	d := Dispatcher{Store: f}
	if err := d.Execute(context.Background(), def); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(f.cidrUpserts) != 1 {
		t.Fatalf("UpsertTargetByCIDR calls: got %d want 1", len(f.cidrUpserts))
	}
	up := f.cidrUpserts[0]
	if up.TenantID != "t-1" || up.CIDR != cidr {
		t.Errorf("UpsertTargetByCIDR tenant/cidr: got %q/%q", up.TenantID, up.CIDR)
	}
	if up.AgentID == nil || *up.AgentID != agent {
		t.Errorf("UpsertTargetByCIDR agent: got %v want %q", up.AgentID, agent)
	}
	if f.createCalls != 1 {
		t.Fatalf("CreateScanForDefinition calls: got %d want 1", f.createCalls)
	}
	in := f.createInputs[0]
	if in.TargetID == nil || *in.TargetID != "target-cidr-1" {
		t.Errorf("scan.target_id: got %v want target-cidr-1", in.TargetID)
	}
	if in.ScanType != model.ScanTypeDiscovery {
		t.Errorf("scan.scan_type: got %q want %q", in.ScanType, model.ScanTypeDiscovery)
	}
}

// TestExecuteCIDRScopeMissingCIDR — the scan_definitions CHECK enforces
// a non-null cidr for scope=cidr, but the dispatcher should also refuse
// to create an orphan scan row if the value is somehow empty.
func TestExecuteCIDRScopeMissingCIDR(t *testing.T) {
	agent := "agent-1"
	def := model.ScanDefinition{
		ID:        "def-cidr",
		TenantID:  "t-1",
		Kind:      model.ScanDefinitionKindDiscovery,
		ScopeKind: model.ScanDefinitionScopeCIDR,
		CIDR:      nil,
		AgentID:   &agent,
	}
	f := &fakeStore{}
	d := Dispatcher{Store: f}
	if err := d.Execute(context.Background(), def); err == nil {
		t.Fatal("Execute: expected error for missing cidr, got nil")
	}
	if len(f.cidrUpserts) != 0 {
		t.Errorf("UpsertTargetByCIDR should not be called; got %d", len(f.cidrUpserts))
	}
	if f.createCalls != 0 {
		t.Errorf("CreateScanForDefinition should not be called; got %d", f.createCalls)
	}
}

// TestExecuteCIDRScopeMissingAgent — a CIDR-scope definition without an
// agent cannot dispatch (forwardDirective needs an agent to send to).
// Refuse early rather than create a zombie scan row.
func TestExecuteCIDRScopeMissingAgent(t *testing.T) {
	cidr := "10.0.0.0/24"
	def := model.ScanDefinition{
		ID:        "def-cidr",
		TenantID:  "t-1",
		Kind:      model.ScanDefinitionKindDiscovery,
		ScopeKind: model.ScanDefinitionScopeCIDR,
		CIDR:      &cidr,
		AgentID:   nil,
	}
	f := &fakeStore{}
	d := Dispatcher{Store: f}
	if err := d.Execute(context.Background(), def); err == nil {
		t.Fatal("Execute: expected error for missing agent, got nil")
	}
	if len(f.cidrUpserts) != 0 {
		t.Errorf("UpsertTargetByCIDR should not be called; got %d", len(f.cidrUpserts))
	}
}

func strPtr(s string) *string {
	return &s
}
