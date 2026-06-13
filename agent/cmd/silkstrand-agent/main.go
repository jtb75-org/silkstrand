// Accept certs with negative serial numbers (common in older/self-signed
// MSSQL certs). Go 1.23+ rejects these by default.
//go:debug x509negativeserial=1

package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/jtb75/silkstrand/agent/internal/allowlistreport"
	"github.com/jtb75/silkstrand/agent/internal/bootstrap"
	"github.com/jtb75/silkstrand/agent/internal/cache"
	"github.com/jtb75/silkstrand/agent/internal/config"
	"github.com/jtb75/silkstrand/agent/internal/logstream"
	"github.com/jtb75/silkstrand/agent/internal/nettls"
	"github.com/jtb75/silkstrand/agent/internal/prober"
	"github.com/jtb75/silkstrand/agent/internal/runner"
	"github.com/jtb75/silkstrand/agent/internal/tunnel"
	"github.com/jtb75/silkstrand/agent/internal/updater"
	"github.com/jtb75/silkstrand/agent/internal/vault"
)

// version is set via ldflags: -X main.version=$VERSION
var version = "dev"

func main() {
	// CLI subcommands — keep minimal to avoid a flag package.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "uninstall":
			if err := runUninstall(); err != nil {
				slog.Error("uninstall", "error", err)
				os.Exit(1)
			}
			return
		case "version", "--version", "-v":
			printlnSafe(version)
			return
		}
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("loading config", "error", err)
		os.Exit(1)
	}

	// Set up structured JSON logging
	logLevel := config.ParseLogLevel(cfg.LogLevel)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	// Configure outbound TLS (custom CA) + egress proxy once, before the first
	// network call (bootstrap below). ADR 013 D3.
	if err := nettls.Configure(nettls.Options{CACertPath: cfg.CACertPath}); err != nil {
		slog.Error("configuring outbound TLS", "error", err)
		os.Exit(1)
	}

	// If no explicit credentials, try loading from disk or the install-token flow.
	if err := bootstrap.EnsureCreds(cfg, version); err != nil {
		slog.Error("bootstrap", "error", err)
		os.Exit(1)
	}
	if err := cfg.RequireCreds(); err != nil {
		slog.Error("missing credentials after bootstrap", "error", err)
		os.Exit(1)
	}

	slog.Info("starting silkstrand-agent",
		"version", version,
		"agent_id", cfg.AgentID,
		"api_url", cfg.APIURL,
		"bundle_dir", cfg.BundleDir,
	)

	// Load optional Ed25519 public key for bundle signature verification
	var publicKey ed25519.PublicKey
	if cfg.PublicKeyPath != "" {
		keyData, err := os.ReadFile(cfg.PublicKeyPath)
		if err != nil {
			slog.Error("reading public key", "path", cfg.PublicKeyPath, "error", err)
			os.Exit(1)
		}
		if len(keyData) != ed25519.PublicKeySize {
			slog.Error("invalid public key size", "expected", ed25519.PublicKeySize, "got", len(keyData))
			os.Exit(1)
		}
		publicKey = ed25519.PublicKey(keyData)
		slog.Info("bundle signature verification enabled")
	} else {
		slog.Warn("bundle signature verification disabled (no SILKSTRAND_PUBLIC_KEY set)")
	}

	// Build components
	bundleCache := cache.New(cfg.BundleDir, publicKey)
	pythonRunner := runner.NewPythonRunner()
	tun := tunnel.New(cfg.APIURL, cfg.AgentID, cfg.AgentKey)
	// Report container-ness in heartbeats so the UI offers image-recreate
	// instead of the in-place upgrade containers can't do (ADR 013 follow-up).
	tun.InContainer = updater.InContainer()

	// Install the dual-handler slog stack per ADR 008 D1: stdout keeps
	// every level (debug stays local per D2), tunnel ships info+ only
	// so the control-plane UI has a live console without drowning in
	// debug output. A rate-limit drop triggers a summary log line
	// (D6); see logstream/tunnel_handler.go.
	stdoutHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	tunnelHandler := logstream.NewTunnelHandler(tun)
	slog.SetDefault(slog.New(logstream.NewMulti(stdoutHandler, tunnelHandler)))

	// Concurrency limiter: 1 scan at a time
	scanSem := make(chan struct{}, 1)
	var currentMu sync.Mutex
	var currentScanID, currentChunkID string

	// Wire up directive handler
	tun.OnDirective = func(d tunnel.DirectivePayload) {
		select {
		case scanSem <- struct{}{}:
			currentMu.Lock()
			currentScanID, currentChunkID = d.ScanID, d.ChunkID
			currentMu.Unlock()
		default:
			currentMu.Lock()
			sameChunk := d.ChunkID != "" && d.ScanID == currentScanID && d.ChunkID == currentChunkID
			currentMu.Unlock()
			if sameChunk {
				sendStartedPayload(tun, tunnel.ScanStartedPayload{ScanID: d.ScanID, ChunkID: d.ChunkID})
				return
			}
			slog.Warn("scan rejected, already running", "scan_id", d.ScanID)
			sendScanError(tun, tunnel.ScanErrorPayload{
				ScanID:  d.ScanID,
				ChunkID: d.ChunkID,
				Error:   "agent busy: another scan is in progress",
			})
			return
		}

		go func() {
			defer func() {
				currentMu.Lock()
				if currentScanID == d.ScanID && currentChunkID == d.ChunkID {
					currentScanID, currentChunkID = "", ""
				}
				currentMu.Unlock()
				<-scanSem
			}()
			if d.ScanType == "discovery" {
				handleDiscoveryDirective(tun, d)
			} else {
				handleDirective(tun, bundleCache, pythonRunner, d)
			}
		}()
	}

	// Wire up connectivity probe handler.
	tun.OnProbe = func(p tunnel.ProbePayload) {
		slog.Info("probe requested", "probe_id", p.ProbeID, "type", p.TargetType)
		res := prober.Probe(context.Background(), p.TargetType, p.TargetConfig, p.Credentials)
		reply, _ := json.Marshal(tunnel.ProbeResultPayload{
			ProbeID: p.ProbeID, OK: res.OK, Error: res.Error, Detail: res.Detail,
		})
		tun.Send(tunnel.Message{Type: tunnel.TypeProbeResult, Payload: reply})
	}

	// Wire up credential test handler. Tests a credential source (e.g. Vault)
	// using the agent's local network and sends the result back.
	tun.OnCredentialTest = func(ct tunnel.CredentialTestPayload) {
		slog.Info("credential test requested", "test_id", ct.TestID, "resolver_type", ct.ResolverType)
		go func() {
			start := time.Now()
			var result tunnel.CredentialTestResultPayload
			result.TestID = ct.TestID
			switch ct.ResolverType {
			case "hashicorp_vault":
				var cfg vault.ResolveConfig
				if err := json.Unmarshal(ct.Config, &cfg); err != nil {
					result.Success = false
					result.Error = "invalid config: " + err.Error()
				} else {
					cred, err := vault.Resolve(context.Background(), cfg)
					if err != nil {
						result.Success = false
						result.Error = err.Error()
					} else {
						result.Success = true
						result.Username = cred.Username
					}
				}
			default:
				result.Success = false
				result.Error = "unsupported resolver type: " + ct.ResolverType
			}
			result.DurationMs = time.Since(start).Milliseconds()
			reply, _ := json.Marshal(result)
			tun.Send(tunnel.Message{Type: tunnel.TypeCredentialTestResult, Payload: reply})
			slog.Info("credential test completed", "test_id", ct.TestID, "success", result.Success, "duration_ms", result.DurationMs)
		}()
	}

	// Wire up upgrade handler. On success the process exits; the service
	// manager (systemd/launchd) restarts us with the new binary.
	tun.OnUpgrade = func(up tunnel.UpgradePayload) {
		suffix := runtime.GOOS + "-" + runtime.GOARCH
		expectedSHA := up.SHA256ByPlatform[suffix]
		slog.Info("upgrade requested", "version", up.Version, "platform", suffix)
		if err := updater.Apply(up.BaseURL, up.Version, expectedSHA); err != nil {
			slog.Error("upgrade failed", "error", err)
			return
		}
		slog.Info("upgrade complete; exiting so service manager restarts us")
		os.Exit(0)
	}

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	go allowlistreport.Run(ctx, tun, allowlistPath())

	tun.Run(ctx, version)
	slog.Info("agent stopped")
}

