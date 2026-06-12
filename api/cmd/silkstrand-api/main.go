// Package main is the DC API server entrypoint. Post-ADR-006/007 P2 the
// asset-first ingest path is live: asset_discovered batches upsert into
// assets + asset_endpoints, asset_events are derived, the rule engine
// fires (with collection-aware predicates), and allowlist_snapshot
// populates agent_allowlists so the UI viewer works.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/jtb75/silkstrand/api/internal/allowlist"
	"github.com/jtb75/silkstrand/api/internal/audit"
	"github.com/jtb75/silkstrand/api/internal/config"
	"github.com/jtb75/silkstrand/api/internal/events"
	"github.com/jtb75/silkstrand/api/internal/handler"
	"github.com/jtb75/silkstrand/api/internal/middleware"
	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/notify"
	"github.com/jtb75/silkstrand/api/internal/policy"
	"github.com/jtb75/silkstrand/api/internal/pubsub"
	"github.com/jtb75/silkstrand/api/internal/rules"
	"github.com/jtb75/silkstrand/api/internal/scheduler"
	"github.com/jtb75/silkstrand/api/internal/store"
	"github.com/jtb75/silkstrand/api/internal/websocket"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	pgStore, err := store.NewPostgresStore(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pgStore.Close()

	if err := runMigrations(pgStore.DB()); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	ps, err := pubsub.New(cfg.RedisURL)
	if err != nil {
		slog.Warn("redis not available, pub/sub disabled", "error", err)
		ps = nil
	} else {
		defer ps.Close()
	}

	// Audit writer (ADR 005 D4). Enabled by default; disable with
	// AUDIT_EVENTS_ENABLED=false. PostgresWriter batch-inserts via a
	// background goroutine; NoopWriter drops silently.
	var auditW audit.Writer
	if cfg.AuditEventsEnabled {
		auditW = audit.NewPostgresWriter(pgStore.DB())
		slog.Info("audit events enabled")
	} else {
		auditW = audit.NoopWriter{}
		slog.Info("audit events disabled")
	}
	defer auditW.Close()

	websocket.AllowedOrigins = cfg.AllowedOrigins
	hub := websocket.NewHub()
	notifier := notify.New(pgStore, cfg.CredentialEncryptionKey)
	// Events bus — in-process pub/sub for SSE fan-out. Passed to handlers
	// that need to publish (agent WSS → scan_progress, agent_log, scan_status)
	// and to the events handler that exposes the SSE subscriber endpoint.
	eventBus := events.NewMemoryBus()
	sched := scheduler.New(pgStore, ps, eventBus)
	hub.OnMessage = buildOnMessage(pgStore, ps, notifier, sched.D, eventBus, auditW)

	// Scheduler goroutine (ADR 007 D4). Started before routes are served
	// so locally-due definitions dispatch promptly on boot.
	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel()
	go sched.Run(schedCtx)

	// Handlers — surviving surface
	healthH := handler.NewHealthHandler(pgStore, redisPingFunc(ps))
	targetH := handler.NewTargetHandler(pgStore)
	scanH := handler.NewScanHandler(pgStore, ps, hub, eventBus)
	agentH := handler.NewAgentHandler(hub, pgStore, ps, cfg.CredentialEncryptionKey, eventBus, auditW)
	agentsH := handler.NewAgentsHandler(pgStore, hub, ps, eventBus, auditW, cfg.AgentReleasesURL)
	credsH := handler.NewCredentialsHandler(pgStore, cfg.CredentialEncryptionKey, auditW, ps)
	probeH := handler.NewProbeHandler(pgStore, ps, cfg.CredentialEncryptionKey)
	bundlesH := handler.NewBundlesHandler(pgStore, cfg.BundleStoragePath, cfg.BundleGCSBucket, cfg.BundleControlsDir)
	internalH := handler.NewInternalHandler(pgStore, cfg.CredentialEncryptionKey)
	assetH := handler.NewAssetHandler(pgStore)

	// Handlers — new asset-first surface (collections has working impls;
	// the rest return 501 in P1).
	collectionsH := handler.NewCollectionsHandler(pgStore, auditW)
	findingsH := handler.NewFindingsHandler(pgStore)
	scanDefsH := handler.NewScanDefinitionsHandler(pgStore, sched.D, auditW)
	credMapH := handler.NewCredentialMappingsHandler(pgStore, auditW)
	dashH := handler.NewDashboardHandler(pgStore)
	profilesH := handler.NewProfilesHandler(pgStore, cfg.BundleControlsDir, cfg.BundleStoragePath)
	rulesH := handler.NewCorrelationRulesHandler(pgStore, auditW)
	tenantPoliciesH := handler.NewTenantPoliciesHandler(pgStore, cfg.PoliciesDir)
	auditH := handler.NewAuditHandler(pgStore.DB())
	eventsH := handler.NewEventsHandler(eventBus, cfg.JWTSecret)

	// Policy evaluator (ADR 011 D10) — loaded once at startup, used for
	// retroactive evaluation replay.
	policyEvaluator := policy.NewEvaluator()
	if err := policyEvaluator.LoadFromDir(cfg.PolicyDir); err != nil {
		slog.Warn("failed to load policies", "error", err, "dir", cfg.PolicyDir)
	}
	evalH := handler.NewEvaluationHandler(pgStore, policyEvaluator, auditW)

	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /healthz", healthH.Healthz)
	mux.HandleFunc("GET /readyz", healthH.Readyz)

	// Agent WSS
	mux.HandleFunc("GET /ws/agent", agentH.Connect)

	// Agent bootstrap + self-delete (authed by agent key, not tenant JWT)
	mux.HandleFunc("POST /api/v1/agents/bootstrap", agentsH.Bootstrap)
	mux.HandleFunc("DELETE /api/v1/agents/self", agentH.SelfDelete)

	// Events SSE stream — auth is handled in-handler because EventSource
	// can't set Authorization headers; accepts either the tenant JWT in
	// the header (curl/CLI) or a short-lived stream token in ?token=.
	// Attaching this to the plain mux avoids double-auth through the
	// middleware chain and preserves the query-string-token path.
	mux.HandleFunc("GET /api/v1/events/stream", eventsH.Stream)

	// Internal (backoffice)
	internalMux := http.NewServeMux()
	internalMux.HandleFunc("PUT /internal/v1/bundles", internalH.UpsertBundle)
	internalMux.HandleFunc("POST /internal/v1/tenants", internalH.CreateTenant)
	internalMux.HandleFunc("GET /internal/v1/tenants", internalH.ListTenants)
	internalMux.HandleFunc("GET /internal/v1/tenants/{id}", internalH.GetTenant)
	internalMux.HandleFunc("PUT /internal/v1/tenants/{id}", internalH.UpdateTenant)
	internalMux.HandleFunc("DELETE /internal/v1/tenants/{id}", internalH.DeleteTenant)
	internalMux.HandleFunc("GET /internal/v1/agents", internalH.ListAgents)
	internalMux.HandleFunc("GET /internal/v1/stats", internalH.GetStats)
	internalMux.HandleFunc("POST /internal/v1/credentials", internalH.CreateCredential)
	authedInternal := middleware.InternalAuth(cfg.InternalAPIKey)(internalMux)
	mux.Handle("/internal/", authedInternal)

	// Tenant API
	apiMux := http.NewServeMux()

	// Targets (narrowed to CIDR / network_range per ADR 006 D8)
	apiMux.HandleFunc("GET /api/v1/targets", targetH.List)
	apiMux.HandleFunc("POST /api/v1/targets", targetH.Create)
	apiMux.HandleFunc("GET /api/v1/targets/{id}", targetH.Get)
	apiMux.HandleFunc("PUT /api/v1/targets/{id}", targetH.Update)
	apiMux.HandleFunc("DELETE /api/v1/targets/{id}", targetH.Delete)

	apiMux.HandleFunc("GET /api/v1/targets/{id}/credential", credsH.Get)
	apiMux.HandleFunc("PUT /api/v1/targets/{id}/credential", credsH.Put)
	apiMux.HandleFunc("DELETE /api/v1/targets/{id}/credential", credsH.Delete)
	apiMux.HandleFunc("POST /api/v1/targets/{id}/probe", probeH.Probe)

	// Bundles + controls
	apiMux.HandleFunc("GET /api/v1/bundles", bundlesH.List)
	apiMux.HandleFunc("POST /api/v1/bundles/upload", bundlesH.Upload)
	apiMux.HandleFunc("GET /api/v1/bundles/{id}/controls", bundlesH.ListControls)
	apiMux.HandleFunc("GET /api/v1/controls", bundlesH.ListAllControls)
	apiMux.HandleFunc("GET /api/v1/controls/{control_id}/rego", bundlesH.GetControlRego)

	// Compliance profiles (ADR 010 D9 — Level 3A)
	apiMux.HandleFunc("GET /api/v1/compliance-profiles", profilesH.List)
	apiMux.HandleFunc("POST /api/v1/compliance-profiles", profilesH.Create)
	apiMux.HandleFunc("GET /api/v1/compliance-profiles/{id}", profilesH.Get)
	apiMux.HandleFunc("PUT /api/v1/compliance-profiles/{id}", profilesH.Update)
	apiMux.HandleFunc("DELETE /api/v1/compliance-profiles/{id}", profilesH.Delete)
	apiMux.HandleFunc("POST /api/v1/compliance-profiles/{id}/controls", profilesH.SetControls)
	apiMux.HandleFunc("GET /api/v1/compliance-profiles/{id}/controls", profilesH.GetControls)
	apiMux.HandleFunc("POST /api/v1/compliance-profiles/{id}/publish", profilesH.Publish)

	// Tenant policies (ADR 011 D9)
	apiMux.HandleFunc("GET /api/v1/tenant-policies", tenantPoliciesH.List)
	apiMux.HandleFunc("POST /api/v1/tenant-policies", tenantPoliciesH.Create)
	apiMux.HandleFunc("POST /api/v1/tenant-policies/validate", tenantPoliciesH.Validate)
	apiMux.HandleFunc("POST /api/v1/tenant-policies/copy-from/{builtin_control_id}", tenantPoliciesH.CopyFromBuiltin)
	apiMux.HandleFunc("GET /api/v1/tenant-policies/{id}", tenantPoliciesH.Get)
	apiMux.HandleFunc("PUT /api/v1/tenant-policies/{id}", tenantPoliciesH.Update)
	apiMux.HandleFunc("DELETE /api/v1/tenant-policies/{id}", tenantPoliciesH.Delete)

	// Agents
	apiMux.HandleFunc("GET /api/v1/agents/downloads", agentsH.Downloads)
	apiMux.HandleFunc("GET /api/v1/agents", agentsH.List)
	apiMux.HandleFunc("POST /api/v1/agents", agentsH.Create)
	apiMux.HandleFunc("GET /api/v1/agents/{id}", agentsH.Get)
	apiMux.HandleFunc("GET /api/v1/agents/{id}/allowlist", agentsH.GetAllowlist)
	apiMux.HandleFunc("GET /api/v1/agents/{id}/logs", agentsH.Logs)
	apiMux.HandleFunc("POST /api/v1/agents/{id}/rotate-key", agentsH.RotateKey)
	apiMux.HandleFunc("POST /api/v1/agents/{id}/upgrade", agentsH.Upgrade)
	apiMux.HandleFunc("DELETE /api/v1/agents/{id}", agentsH.Delete)
	apiMux.HandleFunc("POST /api/v1/agents/install-tokens", agentsH.CreateInstallToken)

	// Scans (ad-hoc debug path; scan_definitions is the durable surface)
	apiMux.HandleFunc("POST /api/v1/scans", scanH.Create)
	apiMux.HandleFunc("GET /api/v1/scans", scanH.List)
	apiMux.HandleFunc("GET /api/v1/scans/{id}", scanH.Get)
	apiMux.HandleFunc("GET /api/v1/scans/{id}/facts", scanH.Facts)
	apiMux.HandleFunc("DELETE /api/v1/scans/{id}", scanH.Delete)

	// Assets (ADR 006 D2) — list works against empty `assets`, detail
	// returns 404 post-migration-017 because all rows are gone; promote
	// returns 501 (superseded by scan_definitions).
	apiMux.HandleFunc("GET /api/v1/assets", assetH.List)
	apiMux.HandleFunc("GET /api/v1/asset-endpoints", assetH.ListEndpoints)
	apiMux.HandleFunc("GET /api/v1/assets/{id}", assetH.Get)
	apiMux.HandleFunc("GET /api/v1/assets/{id}/endpoints/{endpoint_id}", assetH.GetEndpoint)
	apiMux.HandleFunc("POST /api/v1/assets/{id}/promote", assetH.Promote)

	// Collections (ADR 006 D5) — working CRUD in P1.
	apiMux.HandleFunc("GET /api/v1/collections", collectionsH.List)
	apiMux.HandleFunc("POST /api/v1/collections", collectionsH.Create)
	apiMux.HandleFunc("POST /api/v1/collections/preview", collectionsH.Preview)
	apiMux.HandleFunc("GET /api/v1/collections/{id}", collectionsH.Get)
	apiMux.HandleFunc("PUT /api/v1/collections/{id}", collectionsH.Update)
	apiMux.HandleFunc("DELETE /api/v1/collections/{id}", collectionsH.Delete)
	apiMux.HandleFunc("POST /api/v1/collections/{id}/preview", collectionsH.Preview)
	apiMux.HandleFunc("GET /api/v1/collections/{id}/members", collectionsH.Members)

	// Correlation rules — 501 stubs (P2).
	apiMux.HandleFunc("GET /api/v1/correlation-rules", rulesH.List)
	apiMux.HandleFunc("POST /api/v1/correlation-rules", rulesH.Create)
	apiMux.HandleFunc("GET /api/v1/correlation-rules/{id}", rulesH.Get)
	apiMux.HandleFunc("PUT /api/v1/correlation-rules/{id}", rulesH.Update)
	apiMux.HandleFunc("DELETE /api/v1/correlation-rules/{id}", rulesH.Delete)

	// Findings (ADR 007 D1) — 501 stubs (P3).
	apiMux.HandleFunc("GET /api/v1/findings", findingsH.List)
	apiMux.HandleFunc("GET /api/v1/findings/{id}", findingsH.Get)
	apiMux.HandleFunc("POST /api/v1/findings/{id}/suppress", findingsH.Suppress)
	apiMux.HandleFunc("POST /api/v1/findings/{id}/reopen", findingsH.Reopen)

	// Retroactive policy evaluation (ADR 011 D10).
	apiMux.HandleFunc("POST /api/v1/evaluations/replay", evalH.Replay)

	// Scan definitions (ADR 007 D3) — 501 stubs (P3).
	apiMux.HandleFunc("GET /api/v1/scan-definitions", scanDefsH.List)
	apiMux.HandleFunc("POST /api/v1/scan-definitions", scanDefsH.Create)
	apiMux.HandleFunc("GET /api/v1/scan-definitions/{id}", scanDefsH.Get)
	apiMux.HandleFunc("PUT /api/v1/scan-definitions/{id}", scanDefsH.Update)
	apiMux.HandleFunc("DELETE /api/v1/scan-definitions/{id}", scanDefsH.Delete)
	apiMux.HandleFunc("POST /api/v1/scan-definitions/{id}/execute", scanDefsH.Execute)
	apiMux.HandleFunc("GET /api/v1/scan-definitions/{id}/coverage", scanDefsH.Coverage)
	apiMux.HandleFunc("POST /api/v1/scan-definitions/{id}/enable", scanDefsH.Enable)
	apiMux.HandleFunc("POST /api/v1/scan-definitions/{id}/disable", scanDefsH.Disable)

	// Credential sources (ADR 004 C0 + ADR 006 P6 consolidated surface)
	apiMux.HandleFunc("GET /api/v1/credential-sources", credsH.ListSources)
	apiMux.HandleFunc("POST /api/v1/credential-sources", credsH.CreateSource)
	apiMux.HandleFunc("GET /api/v1/credential-sources/{id}", credsH.GetSource)
	apiMux.HandleFunc("PUT /api/v1/credential-sources/{id}", credsH.UpdateSource)
	apiMux.HandleFunc("DELETE /api/v1/credential-sources/{id}", credsH.DeleteSource)
	apiMux.HandleFunc("POST /api/v1/credential-sources/{id}/test", credsH.TestSource)

	// Credential mappings (ADR 006 P6).
	apiMux.HandleFunc("GET /api/v1/credential-mappings", credMapH.List)
	apiMux.HandleFunc("POST /api/v1/credential-mappings", credMapH.Create)
	apiMux.HandleFunc("POST /api/v1/credential-mappings/bulk", credMapH.BulkCreate)
	apiMux.HandleFunc("GET /api/v1/credential-mappings/{id}", credMapH.Get)
	apiMux.HandleFunc("DELETE /api/v1/credential-mappings/{id}", credMapH.Delete)

	// Events — stream-token mint endpoint (tenant JWT required). The SSE
	// stream itself is on the public mux because EventSource can't send
	// headers, so it authenticates via ?token=<stream-token>.
	apiMux.HandleFunc("POST /api/v1/events/stream-tokens", eventsH.MintStreamToken)

	// Dashboard (P5-a). KPIs, Suggested Actions, Recent Activity.
	apiMux.HandleFunc("GET /api/v1/dashboard", dashH.Get)
	apiMux.HandleFunc("GET /api/v1/dashboard/kpis", dashH.GetKPIs)
	apiMux.HandleFunc("GET /api/v1/dashboard/suggested-actions", dashH.GetSuggestedActions)
	apiMux.HandleFunc("GET /api/v1/dashboard/recent-activity", dashH.GetRecentActivity)

	// Audit events (ADR 005 D5).
	apiMux.HandleFunc("GET /api/v1/audit-events", auditH.List)

	authedAPI := middleware.Auth(cfg.JWTSecret)(middleware.Tenant(pgStore)(apiMux))
	mux.Handle("/api/", authedAPI)

	corsOrigins := strings.Join(cfg.AllowedOrigins, ",")
	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      middleware.CORS(corsOrigins)(middleware.Logging(mux)),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server starting", "port", cfg.Port)
		errCh <- server.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("shutting down", "signal", sig)
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

// buildOnMessage routes agent → server WSS messages. P2 live surface:
// asset_discovered and allowlist_snapshot ingest against the new
// asset / asset_endpoint schema; the rule engine fires per endpoint.
// ADR 008 adds agent_log: republish through the event bus (no DB write)
// for SSE fan-out to the Agents and Scan-Results consoles.
func buildOnMessage(s store.Store, ps *pubsub.PubSub, notifier *notify.Dispatcher, sched scheduler.Dispatcher, bus events.Bus, auditW audit.Writer) func(agentID string, msg websocket.Message) {
	return func(agentID string, msg websocket.Message) {
		ctx := context.Background()

		switch msg.Type {
		case websocket.TypeScanStarted:
			var payload websocket.ScanStartedPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				slog.Error("parsing scan_started payload", "agent_id", agentID, "error", err)
				return
			}
			if err := s.UpdateScanStatus(ctx, payload.ScanID, model.ScanStatusRunning); err != nil {
				slog.Error("updating scan to running", "scan_id", payload.ScanID, "error", err)
			}
			if scan, _ := s.GetScanByID(ctx, payload.ScanID); scan != nil {
				handler.PublishScanStatusFromScan(ctx, bus, scan)
			}
			slog.Info("scan started", "agent_id", agentID, "scan_id", payload.ScanID)

		case websocket.TypeScanResults:
			handleScanResults(ctx, s, bus, auditW, agentID, msg.Payload)
			sched.DrainAgentQueue(ctx, agentID)

		case websocket.TypeScanError:
			var payload websocket.ScanErrorPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				slog.Error("parsing scan_error payload", "agent_id", agentID, "error", err)
				return
			}
			if err := s.FailScan(ctx, payload.ScanID, payload.Error); err != nil {
				slog.Error("updating scan to failed", "scan_id", payload.ScanID, "error", err)
			}
			if scan, _ := s.GetScanByID(ctx, payload.ScanID); scan != nil {
				handler.PublishScanStatusFromScan(ctx, bus, scan)
				auditW.Emit(ctx, audit.Event{
					TenantID: scan.TenantID, EventType: audit.EventScanFailed,
					ActorType: audit.ActorAgent, ActorID: agentID,
					ResourceType: "scan", ResourceID: scan.ID,
					Payload: map[string]any{"error": payload.Error},
				})
			}
			slog.Warn("scan failed", "agent_id", agentID, "scan_id", payload.ScanID, "error", payload.Error)
			sched.DrainAgentQueue(ctx, agentID)

		case websocket.TypeProbeResult:
			var result websocket.ProbeResultPayload
			if err := json.Unmarshal(msg.Payload, &result); err != nil {
				slog.Error("parsing probe_result payload", "agent_id", agentID, "error", err)
				return
			}
			if ps != nil {
				if err := ps.PublishProbeResult(ctx, result.ProbeID, msg.Payload); err != nil {
					slog.Error("publishing probe_result", "probe_id", result.ProbeID, "error", err)
				}
			}

		case websocket.TypeCredentialTestResult:
			var result websocket.CredentialTestResultPayload
			if err := json.Unmarshal(msg.Payload, &result); err != nil {
				slog.Error("parsing credential_test_result payload", "agent_id", agentID, "error", err)
				return
			}
			if ps != nil {
				if err := ps.PublishCredentialTestResult(ctx, result.TestID, msg.Payload); err != nil {
					slog.Error("publishing credential_test_result", "test_id", result.TestID, "error", err)
				}
			}

		case websocket.TypeHeartbeat:
			var hb websocket.HeartbeatPayload
			if err := json.Unmarshal(msg.Payload, &hb); err != nil {
				slog.Debug("parsing heartbeat payload", "agent_id", agentID, "error", err)
			}
			if err := s.UpdateAgentHeartbeat(ctx, agentID, hb.Version); err != nil {
				slog.Error("updating agent heartbeat", "agent_id", agentID, "error", err)
			}

		case websocket.TypeAllowlistSnapshot:
			handleAllowlistSnapshot(ctx, s, agentID, msg.Payload)
			// ADR 013 D5: the snapshot just landed, so an opted-in agent can
			// now run its first discovery against a real allowlist.
			maybeAutoDiscover(ctx, s, sched, agentID)

		case websocket.TypeAssetDiscovered:
			handleAssetDiscovered(ctx, s, notifier, sched, bus, auditW, agentID, msg.Payload)

		case websocket.TypeAgentLog:
			handleAgentLog(ctx, s, bus, agentID, msg.Payload)

		case websocket.TypeDiscoveryCompleted:
			var payload websocket.DiscoveryCompletedPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				slog.Error("parsing discovery_completed payload", "agent_id", agentID, "error", err)
				return
			}
			if err := s.UpdateScanStatus(ctx, payload.ScanID, model.ScanStatusCompleted); err != nil {
				slog.Error("updating discovery scan to completed", "scan_id", payload.ScanID, "error", err)
			}
			if scan, _ := s.GetScanByID(ctx, payload.ScanID); scan != nil {
				handler.PublishScanStatusFromScan(ctx, bus, scan)
			}
			slog.Info("discovery completed", "agent_id", agentID, "scan_id", payload.ScanID,
				"assets_found", payload.AssetsFound, "hosts_scanned", payload.HostsScanned)
			sched.DrainAgentQueue(ctx, agentID)

		case websocket.TypeFactsCollected:
			handleFactsCollected(ctx, s, auditW, agentID, msg.Payload)

		default:
			slog.Warn("unknown message type from agent", "agent_id", agentID, "type", msg.Type)
		}
	}
}

