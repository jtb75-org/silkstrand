import { useMemo, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { ShieldAlert, ShieldCheck, EyeOff, Eye } from 'lucide-react';
import {
  listFindings,
  getFinding,
  suppressFinding,
  reopenFinding,
  listCollections,
} from '../api/client';
import type {
  Finding,
  FindingSourceKind,
  FindingStatus,
  Collection,
} from '../api/types';
import DataTable from '../components/DataTable';
import CodeBlock from '../components/CodeBlock';

type Tab = 'vulnerabilities' | 'compliance';

const TABS: { value: Tab; label: string }[] = [
  { value: 'vulnerabilities', label: 'Vulnerabilities' },
  { value: 'compliance', label: 'Compliance' },
];

// SOC-facing top-level view. Vulnerabilities tab reads network_vuln;
// Compliance tab unions bundle_compliance + network_compliance so both
// categories land in one place.
const SOURCE_KINDS_BY_TAB: Record<Tab, FindingSourceKind[]> = {
  vulnerabilities: ['network_vuln'],
  compliance: ['bundle_compliance', 'network_compliance'],
};

// Severity sorts by risk rank, not alphabetically (critical > … > info).
const SEV_RANK: Record<string, number> = { critical: 5, high: 4, medium: 3, low: 2, info: 1 };
const sevRank = (s?: string) => (s ? SEV_RANK[s.toLowerCase()] ?? 0 : 0);

// Valid values for the URL-backed severity filter (guards ?severity= input).
const SEVERITY_VALUES = ['critical', 'high', 'medium', 'low', 'info'];

function SeverityBadge({ severity }: { severity?: string }) {
  if (!severity) return <span className="muted">—</span>;
  return <span className={`badge badge-sev-${severity.toLowerCase()}`}>{severity}</span>;
}

function StatusBadge({ status }: { status: FindingStatus }) {
  return <span className={`badge badge-${status}`}>{status}</span>;
}

function assetLabel(f: Finding): string {
  const host = f.asset_hostname || f.asset_ip || f.asset_endpoint_id.slice(0, 8) + '…';
  return f.endpoint_port != null ? `${host}:${f.endpoint_port}` : host;
}

export default function Findings() {
  const queryClient = useQueryClient();
  const [tab, setTab] = useState<Tab>('vulnerabilities');

  // Severity is URL-backed (source of truth) so Dashboard deep-links like
  // /findings?severity=high apply the filter on mount, and changing the select
  // stays shareable. status/collection/date stay local. (Mirrors Assets.tsx.)
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSeverity = searchParams.get('severity') ?? '';
  const severity = SEVERITY_VALUES.includes(rawSeverity) ? rawSeverity : '';
  const setSeverity = (v: string) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      if (v) next.set('severity', v);
      else next.delete('severity');
      return next;
    }, { replace: true });
  };
  const [status, setStatus] = useState<FindingStatus | ''>('open');
  const [collectionId, setCollectionId] = useState('');
  const [since, setSince] = useState('');
  const [until, setUntil] = useState('');
  const [openFinding, setOpenFinding] = useState<Finding | null>(null);

  const { data: collections } = useQuery<Collection[]>({
    queryKey: ['collections', { scope: 'finding' }],
    queryFn: () => listCollections({ scope: 'finding' }),
  });

  const params = useMemo(() => ({
    source_kind: SOURCE_KINDS_BY_TAB[tab],
    severity: severity || undefined,
    status: (status || undefined) as FindingStatus | undefined,
    collection_id: collectionId || undefined,
    since: since || undefined,
    until: until || undefined,
  }), [tab, severity, status, collectionId, since, until]);

  const { data: findings, isLoading, error } = useQuery<Finding[]>({
    queryKey: ['findings', params],
    queryFn: () => listFindings(params),
  });

  const suppressMut = useMutation({
    mutationFn: (id: string) => suppressFinding(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['findings'] });
      queryClient.invalidateQueries({ queryKey: ['finding'] }); // refresh an open drawer
    },
  });

  const reopenMut = useMutation({
    mutationFn: (id: string) => reopenFinding(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['findings'] });
      queryClient.invalidateQueries({ queryKey: ['finding'] });
    },
  });

  // Column defs are inline (cheap; data is the stable react-query result).
  // Filtering stays server-side (the existing query params); TanStack adds the
  // client-side column sorting the old table lacked.
  const columns: ColumnDef<Finding>[] = [
    {
      id: 'severity',
      header: 'Severity',
      accessorFn: (f) => f.severity ?? '',
      cell: ({ row }) => <SeverityBadge severity={row.original.severity} />,
      sortingFn: (a, b) => sevRank(a.original.severity) - sevRank(b.original.severity),
    },
    { id: 'title', header: 'Title', accessorKey: 'title' },
    {
      id: 'source',
      header: 'Source',
      enableSorting: false,
      cell: ({ row }) => {
        const f = row.original;
        return <span title={f.source_kind}>{f.source}{f.source_id ? ` / ${f.source_id}` : ''}</span>;
      },
    },
    { id: 'asset', header: 'Asset:Port', enableSorting: false, accessorFn: (f) => assetLabel(f) },
    {
      id: 'status',
      header: 'Status',
      accessorKey: 'status',
      cell: ({ row }) => <StatusBadge status={row.original.status} />,
    },
    {
      id: 'last_seen',
      header: 'Last Seen',
      accessorFn: (f) => f.last_seen,
      cell: ({ row }) => new Date(row.original.last_seen).toLocaleString(),
    },
    {
      id: 'actions',
      header: '',
      enableSorting: false,
      cell: ({ row }) => {
        const f = row.original;
        return (
          <div style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
            {f.status === 'open' && (
              <button
                className="btn btn-sm"
                onClick={(e) => { e.stopPropagation(); suppressMut.mutate(f.id); }}
                disabled={suppressMut.isPending}
              >
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--ss-space-xs)' }}>
                  <EyeOff size={14} /> Suppress
                </span>
              </button>
            )}
            {f.status === 'suppressed' && (
              <button
                className="btn btn-sm"
                onClick={(e) => { e.stopPropagation(); reopenMut.mutate(f.id); }}
                disabled={reopenMut.isPending}
              >
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--ss-space-xs)' }}>
                  <Eye size={14} /> Reopen
                </span>
              </button>
            )}
          </div>
        );
      },
    },
  ];

  return (
    <div>
      <div className="page-header" style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
        <ShieldAlert size={24} style={{ color: 'var(--ss-accent-primary)' }} />
        <h1 style={{ margin: 0 }}>Findings</h1>
      </div>

      <div
        className="tabbar"
        style={{
          display: 'flex',
          gap: 'var(--ss-space-lg)',
          borderBottom: '1px solid var(--ss-border-subtle)',
          marginBottom: 'var(--ss-space-lg)',
        }}
      >
        {TABS.map((t) => (
          <button
            key={t.value}
            onClick={() => setTab(t.value)}
            style={{
              background: 'none',
              border: 'none',
              padding: 'var(--ss-space-sm) var(--ss-space-xs)',
              cursor: 'pointer',
              fontWeight: tab === t.value ? 600 : 400,
              color: tab === t.value ? 'var(--ss-text-primary)' : 'var(--ss-text-secondary)',
              borderBottom: tab === t.value ? '2px solid var(--ss-accent-hover)' : '2px solid transparent',
              marginBottom: -1,
              transition: 'color var(--ss-transition-fast)',
            }}
          >
            {t.label}
          </button>
        ))}
      </div>

      <div className="form-card" style={{ display: 'flex', gap: 'var(--ss-space-md)', flexWrap: 'wrap' }}>
        <div>
          <label htmlFor="f-sev" style={{ display: 'block', fontSize: 12 }}>Severity</label>
          <select id="f-sev" value={severity} onChange={(e) => setSeverity(e.target.value)}>
            <option value="">All</option>
            <option value="critical">critical</option>
            <option value="high">high</option>
            <option value="medium">medium</option>
            <option value="low">low</option>
            <option value="info">info</option>
          </select>
        </div>
        <div>
          <label htmlFor="f-status" style={{ display: 'block', fontSize: 12 }}>Status</label>
          <select id="f-status" value={status} onChange={(e) => setStatus(e.target.value as FindingStatus | '')}>
            <option value="">All</option>
            <option value="open">open</option>
            <option value="resolved">resolved</option>
            <option value="suppressed">suppressed</option>
          </select>
        </div>
        <div>
          <label htmlFor="f-coll" style={{ display: 'block', fontSize: 12 }}>Collection</label>
          <select id="f-coll" value={collectionId} onChange={(e) => setCollectionId(e.target.value)}>
            <option value="">All</option>
            {collections?.map((c) => (
              <option key={c.id} value={c.id}>{c.name}</option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="f-since" style={{ display: 'block', fontSize: 12 }}>Since</label>
          <input id="f-since" type="datetime-local" value={since} onChange={(e) => setSince(e.target.value)} />
        </div>
        <div>
          <label htmlFor="f-until" style={{ display: 'block', fontSize: 12 }}>Until</label>
          <input id="f-until" type="datetime-local" value={until} onChange={(e) => setUntil(e.target.value)} />
        </div>
      </div>

      {isLoading && <p>Loading…</p>}
      {error && <p className="error">Failed to load: {(error as Error).message}</p>}
      {!isLoading && findings && findings.length === 0 && (
        <div style={{ textAlign: 'center', padding: 'var(--ss-space-3xl)', color: 'var(--ss-text-muted)' }}>
          <ShieldCheck size={40} style={{ color: 'var(--ss-success)' }} />
          <p style={{ marginTop: 'var(--ss-space-sm)' }}>No findings match these filters.</p>
        </div>
      )}

      {findings && findings.length > 0 && (
        <DataTable
          columns={columns}
          data={findings}
          getRowId={(f) => f.id}
          onRowClick={(f) => setOpenFinding(f)}
          initialSorting={[{ id: 'severity', desc: true }]}
        />
      )}

      {openFinding && (
        <FindingDetailDrawer
          finding={openFinding}
          onClose={() => setOpenFinding(null)}
          onSuppress={() => suppressMut.mutate(openFinding.id)}
          onReopen={() => reopenMut.mutate(openFinding.id)}
          suppressPending={suppressMut.isPending}
          reopenPending={reopenMut.isPending}
        />
      )}
    </div>
  );
}

function Field({ label, value, mono }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <div style={{ display: 'flex', gap: 'var(--ss-space-md)', padding: 'var(--ss-space-xs) 0' }}>
      <span className="muted" style={{ minWidth: 96, fontSize: 'var(--ss-text-body-sm)' }}>{label}</span>
      <span style={{ fontFamily: mono ? 'monospace' : undefined, fontSize: 'var(--ss-text-body-sm)', wordBreak: 'break-word' }}>
        {value}
      </span>
    </div>
  );
}

// Right-side detail drawer (reuses the shipped .drawer-* shell + CodeBlock).
// Seeds from the clicked row for instant render, then fetches the full finding
// via GET /findings/{id} so evidence/remediation (detail-only fields) are
// present. Missing fields (no CVE / no evidence / no remediation) are omitted.
function FindingDetailDrawer({
  finding,
  onClose,
  onSuppress,
  onReopen,
  suppressPending,
  reopenPending,
}: {
  finding: Finding;
  onClose: () => void;
  onSuppress: () => void;
  onReopen: () => void;
  suppressPending: boolean;
  reopenPending: boolean;
}) {
  const { data, isFetching } = useQuery<Finding>({
    queryKey: ['finding', finding.id],
    queryFn: () => getFinding(finding.id),
    initialData: finding,
  });
  const f = data ?? finding;
  const evidence = f.evidence && Object.keys(f.evidence).length > 0 ? f.evidence : null;

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-label="Finding detail">
        <header className="drawer-header">
          <h2 style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
            <SeverityBadge severity={f.severity} />
            <span>{f.title}</span>
          </h2>
          <button type="button" className="btn btn-sm" onClick={onClose}>×</button>
        </header>
        <div className="drawer-body">
          <section>
            <h3>General</h3>
            <Field label="Severity" value={f.severity || '—'} />
            <Field label="Status" value={<StatusBadge status={f.status} />} />
            <Field label="Asset:Port" value={assetLabel(f)} mono />
            <Field label="Source" value={`${f.source}${f.source_kind ? ` · ${f.source_kind}` : ''}`} />
            {f.cve_id && <Field label="CVE" value={f.cve_id} mono />}
            <Field label="First seen" value={new Date(f.first_seen).toLocaleString()} />
            <Field label="Last seen" value={new Date(f.last_seen).toLocaleString()} />
            {isFetching && <p className="muted" style={{ fontSize: 'var(--ss-text-body-sm)' }}>Loading details…</p>}
          </section>

          {evidence && (
            <section>
              <h3>Evidence</h3>
              <CodeBlock content={JSON.stringify(evidence, null, 2)} />
            </section>
          )}

          {f.remediation && (
            <section>
              <h3>Remediation</h3>
              <p style={{ whiteSpace: 'pre-wrap' }}>{f.remediation}</p>
            </section>
          )}

          <section>
            <h3>Actions</h3>
            {f.status === 'open' && (
              <button className="btn btn-sm" onClick={onSuppress} disabled={suppressPending}>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--ss-space-xs)' }}>
                  <EyeOff size={14} /> Suppress
                </span>
              </button>
            )}
            {f.status === 'suppressed' && (
              <button className="btn btn-sm" onClick={onReopen} disabled={reopenPending}>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--ss-space-xs)' }}>
                  <Eye size={14} /> Reopen
                </span>
              </button>
            )}
            {f.status === 'resolved' && <span className="muted">Resolved.</span>}
          </section>
        </div>
      </aside>
    </>
  );
}
