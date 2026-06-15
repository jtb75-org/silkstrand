package handler

import (
	"database/sql"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/rules"
	"github.com/jtb75/silkstrand/api/internal/store"
)

// DashboardHandler serves the asset-first Dashboard:
//   - GET /api/v1/dashboard/kpis
//   - GET /api/v1/dashboard/suggested-actions
//   - GET /api/v1/dashboard/recent-activity
//
// All reads are tenant-scoped via the Tenant middleware; the handler
// pulls the tenant id off the request context. KPI + suggestion queries
// are intentionally conservative — they read tables that exist today
// (assets, asset_endpoints, asset_events, scans, credential_sources,
// credential_mappings). Findings counts land in P3; until then the
// Critical Findings KPI falls back to 0 when the findings table is
// empty, which matches the post-migration-017 state.
type DashboardHandler struct {
	db    *sql.DB
	store store.Store // for predicate evaluation (coverage-by-collection)
}

// NewDashboardHandler accepts either a *store.PostgresStore or nil. nil
// keeps the P1 call-site (`handler.NewDashboardHandler(nil)`) compiling
// until the main wiring is updated in the same PR.
func NewDashboardHandler(s any) *DashboardHandler {
	h := &DashboardHandler{}
	if ps, ok := s.(*store.PostgresStore); ok && ps != nil {
		h.db = ps.DB()
		h.store = ps
	}
	return h
}

// Get is retained for backwards compatibility with any router still
// pointed at the old summary route — it just redirects callers at the
// KPI endpoint.
func (h *DashboardHandler) Get(w http.ResponseWriter, r *http.Request) {
	h.GetKPIs(w, r)
}

// ---- KPIs ----------------------------------------------------------

type kpiDeltas struct {
	AssetsNewThisWeek  int `json:"assets_new_this_week"`
	FindingsNewToday   int `json:"findings_new_today"`
	CoverageDeltaWeek  int `json:"coverage_delta_week"` // percentage points, 0 until we track history
	UnresolvedNewWeek  int `json:"unresolved_new_week"`
}

// findingsBySeverity is the open-findings count per severity, tenant-scoped.
// Powers the Dashboard "Findings by severity" summary; additive alongside the
// existing critical_findings KPI card.
type findingsBySeverity struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
}

type kpiResponse struct {
	TotalAssets        int                `json:"total_assets"`
	CoveragePercent    int                `json:"coverage_percent"`
	CriticalFindings   int                `json:"critical_findings"`
	NewThisWeek        int                `json:"new_this_week"`
	FindingsBySeverity findingsBySeverity `json:"findings_by_severity"`
	Deltas             kpiDeltas          `json:"deltas"`
}