func handleDirective(tun *tunnel.Tunnel, c *cache.Cache, r runner.Runner, d tunnel.DirectivePayload) {
	// Stamp scan_id onto every log record emitted in this goroutine so
	// the Scan Results console (ADR 008 D4) can filter cleanly.
	ctx := logstream.WithScanID(context.Background(), d.ScanID)

	slog.InfoContext(ctx, "received scan directive",
		"scan_id", d.ScanID,
		"bundle", d.BundleName,
		"version", d.BundleVersion,
		"target", d.TargetIdentifier,
	)

	// Notify server that scan has started
	sendStarted(tun, d.ScanID)

	// Agent-side credential resolution: if the server sent a resolver
	// config instead of plaintext credentials, resolve locally.
	if d.Credentials == nil && d.CredentialResolver != nil {
		resolved, err := resolveCredentialLocally(ctx, d.CredentialResolver)
		if err != nil {
			slog.ErrorContext(ctx, "agent-side credential resolution failed",
				"scan_id", d.ScanID, "resolver_type", d.CredentialResolver.Type, "error", err)
			sendError(tun, d.ScanID, "credential resolution error: "+err.Error())
			return
		}
		d.Credentials = resolved
	}

	// ADR 011 PR 4: try Go collector path before falling back to Python bundle.
	if done := tryCollectorPath(ctx, tun, d); done {
		return
	}

	// Resolve bundle from cache
	bundlePath, err := c.GetOrFetch(d.BundleName, d.BundleVersion, d.BundleURL)
	if err != nil {
		if errors.Is(err, cache.ErrNotCached) {
			slog.ErrorContext(ctx, "bundle not found in cache", "bundle", d.BundleName, "version", d.BundleVersion)
			sendError(tun, d.ScanID, "bundle not cached: "+d.BundleName+"@"+d.BundleVersion)
		} else {
			slog.ErrorContext(ctx, "bundle fetch failed", "error", err)
			sendError(tun, d.ScanID, "bundle error: "+err.Error())
		}
		return
	}

	// ADR 010 D5: try the new bundle.yaml manifest first for per-control execution.
	bundleManifest, err := runner.ReadBundleManifest(bundlePath)
	if err != nil {
		slog.ErrorContext(ctx, "reading bundle manifest", "error", err)
		sendError(tun, d.ScanID, "bundle manifest error: "+err.Error())
		return
	}

	if bundleManifest != nil {
		// Manifest-aware path: execute each control individually and
		// stream results back as they complete.
		handleManifestScan(ctx, tun, r, d, bundlePath, bundleManifest)
		return
	}

	// Legacy path: no bundle.yaml — run the monolithic entrypoint.
	manifest, err := runner.LoadManifest(bundlePath)
	if err != nil {
		slog.ErrorContext(ctx, "loading manifest", "error", err)
		sendError(tun, d.ScanID, "manifest error: "+err.Error())
		return
	}

	results, err := r.Run(ctx, runner.RunRequest{
		BundlePath:   bundlePath,
		Manifest:     manifest,
		TargetConfig: d.TargetConfig,
		Credentials:  d.Credentials,
	})
	if err != nil {
		slog.ErrorContext(ctx, "bundle execution failed", "scan_id", d.ScanID, "error", err)
		sendError(tun, d.ScanID, "execution error: "+err.Error())
		return
	}

	sendResults(tun, d.ScanID, results)
	slog.InfoContext(ctx, "scan completed", "scan_id", d.ScanID)
}

