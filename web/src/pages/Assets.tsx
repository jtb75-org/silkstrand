import { useMemo, useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Boxes, Check, X, Package, Unplug, SearchX } from 'lucide-react';
import {
  listAssets,
  listAssetEndpoints,
  listCollections,
  createCollection,
  listScans,
  importDNSNames,
  type AssetFilterParams,
  type AssetEndpointRow,
  type ImportDNSResult,
} from '../api/client';
import type {
  CVE,
  Collection,
  DiscoveredAsset,
  Scan,
} from '../api/types';
import AssetsFilterChips, { type ChipId } from '../components/AssetsFilterChips';
import AssetDetailDrawer from '../components/AssetDetailDrawer';
import AssetsBulkActions from '../components/AssetsBulkActions';
import PredicateBuilder, { type Predicate } from '../components/PredicateBuilder';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';
import { formatAbsolute, formatRelative } from '../lib/time';

// Three tabs over one filtered population per docs/plans/ui-shape.md §Assets:
//   · Assets     — one row per asset (host-level)
//   · Endpoints  — one row per asset_endpoint (port-level)
//   · Findings   — one row per finding, scoped to the filtered population
// Multi-select persists across tabs and drives the Bulk Actions bar.

type TabId = 'assets' | 'endpoints' | 'findings';

// Severity sorts by risk rank, not alphabetically (critical > … > info).
const SEV_RANK: Record<string, number> = { critical: 5, high: 4, medium: 3, low: 2, info: 1 };
const sevRank = (s?: string | null) => (s ? SEV_RANK[s.toLowerCase()] ?? 0 : 0);

function chipsToParams(chips: Set<ChipId>): AssetFilterParams {
  const p: AssetFilterParams = {};
  if (chips.has('with_cves')) p.cve_count_gte = 1;
  if (chips.has('compliance_candidates')) {
    p.service_in = ['postgresql', 'mysql', 'mssql', 'mongodb'];
  }
  if (chips.has('failing')) p.compliance_status = 'fail';
  if (chips.has('recently_changed')) p.changed_since = '7d';
  if (chips.has('new_this_week')) p.new_since = '7d';
  if (chips.has('manual')) p.source = 'manual';
  if (chips.has('discovered')) p.source = 'discovered';
  return p;
}

function topSeverity(asset: DiscoveredAsset): string | null {
  const r = asset.risk;
  if (r) {
    if (r.max_severity) return r.max_severity;
    if (r.critical > 0) return 'critical';
    if (r.high > 0) return 'high';
    if (r.medium > 0) return 'medium';
    if (r.low > 0) return 'low';
    if (r.info > 0) return 'info';
  }
  // Fallback to legacy cves array
  if (!Array.isArray(asset.cves) || asset.cves.length === 0) return null;
  const order = ['critical', 'high', 'medium', 'low', 'info'];
  for (const sev of order) {
    if ((asset.cves as CVE[]).some((c) => c.severity === sev)) return sev;
  }
  return null;
}

function severityBadgeClass(sev: string): string {
  switch (sev) {
    case 'critical':
    case 'high':
      return 'badge badge-cve-critical';
    case 'medium':
      return 'badge badge-cve-medium';
    case 'low':
    case 'info':
      return 'badge badge-cve-low';
    default:
      return 'badge';
  }
}

// Compact per-severity breakdown for the Assets tab — small count pills
// (e.g. "2 C · 3 H · 1 M"), colored by the existing severity badge buckets.
// Counts come from asset.risk, which GET /api/v1/assets already populates
// (open-finding severity rollup), so no backend change is needed.
function SeverityBreakdown({ asset }: { asset: DiscoveredAsset }) {
  const r = asset.risk;
  const parts = r
    ? ([
        { sev: 'critical', label: 'C', n: r.critical },
        { sev: 'high', label: 'H', n: r.high },
        { sev: 'medium', label: 'M', n: r.medium },
        { sev: 'low', label: 'L', n: r.low },
        { sev: 'info', label: 'I', n: r.info },
      ] as const).filter((p) => p.n > 0)
    : [];
  if (parts.length === 0) return <span className="muted">—</span>;
  return (
    <span style={{ display: 'inline-flex', gap: 'var(--ss-space-xs)', flexWrap: 'wrap' }}>
      {parts.map((p) => (
        <span key={p.sev} className={severityBadgeClass(p.sev)} title={`${p.n} ${p.sev}`}>
          {p.n} {p.label}
        </span>
      ))}
    </span>
  );
}

