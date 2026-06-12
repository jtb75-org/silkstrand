import { useMemo, useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
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
import { formatAbsolute, formatRelative } from '../lib/time';

// Three tabs over one filtered population per docs/plans/ui-shape.md §Assets:
//   · Assets     — one row per asset (host-level)
//   · Endpoints  — one row per asset_endpoint (port-level)
//   · Findings   — one row per finding, scoped to the filtered population
// Multi-select persists across tabs and drives the Bulk Actions bar.

type TabId = 'assets' | 'endpoints' | 'findings';

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

/** Resolve the display hostname for an asset (flat or legacy shape). */
function assetHost(a: DiscoveredAsset): string {
  return a.hostname || a.primary_ip || a.ip || '-';
}

/** Resolve the display IP for an asset (flat or legacy shape). */
function assetIP(a: DiscoveredAsset): string {
  return a.primary_ip || a.ip || '-';
}

/** Coverage indicator per design-system.md: checkmark/cross with accessible labels. */
function CoverageIndicator({ scan, creds }: { scan: boolean; creds: boolean }) {
  return (
    <span title={`Scan ${scan ? 'configured' : 'missing'} · Creds ${creds ? 'mapped' : 'missing'}`}>
      <span
        style={{ color: scan ? '#10b981' : '#ef4444' }}
        aria-label={scan ? 'Scan configured' : 'Scan missing'}
      >
        {scan ? '\u2713' : '\u2717'}
      </span>
      <span style={{ margin: '0 4px' }}>/</span>
      <span
        style={{ color: creds ? '#10b981' : '#ef4444' }}
        aria-label={creds ? 'Credentials mapped' : 'Credentials missing'}
      >
        {creds ? '\u2713' : '\u2717'}
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

  const items = assets?.items ?? [];
  const total = assets?.total ?? 0;
  const scanRunning = !!qc
    .getQueryData<Scan[]>(['scans'])
    ?.some((s) => s.scan_type === 'discovery' && (s.status === 'running' || s.status === 'pending'));

  return (
    <div>
      <div className="page-header">
        <h1>Assets</h1>
      </div>

      {/* Primary filter row: search + collection + Save-as-Collection */}
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12 }}>
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

      <div className="tab-bar" role="tablist" style={{ marginBottom: 12 }}>
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
          onSelect={selectAsset}
        />
      )}
      {!endpointsLoading && !endpointsError && tab === 'endpoints' && (
        <EndpointsView
          items={endpoints?.items ?? []}
          selected={selected}
          onToggle={toggleRow}
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
            <p className="muted" style={{ marginTop: 0, fontSize: 13 }}>
              Discover websites behind a shared ingress / reverse proxy. Each name
              becomes its own asset (a <code>*.example.com</code> wildcard authorizes
              but creates no asset). Paste one per line.
            </p>
            <textarea
              rows={7}
              placeholder={'app.example.com\napi.example.com\n*.internal.example.com'}
              value={importText}
              onChange={(e) => setImportText(e.target.value)}
              style={{ width: '100%', fontFamily: 'monospace', fontSize: 13 }}
            />
            {importResult && (
              <div style={{ marginTop: 12, fontSize: 13 }}>
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
                    <pre style={{ background: '#f3f4f6', padding: 10, borderRadius: 6, userSelect: 'all', overflowX: 'auto', margin: 0 }}>
{'allow:\n' + importResult.allowlist_entries.map((e) => `  - ${e}`).join('\n')}
                    </pre>
                  </>
                )}
              </div>
            )}
            {importMutation.error && (
              <p className="error">{(importMutation.error as Error).message}</p>
            )}
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
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
      style={{ marginRight: 8 }}
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
  onSelect,
}: {
  items: DiscoveredAsset[];
  selected: Set<string>;
  onToggle: (id: string) => void;
  onSelect: (id: string) => void;
}) {
  if (items.length === 0)
    return (
      <div className="ss-empty" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 12, padding: '48px 24px', color: '#6b7280', textAlign: 'center' }}>
        <span style={{ fontSize: 40 }}>&#x1F4E6;</span>
        <span>No assets found. Create a target or trigger a discovery scan.</span>
      </div>
    );
  return (
    <table className="table">
      <thead>
        <tr>
          <th style={{ width: 32 }}></th>
          <th>Host</th>
          <th>IP</th>
          <th>Type</th>
          <th>Env</th>
          <th>#Endpoints</th>
          <th>Max severity</th>
          <th>Coverage</th>
          <th>Last seen</th>
        </tr>
      </thead>
      <tbody>
        {items.map((a) => {
          const sev = topSeverity(a);
          const cov = deriveCoverage(a);
          return (
            <tr
              key={a.id}
              className="clickable-row"
              onClick={() => onSelect(a.id)}
            >
              <td onClick={(e) => e.stopPropagation()}>
                <input
                  type="checkbox"
                  checked={selected.has(a.id)}
                  onChange={() => onToggle(a.id)}
                  aria-label={`Select ${assetHost(a)}`}
                />
              </td>
              <td>{assetHost(a)}</td>
              <td>{assetIP(a)}</td>
              <td>{a.resource_type || '-'}</td>
              <td>{a.environment || '-'}</td>
              <td>{a.endpoints_count ?? 0}</td>
              <td>
                {sev ? <span className={severityBadgeClass(sev)}>{sev}</span> : '-'}
              </td>
              <td>
                <CoverageIndicator scan={cov.scan} creds={cov.creds} />
              </td>
              <td title={formatAbsolute(a.last_seen)}>{formatRelative(a.last_seen)}</td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

// ── Endpoints tab ────────────────────────────────────────────────────────────
// One row per asset_endpoint (port-level), powered by GET /api/v1/asset-endpoints.
function EndpointsView({
  items,
  selected,
  onToggle,
  onSelect,
}: {
  items: AssetEndpointRow[];
  selected: Set<string>;
  onToggle: (id: string) => void;
  onSelect: (assetId: string) => void;
}) {
  if (items.length === 0)
    return (
      <div className="ss-empty" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 12, padding: '48px 24px', color: '#6b7280', textAlign: 'center' }}>
        <span style={{ fontSize: 40 }}>&#x1F50C;</span>
        <span>No endpoints found.</span>
      </div>
    );
  return (
    <table className="table">
      <thead>
        <tr>
          <th style={{ width: 32 }}></th>
          <th>Host</th>
          <th>IP:Port</th>
          <th>Service</th>
          <th>Tech</th>
          <th>Findings</th>
          <th>Coverage</th>
          <th>Last seen</th>
        </tr>
      </thead>
      <tbody>
        {items.map((ep) => (
          <tr
            key={ep.id}
            className="clickable-row"
            onClick={() => onSelect(ep.asset_id)}
          >
            <td onClick={(e) => e.stopPropagation()}>
              <input
                type="checkbox"
                checked={selected.has(ep.id)}
                onChange={() => onToggle(ep.id)}
                aria-label={`Select ${ep.host || ep.ip}:${ep.port}`}
              />
            </td>
            <td>{ep.host || ep.ip || '-'}</td>
            <td>{ep.ip}:{ep.port}</td>
            <td>{ep.service || '-'}</td>
            <td>{ep.technologies?.join(', ') || '-'}</td>
            <td>
              {ep.findings_count > 0
                ? <span className="badge badge-cve-medium">{ep.findings_count}</span>
                : '-'}
            </td>
            <td>
              <CoverageIndicator
                scan={ep.coverage?.has_scan_definition ?? false}
                creds={ep.coverage?.has_credential_mapping ?? false}
              />
            </td>
            <td title={formatAbsolute(ep.last_seen)}>{formatRelative(ep.last_seen)}</td>
          </tr>
        ))}
      </tbody>
    </table>
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
  if (rows.length === 0)
    return (
      <div className="ss-empty" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 12, padding: '48px 24px', color: '#6b7280', textAlign: 'center' }}>
        <span style={{ fontSize: 40 }}>&#x1F50D;</span>
        <span>No findings in the current filtered population.</span>
      </div>
    );
  return (
    <table className="table">
      <thead>
        <tr>
          <th>Severity</th>
          <th>Title</th>
          <th>Source</th>
          <th>Asset</th>
          <th>Last seen</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.key}>
            <td><span className={severityBadgeClass(r.severity)}>{r.severity}</span></td>
            <td>{r.title}</td>
            <td>{r.source}</td>
            <td>{r.asset}</td>
            <td title={formatAbsolute(r.lastSeen)}>{formatRelative(r.lastSeen)}</td>
          </tr>
        ))}
      </tbody>
    </table>
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