// handleManifestScan implements ADR 010 D5: iterate controls from bundle.yaml,
// execute each one individually, and stream partial results over WSS so the
// operator sees real-time per-control progress.
func handleManifestScan(ctx context.Context, tun *tunnel.Tunnel, r runner.Runner, d tunnel.DirectivePayload, bundlePath string, manifest *runner.BundleManifest) {
	total := len(manifest.Controls)
	slog.InfoContext(ctx, "manifest-aware scan starting",
		"scan_id", d.ScanID,
		"bundle", manifest.Name,
		"controls", total,
	)

	var failedControls int
	for i, controlID := range manifest.Controls {
		slog.InfoContext(ctx, "executing control",
			"control", controlID,
			"progress", fmt.Sprintf("%d/%d", i+1, total),
		)

		entrypoint := filepath.Join("controls", controlID, "check.py")
		result, err := r.RunControl(ctx, runner.ControlRunRequest{
			BundlePath:   bundlePath,
			ControlID:    controlID,
			Entrypoint:   entrypoint,
			TargetConfig: d.TargetConfig,
			Credentials:  d.Credentials,
		})
		if err != nil {
			slog.WarnContext(ctx, "control execution failed",
				"control", controlID,
				"error", err,
			)
			failedControls++
			// Emit an error result so the server records the failure.
			errResult := map[string]any{
				"control_id": controlID,
				"status":     "error",
				"title":      controlID,
				"evidence":   map[string]string{"detail": err.Error()},
			}
			raw, _ := json.Marshal(errResult)
			result = json.RawMessage(raw)
		}

		// Stream the single control result as a partial message.
		sendPartialResult(tun, d.ScanID, result)

		// Log the control outcome.
		var parsed map[string]any
		if json.Unmarshal(result, &parsed) == nil {
			slog.InfoContext(ctx, "control completed",
				"control", controlID,
				"status", parsed["status"],
			)
		}
	}

	// Send the terminal (non-partial) message to mark the scan completed.
	sendScanComplete(tun, d.ScanID)

	slog.InfoContext(ctx, "manifest-aware scan completed",
		"scan_id", d.ScanID,
		"controls_total", total,
		"controls_failed", failedControls,
	)
}

