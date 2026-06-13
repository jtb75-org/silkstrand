package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jtb75/silkstrand/api/internal/events"
	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/pubsub"
	"github.com/jtb75/silkstrand/api/internal/store"
)

// Dispatcher is the handoff point between a claimed scan_definition
// and the agent. The scheduler creates a `scans` row (via the store)
// and publishes a directive via Redis pub/sub; the existing
// AgentHandler.forwardDirective path enriches + sends the WSS message.
//
// Dispatch is idempotent per scheduler tick: if publish fails, the
// caller logs and moves on — next_run_at has already advanced, so the
// scan row becomes a one-off pending record that will be failed by the
// stuck-scan cleanup if the agent never picks it up. This matches
// ADR 007 D4 — operators accept "lose a tick" in exchange for
// crash-recovery simplicity.
type Dispatcher struct {
	Store  store.Store
	PubSub DirectivePublisher
	Bus    events.Bus
	// ChunkIPs overrides the discovery chunk size (IPs per chunk). 0 falls
	// back to DefaultDiscoveryChunkIPs. Sourced from DISCOVERY_CHUNK_IPS —
	// lower it (e.g. 64) to exercise multi-chunk/resume on a small LAN.
	ChunkIPs int
}

type DirectivePublisher interface {
	PublishDirective(ctx context.Context, agentID string, directive pubsub.Directive) error
}

// Scheduler polls for due scan_definitions and dispatches them. One
// goroutine per API process; `SELECT ... FOR UPDATE SKIP LOCKED`
// inside ClaimDueScanDefinitions ensures multiple instances never
// double-dispatch the same row.
type Scheduler struct {
	D        Dispatcher
	Interval time.Duration
}

type discoveryTarget struct {
	targetType       string
	targetIdentifier string
}

// New builds a Scheduler with a default 30s tick per ADR 007 D4.
func New(s store.Store, ps *pubsub.PubSub, bus events.Bus, chunkIPs int) *Scheduler {
	return &Scheduler{
		D:        Dispatcher{Store: s, PubSub: ps, Bus: bus, ChunkIPs: chunkIPs},
		Interval: 30 * time.Second,
	}
}

// Run blocks until ctx is canceled, ticking every Interval. Errors
// from individual ticks are logged and never returned — the scheduler
// keeps running.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	slog.Info("scheduler.start", "interval", s.Interval.String())
	// Fire one immediate tick so locally-created due definitions don't
	// wait a full interval on boot.
	s.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			slog.Info("scheduler.stop")
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// Tick runs one scheduler cycle: claim due definitions, advance their
// next_run_at, and dispatch each. Also sweeps stale queued scans.
func (s *Scheduler) Tick(ctx context.Context) {
	// Sweep queued scans older than 30 minutes that have no running/pending
	// sibling to drain them (FailStaleQueuedScans guards on that — a scan
	// queued behind a healthy long-running scan is not stale).
	if n, err := s.D.Store.FailStaleQueuedScans(ctx, 30*time.Minute); err != nil {
		slog.Error("scheduler.stale_sweep", "error", err)
	} else if n > 0 {
		slog.Info("scheduler.stale_sweep", "failed", n)
	}

	if scanIDs, err := s.D.Store.ResetStaleRunningScanChunks(ctx, 30*time.Minute); err != nil {
		slog.Error("scheduler.chunk_reset_sweep", "error", err)
	} else if len(scanIDs) > 0 {
		slog.Info("scheduler.chunk_reset_sweep", "scans", len(scanIDs))
	}

	if scanIDs, err := s.D.Store.ResetUnackedScanChunks(ctx, 2*time.Minute); err != nil {
		slog.Error("scheduler.chunk_unacked_sweep", "error", err)
	} else if len(scanIDs) > 0 {
		slog.Info("scheduler.chunk_unacked_sweep", "scans", len(scanIDs))
	}

	s.reconcileChunkedParents(ctx)

	if n, err := s.D.Store.FailAbandonedChunkedScans(ctx, 24*time.Hour); err != nil {
		slog.Error("scheduler.chunk_abandoned_sweep", "error", err)
	} else if n > 0 {
		slog.Info("scheduler.chunk_abandoned_sweep", "failed", n)
	}

	// Purge agent log events older than 24 hours.
	if n, err := s.D.Store.DeleteOldAgentLogs(ctx, 24*time.Hour); err != nil {
		slog.Error("scheduler.agent_log_purge", "error", err)
	} else if n > 0 {
		slog.Info("scheduler.agent_log_purge", "deleted", n)
	}

	// Purge collected facts older than 30 days (ADR 011 D4).
	if n, err := s.D.Store.DeleteOldCollectedFacts(ctx, 30*24*time.Hour); err != nil {
		slog.Error("scheduler.facts_purge", "error", err)
	} else if n > 0 {
		slog.Info("scheduler.facts_purge", "deleted", n)
	}

	now := time.Now().UTC()
	claimed, err := s.D.Store.ClaimDueScanDefinitions(ctx, now, nextRun, 32)
	if err != nil {
		slog.Error("scheduler.claim", "error", err)
		return
	}
	if len(claimed) == 0 {
		return
	}
	slog.Info("scheduler.tick", "claimed", len(claimed))
	for _, d := range claimed {
		if err := s.D.Execute(ctx, d); err != nil {
			slog.Error("scheduler.dispatch",
				"definition", d.ID, "name", d.Name, "error", err)
			_ = s.D.Store.SetScanDefinitionLastRun(ctx, d.ID, now, "failed")
			continue
		}
		_ = s.D.Store.SetScanDefinitionLastRun(ctx, d.ID, now, "dispatched")
	}
}

