package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// chunk_completed is the one event carrying authoritative counts, so a chunk
// that completes with 0 hosts/assets must serialize an EXPLICIT zero — not omit
// the field (#387). The *int fields make that distinguishable from the
// "no counts" of every other event, where nil must omit them.
func TestScanProgressChunkCountSerialization(t *testing.T) {
	zero := 0
	completed := ScanProgress{
		ScanID: "s", Event: ScanProgressChunkCompleted,
		Chunk: &ScanProgressChunk{ChunkID: "c", HostsScanned: &zero, AssetsFound: &zero},
	}
	b, err := json.Marshal(completed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, `"hosts_scanned":0`) {
		t.Errorf("authoritative zero dropped: want \"hosts_scanned\":0 in %s", got)
	}
	if !strings.Contains(got, `"assets_found":0`) {
		t.Errorf("authoritative zero dropped: want \"assets_found\":0 in %s", got)
	}

	// Non-completion events leave counts nil → fields omitted ("no counts").
	stage := ScanProgress{
		ScanID: "s", Event: ScanProgressStageProgress,
		Chunk: &ScanProgressChunk{ChunkID: "c", CurrentStage: "naabu"},
	}
	b2, err := json.Marshal(stage)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got2 := string(b2)
	if strings.Contains(got2, "hosts_scanned") || strings.Contains(got2, "assets_found") {
		t.Errorf("nil counts should be omitted on stage_progress, got %s", got2)
	}
}