func runMigrations(db *sql.DB) error {
	sourceDriver, err := iofs.New(store.MigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("creating migration source: %w", err)
	}
	dbDriver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("creating migration db driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", sourceDriver, "postgres", dbDriver)
	if err != nil {
		return fmt.Errorf("creating migrator: %w", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("running migrations: %w", err)
	}
	slog.Info("migrations complete")
	return nil
}

func redisPingFunc(ps *pubsub.PubSub) func(context.Context) error {
	if ps == nil {
		return nil
	}
	return ps.Ping
}

// ======================================================================
// P2 ingest handlers (ADR 006 D2 + D4 + D7 + D9)
// ======================================================================

// handleAssetDiscovered upserts a batch of agent-reported (ip, port,
// service, ...) tuples into assets + asset_endpoints, logs provenance,
// derives asset_events for deltas, and runs the collection-aware rule
// engine per endpoint.
func handleAssetDiscovered(ctx context.Context, s store.Store, notifier *notify.Dispatcher, sched scheduler.Dispatcher, bus events.Bus, auditW audit.Writer, agentID string, payload json.RawMessage) {
	var batch websocket.AssetDiscoveredPayload
	if err := json.Unmarshal(payload, &batch); err != nil {
		slog.Error("parsing asset_discovered payload", "agent_id", agentID, "error", err)
		return
	}
	scan, err := s.GetScanByID(ctx, batch.ScanID)
	if err != nil || scan == nil {
		slog.Error("loading discovery scan", "scan_id", batch.ScanID, "error", err)
		return
	}
	if scan.Status == model.ScanStatusPending {
		if err := s.UpdateScanStatus(ctx, scan.ID, model.ScanStatusRunning); err != nil {
			slog.Error("updating discovery scan to running", "scan_id", scan.ID, "error", err)
		}
		scan.Status = model.ScanStatusRunning
		handler.PublishScanStatusFromScan(ctx, bus, scan)
	}

	// Load tenant rules + allowlist snapshot once per batch.
	activeRules, err := s.ListActiveRulesForTrigger(ctx, scan.TenantID, model.RuleTriggerAssetDiscovered)
	if err != nil {
		slog.Warn("loading asset_discovered rules",
			"tenant", scan.TenantID, "error", err)
	}
	aw := loadAgentAllowlist(ctx, s, agentID)

	// Tenant context for store writes that consult TenantID(ctx).
	tctx := store.WithTenantID(ctx, scan.TenantID)

	// Nullable FK values for provenance + events.
	var (
		scanIDPtr   = strPtr(scan.ID)
		targetIDPtr *string
		agentIDPtr  = strPtr(agentID)
	)
	if scan.TargetID != nil {
		targetIDPtr = scan.TargetID
	}

	for _, a := range batch.Assets {
		asset, _, err := upsertHostAsset(tctx, s, scan.TenantID, a)
		if err != nil {
			slog.Error("upserting asset", "scan_id", scan.ID, "ip", a.IP, "error", err)
			continue
		}
		// Record provenance per asset per scan. The (asset_id, discovered_at)
		// PK naturally dedupes within the same millisecond; successive
		// scans produce successive rows — which is what ADR 006 D9 wants.
		if err := s.RecordDiscoverySource(tctx, store.DiscoverySourceInput{
			AssetID:  asset.ID,
			TargetID: targetIDPtr,
			AgentID:  agentIDPtr,
			ScanID:   scanIDPtr,
		}); err != nil {
			slog.Warn("recording discovery source", "asset", asset.ID, "error", err)
		}
		if a.Port == 0 {
			continue // host-only report (no port info from naabu stage yet)
		}
		hostname := a.Hostname
		if hostname == "" && asset.Hostname != nil {
			hostname = *asset.Hostname
		}
		awStatus := evalAllowlistStatus(aw, a.IP, hostname)
		var oldEndpoint *model.AssetEndpoint
		endpoints, _ := s.ListEndpointsForAsset(tctx, asset.ID)
		for i := range endpoints {
			if endpoints[i].Port == a.Port && endpoints[i].Protocol == "tcp" {
				ep := endpoints[i]
				oldEndpoint = &ep
				break
			}
		}
		ep, err := s.UpsertAssetEndpoint(tctx, store.UpsertAssetEndpointInput{
			AssetID:         asset.ID,
			Port:            a.Port,
			Protocol:        "tcp",
			Service:         a.Service,
			Version:         a.Version,
			Technologies:    a.Technologies,
			AllowlistStatus: awStatus,
		})
		if err != nil {
			slog.Error("upserting asset endpoint",
				"scan_id", scan.ID, "ip", a.IP, "port", a.Port, "error", err)
			continue
		}
		events := deriveAssetEvents(scan.TenantID, scan.ID, oldEndpoint, ep, a)
		if err := s.AppendAssetEvents(tctx, events); err != nil {
			slog.Error("appending asset events", "endpoint", ep.ID, "error", err)
		}
		ingestNucleiFindings(tctx, s, scan.TenantID, scan.ID, ep.ID, a.CVEs)
		runRuleActions(tctx, s, notifier, sched, auditW, activeRules, asset, ep)
	}
}

// upsertHostAsset folds a discovered asset into the host-level row.
// Returns isNewAsset=true when this is the first time we've seen the
// host (ie. first_seen == created_at after the upsert).
func upsertHostAsset(ctx context.Context, s store.Store, tenantID string, a websocket.DiscoveredAssetUpsert) (*model.Asset, bool, error) {
	asset, err := s.UpsertAsset(ctx, store.UpsertAssetInput{
		TenantID:  tenantID,
		PrimaryIP: a.IP,
		Hostname:  a.Hostname,
		Source:    model.AssetSourceDiscovered,
	})
	if err != nil {
		return nil, false, err
	}
	// isNew heuristic: equal timestamps before any ON CONFLICT update.
	isNew := asset.FirstSeen.Equal(asset.LastSeen) && asset.LastSeen.Equal(asset.CreatedAt)
	return asset, isNew, nil
}

// handleAllowlistSnapshot persists the agent's reported scan policy and,
// when the hash actually changes, re-evaluates every endpoint owned by
// this agent's known assets so the UI badge reflects the new policy
// without waiting for rediscovery.
func handleAllowlistSnapshot(ctx context.Context, s store.Store, agentID string, payload json.RawMessage) {
	var p websocket.AllowlistSnapshotPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		slog.Warn("parsing allowlist_snapshot payload", "agent_id", agentID, "error", err)
		return
	}
	changed, err := s.UpsertAgentAllowlist(ctx, store.AgentAllowlistInput{
		AgentID:      agentID,
		Hash:         p.Hash,
		Allow:        p.Allow,
		Deny:         p.Deny,
		RateLimitPPS: p.RateLimitPPS,
	})
	if err != nil {
		slog.Error("upserting agent allowlist", "agent_id", agentID, "error", err)
		return
	}
	slog.Info("allowlist snapshot received",
		"agent_id", agentID, "hash", p.Hash, "changed", changed)
	// Reeval across all endpoints owned by this agent is deferred —
	// the provenance join table makes this a multi-step query. Per
	// discovery, each endpoint is restamped with the fresh status
	// naturally. If this becomes a UX gap we'll revisit.
}

