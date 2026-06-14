package recon

import (
	"context"
	"errors"
	"testing"
)

// TestVulnCtxErr: the output-cap cancel is "success" (the pass stopped on
// purpose), but a PARENT cancellation must propagate — otherwise a cancelled
// scan's vuln pass reports done and the pipeline keeps running (hero #8477).
func TestVulnCtxErr(t *testing.T) {
	if err := vulnCtxErr(context.Background()); err != nil {
		t.Errorf("uncanceled ctx → %v, want nil", err)
	}
	capCtx, capCancel := context.WithCancelCause(context.Background())
	capCancel(errVulnOutputCap)
	if err := vulnCtxErr(capCtx); err != nil {
		t.Errorf("output-cap cancel → %v, want nil", err)
	}
	parentCtx, parentCancel := context.WithCancel(context.Background())
	parentCancel()
	if err := vulnCtxErr(parentCtx); !errors.Is(err, context.Canceled) {
		t.Errorf("parent cancel → %v, want context.Canceled (must propagate)", err)
	}
}

// TestCuratedVulnDirs is the regression guard for the ADR 019 P2 / hero #404
// pin-by-explicit-path requirement: the vuln pass selects ONLY the curated vuln
// dirs and NEVER the active/intrusive/noisy categories or detection (P1's). A
// future broad `network/` grab would fail this.
func TestCuratedVulnDirs(t *testing.T) {
	want := map[string]bool{"cves": true, "exposures": true, "misconfig": true, "vulnerabilities": true}
	got := map[string]bool{}
	for _, d := range curatedVulnDirs {
		got[d] = true
	}
	if len(got) != len(want) {
		t.Fatalf("curatedVulnDirs = %v, want exactly %d entries", curatedVulnDirs, len(want))
	}
	for d := range want {
		if !got[d] {
			t.Errorf("curated vuln set missing %q", d)
		}
	}
	// Active/intrusive/noisy categories + detection must never be selected.
	for _, banned := range []string{"c2", "default-login", "enumeration", "honeypot", "jarm", "backdoor", "detection"} {
		if got[banned] {
			t.Errorf("curated vuln set must NOT include %q (active/intrusive/detection)", banned)
		}
	}
}

// TestVulnCapsBounded guards that the D4 resource caps are all positive — an
// unbounded (<=0) cap would defeat the OOM bound.
func TestVulnCapsBounded(t *testing.T) {
	caps := map[string]int{
		"vulnMaxTargets":  vulnMaxTargets,
		"vulnMaxFindings": vulnMaxFindings,
		"vulnConcurrency": vulnConcurrency,
		"vulnBulkSize":    vulnBulkSize,
		"vulnRateLimit":   vulnRateLimit,
		"vulnTimeoutSec":  vulnTimeoutSec,
		"vulnRetries":     vulnRetries,
		"vulnMaxHostErr":  vulnMaxHostErr,
	}
	for name, v := range caps {
		if v <= 0 {
			t.Errorf("cap %s = %d, must be > 0 (bounded)", name, v)
		}
	}
}
