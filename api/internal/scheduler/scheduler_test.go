package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jtb75/silkstrand/api/internal/model"
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
}

func (f *fakeStore) GetAgentAllowlist(ctx context.Context, agentID string) (*store.AgentAllowlistSnapshot, error) {
	return f.allowlistSnap, nil
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
	return nil
}

func (f *fakeStore) FailStaleQueuedScans(ctx context.Context, maxAge time.Duration) (int, error) {
	return 0, nil
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
	// 3 real entries → 3 target upserts + 3 scans (blank skipped).
	if len(f.cidrUpserts) != 3 {
		t.Fatalf("UpsertTargetByCIDR calls: got %d want 3", len(f.cidrUpserts))
	}
	if f.createCalls != 3 {
		t.Fatalf("CreateScanForDefinition calls: got %d want 3", f.createCalls)
	}
	got := []string{f.cidrUpserts[0].CIDR, f.cidrUpserts[1].CIDR, f.cidrUpserts[2].CIDR}
	want := []string{"10.0.0.0/24", "192.168.5.10", "host.example.com"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %q want %q", i, got[i], want[i])
		}
		if f.cidrUpserts[i].AgentID == nil || *f.cidrUpserts[i].AgentID != agent {
			t.Errorf("entry %d agent: got %v want %q", i, f.cidrUpserts[i].AgentID, agent)
		}
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
