package recon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jtb75/silkstrand/agent/internal/tunnel"
)

// ErrNetworkTemplatesMissing means the pinned bundle has no network/detection
// directory. The nuclei-network stage logs + skips (never fails the scan) — the
// detection enrichment is optional (ADR 019 P1 / hero preflight).
var ErrNetworkTemplatesMissing = errors.New("network_templates_missing")

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// ADR 019 P1 — nuclei-network service detection. A separate sequential pass
// over naabu's open host:port pairs using nuclei's `network` protocol detection
// templates (`network/detection/`). It backfills asset_endpoints.service +
// version for NON-web ports; web ports are routed to httpx (which owns them).
// Detection only — no vuln findings (that's P2/#377).

// NucleiNetworkHit is one network/detection match against a host:port.
type NucleiNetworkHit struct {
	HostPort   string   // matched-at (host:port)
	TemplateID string   // e.g. "ftp-detect", "mssql-detect"
	Extracted  []string // extracted-results (banner / version strings)
}

// canonicalServices maps a service/protocol token (plus common aliases) to the
// canonical asset_endpoints.service value collections filter on (service='ssh').
// Canonicalizing FAMILIES is the whole point of P1: the real bundle is full of
// vendor-prefixed ids (bitvise-ssh-detect, maverick-ssh-detect, ws_ftp-ssh-detect,
// xlight-ftp-service-detect, vnc-service-detect, rsyncd-service-detect …) that
// must all collapse to ssh/ftp/vnc/rsync — not fragment into per-vendor labels.
var canonicalServices = map[string]string{
	"ssh": "ssh", "sshd": "ssh", "openssh": "ssh", "dropbear": "ssh",
	"ftp": "ftp", "ftps": "ftp", "ftpd": "ftp",
	"sftp":   "sftp",
	"telnet": "telnet",
	"smtp":   "smtp", "smtpd": "smtp",
	"imap": "imap", "pop3": "pop3",
	"mssql": "mssql",
	"mysql": "mysql", "mariadb": "mysql",
	"postgres": "postgresql", "postgresql": "postgresql",
	"mongodb": "mongodb", "mongo": "mongodb",
	"redis":     "redis",
	"memcached": "memcached",
	"rdp":       "rdp",
	"vnc":       "vnc",
	"ldap":      "ldap", "ldaps": "ldap",
	"rsync": "rsync", "rsyncd": "rsync",
	"snmp":     "snmp",
	"nfs":      "nfs",
	"kafka":    "kafka",
	"rabbitmq": "rabbitmq", "amqp": "rabbitmq",
	"elasticsearch": "elasticsearch",
	"rtsp":          "rtsp",
}

// httpFamilyTokens are web protocols httpx owns — routed to httpx, never
// persisted by nuclei-network (defensive: http detection lives under http/, not
// network/detection/, so these rarely fire here).
var httpFamilyTokens = map[string]bool{"http": true, "https": true, "tls": true, "ssl": true}

// detectNoise are tokens skipped during family detection so the protocol token
// is reachable (vnc-service-detect → vnc, *-server-detect → the service).
var detectNoise = map[string]bool{
	"detect": true, "detection": true, "service": true,
	"server": true, "transport": true, "exposed": true,
}

// classifyTemplate resolves a template-id to (canonical service, httpFamily).
// It scans tokens RIGHT-to-LEFT — the protocol token typically sits nearest the
// -detect suffix (sshd-dropbear-detect → ssh, ws_ftp-ssh-detect → ssh) — and
// returns the first canonical service or http-family token, with alias
// collapsing so vendor variants resolve to one service. Genuinely-unknown
// templates fall back to the detect-suffix-stripped id (community templates
// still get a label without code changes).
func classifyTemplate(templateID string) (service string, httpFamily bool) {
	s := strings.ToLower(strings.TrimSpace(templateID))
	tokens := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' })
	for i := len(tokens) - 1; i >= 0; i-- {
		t := tokens[i]
		if detectNoise[t] {
			continue
		}
		if httpFamilyTokens[t] {
			return t, true
		}
		if canon, ok := canonicalServices[t]; ok {
			return canon, false
		}
	}
	fb := strings.TrimSuffix(s, "-detection")
	fb = strings.TrimSuffix(fb, "-detect")
	return fb, false
}

// portDecision is the per-port outcome after the whole detection pass.
type portDecision struct {
	service  string
	version  string
	backfill bool // true → persist service/version + exclude from httpx
}

// classifyPort applies the web-precedence rule (hero #8083) to all hits on one
// host:port:
//   - non-HTTP hit(s) only         → backfill, exclude from httpx
//   - no hit / HTTP-family / BOTH  → no backfill (httpx owns it)
//
// First non-HTTP hit with a usable service wins; version is its first non-empty
// extracted value.
func classifyPort(hits []NucleiNetworkHit) portDecision {
	var sawHTTP, sawNonHTTP bool
	var svc, ver string
	for _, h := range hits {
		s, http := classifyTemplate(h.TemplateID)
		if http {
			sawHTTP = true
			continue
		}
		sawNonHTTP = true
		if svc == "" && s != "" {
			svc = s
			ver = firstExtracted(h.Extracted)
		}
	}
	if sawNonHTTP && !sawHTTP && svc != "" {
		return portDecision{service: svc, version: ver, backfill: true}
	}
	return portDecision{}
}