// GET /api/v1/dashboard/kpis
func (h *DashboardHandler) GetKPIs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "dashboard store not initialised")
		return
	}
	ctx := r.Context()
	tenantID := store.TenantID(ctx)
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "tenant not resolved")
		return
	}

	resp := kpiResponse{}

	// Total assets (hosts) for the tenant.
	if err := h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assets WHERE tenant_id = $1`, tenantID,
	).Scan(&resp.TotalAssets); err != nil {
		slog.Error("dashboard: total assets", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to compute KPIs")
		return
	}

	// Endpoints + covered endpoints (has a scan_definition pointing at the
	// endpoint directly, or at a collection the endpoint belongs to —
	// collection membership resolution lands in P4, so today we only count
	// the direct scope_kind='asset_endpoint' case). Good enough to show
	// motion; will tighten in P4.
	var endpoints, covered int
	_ = h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM asset_endpoints ae
		   JOIN assets a ON a.id = ae.asset_id
		  WHERE a.tenant_id = $1`, tenantID,
	).Scan(&endpoints)
	_ = h.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT ae.id)
		   FROM asset_endpoints ae
		   JOIN assets a ON a.id = ae.asset_id
		   JOIN scan_definitions sd
		     ON sd.scope_kind = 'asset_endpoint' AND sd.asset_endpoint_id = ae.id
		  WHERE a.tenant_id = $1 AND sd.enabled = TRUE`, tenantID,
	).Scan(&covered)
	if endpoints > 0 {
		resp.CoveragePercent = (covered * 100) / endpoints
	}

	// Critical findings. Falls back to 0 if findings is empty (pre-P3).
	_ = h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM findings
		  WHERE tenant_id = $1 AND status = 'open' AND severity = 'critical'`,
		tenantID,
	).Scan(&resp.CriticalFindings)

	// Open findings by severity (one grouped query) for the severity summary.
	if sevRows, err := h.db.QueryContext(ctx,
		`SELECT COALESCE(severity, 'info'), COUNT(*) FROM findings
		  WHERE tenant_id = $1 AND status = 'open'
		  GROUP BY severity`, tenantID,
	); err == nil {
		defer sevRows.Close()
		for sevRows.Next() {
			var sev string
			var n int
			if err := sevRows.Scan(&sev, &n); err != nil {
				continue
			}
			switch sev {
			case "critical":
				resp.FindingsBySeverity.Critical = n
			case "high":
				resp.FindingsBySeverity.High = n
			case "medium":
				resp.FindingsBySeverity.Medium = n
			case "low":
				resp.FindingsBySeverity.Low = n
			case "info":
				resp.FindingsBySeverity.Info = n
			}
		}
	}

	// New this week = assets first_seen within 7d.
	weekAgo := time.Now().Add(-7 * 24 * time.Hour)
	_ = h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assets WHERE tenant_id = $1 AND first_seen >= $2`,
		tenantID, weekAgo,
	).Scan(&resp.NewThisWeek)
	resp.Deltas.AssetsNewThisWeek = resp.NewThisWeek

	// Findings created today (for the Critical card's "+N today" delta).
	todayStart := time.Now().Truncate(24 * time.Hour)
	_ = h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM findings
		  WHERE tenant_id = $1 AND status = 'open' AND first_seen >= $2`,
		tenantID, todayStart,
	).Scan(&resp.Deltas.FindingsNewToday)

	writeJSON(w, http.StatusOK, resp)
}

// ---- Coverage by collection ---------------------------------------

type coverageRow struct {
	CollectionID     string `json:"collection_id"`
	Name             string `json:"name"`
	MatchedEndpoints int    `json:"matched_endpoints"`
	CoveredEndpoints int    `json:"covered_endpoints"`
	CoveragePercent  int    `json:"coverage_percent"`
}

type coverageByCollectionResponse struct {
	Items     []coverageRow `json:"items"`
	Truncated bool          `json:"truncated"`
}

// coverageByCollectionLimit caps the strip to the worst-covered collections so
// the Dashboard stays bounded; truncation is reported, never silent.
const coverageByCollectionLimit = 20

