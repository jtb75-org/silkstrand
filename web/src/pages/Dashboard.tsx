import { useQuery } from '@tanstack/react-query';
import { Link, useNavigate } from 'react-router-dom';
import {
  getDashboardKpis,
  getSuggestedActions,
  getRecentActivity,
  listAssets,
} from '../api/client';
import {
  KpiCard,
  SuggestedActions,
  RecentActivity,
  CollectionList,
} from '../components/DashboardWidgets';
import SummaryChips, { type SummarySegment } from '../components/SummaryChips';

// Severity heat colors. critical/medium/low/info map to semantic design tokens;
// `high` is the one severity with no dedicated token (flagged for a future
// severity-token addition), so it uses an orange literal that fits the ramp.
const SEVERITY_COLOR: Record<string, string> = {
  critical: 'var(--ss-danger)',
  high: '#f97316',
  medium: 'var(--ss-warning)',
  low: 'var(--ss-info)',
  info: 'var(--ss-text-muted)',
};
const SEVERITY_ORDER = ['critical', 'high', 'medium', 'low', 'info'] as const;

// Asset-first Dashboard (P5-a). Layout per docs/plans/ui-shape.md
// § Dashboard: KPI row + 8/4 grid (Unclassified Endpoints on the left;
// Suggested Actions + Recent Activity on the right). Coverage gaps are
// surfaced ONLY via Suggested Actions per the design decision in the
// spec; no duplicated "assets without scans" widget here.

function formatDelta(n: number, suffix: string): string {
  if (n === 0) return `no change ${suffix}`.trim();
  const sign = n > 0 ? '+' : '';
  return `${sign}${n} ${suffix}`.trim();
}

export default function Dashboard() {
  const navigate = useNavigate();
  const kpisQ = useQuery({ queryKey: ['dashboard', 'kpis'], queryFn: getDashboardKpis });
  const actionsQ = useQuery({
    queryKey: ['dashboard', 'suggested-actions'],
    queryFn: getSuggestedActions,
  });
  const activityQ = useQuery({
    queryKey: ['dashboard', 'recent-activity'],
    queryFn: getRecentActivity,
  });

  // Unclassified Endpoints — in P1 the shape is "assets with unknown
  // service". Post-P4 this should read from a collection with scope
  // 'endpoint' and predicate service IS NULL / 'unknown'. For now we
  // reuse the existing assets list as a best-effort preview.
  const unclassifiedQ = useQuery({
    queryKey: ['dashboard', 'unclassified'],
    queryFn: () => listAssets({ page_size: 5 }),
  });

  const k = kpisQ.data;
  const severitySegments: SummarySegment[] = k
    ? SEVERITY_ORDER.map((sev) => ({
        key: sev,
        label: sev.charAt(0).toUpperCase() + sev.slice(1),
        count: k.findings_by_severity?.[sev] ?? 0,
        color: SEVERITY_COLOR[sev],
        onClick: () => navigate(`/findings?severity=${sev}`),
      }))
    : [];
  const rows =
    unclassifiedQ.data?.items.slice(0, 5).map((a) => ({
      id: a.id,
      primary: `${a.ip}${a.port ? ':' + a.port : ''}`,
      secondary: a.service || a.hostname || 'unknown',
      badge: a.compliance_status || '',
    })) ?? [];

  return (
    <div>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          marginBottom: 16,
        }}
      >
        <h1 style={{ margin: 0 }}>Dashboard</h1>
        <Link to="/scans/definitions/new" className="btn btn-primary">
          + New Scan
        </Link>
      </div>

      <div className="dash-kpi-row">
        <KpiCard
          label="Total Assets"
          value={kpisQ.isLoading ? '—' : k?.total_assets ?? 0}
          delta={k ? formatDelta(k.deltas.assets_new_this_week, 'this wk') : undefined}
          deltaTone={k && k.deltas.assets_new_this_week > 0 ? 'positive' : 'neutral'}
        />
        <KpiCard
          label="Coverage"
          value={kpisQ.isLoading ? '—' : `${k?.coverage_percent ?? 0}%`}
          delta={k ? formatDelta(k.deltas.coverage_delta_week, 'pts this wk') : undefined}
        />
        <KpiCard
          label="Critical Findings"
          value={kpisQ.isLoading ? '—' : k?.critical_findings ?? 0}
          delta={k ? formatDelta(k.deltas.findings_new_today, 'today') : undefined}
          deltaTone={k && k.deltas.findings_new_today > 0 ? 'negative' : 'neutral'}
        />
        <KpiCard
          label="New This Week"
          value={kpisQ.isLoading ? '—' : k?.new_this_week ?? 0}
          delta={k ? formatDelta(k.deltas.unresolved_new_week, 'unresolved') : undefined}
        />
      </div>

      <section style={{ marginBottom: 'var(--ss-space-lg)' }}>
        <h2 style={{ fontSize: 'var(--ss-text-h3)', marginBottom: 'var(--ss-space-sm)' }}>
          Findings by severity
        </h2>
        {kpisQ.isLoading ? (
          <p className="muted">Loading…</p>
        ) : (
          <SummaryChips variant="bar" segments={severitySegments} emptyText="No open findings." />
        )}
      </section>

      <div className="dash-grid">
        <div>
          <CollectionList
            title="Unclassified Endpoints"
            rows={rows}
            viewAllHref="/assets?service=unknown"
            isLoading={unclassifiedQ.isLoading}
            error={unclassifiedQ.error}
            emptyMessage="No unclassified endpoints."
          />
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <SuggestedActions
            items={actionsQ.data?.items ?? []}
            isLoading={actionsQ.isLoading}
            error={actionsQ.error}
          />
          <RecentActivity
            items={activityQ.data?.items ?? []}
            isLoading={activityQ.isLoading}
            error={activityQ.error}
          />
        </div>
      </div>
    </div>
  );
}