/** Resolve the display hostname for an asset (flat or legacy shape). */
function assetHost(a: DiscoveredAsset): string {
  return a.hostname || a.primary_ip || a.ip || '-';
}

/** Resolve the display IP for an asset (flat or legacy shape). */
function assetIP(a: DiscoveredAsset): string {
  return a.primary_ip || a.ip || '-';
}

/** Coverage indicator per design-system.md §6: Lucide check/cross, token color,
 *  accessible labels on each wrapper (the glyphs themselves are decorative). */
function CoverageIndicator({ scan, creds }: { scan: boolean; creds: boolean }) {
  return (
    <span
      title={`Scan ${scan ? 'configured' : 'missing'} · Creds ${creds ? 'mapped' : 'missing'}`}
      style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--ss-space-xs)' }}
    >
      <span
        style={{ display: 'inline-flex', color: scan ? 'var(--ss-success)' : 'var(--ss-danger)' }}
        aria-label={scan ? 'Scan configured' : 'Scan missing'}
      >
        {scan ? <Check size={16} aria-hidden="true" /> : <X size={16} aria-hidden="true" />}
      </span>
      <span aria-hidden="true">/</span>
      <span
        style={{ display: 'inline-flex', color: creds ? 'var(--ss-success)' : 'var(--ss-danger)' }}
        aria-label={creds ? 'Credentials mapped' : 'Credentials missing'}
      >
        {creds ? <Check size={16} aria-hidden="true" /> : <X size={16} aria-hidden="true" />}
      </span>
    </span>
  );
}

/** Derive scan/creds booleans from the coverage rollup object. */
function deriveCoverage(a: DiscoveredAsset): { scan: boolean; creds: boolean } {
  const cov = a.coverage;
  if (!cov) return { scan: false, creds: false };
  // New rollup shape from the flattened API:
  if ('endpoints_with_scan_definition' in cov) {
    return {
      scan: (cov.endpoints_with_scan_definition ?? 0) > 0,
      creds: (cov.endpoints_with_credential_mapping ?? 0) > 0,
    };
  }
  // Legacy CoverageFlags shape (backwards compat):
  const legacy = cov as unknown as { scan_configured?: boolean; creds_mapped?: boolean };
  return {
    scan: !!legacy.scan_configured,
    creds: !!legacy.creds_mapped,
  };
}

function LastSeen({ ts }: { ts: string }) {
  return <span title={formatAbsolute(ts)}>{formatRelative(ts)}</span>;
}

