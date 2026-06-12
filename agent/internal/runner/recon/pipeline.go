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

	// When the directive targets a hostname, probe each open port BY NAME so
	// httpx sets SNI + Host and we see the actual vhost behind a shared ingress,
	// not its default backend — and the resulting asset is keyed on the name
	// (ADR 014 D3). naabu still emitted the IP-keyed host asset above; the two
	// cross-link by IP.
	isHostname := classifyTarget(req.TargetIdentifier) == targetHostname

	httpInputs := make([]string, 0, len(findings))
	for _, f := range findings {
		host := f.IP
		if isHostname {
			host = req.TargetIdentifier
		}
		httpInputs = append(httpInputs, fmt.Sprintf("%s:%d", host, f.Port))
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
		up := tunnel.DiscoveredAssetUpsert{
			IP:           f.IP,
			Port:         f.Port,
			Hostname:     f.Host,
			Service:      strings.ToLower(f.WebServer),
			Technologies: tech,
			ObservedAt:   now,
		}
		if isHostname {
			up.ResourceType = resourceTypeHTTPService
			if up.Hostname == "" {
				up.Hostname = req.TargetIdentifier
			}
		}
		httpxBatcher.Add(up)
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
		up := tunnel.DiscoveredAssetUpsert{
			IP:         ip,
			Port:       port,
			CVEs:       raw,
			ObservedAt: now,
		}
		if isHostname {
			// Attach the CVE to the vhost asset (keyed on name), not the
			// IP-keyed host, so findings land on the right site (ADR 014 D3).
			up.ResourceType = resourceTypeHTTPService
			up.Hostname = req.TargetIdentifier
		}
		nucleiBatcher.Add(up)
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

// Target kinds for a discovery directive's TargetIdentifier.
const (
	targetCIDR     = "cidr"
	targetRange    = "range"
	targetIP       = "ip"
	targetHostname = "hostname"

	resourceTypeHTTPService = "http_service" // wire value mirrors api model.ResourceTypeHTTPService
)

// classifyTarget distinguishes a CIDR, an IP range (IP-IP), a single IP, and a
// hostname. A range requires IPs on *both* sides of the dash, so a hyphenated
// hostname (my-app.example.com) is no longer misread as a range.
func classifyTarget(t string) string {
	t = strings.TrimSpace(t)
	if strings.Contains(t, "/") {
		return targetCIDR
	}
	if i := strings.Index(t, "-"); i > 0 {
		if net.ParseIP(strings.TrimSpace(t[:i])) != nil && net.ParseIP(strings.TrimSpace(t[i+1:])) != nil {
			return targetRange
		}
	}
	if net.ParseIP(t) != nil {
		return targetIP
	}
	return targetHostname
}

// vetTargetAgainstAllowlist enforces D11 before any subprocess spawns.
// Hostname targets are resolved; every resolved IP must pass.
func vetTargetAgainstAllowlist(target string, allow *Allowlist) error {
	t := strings.TrimSpace(target)
	switch classifyTarget(t) {
	case targetCIDR:
		if !allow.AllowsCIDR(t) {
			return fmt.Errorf("allowlist_violation: %s", t)
		}
		return nil
	case targetRange:
		// Cheap heuristic: reject if either endpoint is denied or unallowed.
		i := strings.Index(t, "-")
		from := strings.TrimSpace(t[:i])
		to := strings.TrimSpace(t[i+1:])
		if !allow.Allows(from) || !allow.Allows(to) {
			return fmt.Errorf("allowlist_violation: %s", t)
		}
		return nil
	case targetIP:
		if !allow.Allows(t) {
			return fmt.Errorf("allowlist_violation: %s", t)
		}
		return nil
	default: // hostname — resolve and check every resolved IP.
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
