package recon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jtb75/silkstrand/agent/internal/tunnel"
)

// ADR 019 P2 — network-vuln sub-pass (#377). A SECOND nuclei invocation over
// the same naabu host:port list as the P1 detection pass, but with a CURATED
// set of network vuln template dirs → network_vuln FINDINGS (stage label
// "nuclei-network-vuln", which the server routes to ingestNucleiFindings, not
// the detection backfill). Opt-in (default off); detection stays no-findings.

// curatedVulnDirs are the EXPLICIT network/ subdirs the vuln pass selects
// (hero #404 / kimi #8302: pin by path, never a broad network/ grab). The
// active/intrusive/noisy categories (c2, default-login, enumeration, honeypot,
// jarm, backdoor) and detection/ are deliberately excluded. weak-TLS/ssl is
// curated out of the bundle entirely — deferred to a later re-pin / tlsx.
var curatedVulnDirs = []string{"cves", "exposures", "misconfig", "vulnerabilities"}

// Resource caps (ADR 019 D4 — bound runtime/memory regardless of the sequential
// placement). Conservative constants, re-validate against the agent memory
// budget the way the 2Gi fix was.
const (
	vulnMaxTargets  = 4096 // per-chunk host:port cap (deterministic first-N, loud)
	vulnMaxFindings = 2000 // per-chunk emitted-findings cap → cancel the subprocess
	vulnConcurrency = 25   // -c  template concurrency
	vulnBulkSize    = 25   // -bs hosts in parallel
	vulnRateLimit   = 150  // -rl requests/sec
	vulnTimeoutSec  = 5    // -timeout
	vulnRetries     = 1    // -retries
	vulnMaxHostErr  = 30   // -mhe bail on dead hosts
)

// errVulnOutputCap is the cancel CAUSE set when the vuln pass hits its
// max-findings cap. It lets runNucleiNetworkVuln distinguish "we stopped on
// purpose" (→ success) from a parent-context cancellation (→ propagate).
var errVulnOutputCap = errors.New("nuclei-network-vuln: max-output cap reached")

// vulnCtxErr maps a (possibly canceled) context to the error runNucleiNetworkVuln
// should return: nil when the context is fine OR was canceled by our own
// output-cap; the context error otherwise (a real parent cancellation that must
// propagate, not be masked as success).
func vulnCtxErr(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	if errors.Is(context.Cause(ctx), errVulnOutputCap) {
		return nil
	}
	return ctx.Err()
}

// NucleiNetworkVulnHit is one curated network vuln match against a host:port.
type NucleiNetworkVulnHit struct {
	HostPort   string
	TemplateID string
	CVEs       []string
	Severity   string
	Name       string
}

// curatedVulnTemplatePaths returns the existing curated vuln dirs under
// <templates>/network/, or ok=false when none are present (log+skip, never
// fail the scan — same best-effort contract as the detection preflight).
func curatedVulnTemplatePaths() ([]string, bool) {
	dir, err := EnsureTemplates()
	if err != nil {
		return nil, false
	}
	var paths []string
	for _, d := range curatedVulnDirs {
		p := filepath.Join(dir, "network", d)
		if dirExists(p) {
			paths = append(paths, p)
		}
	}
	return paths, len(paths) > 0
}

