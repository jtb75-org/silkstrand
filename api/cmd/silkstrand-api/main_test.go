package main

import "testing"

// TestStageEmitsFindings is the regression guard for ADR 019 P1: the
// nuclei-network detection pass is backfill-only and must never create findings
// (vulns are P2/#377); every other stage may. Extracted as a pure predicate so
// it's testable without standing up a handleAssetDiscovered harness.
func TestStageEmitsFindings(t *testing.T) {
	if stageEmitsFindings(stageNucleiNetwork) {
		t.Errorf("stage %q must NOT emit findings (detection is backfill-only)", stageNucleiNetwork)
	}
	for _, stage := range []string{"naabu", "httpx", "nuclei", ""} {
		if !stageEmitsFindings(stage) {
			t.Errorf("stage %q should emit findings", stage)
		}
	}
}