export default function Assets() {
  const navigate = useNavigate();
  const { id: selectedAssetId } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = ((searchParams.get('tab') as TabId) || 'assets') as TabId;
  const [chips, setChips] = useState<Set<ChipId>>(new Set());
  const [search, setSearch] = useState('');
  const [collectionId, setCollectionId] = useState<string>('');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [saveOpen, setSaveOpen] = useState(false);

  // ADR 014 D2: import DNS names → http_service assets.
  const [importOpen, setImportOpen] = useState(false);
  const [importText, setImportText] = useState('');
  const [importResult, setImportResult] = useState<ImportDNSResult | null>(null);

  const filters: AssetFilterParams = useMemo(
    () => ({
      ...chipsToParams(chips),
      q: search || undefined,
      page: 1,
      page_size: 200,
    }),
    [chips, search],
  );

  const qc = useQueryClient();

  const importMutation = useMutation({
    mutationFn: () =>
      importDNSNames(importText.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean)),
    onSuccess: (res) => {
      setImportResult(res);
      qc.invalidateQueries({ queryKey: ['assets'] });
    },
  });

  const { data: assets, isLoading, error } = useQuery({
    queryKey: ['assets', filters, collectionId],
    queryFn: () => listAssets(filters),
    refetchInterval: () => {
      const scans = qc.getQueryData<Scan[]>(['scans']);
      const running = scans?.some(
        (s) => s.scan_type === 'discovery' && (s.status === 'running' || s.status === 'pending'),
      );
      return running ? 5000 : false;
    },
    refetchIntervalInBackground: false,
  });

  useQuery({
    queryKey: ['scans'],
    queryFn: () => listScans(),
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  });

  const endpointFilters = useMemo(
    () => ({
      q: search || undefined,
      source: (chips.has('manual') ? 'manual' : chips.has('discovered') ? 'discovered' : undefined) as string | undefined,
      page: 1,
      page_size: 200,
    }),
    [chips, search],
  );

  const { data: endpoints, isLoading: endpointsLoading, error: endpointsError } = useQuery({
    queryKey: ['asset-endpoints', endpointFilters],
    queryFn: () => listAssetEndpoints(endpointFilters),
    enabled: tab === 'endpoints',
  });

  const { data: collections } = useQuery({
    queryKey: ['collections'],
    queryFn: () => listCollections(),
  });

  function toggleChip(id: ChipId) {
    setChips((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      if (id === 'manual') next.delete('discovered');
      if (id === 'discovered') next.delete('manual');
      return next;
    });
  }

  function selectTab(t: TabId) {
    const next = new URLSearchParams(searchParams);
    if (t === 'assets') next.delete('tab');
    else next.set('tab', t);
    setSearchParams(next, { replace: true });
  }

  function selectAsset(id: string) {
    navigate(`/assets/${id}${tab !== 'assets' ? `?tab=${tab}` : ''}`);
  }

  function closeDrawer() {
    navigate(`/assets${tab !== 'assets' ? `?tab=${tab}` : ''}`);
  }

  function toggleRow(id: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  // Header select-all. `ids` is the current rendered row set (DataTable hands us
  // post-sort rows); selection persists across tabs so we never auto-clear.
  function toggleAll(ids: string[], checked: boolean) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (checked) ids.forEach((id) => next.add(id));
      else ids.forEach((id) => next.delete(id));
      return next;
    });
  }

  const items = assets?.items ?? [];
  const total = assets?.total ?? 0;
  const scanRunning = !!qc
    .getQueryData<Scan[]>(['scans'])
    ?.some((s) => s.scan_type === 'discovery' && (s.status === 'running' || s.status === 'pending'));

  return (
    <div>
      <div
        className="page-header"
        style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}
      >
        <Boxes size={24} style={{ color: 'var(--ss-accent-primary)' }} />
        <h1>Assets</h1>
      </div>

      {/* Primary filter row: search + collection + Save-as-Collection */}
      <div
        style={{
          display: 'flex',
          gap: 'var(--ss-space-sm)',
          alignItems: 'center',
          marginBottom: 'var(--ss-space-md)',
        }}
      >
        <input
          type="search"
          placeholder="Search hosts, IPs, services…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          style={{ flex: 1, minWidth: 240 }}
        />
        <select
          value={collectionId}
          onChange={(e) => setCollectionId(e.target.value)}
          aria-label="Collection"
        >
          <option value="">All</option>
          {collections?.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name} ({c.scope})
            </option>
          ))}
        </select>
        <button className="btn btn-sm" onClick={() => setSaveOpen(true)}>
          Save as Collection
        </button>
        <button className="btn btn-sm" onClick={() => { setImportResult(null); setImportOpen(true); }}>
          Import DNS names
        </button>
      </div>

      <AssetsFilterChips
        active={chips}
        total={total}
        onToggle={toggleChip}
        onClear={() => setChips(new Set())}
        scanRunning={scanRunning}
      />

      <div className="tab-bar" role="tablist" style={{ marginBottom: 'var(--ss-space-md)' }}>
        <TabBtn id="assets" cur={tab} onClick={selectTab}>Assets</TabBtn>
        <TabBtn id="endpoints" cur={tab} onClick={selectTab}>Endpoints</TabBtn>
        <TabBtn id="findings" cur={tab} onClick={selectTab}>Findings</TabBtn>
      </div>

      {tab === 'endpoints'
        ? (endpointsError && <p className="error">{(endpointsError as Error).message}</p>)
        : (error && <p className="error">{(error as Error).message}</p>)}
      {tab === 'endpoints'
        ? (endpointsLoading && <p>Loading…</p>)
        : (isLoading && <p>Loading…</p>)}

      {!isLoading && !error && tab === 'assets' && (
        <AssetsView
          items={items}
          selected={selected}
          onToggle={toggleRow}
          onToggleAll={toggleAll}
          onSelect={selectAsset}
        />
      )}
      {!endpointsLoading && !endpointsError && tab === 'endpoints' && (
        <EndpointsView
          items={endpoints?.items ?? []}
          selected={selected}
          onToggle={toggleRow}
          onToggleAll={toggleAll}
          onSelect={(assetId: string) => selectAsset(assetId)}
        />
      )}
      {!isLoading && !error && tab === 'findings' && <FindingsView items={items} />}

      <AssetsBulkActions
        selectionCount={selected.size}
        resolveEndpointIds={() => Array.from(selected)}
        onClear={() => setSelected(new Set())}
        scopeKind={tab === 'assets' ? 'asset' : 'asset_endpoint'}
      />

      {saveOpen && (
        <SaveAsCollectionModal
          scope={tab === 'findings' ? 'finding' : tab === 'endpoints' ? 'endpoint' : 'asset'}
          seedPredicate={filtersToPredicate(filters)}
          onClose={() => setSaveOpen(false)}
        />
      )}

      {importOpen && (
        <div className="modal-backdrop" onClick={() => setImportOpen(false)}>
          <div className="form-card" style={{ maxWidth: 560 }} onClick={(e) => e.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>Import DNS names</h3>
            <p className="muted" style={{ marginTop: 0, fontSize: '13px' }}>
              Discover websites behind a shared ingress / reverse proxy. Each name
              becomes its own asset (a <code>*.example.com</code> wildcard authorizes
              but creates no asset). Paste one per line.
            </p>
            <textarea
              rows={7}
              placeholder={'app.example.com\napi.example.com\n*.internal.example.com'}
              value={importText}
              onChange={(e) => setImportText(e.target.value)}
              style={{ width: '100%', fontFamily: 'monospace', fontSize: '13px' }}
            />
            {importResult && (
              <div style={{ marginTop: 'var(--ss-space-md)', fontSize: '13px' }}>
                <p style={{ margin: '0 0 6px' }}>
                  <strong>{importResult.imported.length}</strong> imported
                  {importResult.wildcards.length > 0 && <>, <strong>{importResult.wildcards.length}</strong> wildcard(s)</>}
                  {importResult.skipped.length > 0 && <>, <strong>{importResult.skipped.length}</strong> skipped</>}.
                </p>
                {importResult.skipped.length > 0 && (
                  <ul className="muted" style={{ margin: '0 0 8px', paddingLeft: 18 }}>
                    {importResult.skipped.map((s, i) => (
                      <li key={i}><code>{s.input}</code> — {s.reason}</li>
                    ))}
                  </ul>
                )}
                {importResult.allowlist_entries.length > 0 && (
                  <>
                    <p style={{ margin: '8px 0 4px' }}>
                      Add these to the agent's <code>scan-allowlist.yaml</code> so they
                      actually scan (the host file is authoritative):
                    </p>
                    <pre style={{ background: 'var(--ss-bg-raised)', padding: 10, borderRadius: 'var(--ss-radius-md)', userSelect: 'all', overflowX: 'auto', margin: 0 }}>
{'allow:\n' + importResult.allowlist_entries.map((e) => `  - ${e}`).join('\n')}
                    </pre>
                  </>
                )}
              </div>
            )}
            {importMutation.error && (
              <p className="error">{(importMutation.error as Error).message}</p>
            )}
            <div style={{ display: 'flex', gap: 'var(--ss-space-sm)', justifyContent: 'flex-end', marginTop: 'var(--ss-space-lg)' }}>
              <button className="btn" onClick={() => setImportOpen(false)}>Close</button>
              <button
                className="btn btn-primary"
                disabled={importMutation.isPending || !importText.trim()}
                onClick={() => importMutation.mutate()}
              >
                {importMutation.isPending ? 'Importing…' : 'Import'}
              </button>
            </div>
          </div>
        </div>
      )}

      {selectedAssetId && (
        <AssetDetailDrawer assetId={selectedAssetId} onClose={closeDrawer} />
      )}
    </div>
  );
}

