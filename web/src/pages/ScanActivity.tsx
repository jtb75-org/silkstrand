import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Activity, SearchX } from 'lucide-react';
import { listScans, listScanDefinitions } from '../api/client';
import type { Scan, ScanDefinition } from '../api/types';
import ScanActivityDrawer from '../components/ScanActivityDrawer';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';

function StatusBadge({ status }: { status: string }) {
  return <span className={`badge badge-${status}`}>{status}</span>;
}

// ScanActivity is the read-only sibling of ScanDefinitions. It surfaces
// the `scans` feed — both scheduled and ad-hoc — filterable by
// scan_definition_id / status / date range per `docs/plans/ui-shape.md`.
export default function ScanActivity() {
  const [openScanId, setOpenScanId] = useState<string | null>(null);
  const [defId, setDefId] = useState('');
  const [status, setStatus] = useState('');
  const [since, setSince] = useState('');
  const [until, setUntil] = useState('');

  const { data: scans, isLoading, error } = useQuery<Scan[]>({
    queryKey: ['scans'],
    queryFn: listScans,
    refetchInterval: (query) => {
      const data = query.state.data as Scan[] | undefined;
      if (data?.some((s) => s.status === 'pending' || s.status === 'running' || s.status === 'queued')) {
        return 5000;
      }
      return false;
    },
  });

  const { data: defs } = useQuery<ScanDefinition[]>({
    queryKey: ['scan-definitions'],
    queryFn: listScanDefinitions,
  });

  // Client-side filtering keeps the page simple until the /scans endpoint
  // grows the matching query params. The filter shape here is deliberately
  // the same as what the server will accept so we can swap later.
  const filtered = useMemo(() => {
    if (!scans) return [];
    const sinceDate = since ? new Date(since) : null;
    const untilDate = until ? new Date(until) : null;
    type ScanWithDef = Scan & { scan_definition_id?: string };
    return scans.filter((s) => {
      const withDef = s as ScanWithDef;
      if (defId && withDef.scan_definition_id !== defId) return false;
      if (status && s.status !== status) return false;
      const created = new Date(s.created_at);
      if (sinceDate && created < sinceDate) return false;
      if (untilDate && created > untilDate) return false;
      return true;
    });
  }, [scans, defId, status, since, until]);

  const columns: ColumnDef<Scan>[] = [
    {
      id: 'status',
      header: 'Status',
      accessorFn: (s) => s.status,
      cell: ({ row }) => <StatusBadge status={row.original.status} />,
    },
    { id: 'type', header: 'Type', accessorFn: (s) => s.scan_type ?? 'compliance' },
    {
      id: 'target',
      header: 'Target',
      accessorFn: (s) => s.target_id ?? '',
      cell: ({ row }) => (row.original.target_id ? `${row.original.target_id.slice(0, 8)}…` : '—'),
    },
    {
      id: 'bundle',
      header: 'Bundle',
      accessorFn: (s) => s.bundle_id ?? '',
      cell: ({ row }) => (row.original.bundle_id ? `${row.original.bundle_id.slice(0, 8)}…` : '—'),
    },
    {
      // Sort chronologically on the raw ISO ts (lexicographic == chronological).
      id: 'created',
      header: 'Created',
      accessorFn: (s) => s.created_at,
      cell: ({ row }) => new Date(row.original.created_at).toLocaleString(),
    },
    {
      id: 'completed',
      header: 'Completed',
      accessorFn: (s) => s.completed_at ?? '',
      cell: ({ row }) => (row.original.completed_at ? new Date(row.original.completed_at).toLocaleString() : '—'),
    },
  ];

  return (
    <div>
      <div className="page-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
          <Activity size={22} style={{ color: 'var(--ss-accent-primary)' }} />
          <h2 style={{ margin: 0 }}>Scan Activity</h2>
        </div>
      </div>

      <div className="form-card" style={{ display: 'flex', gap: 'var(--ss-space-md)', flexWrap: 'wrap' }}>
        <div>
          <label htmlFor="sa-def" style={{ display: 'block', fontSize: 12 }}>Definition</label>
          <select id="sa-def" value={defId} onChange={(e) => setDefId(e.target.value)}>
            <option value="">All</option>
            {defs?.map((d) => (
              <option key={d.id} value={d.id}>{d.name}</option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="sa-status" style={{ display: 'block', fontSize: 12 }}>Status</label>
          <select id="sa-status" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">All</option>
            <option value="queued">queued</option>
            <option value="pending">pending</option>
            <option value="running">running</option>
            <option value="completed">completed</option>
            <option value="failed">failed</option>
          </select>
        </div>
        <div>
          <label htmlFor="sa-since" style={{ display: 'block', fontSize: 12 }}>Since</label>
          <input
            id="sa-since"
            type="datetime-local"
            value={since}
            onChange={(e) => setSince(e.target.value)}
          />
        </div>
        <div>
          <label htmlFor="sa-until" style={{ display: 'block', fontSize: 12 }}>Until</label>
          <input
            id="sa-until"
            type="datetime-local"
            value={until}
            onChange={(e) => setUntil(e.target.value)}
          />
        </div>
      </div>

      {isLoading && <p>Loading…</p>}
      {error && <p className="error">Failed to load: {(error as Error).message}</p>}
      {!isLoading && !error && filtered.length === 0 && (
        <EmptyState icon={<SearchX />} title="No scans match." />
      )}

      {filtered.length > 0 && (
        <DataTable
          columns={columns}
          data={filtered}
          getRowId={(s) => s.id}
          onRowClick={(s) => setOpenScanId(s.id)}
          initialSorting={[{ id: 'created', desc: true }]}
        />
      )}

      {openScanId && (
        <ScanActivityDrawer scanId={openScanId} onClose={() => setOpenScanId(null)} />
      )}
    </div>
  );
}