// maybeAutoDiscover fires the discovery a tenant opted into at install time
// (ADR 013 D5) — exactly once, after the agent's first allowlist snapshot, so
// it resolves against real scope. ClaimAutoDiscover is the fire-once guard.
func maybeAutoDiscover(ctx context.Context, s store.Store, sched scheduler.Dispatcher, agentID string) {
	pending, cron, err := s.ClaimAutoDiscover(ctx, agentID)
	if err != nil {
		slog.Error("auto-discover: claim", "agent_id", agentID, "error", err)
		return
	}
	if !pending {
		return
	}
	agent, err := s.GetAgentByID(ctx, agentID)
	if err != nil || agent == nil {
		slog.Error("auto-discover: agent lookup", "agent_id", agentID, "error", err)
		return
	}
	created, err := s.CreateScanDefinition(ctx, model.ScanDefinition{
		TenantID:  agent.TenantID,
		Name:      "Auto-discovery",
		Kind:      model.ScanDefinitionKindDiscovery,
		ScopeKind: model.ScanDefinitionScopeAgentAllowlist,
		AgentID:   &agentID,
		Schedule:  cron, // nil = on-connect only; set = recurring (D5)
		Enabled:   true,
	})
	if err != nil {
		slog.Error("auto-discover: creating definition", "agent_id", agentID, "error", err)
		return
	}
	if err := sched.Execute(ctx, *created); err != nil {
		slog.Warn("auto-discover: initial dispatch", "agent_id", agentID,
			"definition", created.ID, "error", err)
	}
	slog.Info("auto-discover dispatched", "agent_id", agentID,
		"definition", created.ID, "recurring", cron != nil)
}

