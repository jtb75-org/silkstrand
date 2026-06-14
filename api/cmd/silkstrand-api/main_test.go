package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/store"
)

// TestStageEmitsFindings is the regression guard for ADR 019: the nuclei-network
// DETECTION pass is backfill-only and must never create findings; the VULN pass
// (and every other stage) may. Pure predicate → testable without a
// handleAssetDiscovered harness.
func TestStageEmitsFindings(t *testing.T) {
	if stageEmitsFindings(stageNucleiNetwork) {
		t.Errorf("stage %q must NOT emit findings (detection is backfill-only)", stageNucleiNetwork)
	}
	for _, stage := range []string{stageNucleiNetworkVuln, "naabu", "httpx", "nuclei", ""} {
		if !stageEmitsFindings(stage) {
			t.Errorf("stage %q should emit findings", stage)
		}
	}
}

// TestIsNetworkSubPass: both sub-passes upsert endpoints fill-only (so neither
// clobbers httpx/detection-owned identity incl. technologies); other stages
// stay incoming-wins.
func TestIsNetworkSubPass(t *testing.T) {
	for _, s := range []string{stageNucleiNetwork, stageNucleiNetworkVuln} {
		if !isNetworkSubPass(s) {
			t.Errorf("stage %q should be a network sub-pass (fill-only)", s)
		}
	}
	for _, s := range []string{"naabu", "httpx", "nuclei", ""} {
		if isNetworkSubPass(s) {
			t.Errorf("stage %q must NOT be fill-only (incoming-wins)", s)
		}
	}
}

// TestFindingSource: network vulns are tagged nuclei-network (distinct from HTTP
// nuclei → distinct upsert-key rows); everything else stays "nuclei".
func TestFindingSource(t *testing.T) {
	if got := findingSource(stageNucleiNetworkVuln); got != "nuclei-network" {
		t.Errorf("vuln stage source = %q, want nuclei-network", got)
	}
	for _, s := range []string{"nuclei", "httpx", stageNucleiNetwork} {
		if got := findingSource(s); got != "nuclei" {
			t.Errorf("stage %q source = %q, want nuclei", s, got)
		}
	}
}

type fakeFindingStore struct {
	store.Store
	inputs []store.UpsertFindingInput
}

func (f *fakeFindingStore) UpsertFinding(_ context.Context, in store.UpsertFindingInput) (*model.Finding, error) {
	f.inputs = append(f.inputs, in)
	return &model.Finding{}, nil
}

// TestIngestNucleiFindingsParsesBothShapes locks the tolerant parser (hero
// #8298): SourceID = template_id → template → id; CVE = cves[] → cve_id →
// CVE-shaped id. Covers the native vuln blob AND the legacy nuclei-HTTP blob,
// and that the source param flows through (the distinct-rows reason).
func TestIngestNucleiFindingsParsesBothShapes(t *testing.T) {
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	tests := []struct {
		name       string
		blob       string
		source     string
		wantSource string
		wantSrcID  string
		wantCVE    string
	}{
		{
			name:       "native vuln blob",
			blob:       `[{"template_id":"network/cves/2021/CVE-2021-1234","cves":["CVE-2021-1234"],"severity":"high","name":"Foo RCE"}]`,
			source:     "nuclei-network",
			wantSource: "nuclei-network", wantSrcID: "network/cves/2021/CVE-2021-1234", wantCVE: "CVE-2021-1234",
		},
		{
			name:       "legacy http blob with CVE id",
			blob:       `[{"id":"CVE-2020-5555","template":"http/cves/x","severity":"medium"}]`,
			source:     "nuclei",
			wantSource: "nuclei", wantSrcID: "http/cves/x", wantCVE: "CVE-2020-5555",
		},
		{
			name:       "legacy http blob, no CVE (id is the template)",
			blob:       `[{"id":"http/exposures/y","template":"http/exposures/y","severity":"info"}]`,
			source:     "nuclei",
			wantSource: "nuclei", wantSrcID: "http/exposures/y", wantCVE: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := &fakeFindingStore{}
			ingestNucleiFindings(context.Background(), fs, "t", "scan", "ep", json.RawMessage(tt.blob), tt.source)
			if len(fs.inputs) != 1 {
				t.Fatalf("UpsertFinding calls = %d, want 1", len(fs.inputs))
			}
			in := fs.inputs[0]
			if in.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", in.Source, tt.wantSource)
			}
			if in.SourceID != tt.wantSrcID {
				t.Errorf("SourceID = %q, want %q", in.SourceID, tt.wantSrcID)
			}
			if str(in.CVEID) != tt.wantCVE {
				t.Errorf("CVEID = %q, want %q", str(in.CVEID), tt.wantCVE)
			}
			if in.SourceKind != model.FindingSourceKindNetworkVuln {
				t.Errorf("SourceKind = %q, want network_vuln", in.SourceKind)
			}
		})
	}
}