// firstExtracted returns the first non-empty, trimmed extracted value.
func firstExtracted(vals []string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// parseExtracted tolerantly decodes nuclei's extracted-results, whose JSON
// shape varies by template/protocol: array (the network norm), bare string, or
// (rarely) a map of values. Anything else → no version.
func parseExtracted(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return []string{s}
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err == nil {
		out := make([]string, 0, len(m))
		for _, v := range m {
			out = append(out, v)
		}
		return out
	}
	return nil
}

// networkDetectionDir returns the network/detection template subdir, or ""
// with ok=false when it's absent from the pinned bundle (so the caller can
// log+skip rather than fail the whole scan — ADR 019 P1 / hero preflight).
func networkDetectionDir() (string, bool) {
	dir, err := EnsureTemplates()
	if err != nil {
		return "", false
	}
	nd := filepath.Join(dir, "network", "detection")
	if !dirExists(nd) {
		return "", false
	}
	return nd, true
}

// runNucleiNetworkStage runs the detection pass over naabu's findings, then
// classifies PER PORT after the full pass (buffered) and emits service/version
// backfill batches for non-HTTP-only ports, marking them in `excluded` so the
// httpx stage skips them (ADR 019 P1; web-precedence per hero #8083). It is
// best-effort: a missing template set or any error logs + returns without
// touching `excluded` (httpx then sees all ports, as before).
func runNucleiNetworkStage(ctx context.Context, req PipelineRequest, findings []NaabuFinding, isHostname bool, now string, batchSize int, excluded map[string]struct{}) {
	if len(findings) == 0 {
		return
	}
	batcher := NewBatcher(req.ScanID, "nuclei-network", req.Emit, batchSize, 2*time.Second).WithChunk(req.ChunkID, req.ChunkIndex)
	defer batcher.Stop()

	// host:port inputs (probe hostnames BY NAME, like httpx); map matched-at
	// back to the originating finding so backfill keeps naabu's IP identity.
	inputs := make([]string, 0, len(findings))
	byHostPort := make(map[string]NaabuFinding, len(findings))
	for _, f := range findings {
		host := f.IP
		if isHostname {
			host = req.TargetIdentifier
		}
		hp := fmt.Sprintf("%s:%d", host, f.Port)
		inputs = append(inputs, hp)
		byHostPort[hp] = f
	}

	var mu sync.Mutex
	hitsByPort := map[string][]NucleiNetworkHit{}
	var hitCount atomic.Int64
	onHit := func(h NucleiNetworkHit) {
		mu.Lock()
		hitsByPort[h.HostPort] = append(hitsByPort[h.HostPort], h)
		mu.Unlock()
		hitCount.Add(1)
	}

	slog.InfoContext(ctx, "nuclei-network: detecting services", "scan_id", req.ScanID, "inputs", len(inputs))
	stop := startProgress(ctx, progressInterval, "nuclei-network: detecting", func() []any {
		return []any{"scan_id", req.ScanID, "hits", hitCount.Load(), "ports", len(inputs)}
	})
	err := runNucleiNetwork(ctx, inputs, onHit)
	stop()

	switch {
	case errors.Is(err, ErrNetworkTemplatesMissing):
		slog.WarnContext(ctx, "nuclei-network: skipped — network/detection absent from template bundle", "scan_id", req.ScanID)
		return
	case err != nil:
		slog.WarnContext(ctx, "nuclei-network: error (continuing without service backfill)", "scan_id", req.ScanID, "error", err)
		return
	}

	backfilled := 0
	for hp, hits := range hitsByPort {
		f, ok := byHostPort[hp]
		if !ok {
			continue // matched-at didn't echo an input we fed; skip defensively
		}
		d := classifyPort(hits)
		if !d.backfill {
			continue // web / ambiguous / no-service → httpx owns the port
		}
		excluded[fmt.Sprintf("%s:%d", f.IP, f.Port)] = struct{}{}
		batcher.Add(tunnel.DiscoveredAssetUpsert{
			IP:         f.IP,
			Port:       f.Port,
			Hostname:   f.Host,
			Service:    d.service,
			Version:    d.version,
			ObservedAt: now,
		})
		backfilled++
	}
	batcher.Flush()
	slog.InfoContext(ctx, "nuclei-network: complete", "scan_id", req.ScanID, "hits", hitCount.Load(), "backfilled", backfilled)
}

// runNucleiNetwork feeds host:port pairs into nuclei's network/detection
// templates via stdin and streams detection hits. Mirrors runNuclei's process
// handling (stdin feed, JSONL stdout, exit-status-1 = "no hits" success).
func runNucleiNetwork(ctx context.Context, hostPorts []string, onHit func(NucleiNetworkHit)) error {
	if len(hostPorts) == 0 {
		return nil
	}
	bin, err := EnsureTool("nuclei")
	if err != nil {
		return fmt.Errorf("nuclei install: %w", err)
	}
	detectionDir, ok := networkDetectionDir()
	if !ok {
		return ErrNetworkTemplatesMissing
	}
	args := []string{
		"-jsonl",
		"-silent",
		"-no-color",
		"-disable-update-check",
		"-t", detectionDir,
		"-severity", "info,low,medium,high,critical",
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("nuclei-network stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("nuclei-network stdout: %w", err)
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("nuclei-network start: %w", err)
	}
	tail := drainStderr("nuclei-network", stderr)

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
			TemplateID string          `json:"template-id"`
			MatchedAt  string          `json:"matched-at"`
			Extracted  json.RawMessage `json:"extracted-results"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.MatchedAt == "" || line.TemplateID == "" {
			continue
		}
		onHit(NucleiNetworkHit{
			HostPort:   line.MatchedAt,
			TemplateID: line.TemplateID,
			Extracted:  parseExtracted(line.Extracted),
		})
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("nuclei-network stdout scan: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		// nuclei exits non-zero when it finds nothing — treat as success.
		if strings.Contains(err.Error(), "exit status 1") {
			return nil
		}
		return fmt.Errorf("nuclei-network exit: %w%s", err, tail.suffix())
	}
	return nil
}