// GET /api/v1/dashboard/coverage-by-collection
func (h *DashboardHandler) GetCoverageByCollection(w http.ResponseWriter, r *http.Request) {
	if h.db == nil || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "dashboard store not initialised")
		return
	}
	ctx := r.Context()
	tenantID := store.TenantID(ctx)
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "tenant not resolved")
		return
	}

	// Covered endpoint ids — the SAME rule as the global coverage KPI: an
	// endpoint is covered iff an enabled scan_definition targets it directly.
	covered := map[string]bool{}
	rows, err := h.db.QueryContext(ctx,
		`SELECT DISTINCT ae.id
		   FROM asset_endpoints ae
		   JOIN assets a ON a.id = ae.asset_id
		   JOIN scan_definitions sd
		     ON sd.scope_kind = 'asset_endpoint' AND sd.asset_endpoint_id = ae.id
		  WHERE a.tenant_id = $1 AND sd.enabled = TRUE`, tenantID)
	if err != nil {
		slog.Error("coverage-by-collection: covered endpoints", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to compute coverage")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to compute coverage")
			return
		}
		covered[id] = true
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to compute coverage")
		return
	}

	views, err := h.store.ListAllEndpointViewsTenant(ctx)
	if err != nil {
		slog.Error("coverage-by-collection: endpoint views", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to compute coverage")
		return
	}
	colls, err := h.store.ListCollections(ctx)
	if err != nil {
		slog.Error("coverage-by-collection: collections", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to compute coverage")
		return
	}

	items, truncated := aggregateCoverageByCollection(views, covered, colls, coverageByCollectionLimit)
	writeJSON(w, http.StatusOK, coverageByCollectionResponse{Items: items, Truncated: truncated})
}

// aggregateCoverageByCollection computes per-collection endpoint coverage for the
// endpoint-scope collections, reusing rules.Match (the same predicate→members
// evaluation collections use) and the caller-supplied covered-endpoint set (the
// same "has an enabled scan_definition" rule as the global KPI). Rows are sorted
// worst-coverage-first (tie-break by name) and capped at limit; the bool reports
// whether the cap truncated. Pure, for testability.
func aggregateCoverageByCollection(views []store.EndpointRow, covered map[string]bool, colls []model.Collection, limit int) ([]coverageRow, bool) {
	items := make([]coverageRow, 0)
	for ci := range colls {
		c := &colls[ci]
		if c.Scope != model.CollectionScopeEndpoint {
			continue
		}
		matched, cov := 0, 0
		for vi := range views {
			ev := rules.EndpointView{Asset: &views[vi].Asset, Endpoint: &views[vi].Endpoint}
			ok, err := rules.Match(c.Predicate, rules.ScopeEndpoint, ev)
			if err != nil || !ok {
				continue
			}
			matched++
			if covered[views[vi].Endpoint.ID] {
				cov++
			}
		}
		pct := 0
		if matched > 0 {
			pct = cov * 100 / matched
		}
		items = append(items, coverageRow{
			CollectionID:     c.ID,
			Name:             c.Name,
			MatchedEndpoints: matched,
			CoveredEndpoints: cov,
			CoveragePercent:  pct,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CoveragePercent != items[j].CoveragePercent {
			return items[i].CoveragePercent < items[j].CoveragePercent // worst first
		}
		return items[i].Name < items[j].Name
	})
	truncated := false
	if limit > 0 && len(items) > limit {
		items = items[:limit]
		truncated = true
	}
	return items, truncated
}

// ---- Suggested Actions --------------------------------------------

type suggestedAction struct {
	Kind                     string `json:"kind"`
	Title                    string `json:"title"`
	Count                    int    `json:"count"`
	CollectionIDOrPredicate  string `json:"collection_id_or_inline_predicate"`
	PrimaryCTA               string `json:"primary_cta"`
	SecondaryCTA             string `json:"secondary_cta"`
}

// GET /api/v1/dashboard/suggested-actions
//
// Computed groupings of coverage gaps, ordered highest-count first. Each
// action references a filter the UI can re-run on the Assets page. The
// predicate is serialised as a short querystring the frontend can drop
// straight onto `/assets?…` — once collections-backed predicates land in
// P4 we'll switch to `collection_id` where one exists.
func (h *DashboardHandler) GetSuggestedActions(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "dashboard store not initialised")
		return
	}
	ctx := r.Context()
	tenantID := store.TenantID(ctx)
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "tenant not resolved")
		return
	}

	actions := []suggestedAction{}

	// 1) DB-like endpoints with no credential mapping (via any collection).
	var missingCreds int
	_ = h.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM asset_endpoints ae
		  JOIN assets a ON a.id = ae.asset_id
		 WHERE a.tenant_id = $1
		   AND ae.service IN ('postgres','postgresql','mysql','mssql','mongodb','redis','oracle')
		   AND NOT EXISTS (
		     SELECT 1 FROM credential_mappings cm
		       JOIN collections c ON c.id = cm.collection_id
		      WHERE cm.tenant_id = $1
		   )`, tenantID,
	).Scan(&missingCreds)
	if missingCreds > 0 {
		actions = append(actions, suggestedAction{
			Kind:                    "endpoints_missing_credentials",
			Title:                   pluralize(missingCreds, "DB endpoint") + " missing credentials",
			Count:                   missingCreds,
			CollectionIDOrPredicate: "service_in=postgres,postgresql,mysql,mssql,mongodb,redis,oracle",
			PrimaryCTA:              "map-credentials",
			SecondaryCTA:            "create-scan",
		})
	}

	// 2) Assets with no scan history at all.
	var noScans int
	_ = h.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM assets a
		 WHERE a.tenant_id = $1
		   AND NOT EXISTS (
		     SELECT 1 FROM scans s
		      WHERE s.tenant_id = $1
		        AND s.asset_endpoint_id IN (
		          SELECT id FROM asset_endpoints WHERE asset_id = a.id
		        )
		   )`, tenantID,
	).Scan(&noScans)
	if noScans > 0 {
		actions = append(actions, suggestedAction{
			Kind:                    "assets_without_scans",
			Title:                   pluralize(noScans, "asset") + " without scans",
			Count:                   noScans,
			CollectionIDOrPredicate: "has_scans=false",
			PrimaryCTA:              "create-scan",
			SecondaryCTA:            "view",
		})
	}

	// 3) Recent scan failures (7d).
	weekAgo := time.Now().Add(-7 * 24 * time.Hour)
	var failed int
	_ = h.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM scans
		 WHERE tenant_id = $1 AND status = 'failed' AND created_at >= $2`,
		tenantID, weekAgo,
	).Scan(&failed)
	if failed > 0 {
		actions = append(actions, suggestedAction{
			Kind:                    "recent_scan_failures",
			Title:                   pluralize(failed, "scan") + " failed this week",
			Count:                   failed,
			CollectionIDOrPredicate: "status=failed&since=7d",
			PrimaryCTA:              "review-failures",
			SecondaryCTA:            "retry",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": actions})
}

// ---- Recent Activity ----------------------------------------------

type recentActivityItem struct {
	ID          string    `json:"id"`
	EventType   string    `json:"event_type"`
	Severity    string    `json:"severity,omitempty"`
	AssetID     string    `json:"asset_endpoint_id"`
	Hostname    string    `json:"hostname,omitempty"`
	PrimaryIP   string    `json:"primary_ip,omitempty"`
	Port        *int      `json:"port,omitempty"`
	Service     string    `json:"service,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// GET /api/v1/dashboard/recent-activity — last 10 asset_events joined
