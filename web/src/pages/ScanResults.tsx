import { useState } from 'react';
import { useParams, Link } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { ClipboardCheck, SearchX } from 'lucide-react';
import { getScan, listFindings, getScanFacts, replayEvaluation } from '../api/client';
import type { CollectedFactsEntry } from '../api/client';
import type { Scan, ScanResult, Finding } from '../api/types';
import AgentLogConsole from '../components/AgentLogConsole';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';

type ResultsTab = 'overview' | 'findings' | 'facts' | 'console';

function StatusBadge({ status }: { status: string }) {
  return <span className={`badge badge-${status}`}>{status}</span>;
}

// Control status / summary-segment colors live as CSS classes (badge-control-*,
// segment-*) in index.css — they're class-based here, so there are no inline
// colors to sweep for those; the classes are preserved as-is.
function ControlStatusBadge({ status }: { status: string }) {
  return <span className={`badge badge-control-${status}`}>{status}</span>;
}

function SummaryBar({ scan }: { scan: Scan }) {
  const summary = scan.summary;
  if (!summary || summary.total === 0) return null;

  const segments = [
    { label: 'Pass', count: summary.pass, className: 'segment-pass' },
    { label: 'Fail', count: summary.fail, className: 'segment-fail' },
    { label: 'Error', count: summary.error, className: 'segment-error' },
    { label: 'N/A', count: summary.not_applicable, className: 'segment-na' },
  ];

  return (
    <div className="summary-section">
      <div className="summary-bar">
        {segments.map((seg) =>
          seg.count > 0 ? (
            <div
              key={seg.label}
              className={`summary-segment ${seg.className}`}
              style={{ flex: seg.count }}
              title={`${seg.label}: ${seg.count}`}
            />
          ) : null,
        )}
      </div>
      <div className="summary-labels">
        {segments.map((seg) => (
          <span key={seg.label} className="summary-label">
            {seg.label}: {seg.count}
          </span>
        ))}
        <span className="summary-label">Total: {summary.total}</span>
      </div>
    </div>
  );
}

// Per-control detail (evidence / remediation). The legacy table rendered this
// as a sibling expanded <tr>; DataTable owns its <tbody> (no row-expand API),
// so it renders below the table for the single active control — same family as
// the Bundles ControlsPanel substitute.
function ResultDetail({ result }: { result: ScanResult }) {
  return (
    <div className="expanded-row" style={{ marginTop: 'var(--ss-space-md)' }}>
      {result.evidence && (
        <div className="result-detail">
          <strong>Evidence:</strong>
          <pre>{JSON.stringify(result.evidence, null, 2)}</pre>
        </div>
      )}
      {result.remediation && (
        <div className="result-detail">
          <strong>Remediation:</strong>
          <p>{result.remediation}</p>
        </div>
      )}
      {!result.evidence && !result.remediation && (
        <p className="text-muted">No additional details.</p>
      )}
    </div>
  );
}

function FindingsTab({ scanId }: { scanId: string }) {
  const { data, isLoading, error } = useQuery<Finding[]>({
    queryKey: ['findings', { scan_id: scanId }],
    queryFn: () => listFindings({ scan_id: scanId }),
  });

  const columns: ColumnDef<Finding>[] = [
    {
      id: 'severity',
      header: 'Severity',
      accessorFn: (f) => f.severity ?? '',
      cell: ({ row }) => (
        <span className={`badge badge-sev-${(row.original.severity ?? '').toLowerCase()}`}>
          {row.original.severity ?? '—'}
        </span>
      ),
    },
    { id: 'title', header: 'Title', accessorFn: (f) => f.title },
    {
      id: 'source',
      header: 'Source',
      enableSorting: false,
      cell: ({ row }) => (
        <span title={row.original.source_kind}>
          {row.original.source}{row.original.source_id ? ` / ${row.original.source_id}` : ''}
        </span>
      ),
    },
    {
      id: 'endpoint',
      header: 'Endpoint',
      enableSorting: false,
      cell: ({ row }) => {
        const f = row.original;
        const label = f.asset_hostname || f.asset_ip || f.asset_endpoint_id.slice(0, 8) + '…';
        return `${label}${f.endpoint_port != null ? `:${f.endpoint_port}` : ''}`;
      },
    },
    {
      id: 'status',
      header: 'Status',
      accessorFn: (f) => f.status,
      cell: ({ row }) => <span className={`badge badge-${row.original.status}`}>{row.original.status}</span>,
    },
    {
      id: 'last_seen',
      header: 'Last Seen',
      accessorFn: (f) => f.last_seen,
      cell: ({ row }) => new Date(row.original.last_seen).toLocaleString(),
    },
  ];

  if (isLoading) return <p>Loading findings…</p>;
  if (error) return <p className="error">Failed to load findings: {(error as Error).message}</p>;
  if (!data || data.length === 0) {
    return <EmptyState icon={<SearchX />} title="No findings for this scan." />;
  }

  return (
    <DataTable
      columns={columns}
      data={data}
      getRowId={(f) => f.id}
      initialSorting={[{ id: 'severity', desc: false }]}
    />
  );
}

