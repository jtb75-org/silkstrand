package store

import (
	"context"
	"testing"
)

// chunkTestTenantID mirrors the tenant seedChunkScan inserts (kept in sync with
// that helper in postgres_chunks_test.go). Used to scope tenant-aware reads.
const chunkTestTenantID = "f0f0f0f0-1111-4111-8111-000000000001"

// TestListScanChunks covers the scan-detail drawer's initial state (#387):
// chunks come back ordered by chunk_index, current_stage starts nil, and the
// read is tenant-scoped.
func TestListScanChunks(t *testing.T) {
	st := newTestStore(t)
	ctx := WithTenantID(context.Background(), chunkTestTenantID)
	scanID, _ := seedChunkScan(t, st, 3)

	chunks, err := st.ListScanChunks(ctx, scanID)
	if err != nil {
		t.Fatalf("ListScanChunks: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3", len(chunks))
	}
	for i, c := range chunks {
		if c.ChunkIndex != i {
			t.Errorf("chunk[%d].ChunkIndex = %d, want %d (must be index-ordered)", i, c.ChunkIndex, i)
		}
		if c.CurrentStage != nil {
			t.Errorf("chunk[%d].CurrentStage = %q, want nil before any stage", i, *c.CurrentStage)
		}
	}

	// Tenant isolation: a different tenant sees none of these chunks.
	other := WithTenantID(context.Background(), "00000000-0000-4000-8000-000000000999")
	got, err := st.ListScanChunks(other, scanID)
	if err != nil {
		t.Fatalf("cross-tenant ListScanChunks: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("cross-tenant ListScanChunks returned %d chunks, want 0", len(got))
	}
}

// TestUpdateScanChunkStage is the stage_progress dedupe primitive (#387): for a
// RUNNING chunk a stage change returns true (emit), a repeat returns false
// (skip), the latest stage persists for the REST snapshot / reconnect, and once
// the chunk is terminal a late batch can neither change it nor report a change
// (hero's Finding 1 — no stale "running" stage_progress after the terminal).
func TestUpdateScanChunkStage(t *testing.T) {
	st := newTestStore(t)
	ctx := WithTenantID(context.Background(), chunkTestTenantID)
	scanID, agentID := seedChunkScan(t, st, 1)

	// Stage updates only apply to a running chunk → claim it first.
	c, err := st.ClaimNextScanChunk(ctx, scanID, agentID)
	if err != nil || c == nil {
		t.Fatalf("ClaimNextScanChunk: chunk=%v err=%v", c, err)
	}
	chunkID := c.ID

	if changed, err := st.UpdateScanChunkStage(ctx, scanID, chunkID, "naabu"); err != nil || !changed {
		t.Fatalf("first stage set: changed=%v err=%v, want changed=true", changed, err)
	}
	if changed, err := st.UpdateScanChunkStage(ctx, scanID, chunkID, "naabu"); err != nil || changed {
		t.Fatalf("repeat same stage: changed=%v err=%v, want changed=false", changed, err)
	}
	if changed, err := st.UpdateScanChunkStage(ctx, scanID, chunkID, "httpx"); err != nil || !changed {
		t.Fatalf("stage transition: changed=%v err=%v, want changed=true", changed, err)
	}

	chunks, err := st.ListScanChunks(ctx, scanID)
	if err != nil {
		t.Fatalf("ListScanChunks after stage updates: %v", err)
	}
	if chunks[0].CurrentStage == nil || *chunks[0].CurrentStage != "httpx" {
		t.Errorf("persisted current_stage = %v, want httpx", chunks[0].CurrentStage)
	}

	// Once terminal, a late asset_discovered batch must not rewrite the stage
	// or report a change (would otherwise emit a misleading running update).
	if _, err := st.CompleteScanChunk(ctx, chunkID, 0, 0); err != nil {
		t.Fatalf("CompleteScanChunk: %v", err)
	}
	if changed, err := st.UpdateScanChunkStage(ctx, scanID, chunkID, "nuclei"); err != nil || changed {
		t.Fatalf("stage update after completion: changed=%v err=%v, want changed=false", changed, err)
	}
	chunks, _ = st.ListScanChunks(ctx, scanID)
	if chunks[0].CurrentStage == nil || *chunks[0].CurrentStage != "httpx" {
		t.Errorf("current_stage after terminal = %v, want unchanged httpx", chunks[0].CurrentStage)
	}
}