// to asset_endpoints + assets for display metadata. The join is
// tolerant: rows whose referenced endpoint has been deleted still
// surface with empty metadata fields.
func (h *DashboardHandler) GetRecentActivity(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "dashboard store not initialised")
		return
	}
	ctx := r.Context()
	tenantID := store.TenantID(ctx)
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "tenant not resolved")
		return
	}

	rows, err := h.db.QueryContext(ctx, `
		SELECT e.id, e.event_type, COALESCE(e.severity, ''), e.asset_id, e.occurred_at,
		       COALESCE(a.hostname, ''), COALESCE(host(a.primary_ip), ''),
		       ae.port, COALESCE(ae.service, '')
		  FROM asset_events e
		  LEFT JOIN asset_endpoints ae ON ae.id = e.asset_id
		  LEFT JOIN assets a ON a.id = ae.asset_id
		 WHERE e.tenant_id = $1
		 ORDER BY e.occurred_at DESC
		 LIMIT 10`, tenantID)
	if err != nil {
		slog.Error("dashboard: recent activity", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load activity")
		return
	}
	defer rows.Close()

	out := []recentActivityItem{}
	for rows.Next() {
		var it recentActivityItem
		var port sql.NullInt32
		if err := rows.Scan(&it.ID, &it.EventType, &it.Severity, &it.AssetID, &it.OccurredAt,
			&it.Hostname, &it.PrimaryIP, &port, &it.Service); err != nil {
			slog.Error("dashboard: scan activity row", "error", err)
			continue
		}
		if port.Valid {
			p := int(port.Int32)
			it.Port = &p
		}
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ---- helpers -------------------------------------------------------

func pluralize(n int, singular string) string {
	if n == 1 {
		return "1 " + singular
	}
	return itoa(n) + " " + singular + "s"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