// handleAgentLog receives the agent's {type:"agent_log"} WSS message,
// persists it to the agent_log_events table, and republishes on the
// event bus for SSE live-tail consumers. Tenant is resolved from the
// agent row so cross-tenant leakage is impossible.
func handleAgentLog(ctx context.Context, s store.Store, bus events.Bus, agentID string, payload json.RawMessage) {
	if len(payload) == 0 {
		return
	}
	agent, err := s.GetAgentByID(ctx, agentID)
	if err != nil || agent == nil {
		// Agent row should exist — the WSS connection wouldn't have
		// authed otherwise. Drop the event rather than letting an
		// unknown agent's payload onto the bus.
		slog.Warn("agent_log: agent lookup failed", "agent_id", agentID, "error", err)
		return
	}

	// Parse payload to extract structured fields for the table.
	var logEntry struct {
		Level  string `json:"level"`
		Msg    string `json:"msg"`
		ScanID string `json:"scan_id"`
	}
	_ = json.Unmarshal(payload, &logEntry)

	// Persist to DB — best effort; don't block the SSE path on failure.
	if err := s.InsertAgentLogEvent(ctx, store.AgentLogEventInput{
		TenantID: agent.TenantID,
		AgentID:  agentID,
		ScanID:   logEntry.ScanID,
		Level:    logEntry.Level,
		Msg:      logEntry.Msg,
		Attrs:    payload,
	}); err != nil {
		slog.Warn("agent_log persist failed", "agent_id", agentID, "error", err)
	}

	// Publish to bus for SSE live tail (existing behavior).
	if bus != nil {
		if err := bus.Publish(ctx, events.Event{
			TenantID:     agent.TenantID,
			Kind:         "agent_log",
			ResourceType: "agent",
			ResourceID:   agentID,
			OccurredAt:   time.Now().UTC(),
			Payload:      payload,
		}); err != nil {
			slog.Warn("agent_log publish failed", "agent_id", agentID, "error", err)
		}
	}
}