func (s *Scheduler) reconcileChunkedParents(ctx context.Context) {
	parents, err := s.D.Store.ActiveChunkedParentsWithoutRunningChunk(ctx, 100)
	if err != nil {
		slog.Error("scheduler.chunk_reconcile", "error", err)
		return
	}
	for _, p := range parents {
		if _, err := s.D.DispatchNextChunk(ctx, p.AgentID, p.ScanID); err != nil {
			slog.Error("scheduler.chunk_reconcile_dispatch", "scan", p.ScanID, "agent", p.AgentID, "error", err)
		}
	}
}

// nextRun computes the next fire time for a cron expression. Called by
// the store-level claim transaction so advance + select are atomic.
func nextRun(schedule string, from time.Time) (time.Time, error) {
	if schedule == "" {
		return time.Time{}, errors.New("empty schedule")
	}
	c, err := ParseCron(schedule)
	if err != nil {
		return time.Time{}, err
	}
	return c.Next(from)
}

// Execute materializes a scan row for the given definition and
// publishes a directive. Shared by the scheduler tick path and the
// POST /api/v1/scan-definitions/{id}/execute handler.
//
// Scope handling:
//   - asset_endpoint scope: scan the single endpoint. target_id comes
//     from a derived compliance-target row if one exists; for now we
//     dispatch with asset_endpoint_id set and target_id empty (agent
//     ignores target enrichment for discovery; compliance scans
//     against endpoints without a target are a post-P3 concern).
//   - cidr scope: upsert a targets row for (tenant, cidr) and dispatch
//     with that target_id. forwardDirective joins the target row to
//     populate target_type='cidr' + identifier=<cidr>, which naabu/httpx
//     consume as their input. Requires an agent_id on the definition —
//     a CIDR definition without an agent is a misconfiguration.
//   - collection scope: resolves endpoint ids and emits one scan per
//     endpoint (bounded by P3's naive resolver — every endpoint owned
//     by the tenant).
func (d Dispatcher) Execute(ctx context.Context, def model.ScanDefinition) error {
	switch def.ScopeKind {
	case model.ScanDefinitionScopeAssetEndpoint:
		if def.AssetEndpointID == nil {
			return fmt.Errorf("scope=asset_endpoint requires asset_endpoint_id")
		}
		return d.dispatchOne(ctx, def, def.AssetEndpointID, nil)
	case model.ScanDefinitionScopeCollection:
		if def.CollectionID == nil {
			return fmt.Errorf("scope=collection requires collection_id")
		}
		cctx := store.WithTenantID(ctx, def.TenantID)
		ids, err := d.Store.CollectionEndpointIDs(cctx, *def.CollectionID)
		if err != nil {
			return fmt.Errorf("resolving collection: %w", err)
		}
		if len(ids) == 0 {
			slog.Info("scheduler.collection_empty",
				"definition", def.ID, "collection", *def.CollectionID)
			return nil
		}
		for _, epID := range ids {
			epID := epID
			if err := d.dispatchOne(ctx, def, &epID, nil); err != nil {
				slog.Warn("scheduler.dispatch_one",
					"definition", def.ID, "endpoint", epID, "error", err)
			}
		}
		return nil
	case model.ScanDefinitionScopeCIDR:
		if def.CIDR == nil || *def.CIDR == "" {
			return fmt.Errorf("scope=cidr requires cidr")
		}
		if def.AgentID == nil {
			// No agent means the directive has nowhere to go.
			// forwardDirective still needs a target row, so fail loudly
			// here rather than produce an orphan scans row.
			return fmt.Errorf("scope=cidr requires agent_id")
		}
		targetID, err := d.Store.UpsertTargetByCIDR(ctx, def.TenantID, *def.CIDR, def.AgentID, "scheduled")
		if err != nil {
			return fmt.Errorf("upserting cidr target: %w", err)
		}
		if def.Kind == model.ScanDefinitionKindDiscovery {
			return d.dispatchChunkedDiscovery(ctx, def, &targetID, []discoveryTarget{{
				targetType:       model.TargetTypeCIDR,
				targetIdentifier: *def.CIDR,
			}})
		}
		return d.dispatchOne(ctx, def, nil, &targetID)
	case model.ScanDefinitionScopeAgentAllowlist:
		if def.AgentID == nil {
			return fmt.Errorf("scope=agent_allowlist requires agent_id")
		}
		snap, err := d.Store.GetAgentAllowlist(ctx, *def.AgentID)
		if err != nil {
			return fmt.Errorf("loading agent allowlist: %w", err)
		}
		// Fail-safe (ADR 013 D4): never silently broaden. A missing or empty
		// snapshot blocks the run with an actionable message rather than
		// scanning nothing — or, worse, defaulting to something broad.
		entries := allowlistTargets(snap)
		if len(entries) == 0 {
			return fmt.Errorf("agent has not reported an allowlist yet; scope=agent_allowlist has nothing to scan")
		}
		// Transparency: the dispatch is pinned to this snapshot.
		slog.Info("scheduler.agent_allowlist_dispatch", "definition", def.ID,
			"agent", *def.AgentID, "snapshot_hash", snap.Hash,
			"reported_at", snap.ReportedAt, "targets", len(entries))
		// Deny is enforced agent-side (ADR 013 D4) — we dispatch allow entries
		// as-is and let the agent re-vet locally. One parent scan owns one chunk
		// list so large scopes resume from chunk checkpoints instead of creating
		// many independent scans.
		// NB: non-CIDR entries (IP/range/hostname) are upserted as targets.type
		// ='cidr' since that's the only range-target upsert today; the agent
		// keys off target_identifier so this works, but the target row's type is
		// semantically loose — a typed range-target upsert is a future cleanup.
		targets := make([]discoveryTarget, 0, len(entries))
		for _, entry := range entries {
			targets = append(targets, discoveryTarget{
				targetType:       discoveryTargetType(entry),
				targetIdentifier: entry,
			})
		}
		return d.dispatchChunkedDiscovery(ctx, def, nil, targets)
	case model.ScanDefinitionScopeDNSList:
		if def.AgentID == nil {
			return fmt.Errorf("scope=dns_list requires agent_id")
		}
		names, err := d.Store.ListHTTPServiceHostnames(ctx, def.TenantID)
		if err != nil {
			return fmt.Errorf("loading http_service hostnames: %w", err)
		}
		// Fail-safe (mirrors agent_allowlist): nothing imported → block with an
		// actionable error rather than scanning nothing.
		if len(names) == 0 {
			return fmt.Errorf("no imported DNS names; scope=dns_list has nothing to scan")
		}
		slog.Info("scheduler.dns_list_dispatch", "definition", def.ID,
			"agent", *def.AgentID, "targets", len(names))
		// One parent scan owns one vhost-aware chunk per name. The agent re-vets
		// each name against its local allowlist (ADR 014 D3 / D11), so
		// out-of-scope names are blocked agent-side.
		targets := make([]discoveryTarget, 0, len(names))
		for _, name := range names {
			targets = append(targets, discoveryTarget{
				targetType:       discoveryTargetType(name),
				targetIdentifier: name,
			})
		}
		return d.dispatchChunkedDiscovery(ctx, def, nil, targets)
	}
	return fmt.Errorf("unknown scope_kind: %q", def.ScopeKind)
}

