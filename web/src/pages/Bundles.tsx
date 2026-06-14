import { useState, useRef } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Package } from 'lucide-react';
import { useToast } from '../lib/toast';
import { listBundles, getBundleControls, uploadBundle } from '../api/client';
import type { Bundle, BundleControl } from '../api/types';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';

// The global discovery bundle (fixed UUID) carries no CIS controls and isn't
// user-managed, so it stays hidden from the compliance-bundle list.
const DISCOVERY_BUNDLE_ID = '11111111-1111-1111-1111-111111111111';

export default function Bundles() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  const [showUpload, setShowUpload] = useState(false);
  const [expandedId, setExpandedId] = useState<string | null>(null);

  const { data: bundles, isLoading, error } = useQuery<Bundle[]>({
    queryKey: ['bundles'],
    queryFn: listBundles,
  });

  const rows = (bundles ?? []).filter((b) => b.id !== DISCOVERY_BUNDLE_ID);

  // Action-only: the one per-row action toggles an inline controls panel.
  // DataTable owns its <tbody> (no row-expand API), so the panel renders below
  // the table for the single expanded bundle.
  const columns: ColumnDef<Bundle>[] = [
    { id: 'name', header: 'Name', accessorFn: (b) => b.name },
    { id: 'version', header: 'Version', accessorFn: (b) => b.version },
    {
      id: 'engine',
      header: 'Engine',
      accessorFn: (b) => b.engine ?? b.target_type ?? '—',
      cell: ({ row }) => (
        <span className="badge badge-type">{row.original.engine ?? row.original.target_type ?? '—'}</span>
      ),
    },
    {
      id: 'controls',
      header: 'Controls',
      accessorFn: (b) => b.control_count ?? 0,
      cell: ({ row }) => {
        const n = row.original.control_count ?? 0;
        return n > 0 ? `${n} controls` : '—';
      },
    },
    {
      id: 'actions',
      header: '',
      enableSorting: false,
      cell: ({ row }) => {
        const b = row.original;
        const isOpen = expandedId === b.id;
        return (
          <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
            <button className="btn btn-sm" onClick={() => setExpandedId(isOpen ? null : b.id)}>
              {isOpen ? 'Hide controls' : 'View controls'}
            </button>
          </div>
        );
      },
    },
  ];

  const expandedBundle = expandedId ? rows.find((b) => b.id === expandedId) ?? null : null;

  return (
    <div>
      <div className="page-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
          <Package size={22} style={{ color: 'var(--ss-accent-primary)' }} />
          <h2 style={{ margin: 0 }}>Bundles</h2>
        </div>
        <button
          className="btn btn-primary"
          onClick={() => setShowUpload(!showUpload)}
        >
          {showUpload ? 'Cancel' : 'Upload bundle'}
        </button>
      </div>

      {showUpload && (
        <UploadModal
          onSuccess={() => {
            queryClient.invalidateQueries({ queryKey: ['bundles'] });
            setShowUpload(false);
            toast('Bundle uploaded', 'success');
          }}
          onCancel={() => setShowUpload(false)}
        />
      )}

      {isLoading && <p>Loading...</p>}
      {error && <p className="error">Failed to load bundles: {(error as Error).message}</p>}
      {!isLoading && !error && rows.length === 0 && (
        <EmptyState icon={<Package />} title="No bundles registered. Upload a bundle to get started." />
      )}

      {rows.length > 0 && (
        <DataTable
          columns={columns}
          data={rows}
          getRowId={(b) => b.id}
          initialSorting={[{ id: 'name', desc: false }]}
        />
      )}

      {expandedBundle && (
        <div style={{ marginTop: 'var(--ss-space-md)' }}>
          <ControlsPanel bundleId={expandedBundle.id} />
        </div>
      )}
    </div>
  );
}