function TabBtn({
  id,
  cur,
  onClick,
  children,
}: {
  id: TabId;
  cur: TabId;
  onClick: (t: TabId) => void;
  children: React.ReactNode;
}) {
  return (
    <button
      role="tab"
      aria-selected={cur === id}
      className={`btn btn-sm ${cur === id ? 'btn-primary' : ''}`}
      style={{ marginRight: 'var(--ss-space-sm)' }}
      onClick={() => onClick(id)}
    >
      {children}
    </button>
  );
}

// ── Assets tab ───────────────────────────────────────────────────────────────
function AssetsView({
  items,
  selected,
  onToggle,
  onToggleAll,
  onSelect,
}: {
  items: DiscoveredAsset[];
  selected: Set<string>;
  onToggle: (id: string) => void;
  onToggleAll: (ids: string[], checked: boolean) => void;
  onSelect: (id: string) => void;
}) {
  const columns: ColumnDef<DiscoveredAsset>[] = [
    {
      // Stacked identity cell (mock pattern): host primary, IP muted beneath.
      // Sorts by host. Replaces the separate Host + IP columns.
      id: 'asset',
      header: 'Asset',
      accessorFn: (a) => assetHost(a),
      cell: ({ row }) => {
        const host = assetHost(row.original);
        const ip = assetIP(row.original);
        const showIP = ip !== '-' && ip !== host;
        return (
          <div>
            <div>{host}</div>
            {showIP && (
              <div className="muted" style={{ fontSize: 'var(--ss-text-body-sm)' }}>{ip}</div>
            )}
          </div>
        );
      },
    },
    { id: 'type', header: 'Type', accessorFn: (a) => a.resource_type || '-' },
    { id: 'env', header: 'Env', accessorFn: (a) => a.environment || '-' },
    {
      id: 'endpoints',
      header: '#Endpoints',
      accessorFn: (a) => a.endpoints_count ?? 0,
    },
    {
      // Per-severity breakdown (count pills); sorts by worst severity rank.
      id: 'severity',
      header: 'Severity',
      accessorFn: (a) => topSeverity(a) ?? '',
      cell: ({ row }) => <SeverityBreakdown asset={row.original} />,
      sortingFn: (a, b) => sevRank(topSeverity(a.original)) - sevRank(topSeverity(b.original)),
    },
    {
      id: 'coverage',
      header: 'Coverage',
      enableSorting: false,
      cell: ({ row }) => {
        const cov = deriveCoverage(row.original);
        return <CoverageIndicator scan={cov.scan} creds={cov.creds} />;
      },
    },
    {
      id: 'last_seen',
      header: 'Last seen',
      accessorFn: (a) => a.last_seen,
      cell: ({ row }) => <LastSeen ts={row.original.last_seen} />,
    },
  ];

  if (items.length === 0)
    return (
      <EmptyState
        icon={<Package />}
        title="No assets found. Create a target or trigger a discovery scan."
      />
    );

  return (
    <DataTable
      columns={columns}
      data={items}
      getRowId={(a) => a.id}
      selectable
      selectedIds={selected}
      onToggleRow={onToggle}
      onToggleAll={onToggleAll}
      onRowClick={(a) => onSelect(a.id)}
      initialSorting={[{ id: 'severity', desc: true }]}
    />
  );
}