// allowlistTargets returns an agent snapshot's allow entries (trimmed,
// non-empty), or nil if the snapshot is missing/empty. deny is intentionally
// not subtracted — the agent enforces deny locally (ADR 013 D4).
func allowlistTargets(snap *store.AgentAllowlistSnapshot) []string {
	if snap == nil {
		return nil
	}
	out := make([]string, 0, len(snap.Allow))
	for _, e := range snap.Allow {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

func discoveryTargetType(identifier string) string {
	if strings.Contains(identifier, "-") {
		if _, _, err := parseIPv4Range(identifier); err == nil {
			return model.TargetTypeNetworkRange
		}
	}
	if strings.Contains(identifier, "/") {
		return model.TargetTypeCIDR
	}
	return "host"
}

func (d Dispatcher) dispatchOne(ctx context.Context, def model.ScanDefinition, endpointID, targetID *string) error {
	scanType := model.ScanTypeCompliance
	if def.Kind == model.ScanDefinitionKindDiscovery {
		scanType = model.ScanTypeDiscovery
	}
	// Discovery definitions often have bundle_id=NULL (the UI doesn't
	// require one). The scan row and the WSS forwarder both need a valid
	// bundle FK, so default to the global discovery bundle.
	bundleID := def.BundleID
	if bundleID == nil && scanType == model.ScanTypeDiscovery {
		id := model.DiscoveryBundleID
		bundleID = &id
	}
	sc, err := d.Store.CreateScanForDefinition(ctx, store.CreateScanForDefinitionInput{
		TenantID:         def.TenantID,
		ScanDefinitionID: def.ID,
		AgentID:          def.AgentID,
		TargetID:         targetID,
		AssetEndpointID:  endpointID,
		BundleID:         bundleID,
		ScanType:         scanType,
	})
	if err != nil {
		return fmt.Errorf("creating scan: %w", err)
	}
	if def.AgentID == nil || d.PubSub == nil {
		slog.Info("scheduler.scan_created_without_dispatch",
			"scan", sc.ID, "reason", "no agent or pubsub")
		return nil
	}
	// Check if agent already has another running/pending scan — queue if busy.
	// Exclude the scan we just created so it doesn't see itself.
	busy, err := d.Store.AgentHasRunningScanExcluding(ctx, *def.AgentID, sc.ID)
	if err != nil {
		return fmt.Errorf("checking agent busy: %w", err)
	}
	if busy {
		if err := d.Store.UpdateScanStatus(ctx, sc.ID, model.ScanStatusQueued); err != nil {
			return fmt.Errorf("queueing scan: %w", err)
		}
		d.publishScanStatus(ctx, sc.ID)
		slog.Info("scheduler.queued", "scan", sc.ID, "agent", *def.AgentID)
		return nil
	}
	directive := pubsub.Directive{
		ScanID:   sc.ID,
		ScanType: scanType,
		TenantID: def.TenantID,
	}
	if bundleID != nil {
		directive.BundleID = *bundleID
	}
	if targetID != nil {
		directive.TargetID = *targetID
	}
	if endpointID != nil {
		directive.AssetEndpointID = *endpointID
	}
	if err := d.PubSub.PublishDirective(ctx, *def.AgentID, directive); err != nil {
		return fmt.Errorf("publishing directive: %w", err)
	}
	slog.Info("scheduler.dispatched", "definition", def.ID, "scan", sc.ID, "agent", *def.AgentID)
	return nil
}

func (d Dispatcher) dispatchChunkedDiscovery(ctx context.Context, def model.ScanDefinition, parentTargetID *string, targets []discoveryTarget) error {
	if def.AgentID == nil {
		return fmt.Errorf("chunked discovery requires agent_id")
	}
	bundleID := def.BundleID
	if bundleID == nil {
		id := model.DiscoveryBundleID
		bundleID = &id
	}
	sc, err := d.Store.CreateScanForDefinition(ctx, store.CreateScanForDefinitionInput{
		TenantID:         def.TenantID,
		ScanDefinitionID: def.ID,
		AgentID:          def.AgentID,
		TargetID:         parentTargetID,
		BundleID:         bundleID,
		ScanType:         model.ScanTypeDiscovery,
	})
	if err != nil {
		return fmt.Errorf("creating chunked discovery scan: %w", err)
	}
	chunks := make([]store.CreateScanChunkInput, 0, len(targets))
	for _, target := range targets {
		pieces, err := splitDiscoveryTarget(sc.ID, def.TenantID, def.AgentID, target.targetType, target.targetIdentifier, d.ChunkIPs)
		if err != nil {
			_ = d.Store.FailScan(ctx, sc.ID, err.Error())
			return fmt.Errorf("splitting discovery target %q: %w", target.targetIdentifier, err)
		}
		for _, piece := range pieces {
			piece.ChunkIndex = len(chunks)
			chunks = append(chunks, piece)
		}
	}
	if len(chunks) == 0 {
		_ = d.Store.FailScan(ctx, sc.ID, "chunked discovery had no targets")
		return fmt.Errorf("chunked discovery had no targets")
	}
	if err := d.Store.CreateScanChunks(ctx, chunks); err != nil {
		_ = d.Store.FailScan(ctx, sc.ID, err.Error())
		return fmt.Errorf("creating scan chunks: %w", err)
	}
	if d.PubSub == nil {
		slog.Info("scheduler.chunked_scan_created_without_dispatch", "scan", sc.ID, "chunks", len(chunks))
		return nil
	}
	busy, err := d.Store.AgentHasRunningScanExcluding(ctx, *def.AgentID, sc.ID)
	if err != nil {
		return fmt.Errorf("checking agent busy: %w", err)
	}
	if busy {
		if err := d.Store.UpdateScanStatus(ctx, sc.ID, model.ScanStatusQueued); err != nil {
			return fmt.Errorf("queueing chunked scan: %w", err)
		}
		d.publishScanStatus(ctx, sc.ID)
		slog.Info("scheduler.chunked_queued", "scan", sc.ID, "agent", *def.AgentID, "chunks", len(chunks))
		return nil
	}
	_, err = d.DispatchNextChunk(ctx, *def.AgentID, sc.ID)
	return err
}

// DispatchNextChunk claims and publishes the next runnable chunk for a parent
// discovery scan. It returns true when it published a chunk directive.
func (d Dispatcher) DispatchNextChunk(ctx context.Context, agentID, scanID string) (bool, error) {
	if d.PubSub == nil {
		return false, nil
	}
	summary, err := d.Store.ScanChunkSummary(ctx, scanID)
	if err != nil {
		return false, fmt.Errorf("summarizing scan chunks: %w", err)
	}
	if summary == nil || summary.Total == 0 {
		return false, nil
	}
	chunk, err := d.Store.ClaimNextScanChunk(ctx, scanID, agentID)
	if err != nil {
		return false, fmt.Errorf("claiming next scan chunk: %w", err)
	}
	if chunk == nil {
		scan, err := d.Store.GetScanByID(ctx, scanID)
		if err != nil || scan == nil || (scan.Status != model.ScanStatusPending && scan.Status != model.ScanStatusRunning) {
			return false, nil
		}
		if summary.Completed == summary.Total {
			if err := d.Store.UpdateScanStatus(ctx, scanID, model.ScanStatusCompleted); err != nil {
				return false, fmt.Errorf("completing chunked scan: %w", err)
			}
			d.publishScanStatus(ctx, scanID)
			d.DrainAgentQueue(ctx, agentID)
		} else if summary.Failed > 0 && summary.Pending == 0 && summary.Running == 0 {
			if err := d.Store.FailScan(ctx, scanID, "one or more discovery chunks failed"); err != nil {
				return false, fmt.Errorf("failing chunked scan: %w", err)
			}
			d.publishScanStatus(ctx, scanID)
			d.DrainAgentQueue(ctx, agentID)
		}
		return false, nil
	}
	bundleID := model.DiscoveryBundleID
	scan, err := d.Store.GetScanByID(ctx, scanID)
	if err == nil && scan != nil && scan.BundleID != nil {
		bundleID = *scan.BundleID
	}
	directive := pubsub.Directive{
		ScanID:           scanID,
		ScanType:         model.ScanTypeDiscovery,
		BundleID:         bundleID,
		TenantID:         chunk.TenantID,
		ChunkID:          chunk.ID,
		ChunkIndex:       chunk.ChunkIndex,
		ChunkTotal:       summary.Total,
		TargetType:       chunk.TargetType,
		TargetIdentifier: chunk.TargetIdentifier,
	}
	if err := d.PubSub.PublishDirective(ctx, agentID, directive); err != nil {
		if resetErr := d.Store.ResetScanChunkToPending(ctx, chunk.ID); resetErr != nil {
			slog.Error("scheduler.chunk_publish_reset", "scan", scanID, "chunk", chunk.ID, "error", resetErr)
		}
		return false, fmt.Errorf("publishing chunk directive: %w", err)
	}
	slog.Info("scheduler.chunk_dispatched", "scan", scanID, "chunk", chunk.ID, "index", chunk.ChunkIndex, "total", summary.Total, "agent", agentID)
	return true, nil
}

// DrainAgentQueue checks if the given agent has queued scans and
// dispatches the oldest one. Called from terminal scan states
// (completed, failed) and from the stuck-scan cleanup path.
func (d Dispatcher) DrainAgentQueue(ctx context.Context, agentID string) {
	if d.PubSub == nil {
		return
	}
	next, err := d.Store.OldestQueuedScanForAgent(ctx, agentID)
	if err != nil {
		slog.Error("drain_queue.load", "agent", agentID, "error", err)
		return
	}
	if next == nil {
		return
	}
	// Transition queued → pending before publishing the directive.
	if err := d.Store.UpdateScanStatus(ctx, next.ID, model.ScanStatusPending); err != nil {
		slog.Error("drain_queue.status", "scan", next.ID, "error", err)
		return
	}
	d.publishScanStatus(ctx, next.ID)
	if summary, err := d.Store.ScanChunkSummary(ctx, next.ID); err == nil && summary != nil && summary.Total > 0 {
		if _, err := d.DispatchNextChunk(ctx, agentID, next.ID); err != nil {
			slog.Error("drain_queue.chunk_publish", "scan", next.ID, "agent", agentID, "error", err)
		}
		return
	}
	directive := pubsub.Directive{
		ScanID:   next.ID,
		ScanType: next.ScanType,
		TenantID: next.TenantID,
	}
	if next.BundleID != nil {
		directive.BundleID = *next.BundleID
	}
	if next.TargetID != nil {
		directive.TargetID = *next.TargetID
	}
	if next.AssetEndpointID != nil {
		directive.AssetEndpointID = *next.AssetEndpointID
	}
	if err := d.PubSub.PublishDirective(ctx, agentID, directive); err != nil {
		slog.Error("drain_queue.publish", "scan", next.ID, "agent", agentID, "error", err)
		return
	}
	slog.Info("drain_queue.dispatched", "scan", next.ID, "agent", agentID)
}

// publishScanStatus loads the current scan row and emits a scan_status
// event on the bus. Best-effort: errors are logged but not propagated.
func (d Dispatcher) publishScanStatus(ctx context.Context, scanID string) {
	if d.Bus == nil {
		return
	}
	scan, err := d.Store.GetScanByID(ctx, scanID)
	if err != nil || scan == nil {
		return
	}
	type scanStatusPayload struct {
		Status           string  `json:"status"`
		ScanDefinitionID *string `json:"scan_definition_id"`
		AgentID          *string `json:"agent_id"`
	}
	payload, _ := json.Marshal(scanStatusPayload{
		Status:           scan.Status,
		ScanDefinitionID: scan.ScanDefinitionID,
		AgentID:          scan.AgentID,
	})
	if err := d.Bus.Publish(ctx, events.Event{
		TenantID:     scan.TenantID,
		Kind:         "scan_status",
		ResourceType: "scan",
		ResourceID:   scan.ID,
		OccurredAt:   time.Now().UTC(),
		Payload:      payload,
	}); err != nil {
		slog.Warn("scan_status publish failed", "scan_id", scan.ID, "error", err)
	}
}
