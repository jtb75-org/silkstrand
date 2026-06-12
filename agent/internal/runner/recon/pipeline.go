package recon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jtb75/silkstrand/agent/internal/tunnel"
)

// DiscoveryConfig is the agent-side parse of DirectivePayload.TargetConfig
// for a discovery directive (mirrors api/internal/websocket.DiscoveryConfig).
type DiscoveryConfig struct {
	Ports         string `json:"ports,omitempty"`
	RatePPS       int    `json:"rate_pps,omitempty"`
	IncludeHTTPX  bool   `json:"include_httpx"`
	IncludeNuclei bool   `json:"include_nuclei"`
	BatchSize     int    `json:"batch_size,omitempty"`
}

// PipelineRequest packages everything the recon runner needs.
type PipelineRequest struct {
	ScanID           string
	TargetIdentifier string          // CIDR, IP, range, or hostname
	TargetConfig     json.RawMessage // DiscoveryConfig
	Emit             EmitFunc
}

// PipelineResult is the summary returned to the caller (also forms the
// basis of the discovery_completed payload).
type PipelineResult struct {
	AssetsFound  int
	HostsScanned int
}

// Run executes naabu → httpx → nuclei against the directive's target,
// streaming asset_discovered batches as they come. Returns a summary
// suitable for the terminal discovery_completed message. Cancellation
// of ctx propagates SIGTERM to subprocesses.
func Run(ctx context.Context, req PipelineRequest) (*PipelineResult, error) {
	cfg := DiscoveryConfig{IncludeHTTPX: true, IncludeNuclei: true, BatchSize: 10}
	if len(req.TargetConfig) > 0 {
		_ = json.Unmarshal(req.TargetConfig, &cfg)
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 10
	}

	allow, err := Load()
	if err != nil {
		return nil, fmt.Errorf("loading scan allowlist: %w", err)
	}
	if err := vetTargetAgainstAllowlist(req.TargetIdentifier, allow); err != nil {
		return nil, err
	}

	pps := allow.EffectivePPS(cfg.RatePPS)

	// Stage 1: naabu.
	naabuBatcher := NewBatcher(req.ScanID, "naabu", req.Emit, cfg.BatchSize, 2*time.Second)
	defer naabuBatcher.Stop()

	var (
		naabuMu   sync.Mutex
		findings  []NaabuFinding
		hostsSeen = map[string]struct{}{}
		now       = time.Now().UTC().Format(time.RFC3339)
	)
	naabuOnFinding := func(f NaabuFinding) {
		naabuMu.Lock()
		findings = append(findings, f)
		hostsSeen[f.IP] = struct{}{}
		naabuMu.Unlock()
		naabuBatcher.Add(tunnel.DiscoveredAssetUpsert{
			IP:         f.IP,
			Port:       f.Port,
			Hostname:   f.Host,
			ObservedAt: now,
		})
	}
	slog.InfoContext(ctx, "naabu: port-scanning target", "scan_id", req.ScanID,
		"target", req.TargetIdentifier, "rate_pps", pps)
	if err := runNaabu(ctx, req.TargetIdentifier, pps, naabuOnFinding); err != nil {
		naabuBatcher.Flush()
		return nil, err
	}
	naabuBatcher.Flush()
	slog.InfoContext(ctx, "naabu: complete", "scan_id", req.ScanID,
		"open_ports", len(findings), "hosts", len(hostsSeen))

	if !cfg.IncludeHTTPX {
		return &PipelineResult{AssetsFound: len(findings), HostsScanned: len(hostsSeen)}, nil
	}

	// Stage 2: httpx (HTTP/TLS fingerprint over naabu's findings).
	httpxBatcher := NewBatcher(req.ScanID, "httpx", req.Emit, cfg.BatchSize, 2*time.Second)
	defer httpxBatcher.Stop()

	httpInputs := make([]string, 0, len(findings))
	for _, f := range findings {
		httpInputs = append(httpInputs, fmt.Sprintf("%s:%d", f.IP, f.Port))
	}
	var (
		httpxMu       sync.Mutex
		httpxFindings []HTTPXFinding
		urls          []string
	)
	httpxOnFinding := func(f HTTPXFinding) {
		httpxMu.Lock()
		httpxFindings = append(httpxFindings, f)
		urls = append(urls, f.URL)
		httpxMu.Unlock()
		tech, _ := json.Marshal(f.Technologies)
		httpxBatcher.Add(tunnel.DiscoveredAssetUpsert{
			IP:           f.IP,
			Port:         f.Port,
			Hostname:     f.Host,
			Service:      strings.ToLower(f.WebServer),
			Technologies: tech,
			ObservedAt:   now,
		})
	}
	slog.InfoContext(ctx, "httpx: fingerprinting services", "scan_id", req.ScanID, "inputs", len(httpInputs))
	if err := runHTTPX(ctx, httpInputs, httpxOnFinding); err != nil {
		httpxBatcher.Flush()
		return &PipelineResult{AssetsFound: len(findings), HostsScanned: len(hostsSeen)}, err
	}
	httpxBatcher.Flush()
	slog.InfoContext(ctx, "httpx: complete", "scan_id", req.ScanID, "http_services", len(httpxFindings))

	if !cfg.IncludeNuclei {
		return &PipelineResult{AssetsFound: len(findings), HostsScanned: len(hostsSeen)}, nil
	}

	// Stage 3: nuclei (CVE templates against httpx URLs). After this
	// stage we flush per-asset (batch size 1) because CVE results are
	// the high-value-but-late slice.
	nucleiBatcher := NewBatcher(req.ScanID, "nuclei", req.Emit, 1, 1*time.Second)
	defer nucleiBatcher.Stop()

	cveByEndpoint := map[string][]map[string]any{}
	nucleiHits := 0
	nucleiOnHit := func(h NucleiHit) {
		ip, port := splitURLToIPPort(h.URL, httpxFindings)
		if ip == "" {
			return
		}
		nucleiHits++
		key := fmt.Sprintf("%s:%d", ip, port)
		entry := map[string]any{
			"id":       firstCVE(h.CVEs, h.TemplateID),
			"template": h.TemplateID,
			"severity": h.Severity,
		}
		cveByEndpoint[key] = append(cveByEndpoint[key], entry)
		raw, _ := json.Marshal(cveByEndpoint[key])
		nucleiBatcher.Add(tunnel.DiscoveredAssetUpsert{
			IP:         ip,
			Port:       port,
			CVEs:       raw,
			ObservedAt: now,
		})
	}
	nucleiURLs := dedupe(urls)
	slog.InfoContext(ctx, "nuclei: scanning for vulnerabilities", "scan_id", req.ScanID, "urls", len(nucleiURLs))
	if err := runNuclei(ctx, nucleiURLs, nucleiOnHit); err != nil {
		nucleiBatcher.Flush()
		// Nuclei errors don't sink prior stage results.
		return &PipelineResult{AssetsFound: len(findings), HostsScanned: len(hostsSeen)}, err
	}
	nucleiBatcher.Flush()
	slog.InfoContext(ctx, "nuclei: complete", "scan_id", req.ScanID, "findings", nucleiHits)

	return &PipelineResult{AssetsFound: len(findings), HostsScanned: len(hostsSeen)}, nil
}