// tryCollectorPath checks if a Go collector binary is available for the
// directive's target type. If found, it runs the collector, streams facts
// to the server via WSS, and returns true (scan handled). On any failure
// it logs a warning and returns false so the caller falls through to the
// legacy Python bundle path.
func tryCollectorPath(ctx context.Context, tun *tunnel.Tunnel, d tunnel.DirectivePayload) bool {
	collectorID := runner.DetermineCollector(d.TargetType)
	if collectorID == "" {
		return false
	}

	slog.InfoContext(ctx, "using Go collector",
		"scan_id", d.ScanID,
		"collector", collectorID,
	)

	binPath, err := runner.EnsureCollector(collectorID)
	if err != nil {
		slog.WarnContext(ctx, "collector not available, falling back to bundle",
			"collector", collectorID, "error", err)
		return false
	}

	result, err := runner.RunCollector(ctx, binPath, d.TargetConfig, d.Credentials)
	if err != nil {
		slog.WarnContext(ctx, "collector failed, falling back to bundle",
			"collector", collectorID, "error", err)
		return false
	}

	factsCount := len(result.Facts)
	slog.InfoContext(ctx, "collector completed",
		"scan_id", d.ScanID,
		"collector", collectorID,
		"facts_count", factsCount,
	)

	// Stream facts to server for policy evaluation (ADR 011 D5).
	factsPayload, _ := json.Marshal(map[string]any{
		"scan_id":      d.ScanID,
		"collector_id": result.CollectorID,
		"facts":        result.Facts,
	})
	tun.Send(tunnel.Message{
		Type:    tunnel.TypeFactsCollected,
		Payload: factsPayload,
	})

	slog.InfoContext(ctx, "facts sent to server for policy evaluation",
		"scan_id", d.ScanID,
	)

	// Send a terminal scan_results with no results — the server handles
	// policy evaluation and produces findings from the facts.
	sendScanComplete(tun, d.ScanID)

	slog.InfoContext(ctx, "scan completed via collector",
		"scan_id", d.ScanID,
		"collector", collectorID,
	)
	return true
}

