package store

import (
	"context"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/jtb75/silkstrand/api/internal/model"
)

// newTestStore connects to TEST_DATABASE_URL and applies all migrations.
// Skips when TEST_DATABASE_URL is unset (e.g. local `go test` without a DB);
// CI provides a Postgres service so this runs there. Real-DB coverage exists
// because the scheduler's fakeStore can't catch SQL bugs (e.g. the ambiguous
// "id" in ClaimNextScanChunk that shipped past unit tests + 3 review passes).
func newTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping store integration test")
	}
	st, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("connecting to test db: %v", err)
	}
	t.Cleanup(func() { _ = st.db.Close() })

	src, err := iofs.New(MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("migration source: %v", err)
	}
	drv, err := migratepg.WithInstance(st.db, &migratepg.Config{})
	if err != nil {
		t.Fatalf("migration driver: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "postgres", drv)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}
	return st
}

// seedChunkScan inserts a throwaway tenant/agent/scan plus `n` pending chunks,
// returning (scanID, agentID) and registering cleanup.
func seedChunkScan(t *testing.T, st *PostgresStore, n int) (scanID, agentID string) {
	t.Helper()
	ctx := context.Background()
	const (
		tenantID = "f0f0f0f0-1111-4111-8111-000000000001"
		aID      = "f0f0f0f0-2222-4222-8222-000000000002"
		sID      = "f0f0f0f0-3333-4333-8333-000000000003"
	)
	exec := func(q string, args ...any) {
		if _, err := st.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// Clean any leftover from a prior failed run, then seed.
	exec(`DELETE FROM scans WHERE tenant_id = $1`, tenantID)
	exec(`DELETE FROM agents WHERE tenant_id = $1`, tenantID)
	exec(`DELETE FROM tenants WHERE id = $1`, tenantID)
	exec(`INSERT INTO tenants (id, name) VALUES ($1, 'chunk-claim-test')`, tenantID)
	exec(`INSERT INTO agents (id, tenant_id, name) VALUES ($1, $2, 'chunk-test-agent')`, aID, tenantID)
	// status=running so FailScanChunk's parent-active guard applies (the P2 fix).
	exec(`INSERT INTO scans (id, tenant_id, agent_id, scan_type, status) VALUES ($1, $2, $3, 'discovery', 'running')`, sID, tenantID, aID)

	chunks := make([]CreateScanChunkInput, 0, n)
	ag := aID
	for i := 0; i < n; i++ {
		chunks = append(chunks, CreateScanChunkInput{
			ScanID:           sID,
			TenantID:         tenantID,
			AgentID:          &ag,
			ChunkIndex:       i,
			TargetType:       model.TargetTypeCIDR,
			TargetIdentifier: "10.0.0.0/24",
		})
	}
	if err := st.CreateScanChunks(ctx, chunks); err != nil {
		t.Fatalf("CreateScanChunks: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.db.ExecContext(ctx, `DELETE FROM scans WHERE tenant_id = $1`, tenantID)
		_, _ = st.db.ExecContext(ctx, `DELETE FROM agents WHERE tenant_id = $1`, tenantID)
		_, _ = st.db.ExecContext(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID)
	})
	return sID, aID
}

// TestClaimNextScanChunk is the real-Postgres regression test for the
// UPDATE ... FROM next ... RETURNING ambiguous-"id" bug (SQLSTATE 42702):
// the CTE and scan_chunks both expose `id`, so a bare RETURNING failed at
// runtime — invisible to the fakeStore scheduler tests. It claims chunks in
// chunk_index order, transitions each to running, and returns nil when drained.
func TestClaimNextScanChunk(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	scanID, agentID := seedChunkScan(t, st, 3)

	for want := 0; want < 3; want++ {
		c, err := st.ClaimNextScanChunk(ctx, scanID, agentID)
		if err != nil {
			t.Fatalf("ClaimNextScanChunk #%d (regression — ambiguous id?): %v", want, err)
		}
		if c == nil {
			t.Fatalf("ClaimNextScanChunk #%d: got nil, want chunk %d", want, want)
		}
		if c.ChunkIndex != want {
			t.Errorf("claim #%d: chunk_index = %d, want %d", want, c.ChunkIndex, want)
		}
		if c.Status != model.ScanChunkStatusRunning {
			t.Errorf("claim #%d: status = %q, want running", want, c.Status)
		}
		if c.DispatchedAt == nil {
			t.Errorf("claim #%d: dispatched_at not stamped (lease missing)", want)
		}
	}

	// All chunks now running → nothing left to claim.
	last, err := st.ClaimNextScanChunk(ctx, scanID, agentID)
	if err != nil {
		t.Fatalf("ClaimNextScanChunk (drained): %v", err)
	}
	if last != nil {
		t.Errorf("expected nil when no pending chunks, got chunk_index %d", last.ChunkIndex)
	}
}

// TestClaimNextScanChunkRetriesFailed verifies the bounded-retry predicate:
// a failed chunk with attempts < 3 is re-claimable; once it hits the cap it
// is not. Also a real-SQL exercise of the same UPDATE...FROM...RETURNING.
func TestClaimNextScanChunkRetriesFailed(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	scanID, agentID := seedChunkScan(t, st, 1)

	c, err := st.ClaimNextScanChunk(ctx, scanID, agentID)
	if err != nil || c == nil {
		t.Fatalf("initial claim: c=%v err=%v", c, err)
	}
	// Fail it twice (attempts -> 2): still re-claimable.
	for i := 0; i < 2; i++ {
		if _, err := st.FailScanChunk(ctx, c.ID, "boom"); err != nil {
			t.Fatalf("FailScanChunk: %v", err)
		}
		again, err := st.ClaimNextScanChunk(ctx, scanID, agentID)
		if err != nil {
			t.Fatalf("re-claim after fail %d: %v", i, err)
		}
		if again == nil {
			t.Fatalf("re-claim after fail %d: got nil, expected the failed chunk (attempts<3)", i)
		}
	}
	// Third failure -> attempts == 3 -> no longer claimable.
	if _, err := st.FailScanChunk(ctx, c.ID, "boom"); err != nil {
		t.Fatalf("FailScanChunk (3rd): %v", err)
	}
	drained, err := st.ClaimNextScanChunk(ctx, scanID, agentID)
	if err != nil {
		t.Fatalf("claim after max attempts: %v", err)
	}
	if drained != nil {
		t.Errorf("expected nil after 3 failed attempts, got chunk %d", drained.ChunkIndex)
	}
}