func loadAgentAllowlist(ctx context.Context, s store.Store, agentID string) *allowlist.Allowlist {
	snap, err := s.GetAgentAllowlist(ctx, agentID)
	if err != nil {
		slog.Warn("loading agent allowlist", "agent_id", agentID, "error", err)
		return nil
	}
	if snap == nil {
		return nil
	}
	aw, err := allowlist.Parse(snap.Allow, snap.Deny)
	if err != nil {
		slog.Warn("parsing agent allowlist", "agent_id", agentID, "error", err)
		return nil
	}
	return aw
}

func evalAllowlistStatus(aw *allowlist.Allowlist, ip, hostname string) *string {
	if aw == nil {
		return nil
	}
	if ip != "" && aw.Allows(ip) {
		s := model.AllowlistStatusAllowlisted
		return &s
	}
	if hostname != "" && aw.Allows(hostname) {
		s := model.AllowlistStatusAllowlisted
		return &s
	}
	s := model.AllowlistStatusOutOfPolicy
	return &s
}

// deriveAssetEvents diffs the old and new endpoint rows and emits
// per-ADR 006 D4 events. FK points at asset_endpoints(id).
func deriveAssetEvents(tenantID, scanID string, old, new *model.AssetEndpoint, upsert websocket.DiscoveredAssetUpsert) []model.AssetEvent {
	if new == nil {
		return nil
	}
	var events []model.AssetEvent
	sid := scanID
	mk := func(eventType string, payload map[string]any) {
		b, _ := json.Marshal(payload)
		events = append(events, model.AssetEvent{
			TenantID:  tenantID,
			AssetID:   new.ID, // endpoint id per ADR 006 D4
			ScanID:    &sid,
			EventType: eventType,
			Payload:   b,
			// OccurredAt left zero; store uses NOW() default.
		})
	}
	if old == nil {
		mk(model.AssetEventNewAsset, map[string]any{
			"service": derefStr(new.Service),
			"version": derefStr(new.Version),
			"port":    new.Port,
		})
		mk(model.AssetEventPortOpened, map[string]any{
			"port":    new.Port,
			"service": derefStr(new.Service),
		})
	} else if derefStr(old.Service) != derefStr(new.Service) ||
		derefStr(old.Version) != derefStr(new.Version) {
		mk(model.AssetEventVersionChanged, map[string]any{
			"from_service": derefStr(old.Service),
			"to_service":   derefStr(new.Service),
			"from_version": derefStr(old.Version),
			"to_version":   derefStr(new.Version),
		})
	}
	// CVE events — the new `findings` table is P3; for P2 we emit
	// new_cve asset_events from the agent's inline cves[] blob so rules
	// with event_type=new_cve can at least observe them. cve_resolved
	// requires comparing old/new which we don't persist yet — skip.
	added := cvesFromPayload(upsert.CVEs)
	for _, id := range added {
		mk(model.AssetEventNewCVE, map[string]any{"cve_id": id})
	}
	return events
}

