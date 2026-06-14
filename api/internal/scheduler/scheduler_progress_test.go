package scheduler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jtb75/silkstrand/api/internal/events"
	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/store"
)

// FailScan is only exercised by the chunked-parent failure path (#387); the
// other scheduler tests never reach it, so it lives here with that test.
func (f *fakeStore) FailScan(_ context.Context, _, _ string) error { return nil }

type captureBus struct{ published []events.Event }

func (b *captureBus) Publish(_ context.Context, e events.Event) error {
	b.published = append(b.published, e)
	return nil
}
func (b *captureBus) Subscribe(_ context.Context, _ events.Filter) (<-chan events.Event, func()) {
	return nil, func() {}
}

func (b *captureBus) progressEvents() []model.ScanProgress {
	var out []model.ScanProgress
	for _, e := range b.published {
		if e.Kind != "scan_progress" {
			continue
		}
		var p model.ScanProgress
		if err := json.Unmarshal(e.Payload, &p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// The parent terminal scan_progress must fire exactly once, from the
// DispatchNextChunk transition — not the WSS handler — so the drawer never sees
// a duplicate scan_completed/scan_failed (#387).
func TestDispatchNextChunkEmitsParentTerminal(t *testing.T) {
	const scanID, agent = "scan-1", "agent-1"
	runningDiscovery := map[string]*model.Scan{
		scanID: {ID: scanID, TenantID: "t-1", AgentID: strPtr(agent), ScanType: model.ScanTypeDiscovery, Status: model.ScanStatusRunning},
	}

	t.Run("all chunks complete -> one scan_completed", func(t *testing.T) {
		f := &fakeStore{
			scans:     runningDiscovery,
			summaries: []*store.ScanChunkSummary{{Total: 1, Completed: 1}},
			claims:    []*model.ScanChunk{nil}, // nothing left to claim
		}
		bus := &captureBus{}
		d := Dispatcher{Store: f, PubSub: &fakePublisher{}, Bus: bus}

		published, err := d.DispatchNextChunk(context.Background(), agent, scanID)
		if err != nil {
			t.Fatalf("DispatchNextChunk: %v", err)
		}
		if published {
			t.Fatal("expected no chunk published at terminal")
		}
		prog := bus.progressEvents()
		if len(prog) != 1 {
			t.Fatalf("scan_progress events = %d, want 1 (no dupes)", len(prog))
		}
		if prog[0].Event != model.ScanProgressScanCompleted {
			t.Errorf("event = %q, want scan_completed", prog[0].Event)
		}
		if prog[0].ChunksTotal != 1 || prog[0].ChunksCompleted != 1 {
			t.Errorf("rollup = %d/%d, want 1/1", prog[0].ChunksTotal, prog[0].ChunksCompleted)
		}
	})

	t.Run("retries exhausted -> one scan_failed", func(t *testing.T) {
		f := &fakeStore{
			scans:     runningDiscovery,
			summaries: []*store.ScanChunkSummary{{Total: 1, Failed: 1}},
			claims:    []*model.ScanChunk{nil},
		}
		bus := &captureBus{}
		d := Dispatcher{Store: f, PubSub: &fakePublisher{}, Bus: bus}

		if _, err := d.DispatchNextChunk(context.Background(), agent, scanID); err != nil {
			t.Fatalf("DispatchNextChunk: %v", err)
		}
		prog := bus.progressEvents()
		if len(prog) != 1 {
			t.Fatalf("scan_progress events = %d, want 1", len(prog))
		}
		if prog[0].Event != model.ScanProgressScanFailed {
			t.Errorf("event = %q, want scan_failed", prog[0].Event)
		}
		if prog[0].ChunksFailed != 1 {
			t.Errorf("chunks_failed = %d, want 1", prog[0].ChunksFailed)
		}
	})
}