// ── Endpoints tab ────────────────────────────────────────────────────────────
// One row per asset_endpoint (port-level), powered by GET /api/v1/asset-endpoints.
function EndpointsView({
  items,
  selected,
  onToggle,
  onToggleAll,
  onSelect,
}: {
  items: AssetEndpointRow[];
  selected: Set<string>;
  onToggle: (id: string) => void;
  onToggleAll: (ids: string[], checked: boolean) => void;
  onSelect: (assetId: string) => void;
}) {
  const columns: ColumnDef<AssetEndpointRow>[] = [
    { id: 'host', header: 'Host', accessorFn: (ep) => ep.host || ep.ip || '-' },
    { id: 'ip_port', header: 'IP:Port', accessorFn: (ep) => `${ep.ip}:${ep.port}` },
    { id: 'service', header: 'Service', accessorFn: (ep) => ep.service || '-' },
    {
      id: 'tech',
      header: 'Tech',
      enableSorting: false,
      accessorFn: (ep) => ep.technologies?.join(', ') || '-',
    },
    {
      id: 'findings',
      header: 'Findings',
      accessorFn: (ep) => ep.findings_count,
      cell: ({ row }) =>
        row.original.findings_count > 0
          ? <span className="badge badge-cve-medium">{row.original.findings_count}</span>
          : '-',
    },
    {
      id: 'coverage',
      header: 'Coverage',
      enableSorting: false,
      cell: ({ row }) => (
        <CoverageIndicator
          scan={row.original.coverage?.has_scan_definition ?? false}
          creds={row.original.coverage?.has_credential_mapping ?? false}
        />
      ),
    },
    {
      id: 'last_seen',
      header: 'Last seen',
      accessorFn: (ep) => ep.last_seen,
      cell: ({ row }) => <LastSeen ts={row.original.last_seen} />,
    },
  ];

  if (items.length === 0)
    return <EmptyState icon={<Unplug />} title="No endpoints found." />;

  return (
    <DataTable
      columns={columns}
      data={items}
      getRowId={(ep) => ep.id}
      selectable
      selectedIds={selected}
      onToggleRow={onToggle}
      onToggleAll={onToggleAll}
      onRowClick={(ep) => onSelect(ep.asset_id)}
    />
  );
}