// vetTargetAgainstAllowlist enforces D11 before any subprocess spawns.
// Hostname targets are resolved; every resolved IP must pass.
func vetTargetAgainstAllowlist(target string, allow *Allowlist) error {
	t := strings.TrimSpace(target)
	switch {
	case strings.Contains(t, "/"):
		if !allow.AllowsCIDR(t) {
			return fmt.Errorf("allowlist_violation: %s", t)
		}
		return nil
	case strings.Contains(t, "-"):
		// IP range: walk endpoints, reject if either is denied or
		// neither endpoint is allowed (cheap heuristic; tighter check
		// could enumerate every IP in the range).
		i := strings.Index(t, "-")
		from := strings.TrimSpace(t[:i])
		to := strings.TrimSpace(t[i+1:])
		if !allow.Allows(from) || !allow.Allows(to) {
			return fmt.Errorf("allowlist_violation: %s", t)
		}
		return nil
	}
	if ip := net.ParseIP(t); ip != nil {
		if !allow.Allows(t) {
			return fmt.Errorf("allowlist_violation: %s", t)
		}
		return nil
	}
	// Hostname.
	ips, err := net.LookupIP(t)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("allowlist_violation: cannot resolve %s", t)
	}
	for _, ip := range ips {
		if !allow.Allows(ip.String()) {
			return fmt.Errorf("allowlist_violation: %s resolved to disallowed %s", t, ip)
		}
	}
	return nil
}

// firstCVE picks a stable single id for the CVEs JSONB array entry.
// Returns the first CVE-* id, falling back to the template id.
func firstCVE(cves []string, templateID string) string {
	for _, c := range cves {
		if strings.HasPrefix(strings.ToUpper(c), "CVE-") {
			return c
		}
	}
	if len(cves) > 0 {
		return cves[0]
	}
	return templateID
}

func dedupe(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// splitURLToIPPort maps a nuclei matched URL back to the IP/port
// combination the httpx stage already discovered. Avoids a DNS lookup.
func splitURLToIPPort(url string, httpxFindings []HTTPXFinding) (string, int) {
	for _, h := range httpxFindings {
		if h.URL == url {
			return h.IP, h.Port
		}
	}
	return "", 0
}

var ErrAllowlistViolation = errors.New("allowlist_violation")
