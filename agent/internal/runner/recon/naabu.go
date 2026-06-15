package recon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// NaabuFinding is one host:port hit from a naabu JSON line.
type NaabuFinding struct {
	IP   string
	Port int
	Host string
}

// connectTuning trims CONNECT-scan cost on sparse CIDRs. CONNECT blocks on the
// per-connection timeout for every dead host:port (SYN fires-and-forgets), so a
// sparse range is dominated by dead-host timeouts — a /24 took ~6 min with the
// defaults (3 retries x 1000ms x low concurrency). One retry, a short timeout,
// and high concurrency suit the low-latency internal targets the agent scans,
// where a live host answers well under 700ms.
var connectTuning = []string{"-retries", "1", "-timeout", "700", "-c", "100"}

// defaultNaabuPorts is the scan set used when a directive doesn't specify ports.
// naabu's default (top-100) structurally misses common DB/service ports — e.g.
// mongodb 27017 — so we scan a curated union of common web/infra ports PLUS the
// database/cache/search/broker ports we care about. Chosen over `-top-ports
// 1000` because CONNECT scan (the unprivileged default) pays the per-port
// timeout on every dead port: top-1000 is ~10x the dead-host cost of this ~50
// port list on sparse CIDRs, while this list guarantees the DB ports are
// covered. A directive's `ports` overrides this entirely.
const defaultNaabuPorts = "21,22,23,25,53,80,110,111,135,139,143,389,443," +
	"445,587,636,993,995,1433,1521,2049,2375,2379,3000,3306,3389,4369," +
	"5000,5432,5433,5601,5672,5900,5984,6379,7000,7001,8000,8080,8081," +
	"8086,8088,8443,8888,9000,9042,9090,9100,9200,9300,11211,15672," +
	"27017,27018,27019"

// capNetRaw is the CAP_NET_RAW bit position in a Linux capability bitmask.
const capNetRaw = 13

// hasRawSocketCapability reports whether the process can open raw sockets, which
// naabu's default SYN scan requires. The effective capability set
// (/proc/self/status CapEff) is authoritative when available — even for uid 0,
// since a container can run as root with CAP_NET_RAW dropped (the agent helm
// chart drops ALL caps). Only when CapEff is unavailable (non-Linux, or an
// unreadable/garbled status file) do we fall back to the uid heuristic: a
// non-container root typically can open raw sockets. A package var so tests can
// stub it.
var hasRawSocketCapability = func() bool {
	if data, err := os.ReadFile("/proc/self/status"); err == nil {
		if hasNetRaw, ok := parseCapEffNetRaw(data); ok {
			return hasNetRaw
		}
	}
	return os.Geteuid() == 0
}

// parseCapEffNetRaw scans /proc/<pid>/status content for the CapEff line and
// reports whether CAP_NET_RAW is set. ok is false when there is no parseable
// CapEff line (non-Linux, or a garbled file), so the caller can fall back.
func parseCapEffNetRaw(status []byte) (hasNetRaw, ok bool) {
	for _, line := range strings.Split(string(status), "\n") {
		rest, found := strings.CutPrefix(line, "CapEff:")
		if !found {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(rest), 16, 64)
		if err != nil {
			return false, false
		}
		return v&(1<<capNetRaw) != 0, true
	}
	return false, false
}

// naabuScanArgs picks naabu's scan-mode args and a human reason. Precedence:
// an explicit SILKSTRAND_NAABU_SCAN_TYPE override always wins; otherwise the
// mode is auto-selected from raw-socket availability — SYN (naabu default) when
// CAP_NET_RAW is present, CONNECT when it isn't (so an unprivileged agent gets
// working discovery without the operator having to set the env var). CONNECT in
// either path gets the tuned params.
func naabuScanArgs(envScanType string, hasRawCap bool) (args []string, mode, reason string) {
	if env := strings.TrimSpace(envScanType); env != "" {
		args = []string{"-scan-type", env}
		if env == "c" {
			args = append(args, connectTuning...)
		}
		return args, env, "SILKSTRAND_NAABU_SCAN_TYPE override"
	}
	if !hasRawCap {
		args = []string{"-scan-type", "c"}
		args = append(args, connectTuning...)
		return args, "c", "no CAP_NET_RAW (unprivileged); CONNECT scan auto-selected"
	}
	// SYN is naabu's default; no -scan-type arg needed.
	return nil, "s", "CAP_NET_RAW present; SYN scan (naabu default)"
}

// naabuPortArgs builds the -p argument: the directive's explicit ports when set,
// otherwise the curated defaultNaabuPorts.
func naabuPortArgs(ports string) []string {
	if p := strings.TrimSpace(ports); p != "" {
		return []string{"-p", p}
	}
	return []string{"-p", defaultNaabuPorts}
}

// runNaabu invokes naabu against the given target and streams JSON
// findings to onFinding. Blocks until naabu exits or ctx is cancelled.
func runNaabu(ctx context.Context, target, ports string, ratePPS int, onFinding func(NaabuFinding)) error {
	bin, err := EnsureTool("naabu")
	if err != nil {
		return fmt.Errorf("naabu install: %w", err)
	}
	args := []string{
		"-host", target,
		"-json",
		"-silent",
		"-rate", strconv.Itoa(ratePPS),
	}
	args = append(args, naabuPortArgs(ports)...)
	scanArgs, mode, reason := naabuScanArgs(os.Getenv("SILKSTRAND_NAABU_SCAN_TYPE"), hasRawSocketCapability())
	args = append(args, scanArgs...)
	slog.InfoContext(ctx, "naabu: scan configuration", "scan_mode", mode, "reason", reason,
		"ports", naabuPortArgs(ports)[1])
	cmd := exec.CommandContext(ctx, bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("naabu stdout pipe: %w", err)
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("naabu start: %w", err)
	}
	tail := drainStderr("naabu", stderr)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var line struct {
			IP   string `json:"ip"`
			Host string `json:"host"`
			Port int    `json:"port"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue // tolerate malformed line, keep scanning
		}
		if line.IP == "" {
			continue
		}
		onFinding(NaabuFinding{IP: line.IP, Port: line.Port, Host: line.Host})
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("naabu stdout scan: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("naabu exit: %w%s", err, tail.suffix())
	}
	return nil
}

// stderrTail keeps the last N stderr lines from a subprocess so they can
// be appended to the error returned when the process exits non-zero.
type stderrTail struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func (t *stderrTail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

func (t *stderrTail) suffix() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 {
		return ""
	}
	return ": " + strings.Join(t.lines, " | ")
}

func drainStderr(_ string, r io.ReadCloser) *stderrTail {
	tail := &stderrTail{max: 10}
	go func() {
		defer r.Close()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			tail.add(scanner.Text())
		}
	}()
	return tail
}
