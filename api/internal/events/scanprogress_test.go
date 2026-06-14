package events

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jtb75/silkstrand/api/internal/model"
)

type recordingBus struct{ published []Event }

func (b *recordingBus) Publish(_ context.Context, e Event) error {
	b.published = append(b.published, e)
	return nil
}
func (b *recordingBus) Subscribe(_ context.Context, _ Filter) (<-chan Event, func()) {
	return nil, func() {}
}

func TestPublishScanProgressEnvelope(t *testing.T) {
	b := &recordingBus{}
	PublishScanProgress(context.Background(), b, "tenant-1", model.ScanProgress{
		ScanID:          "scan-1",
		Event:           model.ScanProgressChunkCompleted,
		Status:          model.ScanStatusRunning,
		ChunksTotal:     4,
		ChunksCompleted: 2,
	})
	if len(b.published) != 1 {
		t.Fatalf("published %d events, want 1", len(b.published))
	}
	e := b.published[0]
	if e.Kind != "scan_progress" {
		t.Errorf("kind = %q, want scan_progress", e.Kind)
	}
	if e.ResourceType != "scan" || e.ResourceID != "scan-1" {
		t.Errorf("resource = %s/%s, want scan/scan-1", e.ResourceType, e.ResourceID)
	}
	if e.TenantID != "tenant-1" {
		t.Errorf("tenant = %q, want tenant-1", e.TenantID)
	}
	var p model.ScanProgress
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// scan_id MUST be top-level in the payload — the bus filters per-scan on it.
	if p.ScanID != "scan-1" {
		t.Errorf("payload scan_id = %q, want scan-1", p.ScanID)
	}
	if p.ChunksTotal != 4 || p.ChunksCompleted != 2 {
		t.Errorf("rollup = %d/%d, want 4/2", p.ChunksTotal, p.ChunksCompleted)
	}
}

func TestPublishScanProgressGuards(t *testing.T) {
	b := &recordingBus{}
	PublishScanProgress(context.Background(), b, "", model.ScanProgress{ScanID: "s"})    // no tenant
	PublishScanProgress(context.Background(), b, "t", model.ScanProgress{ScanID: ""})    // no scan id
	PublishScanProgress(context.Background(), nil, "t", model.ScanProgress{ScanID: "s"}) // nil bus
	if len(b.published) != 0 {
		t.Fatalf("guards failed: %d events published, want 0", len(b.published))
	}
}

// The per-scan SSE subscription filters by reading payload.scan_id; confirm the
// new scan_progress kind matches the right scan and respects tenant isolation.
func TestFilterMatchesScanProgressByScanID(t *testing.T) {
	payload, _ := json.Marshal(model.ScanProgress{ScanID: "scan-1", Event: model.ScanProgressStageProgress})
	e := Event{Kind: "scan_progress", TenantID: "t-1", ResourceType: "scan", ResourceID: "scan-1", Payload: payload}

	if !(Filter{TenantID: "t-1", Kinds: []string{"scan_progress"}, ScanID: "scan-1"}).matches(e) {
		t.Error("expected match for scan-1 subscription")
	}
	if (Filter{TenantID: "t-1", Kinds: []string{"scan_progress"}, ScanID: "scan-2"}).matches(e) {
		t.Error("expected no match for a different scan_id")
	}
	if (Filter{TenantID: "t-2", Kinds: []string{"scan_progress"}}).matches(e) {
		t.Error("expected no cross-tenant match")
	}
}