func sendStarted(tun *tunnel.Tunnel, scanID string) {
	sendStartedPayload(tun, tunnel.ScanStartedPayload{ScanID: scanID})
}

func sendStartedPayload(tun *tunnel.Tunnel, p tunnel.ScanStartedPayload) {
	payload, _ := json.Marshal(p)
	tun.Send(tunnel.Message{Type: tunnel.TypeScanStarted, Payload: payload})
}

func sendResults(tun *tunnel.Tunnel, scanID string, results json.RawMessage) {
	payload, _ := json.Marshal(tunnel.ScanResultsPayload{ScanID: scanID, Results: results})
	tun.Send(tunnel.Message{Type: tunnel.TypeScanResults, Payload: payload})
}

// sendPartialResult streams a single control's result while keeping the
// scan status as running on the server (partial=true).
func sendPartialResult(tun *tunnel.Tunnel, scanID string, result json.RawMessage) {
	// Wrap the single result in an array to match the server's expected
	// `results: []json.RawMessage` shape.
	wrapped, _ := json.Marshal([]json.RawMessage{result})
	payload, _ := json.Marshal(tunnel.ScanResultsPayload{
		ScanID:  scanID,
		Results: wrapped,
		Partial: true,
	})
	tun.Send(tunnel.Message{Type: tunnel.TypeScanResults, Payload: payload})
}

// sendScanComplete sends a terminal scan_results message with partial=false
// and no results, signaling the server to mark the scan as completed.
func sendScanComplete(tun *tunnel.Tunnel, scanID string) {
	payload, _ := json.Marshal(tunnel.ScanResultsPayload{
		ScanID:  scanID,
		Results: json.RawMessage(`[]`),
		Partial: false,
	})
	tun.Send(tunnel.Message{Type: tunnel.TypeScanResults, Payload: payload})
}

func sendError(tun *tunnel.Tunnel, scanID, errMsg string) {
	sendScanError(tun, tunnel.ScanErrorPayload{ScanID: scanID, Error: errMsg})
}

func sendScanError(tun *tunnel.Tunnel, p tunnel.ScanErrorPayload) {
	payload, _ := json.Marshal(p)
	tun.Send(tunnel.Message{Type: tunnel.TypeScanError, Payload: payload})
}

// resolveCredentialLocally dispatches on the resolver type and returns
// the credential JSON ({"username":"...","password":"..."}) for the
// bundle runner.
func resolveCredentialLocally(ctx context.Context, rc *tunnel.CredentialResolverConfig) (json.RawMessage, error) {
	switch rc.Type {
	case "hashicorp_vault":
		var cfg vault.ResolveConfig
		if err := json.Unmarshal(rc.Config, &cfg); err != nil {
			return nil, fmt.Errorf("parsing vault config: %w", err)
		}
		cred, err := vault.Resolve(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("vault resolve: %w", err)
		}
		credJSON, _ := json.Marshal(map[string]string{
			"username": cred.Username,
			"password": cred.Password,
		})
		slog.InfoContext(ctx, "agent-side credential resolved",
			"resolver_type", "hashicorp_vault")
		return credJSON, nil
	default:
		return nil, fmt.Errorf("unsupported credential resolver type: %q", rc.Type)
	}
}

