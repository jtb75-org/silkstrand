package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/store"
)

// fakeScanStore implements just the two reads ScanHandler.Get touches; the
// embedded nil store.Store panics if anything else is unexpectedly called.
type fakeScanStore struct {
	store.Store
	scan      *model.Scan
	chunks    []model.ScanChunk
	chunksErr error
}

func (f *fakeScanStore) GetScan(_ context.Context, _ string) (*model.Scan, error) {
	return f.scan, nil
}
func (f *fakeScanStore) ListScanChunks(_ context.Context, _ string) ([]model.ScanChunk, error) {
	return f.chunks, f.chunksErr
}

func getScanReq(h *ScanHandler, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/scans/"+id, nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	return rec
}

// GET /scans/{id} must fail loudly if the chunk list can't load — degrading to
// the implicit single-chunk rollup would hand the UI a different contract shape
// for a real chunked scan (#387, hero's Finding 2).
func TestScanGetChunkListErrorReturns500(t *testing.T) {
	st := &fakeScanStore{
		scan:      &model.Scan{ID: "scan-1", TenantID: "t", ScanType: model.ScanTypeDiscovery, Status: model.ScanStatusRunning},
		chunksErr: errors.New("db down"),
	}
	rec := getScanReq(NewScanHandler(st, nil, nil, nil), "scan-1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (must not synthesize implicit-chunk shape)", rec.Code)
	}
}

// Happy path: ScanDetail returns the chunk rollup computed from the rows.
func TestScanGetReturnsScanDetail(t *testing.T) {
	st := &fakeScanStore{
		scan: &model.Scan{ID: "scan-1", TenantID: "t", ScanType: model.ScanTypeDiscovery, Status: model.ScanStatusRunning},
		chunks: []model.ScanChunk{
			{ID: "c0", ScanID: "scan-1", ChunkIndex: 0, Status: model.ScanChunkStatusCompleted},
			{ID: "c1", ScanID: "scan-1", ChunkIndex: 1, Status: model.ScanChunkStatusRunning},
		},
	}
	rec := getScanReq(NewScanHandler(st, nil, nil, nil), "scan-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		ID              string `json:"id"`
		ChunksTotal     int    `json:"chunks_total"`
		ChunksCompleted int    `json:"chunks_completed"`
		ChunksFailed    int    `json:"chunks_failed"`
		Chunks          []struct {
			ChunkIndex int `json:"chunk_index"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode ScanDetail: %v", err)
	}
	if got.ID != "scan-1" {
		t.Errorf("id = %q (embedded Scan fields must be promoted), want scan-1", got.ID)
	}
	if got.ChunksTotal != 2 || got.ChunksCompleted != 1 || got.ChunksFailed != 0 {
		t.Errorf("rollup = %d/%d/%d, want 2/1/0", got.ChunksTotal, got.ChunksCompleted, got.ChunksFailed)
	}
	if len(got.Chunks) != 2 {
		t.Fatalf("chunks len = %d, want 2", len(got.Chunks))
	}
}