func cvesFromPayload(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := []string{}
	for _, item := range arr {
		if id, ok := item["id"].(string); ok && id != "" {
			out = append(out, id)
		}
	}
	return out
}

// runRuleActions evaluates the loaded rules against the (asset, endpoint)
// pair and dispatches each fired action. P2 wires suggest_target (no-op
// marker), notify, and run_scan_definition (still a TODO for actual
// dispatch — requires a scan_definition to execute against, which is
// P3-backend). auto_create_target is accepted but logged as no-op until
// P3 lands scan_definitions.
func runRuleActions(ctx context.Context, s store.Store, notifier *notify.Dispatcher, sched scheduler.Dispatcher, auditW audit.Writer, ruleSet []model.CorrelationRule, asset *model.Asset, ep *model.AssetEndpoint) {
	if asset == nil || ep == nil || len(ruleSet) == 0 {
		return
	}
	fired := rules.EvaluateAsset(ctx, s, ruleSet, asset, ep)
	for _, act := range fired {
		auditW.Emit(ctx, audit.Event{
			TenantID: act.TenantID, EventType: audit.EventRuleFired,
			ActorType: audit.ActorSystem,
			ResourceType: "rule", ResourceID: act.RuleID,
			Payload: map[string]any{
				"rule_name": act.RuleName, "action": act.Type,
				"asset_id": asset.ID, "endpoint_id": ep.ID,
			},
		})
		switch act.Type {
		case rules.ActionSuggestTarget:
			slog.Info("rule.fired",
				"rule", act.RuleName, "action", act.Type,
				"asset", asset.ID, "endpoint", ep.ID, "bundle", act.BundleID())
			// TODO(post-P5): rule fires and is logged, but the suggestion
			// is intentionally not persisted. The Dashboard's "Suggested
			// Actions" widget computes coverage gaps live from
			// scan_definitions + credential_mappings, so there is no DB
			// writeback required for the Phase-5 surface.
			//
			// If a future workflow needs persisted suggestions (e.g. an
			// approval queue, or aging metrics on "how long has this
			// suggestion been ignored"), add either:
			//   * an `asset_endpoints.suggestions` JSONB column, or
			//   * a dedicated `suggested_actions` side table
			// — whichever matches the product shape at that time.

		case rules.ActionAutoCreateTarget:
			slog.Info("rule.fired.no_op_until_p3",
				"rule", act.RuleName, "action", act.Type,
				"asset", asset.ID, "endpoint", ep.ID,
				"note", "auto_create_target superseded by scan_definitions in P3")

		case rules.ActionNotify:
			if notifier == nil {
				continue
			}
			channelSourceID, _ := act.Params["credential_source_id"].(string)
			if channelSourceID == "" {
				// Back-compat: older seeds use "channel" (name).
				// Not supported post-P2; log and skip.
				slog.Warn("notify action missing credential_source_id",
					"rule", act.RuleName)
				continue
			}
			severity, _ := act.Params["severity"].(string)
			if severity == "" {
				severity = notify.SeverityInfo
			}
			title, _ := act.Params["title"].(string)
			if title == "" {
				title = "Rule " + act.RuleName + " fired"
			}
			message, _ := act.Params["message"].(string)
			notifier.DispatchAsync(notify.Event{
				TenantID:        act.TenantID,
				ChannelSourceID: channelSourceID,
				Severity:        severity,
				Title:           title,
				Message:         message,
				AssetID:         asset.ID,
				AssetEndpointID: ep.ID,
				RuleID:          act.RuleID,
				RuleName:        act.RuleName,
				Payload: map[string]any{
					"ip":      derefStrAsset(asset.PrimaryIP),
					"port":    ep.Port,
					"service": derefStr(ep.Service),
					"version": derefStr(ep.Version),
				},
			})
			slog.Info("rule.fired",
				"rule", act.RuleName, "action", act.Type,
				"asset", asset.ID, "channel_source", channelSourceID)

		case rules.ActionRunScanDefinition:
			scanDefID, _ := act.Params["scan_definition_id"].(string)
			if scanDefID == "" {
				slog.Warn("run_scan_definition action missing scan_definition_id",
					"rule", act.RuleName)
				continue
			}
			def, err := s.GetScanDefinition(ctx, scanDefID)
			if err != nil || def == nil {
				slog.Warn("run_scan_definition: definition missing",
					"rule", act.RuleName, "scan_definition_id", scanDefID, "error", err)
				continue
			}
			if err := sched.Execute(ctx, *def); err != nil {
				slog.Warn("run_scan_definition dispatch failed",
					"rule", act.RuleName, "scan_definition_id", scanDefID, "error", err)
				continue
			}
			slog.Info("rule.fired",
				"rule", act.RuleName, "action", act.Type,
				"scan_definition_id", scanDefID,
				"asset", asset.ID, "endpoint", ep.ID)
		}
	}
}