export default function ScanResults() {
  const { id } = useParams<{ id: string }>();
  const [tab, setTab] = useState<ResultsTab>('overview');

  const { data: scan, isLoading, error } = useQuery<Scan>({
    queryKey: ['scan', id],
    queryFn: () => getScan(id!),
    enabled: !!id,
    refetchInterval: (query) => {
      const data = query.state.data as Scan | undefined;
      if (data?.status === 'running' || data?.status === 'pending' || data?.status === 'queued') {
        return 5000;
      }
      return false;
    },
  });

  if (isLoading) return <p>Loading...</p>;
  if (error) return <p className="error">Failed to load scan: {(error as Error).message}</p>;
  if (!scan) return <p>Scan not found.</p>;

  return (
    <div>
      <Link to="/scans" className="back-link">
        Back to Scans
      </Link>

      <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
        <ClipboardCheck size={24} style={{ color: 'var(--ss-accent-primary)' }} />
        <h1 style={{ margin: 0 }}>Scan Results</h1>
      </div>

      <div
        className="tabbar"
        style={{
          display: 'flex',
          gap: 'var(--ss-space-lg)',
          borderBottom: '1px solid var(--ss-border-default)',
          marginBottom: 'var(--ss-space-lg)',
          marginTop: 'var(--ss-space-lg)',
        }}
      >
        {(['overview', 'findings', 'facts', 'console'] as ResultsTab[]).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            style={{
              background: 'none',
              border: 'none',
              padding: 'var(--ss-space-sm) var(--ss-space-xs)',
              cursor: 'pointer',
              fontWeight: tab === t ? 600 : 400,
              borderBottom: tab === t ? '2px solid var(--ss-accent-primary)' : '2px solid transparent',
              marginBottom: -1,
              textTransform: 'capitalize',
            }}
          >
            {t}
          </button>
        ))}
      </div>

      {tab === 'findings' && id && <FindingsTab scanId={id} />}
      {tab === 'overview' && <ScanOverview scan={scan} /> }
      {tab === 'facts' && id && <FactsTab scanId={id} />}
      {tab === 'console' && id && (
        <div className="scan-console-wrap">
          <AgentLogConsole filter={{ scanId: id }} />
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Facts tab — collected facts from ADR 011 collector pipeline
// ---------------------------------------------------------------------------

function FactsTab({ scanId }: { scanId: string }) {
  const { data, isLoading, error } = useQuery<{ items: CollectedFactsEntry[] }>({
    queryKey: ['scan-facts', scanId],
    queryFn: () => getScanFacts(scanId),
  });

  if (isLoading) return <p>Loading facts...</p>;
  if (error) return <p className="error">Failed to load facts: {(error as Error).message}</p>;

  const items = data?.items ?? [];

  if (items.length === 0) {
    return (
      <div className="detail-card" style={{ marginTop: 'var(--ss-space-md)' }}>
        <p className="muted">
          No facts collected — this scan used the legacy bundle runner.
        </p>
      </div>
    );
  }

  return (
    <div>
      {items.map((entry) => (
        <CollectorFactsSection key={entry.collector_id} entry={entry} />
      ))}
    </div>
  );
}

function CollectorFactsSection({ entry }: { entry: CollectedFactsEntry }) {
  const [collapsed, setCollapsed] = useState(false);
  return (
    <div className="detail-card" style={{ marginBottom: 'var(--ss-space-lg)' }}>
      <div
        style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', cursor: 'pointer' }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <div>
          <strong style={{ fontFamily: 'monospace', fontSize: 14 }}>{entry.collector_id}</strong>
          <span className="muted" style={{ marginLeft: 'var(--ss-space-md)', fontSize: 12 }}>
            {new Date(entry.collected_at).toLocaleString()}
          </span>
        </div>
        <span style={{ fontWeight: 600, fontSize: 16 }}>{collapsed ? '+' : '−'}</span>
      </div>
      {!collapsed && (
        <pre style={{
          // Dark code block — no dark-surface token exists, kept literal.
          background: '#1e293b',
          color: '#e2e8f0',
          padding: 'var(--ss-space-lg)',
          borderRadius: 'var(--ss-radius-md)',
          marginTop: 'var(--ss-space-md)',
          marginBottom: 0,
          overflow: 'auto',
          maxHeight: 500,
          fontSize: 13,
          lineHeight: 1.5,
        }}>
          {JSON.stringify(entry.facts, null, 2)}
        </pre>
      )}
    </div>
  );
}

function ReEvaluateButton({ scanId }: { scanId: string }) {
  const queryClient = useQueryClient();
  const [toast, setToast] = useState<string | null>(null);

  const mutation = useMutation({
    mutationFn: () => replayEvaluation({ scan_id: scanId }),
    onSuccess: (data) => {
      setToast(
        `Re-evaluation complete: ${data.findings_created} created, ${data.findings_updated} updated`,
      );
      void queryClient.invalidateQueries({ queryKey: ['findings'] });
      void queryClient.invalidateQueries({ queryKey: ['scan', scanId] });
      setTimeout(() => setToast(null), 5000);
    },
    onError: (err: Error) => {
      setToast(`Re-evaluation failed: ${err.message}`);
      setTimeout(() => setToast(null), 5000);
    },
  });

  return (
    <div style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
      <button
        className="btn btn-secondary"
        disabled={mutation.isPending}
        onClick={() => mutation.mutate()}
        title="Re-evaluate stored facts against current policies"
      >
        {mutation.isPending ? 'Re-evaluating...' : 'Re-evaluate'}
      </button>
      {toast && (
        <span
          style={{
            padding: '4px 10px',
            borderRadius: 'var(--ss-radius-sm)',
            fontSize: 13,
            // Success/danger toast tints — no success-bg/danger-bg/-text token
            // exists, so these stay literal (same call as Credentials).
            background: mutation.isError ? '#fef2f2' : '#f0fdf4',
            color: mutation.isError ? '#991b1b' : '#166534',
          }}
        >
          {toast}
        </span>
      )}
    </div>
  );
}

function ScanOverview({ scan }: { scan: Scan }) {
  const [activeResultId, setActiveResultId] = useState<string | null>(null);

  const columns: ColumnDef<ScanResult>[] = [
    { id: 'control_id', header: 'Control ID', accessorFn: (r) => r.control_id },
    { id: 'title', header: 'Title', accessorFn: (r) => r.title },
    {
      id: 'status',
      header: 'Status',
      accessorFn: (r) => r.status,
      cell: ({ row }) => <ControlStatusBadge status={row.original.status} />,
    },
    {
      id: 'severity',
      header: 'Severity',
      accessorFn: (r) => r.severity ?? '',
      cell: ({ row }) => row.original.severity || '-',
    },
    {
      id: 'indicator',
      header: '',
      enableSorting: false,
      cell: ({ row }) => (
        <span className="expand-indicator">{activeResultId === row.original.id ? '-' : '+'}</span>
      ),
    },
  ];

  const activeResult = scan.results?.find((r) => r.id === activeResultId) ?? null;

  return (
    <div>

      <div className="scan-meta">
        <div>
          <strong>Status:</strong> <StatusBadge status={scan.status} />
        </div>
        <div>
          <strong>Target:</strong> {scan.target_id}
        </div>
        <div>
          <strong>Bundle:</strong> {scan.bundle_id}
        </div>
        <div>
          <strong>Created:</strong> {new Date(scan.created_at).toLocaleString()}
        </div>
        {scan.started_at && (
          <div>
            <strong>Started:</strong> {new Date(scan.started_at).toLocaleString()}
          </div>
        )}
        {scan.completed_at && (
          <div>
            <strong>Completed:</strong> {new Date(scan.completed_at).toLocaleString()}
          </div>
        )}
      </div>

      {scan.status === 'failed' && scan.error_message && (
        <div className="detail-card" style={{ borderColor: 'var(--ss-danger)', marginTop: 'var(--ss-space-md)' }}>
          <strong>Failure reason:</strong>
          <pre style={{ whiteSpace: 'pre-wrap', marginTop: 'var(--ss-space-xs)', marginBottom: 0 }}>
            {scan.error_message}
          </pre>
        </div>
      )}

      <SummaryBar scan={scan} />

      {(scan.status === 'completed' || scan.status === 'failed') && (
        <div style={{ margin: 'var(--ss-space-md) 0' }}>
          <ReEvaluateButton scanId={scan.id} />
        </div>
      )}

      {scan.results && scan.results.length > 0 ? (
        <>
          <DataTable
            columns={columns}
            data={scan.results}
            getRowId={(r) => r.id}
            onRowClick={(r) => setActiveResultId((cur) => (cur === r.id ? null : r.id))}
            initialSorting={[{ id: 'control_id', desc: false }]}
          />
          {activeResult && <ResultDetail result={activeResult} />}
        </>
      ) : (
        <p>
          {scan.status === 'queued'
            ? 'Scan queued — waiting for agent...'
            : scan.status === 'pending' || scan.status === 'running'
              ? 'Scan in progress...'
              : 'No results.'}
        </p>
      )}
    </div>
  );
}
