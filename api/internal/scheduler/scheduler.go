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
	PubSub *pubsub.PubSub
	Bus    events.Bus
}

// Scheduler polls for due scan_definitions and dispatches them. One
// goroutine per API process; `SELECT ... FOR UPDATE SKIP LOCKED`
// inside ClaimDueScanDefinitions ensures multiple instances never
// double-dispatch the same row.
type Scheduler struct {
	D        Dispatcher
	Interval time.Duration
}

// New builds a Scheduler with a default 30s tick per ADR 007 D4.
func New(s store.Store, ps *pubsub.PubSub, bus events.Bus) *Scheduler {
	return &Scheduler{
		D:        Dispatcher{Store: s, PubSub: ps, Bus: bus},
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
		// as-is and let the agent re-vet locally. One scan per entry.
		// NB: non-CIDR entries (IP/range/hostname) are upserted as targets.type
		// ='cidr' since that's the only range-target upsert today; the agent
		// keys off target_identifier so this works, but the target row's type is
		// semantically loose — a typed range-target upsert is a future cleanup.
		var firstErr error
		for _, entry := range entries {
			targetID, err := d.Store.UpsertTargetByCIDR(ctx, def.TenantID, entry, def.AgentID, "scheduled")
			if err != nil {
				slog.Warn("scheduler.agent_allowlist_target", "definition", def.ID, "entry", entry, "error", err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if err := d.dispatchOne(ctx, def, nil, &targetID); err != nil {
				slog.Warn("scheduler.dispatch_one", "definition", def.ID, "entry", entry, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		return firstErr
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
		// One vhost-aware directive per name. The agent re-vets each name against
		// its local allowlist (ADR 014 D3 / D11), so out-of-scope names are
		// blocked agent-side. As with agent_allowlist, names upsert as cidr-type
		// targets — the agent keys off target_identifier.
		var firstErr error
		for _, name := range names {
			targetID, err := d.Store.UpsertTargetByCIDR(ctx, def.TenantID, name, def.AgentID, "scheduled")
			if err != nil {
				slog.Warn("scheduler.dns_list_target", "definition", def.ID, "name", name, "error", err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if err := d.dispatchOne(ctx, def, nil, &targetID); err != nil {
				slog.Warn("scheduler.dispatch_one", "definition", def.ID, "name", name, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		return firstErr
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