// ingestNucleiFindings writes one findings row per CVE from the
// agent's inline cves[] blob on a discovery asset_discovered batch
// (ADR 007 D2 network_vuln source). The upsert key is
// (endpoint, source_kind, source, source_id) so rescans update
// last_seen instead of duplicating.
func ingestNucleiFindings(ctx context.Context, s store.Store, tenantID, scanID, endpointID string, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return
	}
	sid := scanID
	for _, hit := range arr {
		templateID, _ := hit["template_id"].(string)
		if templateID == "" {
			if v, ok := hit["id"].(string); ok {
				templateID = v
			}
		}
		cveID := firstCVE(hit)
		severity := strPtrOrNil(stringFrom(hit, "severity"))
		title := stringFrom(hit, "name")
		if title == "" {
			title = templateID
		}
		if title == "" {
			title = cveID
		}
		evidence, _ := json.Marshal(hit)
		var cvePtr *string
		if cveID != "" {
			c := cveID
			cvePtr = &c
		}
		if _, err := s.UpsertFinding(ctx, store.UpsertFindingInput{
			TenantID:        tenantID,
			AssetEndpointID: endpointID,
			ScanID:          &sid,
			SourceKind:      model.FindingSourceKindNetworkVuln,
			Source:          "nuclei",
			SourceID:        templateID,
			CVEID:           cvePtr,
			Severity:        severity,
			Title:           title,
			Status:          model.FindingStatusOpen,
			Evidence:        evidence,
		}); err != nil {
			slog.Warn("ingest nuclei finding", "endpoint", endpointID, "template", templateID, "error", err)
		}
	}
}

func firstCVE(hit map[string]any) string {
	if arr, ok := hit["cves"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
			if m, ok := v.(map[string]any); ok {
				if s, ok := m["id"].(string); ok && s != "" {
					return s
				}
			}
		}
	}
	if s, ok := hit["cve_id"].(string); ok {
		return s
	}
	return ""
}