// runNucleiNetworkVulnStage runs the curated vuln pass over the same host:port
// list as detection (best-effort; missing templates or errors log + return).
// Findings attach to the originating NaabuFinding's IP/port (not nuclei's
// matched-at echo), same as P1.
func runNucleiNetworkVulnStage(ctx context.Context, req PipelineRequest, findings []NaabuFinding, isHostname bool, now string, batchSize int) {
	if len(findings) == 0 {
		return
	}
	paths, ok := curatedVulnTemplatePaths()
	if !ok {
		slog.WarnContext(ctx, "nuclei-network-vuln: skipped — no curated vuln template dirs in bundle", "scan_id", req.ScanID)
		return
	}

	inputs, byHostPort := buildHostPortInputs(findings, isHostname, req.TargetIdentifier)
	if len(inputs) > vulnMaxTargets {
		slog.WarnContext(ctx, "nuclei-network-vuln: target cap applied", "scan_id", req.ScanID,
			"chunk", req.ChunkIndex, "original", len(inputs), "capped", vulnMaxTargets)
		inputs = inputs[:vulnMaxTargets]
	}

	batcher := NewBatcher(req.ScanID, "nuclei-network-vuln", req.Emit, batchSize, 2*time.Second).WithChunk(req.ChunkID, req.ChunkIndex)
	defer batcher.Stop()

	// Max-output cap: cancel the child context (with a sentinel CAUSE) when the
	// cap is hit so the subprocess stops. The sentinel lets the runner tell our
	// cap-cancel (→ success) apart from a PARENT cancellation — chunk timeout /
	// agent shutdown / user abort — which must propagate, not be masked as done.
	vctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var emitted atomic.Int64

	onHit := func(h NucleiNetworkVulnHit) {
		f, ok := byHostPort[h.HostPort]
		if !ok {
			return // matched-at didn't echo an input we fed; skip defensively
		}
		if emitted.Load() >= vulnMaxFindings {
			return
		}
		n := emitted.Add(1)
		// Parser-native blob (server ingestNucleiFindings reads template_id +
		// cves[] + severity + name).
		entry := map[string]any{
			"template_id": h.TemplateID,
			"cves":        h.CVEs,
			"severity":    h.Severity,
			"name":        h.Name,
		}
		raw, _ := json.Marshal([]map[string]any{entry})
		batcher.Add(tunnel.DiscoveredAssetUpsert{
			IP: f.IP, Port: f.Port, Hostname: f.Host, CVEs: raw, ObservedAt: now,
		})
		if n >= vulnMaxFindings {
			slog.WarnContext(ctx, "nuclei-network-vuln: max-output cap reached, stopping pass",
				"scan_id", req.ScanID, "cap", vulnMaxFindings)
			cancel(errVulnOutputCap)
		}
	}

	slog.InfoContext(ctx, "nuclei-network-vuln: scanning for network vulns", "scan_id", req.ScanID, "targets", len(inputs), "dirs", len(paths))
	stop := startProgress(ctx, progressInterval, "nuclei-network-vuln: scanning", func() []any {
		return []any{"scan_id", req.ScanID, "findings", emitted.Load(), "targets", len(inputs)}
	})
	err := runNucleiNetworkVuln(vctx, inputs, paths, onHit)
	stop()
	batcher.Flush()

	// err is nil on a clean run AND on our cap-cancel (the runner swallows only
	// that). A non-nil err here is a real tool failure or a parent cancellation
	// — log it (parent-cancel is expected during abort, so don't shout).
	if err != nil {
		slog.WarnContext(ctx, "nuclei-network-vuln: ended early", "scan_id", req.ScanID, "error", err)
	}
	slog.InfoContext(ctx, "nuclei-network-vuln: complete", "scan_id", req.ScanID, "findings", emitted.Load())
}

// runNucleiNetworkVuln feeds host:port pairs into nuclei restricted to the
// curated vuln template paths, with the D4 resource caps + active-tag
// exclusions, and streams hits.
func runNucleiNetworkVuln(ctx context.Context, hostPorts, templatePaths []string, onHit func(NucleiNetworkVulnHit)) error {
	if len(hostPorts) == 0 || len(templatePaths) == 0 {
		return nil
	}
	bin, err := EnsureTool("nuclei")
	if err != nil {
		return fmt.Errorf("nuclei install: %w", err)
	}
	args := []string{
		"-jsonl", "-silent", "-no-color", "-disable-update-check",
		// Belt-and-braces over the explicit path allowlist (hero #8298 item 4):
		// never run active/destructive templates even if one slips into a dir.
		"-etags", "intrusive,fuzz,dast,brute-force,dos",
		"-c", strconv.Itoa(vulnConcurrency),
		"-bs", strconv.Itoa(vulnBulkSize),
		"-rl", strconv.Itoa(vulnRateLimit),
		"-timeout", strconv.Itoa(vulnTimeoutSec),
		"-retries", strconv.Itoa(vulnRetries),
		"-mhe", strconv.Itoa(vulnMaxHostErr),
	}
	for _, p := range templatePaths {
		args = append(args, "-t", p)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("nuclei-network-vuln stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("nuclei-network-vuln stdout: %w", err)
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("nuclei-network-vuln start: %w", err)
	}
	tail := drainStderr("nuclei-network-vuln", stderr)

	go func() {
		defer stdin.Close()
		for _, hp := range hostPorts {
			_, _ = stdin.Write([]byte(hp + "\n"))
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	for scanner.Scan() {
		var line struct {
			TemplateID string `json:"template-id"`
			Info       struct {
				Name           string `json:"name"`
				Severity       string `json:"severity"`
				Classification struct {
					CVEID []string `json:"cve-id"`
				} `json:"classification"`
			} `json:"info"`
			MatchedAt string `json:"matched-at"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.MatchedAt == "" || line.TemplateID == "" {
			continue
		}
		onHit(NucleiNetworkVulnHit{
			HostPort:   line.MatchedAt,
			TemplateID: line.TemplateID,
			CVEs:       line.Info.Classification.CVEID,
			Severity:   line.Info.Severity,
			Name:       line.Info.Name,
		})
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("nuclei-network-vuln stdout scan: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		// If the context was canceled, return success ONLY for our own
		// output-cap; a parent cancellation propagates (don't mask a cancelled
		// scan as a completed stage).
		if ctx.Err() != nil {
			return vulnCtxErr(ctx)
		}
		// nuclei exits non-zero when it finds nothing — treat as success.
		if strings.Contains(err.Error(), "exit status 1") {
			return nil
		}
		return fmt.Errorf("nuclei-network-vuln exit: %w%s", err, tail.suffix())
	}
	return nil
}