// ── Findings tab ─────────────────────────────────────────────────────────────
function FindingsView({ items }: { items: DiscoveredAsset[] }) {
  // ADR 007 splits findings into its own table; until the Findings API is
  // wired into this tab we derive a minimal view from the CVE arrays on
  // the asset rows. This keeps the tab functional.
  type Row = {
    key: string;
    assetId: string;
    severity: string;
    title: string;
    source: string;
    asset: string;
    lastSeen: string;
  };
  const rows: Row[] = [];
  for (const a of items) {
    if (!Array.isArray(a.cves)) continue;
    for (const c of a.cves as CVE[]) {
      rows.push({
        key: `${a.id}:${c.id}`,
        assetId: a.id,
        severity: c.severity || 'info',
        title: c.id,
        source: c.template || 'nuclei',
        asset: assetIP(a),
        lastSeen: a.last_seen,
      });
    }
  }

  const columns: ColumnDef<Row>[] = [
    {
      id: 'severity',
      header: 'Severity',
      accessorFn: (r) => r.severity,
      cell: ({ row }) => <span className={severityBadgeClass(row.original.severity)}>{row.original.severity}</span>,
      sortingFn: (a, b) => sevRank(a.original.severity) - sevRank(b.original.severity),
    },
    { id: 'title', header: 'Title', accessorKey: 'title' },
    { id: 'source', header: 'Source', accessorKey: 'source' },
    { id: 'asset', header: 'Asset', accessorKey: 'asset' },
    {
      id: 'last_seen',
      header: 'Last seen',
      accessorFn: (r) => r.lastSeen,
      cell: ({ row }) => <LastSeen ts={row.original.lastSeen} />,
    },
  ];

  if (rows.length === 0)
    return (
      <EmptyState icon={<SearchX />} title="No findings in the current filtered population." />
    );

  return (
    <DataTable
      columns={columns}
      data={rows}
      getRowId={(r) => r.key}
      initialSorting={[{ id: 'severity', desc: true }]}
    />
  );
}

// Derive a starting predicate from the current Assets filter state so the
// "Save as Collection" flow seeds with a useful object instead of `{}`.
function filtersToPredicate(f: AssetFilterParams): Predicate {
  const clauses: Predicate[] = [];
  if (f.service_in?.length) clauses.push({ service: { $in: f.service_in } });
  if (f.service) clauses.push({ service: f.service });
  if (f.source) clauses.push({ source: f.source });
  if (f.compliance_status) clauses.push({ compliance_status: f.compliance_status });
  if (f.cve_count_gte != null) clauses.push({ cve_count: { $gte: f.cve_count_gte } });
  if (f.q) clauses.push({ q: f.q });
  if (clauses.length === 0) return {};
  if (clauses.length === 1) return clauses[0];
  return { $and: clauses };
}

function SaveAsCollectionModal({
  scope,
  seedPredicate,
  onClose,
}: {
  scope: 'asset' | 'endpoint' | 'finding';
  seedPredicate: Predicate;
  onClose: () => void;
}) {
  const [predicate, setPredicate] = useState<Predicate>(seedPredicate);
  const qc = useQueryClient();
  const mut = useMutation({
    mutationFn: (name: string) =>
      createCollection({ name, scope, predicate }),
    onSuccess: (c: Collection) => {
      qc.invalidateQueries({ queryKey: ['collections'] });
      onClose();
      void c;
    },
  });
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <header className="modal-header">
          <h3>Save current filter as a Collection</h3>
          <button className="btn btn-sm" onClick={onClose}>×</button>
        </header>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const fd = new FormData(e.currentTarget);
            const name = (fd.get('name') as string).trim();
            if (name) mut.mutate(name);
          }}
        >
          <div className="modal-body">
            <div className="form-group">
              <label htmlFor="name">Name</label>
              <input id="name" name="name" required autoFocus />
            </div>
            <div className="form-group">
              <label>Scope</label>
              <div className="muted">{scope}</div>
            </div>
            <div className="form-group">
              <label>Predicate</label>
              <PredicateBuilder value={predicate} onChange={setPredicate} />
            </div>
            {mut.error && <p className="error">{(mut.error as Error).message}</p>}
          </div>
          <footer className="modal-footer">
            <button type="button" className="btn" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn btn-primary" disabled={mut.isPending}>
              {mut.isPending ? 'Saving…' : 'Save'}
            </button>
          </footer>
        </form>
      </div>
    </div>
  );
}