func stringFrom(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// handleScanResults writes bundle compliance results to findings and
// marks the scan completed. The agent's results payload is a JSON
// array under `results` with per-control rows. Shape is tolerant —
// each row provides at least id/status; title + severity + evidence
// are optional.
func handleScanResults(ctx context.Context, s store.Store, bus events.Bus, auditW audit.Writer, agentID string, payload json.RawMessage) {
	var wrapper struct {
		ScanID  string            `json:"scan_id"`
		Results []json.RawMessage `json:"results"`
		Partial bool              `json:"partial"` // ADR 010: true = more results coming, don't complete scan
	}
	if err := json.Unmarshal(payload, &wrapper); err != nil {
		slog.Error("parsing scan_results payload", "agent_id", agentID, "error", err)
		return
	}
	if wrapper.ScanID == "" {
		return
	}
	scan, err := s.GetScanByID(ctx, wrapper.ScanID)
	if err != nil || scan == nil {
		slog.Error("loading compliance scan", "scan_id", wrapper.ScanID, "error", err)
		return
	}
	// Findings write-through only if the scan was targeted at an
	// asset_endpoint (scheduler / scan_definition path). The legacy
	// ad-hoc /api/v1/scans route is target-driven and has no endpoint
	// binding yet; skip findings in that shape — they'll light up once
	// scan_definitions becomes the sole entry point.
	if scan.AssetEndpointID != nil {
		bundleID := ""
		if scan.BundleID != nil {
			bundleID = *scan.BundleID
		}
		sid := scan.ID
		tctx := store.WithTenantID(ctx, scan.TenantID)
		for _, raw := range wrapper.Results {
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				continue
			}
			controlID := stringFrom(row, "control_id")
			if controlID == "" {
				controlID = stringFrom(row, "id")
			}
			statusStr := stringFrom(row, "status")
			findingStatus := model.FindingStatusOpen
			if statusStr == "pass" || statusStr == "passed" {
				findingStatus = model.FindingStatusResolved
			}
			title := stringFrom(row, "title")
			if title == "" {
				title = controlID
			}
			severity := strPtrOrNil(stringFrom(row, "severity"))
			remediation := strPtrOrNil(stringFrom(row, "remediation"))
			if _, err := s.UpsertFinding(tctx, store.UpsertFindingInput{
				TenantID:        scan.TenantID,
				AssetEndpointID: *scan.AssetEndpointID,
				ScanID:          &sid,
				SourceKind:      model.FindingSourceKindBundleCompliance,
				Source:          bundleID,
				SourceID:        controlID,
				Severity:        severity,
				Title:           title,
				Status:          findingStatus,
				Evidence:        raw,
				Remediation:     remediation,
			}); err != nil {
				slog.Warn("ingest bundle finding",
					"scan", scan.ID, "control", controlID, "error", err)
			}
		}
	}

	// ADR 010 D5: if partial=true, write findings but keep the scan
	// running — more control results are still coming. Only mark
	// completed when partial is false (the default for legacy agents).
	if !wrapper.Partial {
		if err := s.UpdateScanStatus(ctx, wrapper.ScanID, model.ScanStatusCompleted); err != nil {
			slog.Error("updating scan to completed", "scan_id", wrapper.ScanID, "error", err)
		}
		if completedScan, _ := s.GetScanByID(ctx, wrapper.ScanID); completedScan != nil {
			handler.PublishScanStatusFromScan(ctx, bus, completedScan)
			auditW.Emit(ctx, audit.Event{
				TenantID: completedScan.TenantID, EventType: audit.EventScanCompleted,
				ActorType: audit.ActorAgent, ActorID: agentID,
				ResourceType: "scan", ResourceID: completedScan.ID,
				Payload: map[string]any{"results_count": len(wrapper.Results)},
			})
		}
	}

	slog.Info("scan_results processed",
		"agent_id", agentID, "scan_id", wrapper.ScanID, "results", len(wrapper.Results), "partial", wrapper.Partial)
}

// handleFactsCollected stores raw collector facts (ADR 011 D5). Policy
// evaluation is deferred to PR 2 — this handler only persists and audits.
func handleFactsCollected(ctx context.Context, s store.Store, auditW audit.Writer, agentID string, payload json.RawMessage) {
	var p websocket.FactsCollectedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		slog.Error("parsing facts_collected payload", "agent_id", agentID, "error", err)
		return
	}
	if p.ScanID == "" || p.CollectorID == "" {
		slog.Warn("facts_collected missing scan_id or collector_id", "agent_id", agentID)
		return
	}

	scan, err := s.GetScanByID(ctx, p.ScanID)
	if err != nil || scan == nil {
		slog.Error("facts_collected scan lookup failed", "scan_id", p.ScanID, "error", err)
		return
	}

	endpointID := ""
	if scan.AssetEndpointID != nil {
		endpointID = *scan.AssetEndpointID
	}
	if endpointID == "" {
		slog.Warn("facts_collected scan has no asset_endpoint_id, skipping", "scan_id", p.ScanID)
		return
	}

	id := generateUUID()
	now := time.Now().UTC()
	f := model.CollectedFacts{
		ID:              id,
		TenantID:        scan.TenantID,
		AssetEndpointID: endpointID,
		ScanID:          p.ScanID,
		CollectorID:     p.CollectorID,
		Facts:           p.Facts,
		CollectedAt:     now,
	}
	if err := s.InsertCollectedFacts(ctx, f); err != nil {
		slog.Error("inserting collected facts", "scan_id", p.ScanID, "error", err)
		return
	}

	slog.Info("facts collected", "scan_id", p.ScanID, "collector_id", p.CollectorID)

	auditW.Emit(ctx, audit.Event{
		TenantID:     scan.TenantID,
		EventType:    audit.EventFactsCollected,
		ActorType:    audit.ActorAgent,
		ActorID:      agentID,
		ResourceType: "scan",
		ResourceID:   p.ScanID,
		Payload: map[string]any{
			"collector_id":      p.CollectorID,
			"asset_endpoint_id": endpointID,
		},
	})
}

func strPtr(s string) *string { return &s }
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func derefStrAsset(p *string) string { return derefStr(p) }

// generateUUID produces a random v4 UUID string.
func generateUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