function ControlsPanel({ bundleId }: { bundleId: string }) {
  const { data: controls, isLoading, error } = useQuery<BundleControl[]>({
    queryKey: ['bundle-controls', bundleId],
    queryFn: () => getBundleControls(bundleId),
  });

  if (isLoading) return <div style={{ padding: 'var(--ss-space-lg)' }}>Loading controls...</div>;
  if (error) return <div style={{ padding: 'var(--ss-space-lg)' }} className="error">Failed to load controls: {(error as Error).message}</div>;
  if (!controls || controls.length === 0) return <div style={{ padding: 'var(--ss-space-lg)' }} className="muted">No controls registered for this bundle.</div>;

  const columns: ColumnDef<BundleControl>[] = [
    {
      id: 'control_id',
      header: 'Control ID',
      accessorFn: (c) => c.control_id,
      cell: ({ row }) => <span style={{ fontFamily: 'monospace', fontSize: 13 }}>{row.original.control_id}</span>,
    },
    { id: 'name', header: 'Name', accessorFn: (c) => c.name },
    {
      id: 'severity',
      header: 'Severity',
      accessorFn: (c) => c.severity ?? '',
      cell: ({ row }) =>
        row.original.severity ? <SeverityBadge severity={row.original.severity} /> : <span className="muted">—</span>,
    },
    { id: 'section', header: 'Section', accessorFn: (c) => c.section ?? '—' },
    { id: 'engine', header: 'Engine', accessorFn: (c) => c.engine },
    {
      id: 'versions',
      header: 'Versions',
      enableSorting: false,
      cell: ({ row }) => {
        const versions = Array.isArray(row.original.engine_versions) ? row.original.engine_versions : [];
        return versions.length > 0 ? versions.join(', ') : '—';
      },
    },
    {
      id: 'tags',
      header: 'Tags',
      enableSorting: false,
      cell: ({ row }) => {
        const tags = Array.isArray(row.original.tags) ? row.original.tags : [];
        return tags.length > 0 ? tags.join(', ') : '—';
      },
    },
  ];

  return (
    <DataTable
      columns={columns}
      data={controls}
      getRowId={(c) => c.control_id}
      initialSorting={[{ id: 'control_id', desc: false }]}
    />
  );
}

function SeverityBadge({ severity }: { severity: string }) {
  const s = severity.toLowerCase();
  let cls = 'badge';
  if (s === 'critical' || s === 'high') cls += ' badge-failed';
  else if (s === 'medium') cls += ' badge-warning';
  else if (s === 'low' || s === 'info') cls += ' badge-completed';
  return <span className={cls}>{severity}</span>;
}

function UploadModal({
  onSuccess,
  onCancel,
}: {
  onSuccess: () => void;
  onCancel: () => void;
}) {
  const tarballRef = useRef<HTMLInputElement>(null);
  const signatureRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setErrorMsg(null);
    const tarball = tarballRef.current?.files?.[0];
    if (!tarball) {
      setErrorMsg('Select a tarball file.');
      return;
    }
    const signature = signatureRef.current?.files?.[0];
    setUploading(true);
    try {
      await uploadBundle(tarball, signature);
      onSuccess();
    } catch (err) {
      setErrorMsg((err as Error).message);
    } finally {
      setUploading(false);
    }
  }

  return (
    <div className="form-card" style={{ maxWidth: 520, marginBottom: 'var(--ss-space-xl)' }}>
      <h3 style={{ marginTop: 0 }}>Upload bundle</h3>
      <form onSubmit={handleSubmit}>
        <div className="form-group">
          <label htmlFor="bundle-tarball">Bundle tarball (.tar.gz)</label>
          <input
            id="bundle-tarball"
            type="file"
            accept=".tar.gz,.tgz"
            ref={tarballRef}
            required
          />
        </div>
        <div className="form-group">
          <label htmlFor="bundle-sig">Signature (.sig, optional)</label>
          <input
            id="bundle-sig"
            type="file"
            accept=".sig"
            ref={signatureRef}
          />
        </div>
        {errorMsg && <p className="error">{errorMsg}</p>}
        <div style={{ display: 'flex', gap: 'var(--ss-space-sm)', marginTop: 'var(--ss-space-md)' }}>
          <button
            type="submit"
            className="btn btn-primary"
            disabled={uploading}
          >
            {uploading ? 'Uploading...' : 'Upload'}
          </button>
          <button
            type="button"
            className="btn"
            onClick={onCancel}
            disabled={uploading}
          >
            Cancel
          </button>
        </div>
      </form>
    </div>
  );
}