// runUninstall calls the DC's /api/v1/agents/self endpoint to deregister
// this agent, using credentials loaded from env or the persisted creds
// file. Used by `silkstrand-agent uninstall` (container flow) and by a
// `docker run ... uninstall` one-off.
//
// Intentionally best-effort: if the server is unreachable or the agent row
// has already been deleted, we still exit 0 so `docker rm` etc. proceed.
func runUninstall() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// This subcommand returns before normal startup, so configure outbound TLS
	// here too — the self-deregister below must trust the custom CA on a
	// TLS-inspected network (ADR 013 D3). Best-effort: a bad CA path shouldn't
	// block uninstall, so log and continue rather than abort.
	if err := nettls.Configure(nettls.Options{CACertPath: cfg.CACertPath}); err != nil {
		slog.Warn("configuring outbound TLS for deregister (continuing)", "error", err)
	}
	if err := bootstrap.EnsureCreds(cfg, version); err != nil {
		// No creds to call with — nothing to deregister.
		slog.Warn("no credentials available; skipping server deregister", "error", err)
		return nil
	}
	httpURL := cfg.APIURL
	switch {
	case len(httpURL) >= 6 && httpURL[:6] == "wss://":
		httpURL = "https://" + httpURL[6:]
	case len(httpURL) >= 5 && httpURL[:5] == "ws://":
		httpURL = "http://" + httpURL[5:]
	}
	url := httpURL + "/api/v1/agents/self?agent_id=" + cfg.AgentID
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AgentKey)
	client := nettls.Client(15 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("deregister call failed (continuing)", "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("deregister returned non-2xx (continuing)", "status", resp.StatusCode)
		return nil
	}
	slog.Info("agent deregistered", "agent_id", cfg.AgentID)
	return nil
}

func printlnSafe(s string) { _, _ = os.Stdout.WriteString(s + "\n") }

// allowlistPath mirrors the recon package's default so both pieces read
// the same file.
func allowlistPath() string {
	if v := os.Getenv("SILKSTRAND_SCAN_ALLOWLIST_PATH"); v != "" {
		return v
	}
	return "/etc/silkstrand/scan-allowlist.yaml"
}

// handleDiscoveryDirective dispatches a discovery scan: walks the
// naabu → httpx → nuclei pipeline, streams asset_discovered batches
// over the tunnel, and emits a terminal discovery_completed.
//
// Bypasses the bundle cache — discovery directives carry no bundle
// content (the recon framework lives in this binary). The bundle_id /
// bundle_name on the directive are nominal so existing audit / scan
// rows have something to point at.
func handleDiscoveryDirective(tun *tunnel.Tunnel, d tunnel.DirectivePayload) {
	// Per-scan log scope — see ADR 008 D4.
	ctx := logstream.WithScanID(context.Background(), d.ScanID)
	sendStartedPayload(tun, tunnel.ScanStartedPayload{ScanID: d.ScanID, ChunkID: d.ChunkID})

	slog.InfoContext(ctx, "received discovery directive",
		"scan_id", d.ScanID,
		"target", d.TargetIdentifier,
		"target_type", d.TargetType,
	)

	emit := func(msgType string, payload any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		tun.Send(tunnel.Message{Type: msgType, Payload: raw})
		return nil
	}

	res, err := runner.ReconRun(ctx, d, emit)
	if err != nil {
		slog.ErrorContext(ctx, "discovery failed", "scan_id", d.ScanID, "error", err)
		sendScanError(tun, tunnel.ScanErrorPayload{
			ScanID:  d.ScanID,
			ChunkID: d.ChunkID,
			Error:   "discovery: " + err.Error(),
		})
		return
	}

	completed, _ := json.Marshal(tunnel.DiscoveryCompletedPayload{
		ScanID:       d.ScanID,
		ChunkID:      d.ChunkID,
		ChunkIndex:   d.ChunkIndex,
		AssetsFound:  res.AssetsFound,
		HostsScanned: res.HostsScanned,
	})
	tun.Send(tunnel.Message{Type: tunnel.TypeDiscoveryCompleted, Payload: completed})
	slog.InfoContext(ctx, "discovery completed", "scan_id", d.ScanID,
		"assets_found", res.AssetsFound, "hosts_scanned", res.HostsScanned)
}
