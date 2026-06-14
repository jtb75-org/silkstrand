import { useState, useRef, useMemo, useCallback, useEffect } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { useSearchParams } from 'react-router-dom';
import { useToast } from '../lib/toast';
import {
  listBundles, getBundleControls, uploadBundle, listControls,
  listComplianceProfiles, createComplianceProfile, deleteComplianceProfile,
  getProfileControls, setProfileControls, publishProfile,
  listTenantPolicies,
} from '../api/client';
import type { Bundle, BundleControl, ControlEntry } from '../api/types';
import type { ComplianceProfile, TenantPolicy } from '../api/client';
import type { ColumnDef } from '@tanstack/react-table';
import { ShieldCheck, Package } from 'lucide-react';
import ControlDetailDrawer from '../components/ControlDetailDrawer';
import FrameworkChip from '../components/FrameworkChip';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';

type Tab = 'frameworks' | 'controls' | 'profiles';

// The global discovery bundle (fixed UUID) isn't a CIS framework and stays
// hidden from the frameworks/profile surfaces.
const DISCOVERY_BUNDLE_ID = '11111111-1111-1111-1111-111111111111';

export default function Compliance() {
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = (searchParams.get('tab') as Tab) || 'frameworks';

  function setTab(t: Tab) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('tab', t);
      return next;
    });
  }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
        <ShieldCheck size={24} style={{ color: 'var(--ss-accent-primary)' }} />
        <h1 style={{ margin: 0 }}>Compliance</h1>
      </div>

      <div className="tab-bar" style={{ display: 'flex', gap: 'var(--ss-space-xs)', borderBottom: '1px solid var(--ss-border-default)', marginTop: 'var(--ss-space-lg)' }}>
        <TabButton active={tab === 'frameworks'} onClick={() => setTab('frameworks')}>Frameworks</TabButton>
        <TabButton active={tab === 'controls'} onClick={() => setTab('controls')}>Controls</TabButton>
        <TabButton active={tab === 'profiles'} onClick={() => setTab('profiles')}>Profiles</TabButton>
      </div>

      <div style={{ marginTop: 'var(--ss-space-xl)' }}>
        {tab === 'frameworks' && <FrameworksTab />}
        {tab === 'controls' && <ControlsTab />}
        {tab === 'profiles' && <ProfilesTab />}
      </div>
    </div>
  );
}

function TabButton({ active, children, onClick }: { active: boolean; children: React.ReactNode; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      style={{
        padding: 'var(--ss-space-sm) var(--ss-space-lg)',
        border: 'none',
        borderBottom: active ? '2px solid var(--ss-accent-primary)' : '2px solid transparent',
        background: 'none',
        fontWeight: active ? 600 : 400,
        cursor: 'pointer',
      }}
    >
      {children}
    </button>
  );
}

function FrameworksTab() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  const [showUpload, setShowUpload] = useState(false);
  const [expandedId, setExpandedId] = useState<string | null>(null);

  const { data: bundles, isLoading, error } = useQuery<Bundle[]>({
    queryKey: ['bundles'],
    queryFn: listBundles,
  });

  const rows = (bundles ?? []).filter((b) => b.id !== DISCOVERY_BUNDLE_ID);

  // Action-only: the one per-row action toggles an inline controls panel that
  // renders below the table (DataTable has no row-expand API) \u2014 same family as
  // the Bundles page ControlsPanel substitute.
  const columns: ColumnDef<Bundle>[] = [
    { id: 'name', header: 'Name', accessorFn: (b) => b.name },
    { id: 'version', header: 'Version', accessorFn: (b) => b.version },
    {
      id: 'engine',
      header: 'Engine',
      accessorFn: (b) => b.engine ?? b.target_type ?? '\u2014',
      cell: ({ row }) => (
        <span className="badge badge-type">{row.original.engine ?? row.original.target_type ?? '\u2014'}</span>
      ),
    },
    {
      id: 'controls',
      header: 'Controls',
      accessorFn: (b) => b.control_count ?? 0,
      cell: ({ row }) => {
        const n = row.original.control_count ?? 0;
        return n > 0 ? `${n} controls` : '\u2014';
      },
    },
    {
      id: 'hash',
      header: 'Hash',
      enableSorting: false,
      cell: ({ row }) => {
        const hash = row.original.tarball_hash;
        return hash ? (
          <span title={hash} style={{ fontFamily: 'monospace', fontSize: 12, cursor: 'help' }}>
            {hash.substring(0, 12) + '\u2026'}
          </span>
        ) : (
          <span className="muted">{'\u2014'}</span>
        );
      },
    },
    {
      id: 'actions',
      header: '',
      enableSorting: false,
      cell: ({ row }) => {
        const isOpen = expandedId === row.original.id;
        return (
          <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
            <button className="btn btn-sm" onClick={() => setExpandedId(isOpen ? null : row.original.id)}>
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
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 'var(--ss-space-lg)' }}>
        <h2 style={{ margin: 0 }}>Frameworks</h2>
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
        row.original.severity ? <SeverityBadge severity={row.original.severity} /> : <span className="muted">{'\u2014'}</span>,
    },
    { id: 'section', header: 'Section', accessorFn: (c) => c.section ?? '\u2014' },
    { id: 'engine', header: 'Engine', accessorFn: (c) => c.engine },
    {
      id: 'versions',
      header: 'Versions',
      enableSorting: false,
      cell: ({ row }) => {
        const versions = Array.isArray(row.original.engine_versions) ? row.original.engine_versions : [];
        return versions.length > 0 ? versions.join(', ') : '\u2014';
      },
    },
    {
      id: 'tags',
      header: 'Tags',
      enableSorting: false,
      cell: ({ row }) => {
        const tags = Array.isArray(row.original.tags) ? row.original.tags : [];
        return tags.length > 0 ? tags.join(', ') : '\u2014';
      },
    },
  ];

  if (isLoading) return <div style={{ padding: 'var(--ss-space-lg)' }}>Loading controls...</div>;
  if (error) return <div style={{ padding: 'var(--ss-space-lg)' }} className="error">Failed to load controls: {(error as Error).message}</div>;
  if (!controls || controls.length === 0) return <div style={{ padding: 'var(--ss-space-lg)' }} className="muted">No controls registered for this bundle.</div>;

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

function PolicyProvenanceBadge({
  controlId,
  policyMap,
}: {
  controlId: string;
  policyMap: Map<string, TenantPolicy>;
}) {
  const policy = policyMap.get(controlId);
  if (!policy) return null; // builtin — no badge needed
  // Provenance tints (blue = custom, purple = customized) have no design-system
  // token equivalents, so they stay literal.
  if (policy.origin === 'custom') {
    return (
      <span
        className="badge"
        style={{ background: '#dbeafe', color: '#1e40af', fontSize: 11, padding: '1px 6px' }}
      >
        Custom
      </span>
    );
  }
  // derived (copy-and-edit)
  return (
    <span
      className="badge"
      style={{ background: '#ede9fe', color: '#6d28d9', fontSize: 11, padding: '1px 6px' }}
    >
      Customized
    </span>
  );
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

// ---------------------------------------------------------------------------
// Controls tab — cross-framework control browser (Level 2B)
// ---------------------------------------------------------------------------

const SEVERITY_OPTIONS = ['critical', 'high', 'medium', 'low', 'info'] as const;

const ENGINE_OPTIONS = [
  'postgresql',
  'mssql',
  'mongodb',
  'mysql',
  'host',
  'cidr',
  'cloud',
] as const;

function ControlsTab() {
  const [searchParams, setSearchParams] = useSearchParams();

  // Read filter state from URL search params.
  const framework = searchParams.get('framework') || '';
  const engine = searchParams.get('engine') || '';
  const severity = searchParams.get('severity') || '';
  const tagFilter = searchParams.get('tag') || '';
  const searchText = searchParams.get('q') || '';

  const [selectedControl, setSelectedControl] = useState<ControlEntry | null>(null);

  function setFilter(key: string, value: string) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      if (value) next.set(key, value);
      else next.delete(key);
      // Ensure tab stays
      if (!next.has('tab')) next.set('tab', 'controls');
      return next;
    });
  }

  // Fetch bundles to populate the framework dropdown options.
  const { data: bundles } = useQuery<Bundle[]>({
    queryKey: ['bundles'],
    queryFn: listBundles,
  });

  // Tenant policy overrides — used for provenance badges.
  // The API may not exist yet (PR 6), so we treat errors as empty.
  const { data: tenantPolicies } = useQuery<TenantPolicy[]>({
    queryKey: ['tenant-policies'],
    queryFn: listTenantPolicies,
    retry: false,
  });

  const policyMap = useMemo(() => {
    const m = new Map<string, TenantPolicy>();
    if (tenantPolicies) {
      for (const p of tenantPolicies) {
        m.set(p.control_id, p);
      }
    }
    return m;
  }, [tenantPolicies]);

  const frameworkOptions = useMemo(() => {
    if (!bundles) return [];
    const names = new Set<string>();
    for (const b of bundles) {
      if (b.id !== DISCOVERY_BUNDLE_ID && b.framework) {
        names.add(b.framework);
      }
    }
    return Array.from(names).sort();
  }, [bundles]);

  // Fetch controls with applied filters.
  const { data, isLoading, error } = useQuery({
    queryKey: ['controls', { framework, engine, severity, tag: tagFilter, q: searchText }],
    queryFn: () =>
      listControls({
        framework: framework || undefined,
        engine: engine || undefined,
        severity: severity || undefined,
        tag: tagFilter || undefined,
        q: searchText || undefined,
      }),
  });

  const controls = data?.items ?? [];

  // Row-click opens the ControlDetailDrawer (the page's existing detail surface);
  // policy/frameworks/tags are composed/badge columns and stay non-sortable.
  const columns: ColumnDef<ControlEntry>[] = [
    {
      id: 'control_id',
      header: 'Control ID',
      accessorFn: (c) => c.control_id,
      cell: ({ row }) => <span style={{ fontFamily: 'monospace', fontSize: 13 }}>{row.original.control_id}</span>,
    },
    { id: 'name', header: 'Name', accessorFn: (c) => c.name },
    {
      id: 'policy',
      header: 'Policy',
      enableSorting: false,
      cell: ({ row }) => <PolicyProvenanceBadge controlId={row.original.control_id} policyMap={policyMap} />,
    },
    {
      id: 'severity',
      header: 'Severity',
      accessorFn: (c) => c.severity ?? '',
      cell: ({ row }) =>
        row.original.severity ? <SeverityBadge severity={row.original.severity} /> : <span className="muted">{'—'}</span>,
    },
    {
      id: 'engine',
      header: 'Engine',
      accessorFn: (c) => c.engine,
      cell: ({ row }) => {
        const versions = Array.isArray(row.original.engine_versions) ? row.original.engine_versions : [];
        return <span title={versions.length > 0 ? `Versions: ${versions.join(', ')}` : ''}>{row.original.engine}</span>;
      },
    },
    {
      id: 'frameworks',
      header: 'Frameworks',
      enableSorting: false,
      cell: ({ row }) =>
        row.original.frameworks.map((fw) => (
          <FrameworkChip key={`${fw.bundle_id}-${fw.section}`} bundleName={fw.bundle_name} section={fw.section} />
        )),
    },
    {
      id: 'tags',
      header: 'Tags',
      enableSorting: false,
      cell: ({ row }) => {
        const tags = Array.isArray(row.original.tags) ? row.original.tags : [];
        return tags.length > 0 ? (
          tags.map((t) => (
            <span key={t} className="badge" style={{ fontSize: 11, padding: '1px 6px', marginRight: 'var(--ss-space-xs)', opacity: 0.7 }}>
              {t}
            </span>
          ))
        ) : (
          <span className="muted">{'—'}</span>
        );
      },
    },
  ];

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 'var(--ss-space-lg)' }}>
        <h2 style={{ margin: 0 }}>Controls</h2>
        {data && <span className="muted">{data.total} control{data.total !== 1 ? 's' : ''}</span>}
      </div>

      {/* Filter bar */}
      <div style={{ display: 'flex', gap: 'var(--ss-space-sm)', flexWrap: 'wrap', marginBottom: 'var(--ss-space-lg)' }}>
        <select
          value={framework}
          onChange={(e) => setFilter('framework', e.target.value)}
          style={{ minWidth: 140 }}
        >
          <option value="">All frameworks</option>
          {frameworkOptions.map((f) => (
            <option key={f} value={f}>{f}</option>
          ))}
        </select>

        <select
          value={engine}
          onChange={(e) => setFilter('engine', e.target.value)}
          style={{ minWidth: 120 }}
        >
          <option value="">All engines</option>
          {ENGINE_OPTIONS.map((e) => (
            <option key={e} value={e}>{e}</option>
          ))}
        </select>

        <select
          value={severity}
          onChange={(e) => setFilter('severity', e.target.value)}
          style={{ minWidth: 110 }}
        >
          <option value="">All severities</option>
          {SEVERITY_OPTIONS.map((s) => (
            <option key={s} value={s}>{s}</option>
          ))}
        </select>

        <input
          type="text"
          placeholder="Tag..."
          value={tagFilter}
          onChange={(e) => setFilter('tag', e.target.value)}
          style={{ width: 120 }}
        />

        <input
          type="text"
          placeholder="Search control ID or name..."
          value={searchText}
          onChange={(e) => setFilter('q', e.target.value)}
          style={{ flex: 1, minWidth: 180 }}
        />
      </div>

      {isLoading && <p>Loading controls...</p>}
      {error && <p className="error">Failed to load controls: {(error as Error).message}</p>}
      {!isLoading && controls.length === 0 && (
        <p className="muted">No controls match the current filters.</p>
      )}

      {controls.length > 0 && (
        <DataTable
          columns={columns}
          data={controls}
          getRowId={(c) => c.control_id}
          onRowClick={(c) => setSelectedControl(c)}
          initialSorting={[{ id: 'control_id', desc: false }]}
        />
      )}

      {selectedControl && (
        <ControlDetailDrawer
          control={selectedControl}
          onClose={() => setSelectedControl(null)}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Profiles tab — custom compliance profiles (Level 3C)
// ---------------------------------------------------------------------------

function ProfilesTab() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  const [showCreate, setShowCreate] = useState(false);
  const [editingProfileId, setEditingProfileId] = useState<string | null>(null);
  const [publishConfirm, setPublishConfirm] = useState<ComplianceProfile | null>(null);
  const [deleteConfirm, setDeleteConfirm] = useState<ComplianceProfile | null>(null);

  const { data: profiles, isLoading, error } = useQuery<ComplianceProfile[]>({
    queryKey: ['compliance-profiles'],
    queryFn: listComplianceProfiles,
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => deleteComplianceProfile(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['compliance-profiles'] });
      toast('Profile deleted', 'success');
      setDeleteConfirm(null);
    },
    onError: (err: Error) => toast(err.message, 'error'),
  });

  const publishMut = useMutation({
    mutationFn: (id: string) => publishProfile(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['compliance-profiles'] });
      queryClient.invalidateQueries({ queryKey: ['bundles'] });
      toast('Profile published', 'success');
      setPublishConfirm(null);
    },
    onError: (err: Error) => {
      if (err.message.includes('501') || err.message.toLowerCase().includes('not implemented')) {
        toast('Bundle assembly not yet available — coming soon', 'info');
      } else {
        toast(err.message, 'error');
      }
      setPublishConfirm(null);
    },
  });

  // If we're editing a profile, show the control picker full-page style.
  if (editingProfileId) {
    return (
      <ControlPicker
        profileId={editingProfileId}
        onClose={() => {
          setEditingProfileId(null);
          queryClient.invalidateQueries({ queryKey: ['compliance-profiles'] });
        }}
      />
    );
  }

  const columns: ColumnDef<ComplianceProfile>[] = [
    { id: 'name', header: 'Name', accessorFn: (p) => p.name },
    { id: 'based_on', header: 'Based on', accessorFn: (p) => p.base_framework || 'Custom' },
    { id: 'controls', header: 'Controls', accessorFn: (p) => p.control_count },
    { id: 'version', header: 'Version', accessorFn: (p) => p.version },
    {
      id: 'status',
      header: 'Status',
      accessorFn: (p) => p.status,
      cell: ({ row }) => <ProfileStatusBadge status={row.original.status} />,
    },
    {
      id: 'actions',
      header: '',
      enableSorting: false,
      cell: ({ row }) => {
        const p = row.original;
        return (
          <div style={{ display: 'flex', gap: 'var(--ss-space-xs)', justifyContent: 'flex-end' }}>
            <button className="btn btn-sm" onClick={() => setEditingProfileId(p.id)}>Edit</button>
            <button className="btn btn-sm" onClick={() => setPublishConfirm(p)}>Publish</button>
            <button className="btn btn-sm" style={{ color: 'var(--ss-danger)' }} onClick={() => setDeleteConfirm(p)}>Delete</button>
          </div>
        );
      },
    },
  ];

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 'var(--ss-space-lg)' }}>
        <h2 style={{ margin: 0 }}>Profiles</h2>
        <button className="btn btn-primary" onClick={() => setShowCreate(true)}>
          + New profile
        </button>
      </div>

      {showCreate && (
        <CreateProfileModal
          onCreated={(profile) => {
            setShowCreate(false);
            queryClient.invalidateQueries({ queryKey: ['compliance-profiles'] });
            setEditingProfileId(profile.id);
          }}
          onCancel={() => setShowCreate(false)}
        />
      )}

      {isLoading && <p>Loading...</p>}
      {error && <p className="error">Failed to load profiles: {(error as Error).message}</p>}

      {!isLoading && profiles && profiles.length === 0 && !showCreate && (
        <p className="muted">No custom profiles yet. Create one to build a tenant-specific compliance bundle.</p>
      )}

      {profiles && profiles.length > 0 && (
        <DataTable
          columns={columns}
          data={profiles}
          getRowId={(p) => p.id}
          initialSorting={[{ id: 'name', desc: false }]}
        />
      )}

      {/* Publish confirmation modal */}
      {publishConfirm && (
        <div className="modal-backdrop" onClick={() => setPublishConfirm(null)}>
          <div className="form-card" style={{ maxWidth: 440 }} onClick={(e) => e.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>Publish profile</h3>
            <p>
              Publish <strong>{publishConfirm.name}</strong> v{publishConfirm.version + 1}?
              This will build and sign a bundle from the selected controls.
            </p>
            <div style={{ display: 'flex', gap: 'var(--ss-space-sm)', justifyContent: 'flex-end', marginTop: 'var(--ss-space-lg)' }}>
              <button className="btn" onClick={() => setPublishConfirm(null)} disabled={publishMut.isPending}>
                Cancel
              </button>
              <button
                className="btn btn-primary"
                disabled={publishMut.isPending}
                onClick={() => publishMut.mutate(publishConfirm.id)}
              >
                {publishMut.isPending ? 'Publishing...' : 'Publish'}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Delete confirmation modal */}
      {deleteConfirm && (
        <div className="modal-backdrop" onClick={() => setDeleteConfirm(null)}>
          <div className="form-card" style={{ maxWidth: 440 }} onClick={(e) => e.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>Delete profile</h3>
            <p>
              Delete <strong>{deleteConfirm.name}</strong>? This cannot be undone.
            </p>
            <div style={{ display: 'flex', gap: 'var(--ss-space-sm)', justifyContent: 'flex-end', marginTop: 'var(--ss-space-lg)' }}>
              <button className="btn" onClick={() => setDeleteConfirm(null)} disabled={deleteMut.isPending}>
                Cancel
              </button>
              <button
                className="btn"
                style={{ background: 'var(--ss-danger)', color: 'var(--ss-text-on-accent)', border: 'none' }}
                disabled={deleteMut.isPending}
                onClick={() => deleteMut.mutate(deleteConfirm.id)}
              >
                {deleteMut.isPending ? 'Deleting...' : 'Delete profile'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function ProfileStatusBadge({ status }: { status: string }) {
  if (status === 'published') {
    return <span className="badge badge-completed">Published</span>;
  }
  return <span className="badge">Draft</span>;
}

// ---------------------------------------------------------------------------
// Create profile modal
// ---------------------------------------------------------------------------

function CreateProfileModal({
  onCreated,
  onCancel,
}: {
  onCreated: (profile: ComplianceProfile) => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [baseFramework, setBaseFramework] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const { data: bundles } = useQuery<Bundle[]>({
    queryKey: ['bundles'],
    queryFn: listBundles,
  });

  const frameworkOptions = useMemo(() => {
    if (!bundles) return [];
    const names = new Set<string>();
    for (const b of bundles) {
      if (b.id !== DISCOVERY_BUNDLE_ID && b.framework) {
        names.add(b.framework);
      }
    }
    return Array.from(names).sort();
  }, [bundles]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) { setError('Name is required.'); return; }
    setCreating(true);
    setError(null);
    try {
      const profile = await createComplianceProfile({
        name: name.trim(),
        description: description.trim() || undefined,
        base_framework: baseFramework || undefined,
      });

      // If a base framework was selected, pre-populate controls from it.
      if (baseFramework) {
        try {
          const controls = await listControls({ framework: baseFramework, page_size: 10000 });
          if (controls.items.length > 0) {
            const ids = controls.items.map((c) => c.control_id);
            await setProfileControls(profile.id, ids);
          }
        } catch {
          // Non-fatal: profile was created, controls just won't be pre-populated.
        }
      }

      onCreated(profile);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setCreating(false);
    }
  }

  return (
    <div className="modal-backdrop" onClick={onCancel}>
      <div className="form-card" style={{ maxWidth: 480 }} onClick={(e) => e.stopPropagation()}>
        <h3 style={{ marginTop: 0 }}>New profile</h3>
        <form onSubmit={handleSubmit}>
          <div className="form-group">
            <label htmlFor="profile-name">Name <span style={{ color: 'var(--ss-danger)' }}>*</span></label>
            <input
              id="profile-name"
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. ACME Database Hardening"
              autoFocus
            />
          </div>
          <div className="form-group">
            <label htmlFor="profile-desc">Description</label>
            <textarea
              id="profile-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
              placeholder="Optional description"
            />
          </div>
          <div className="form-group">
            <label htmlFor="profile-base">Start from</label>
            <select
              id="profile-base"
              value={baseFramework}
              onChange={(e) => setBaseFramework(e.target.value)}
            >
              <option value="">Blank (no controls)</option>
              {frameworkOptions.map((f) => (
                <option key={f} value={f}>{f}</option>
              ))}
            </select>
            <small className="muted">
              Selecting a framework pre-loads its controls into the picker.
            </small>
          </div>
          {error && <p className="error">{error}</p>}
          <div style={{ display: 'flex', gap: 'var(--ss-space-sm)', marginTop: 'var(--ss-space-lg)', justifyContent: 'flex-end' }}>
            <button type="button" className="btn" onClick={onCancel} disabled={creating}>
              Cancel
            </button>
            <button type="submit" className="btn btn-primary" disabled={creating}>
              {creating ? 'Creating...' : 'Create'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Control picker — full-page editor for a profile's control list
// ---------------------------------------------------------------------------

const PICKER_SEVERITY_OPTIONS = ['critical', 'high', 'medium', 'low', 'info'] as const;
const PICKER_ENGINE_OPTIONS = ['postgresql', 'mssql', 'mongodb', 'mysql', 'host', 'cidr', 'cloud'] as const;

function ControlPicker({
  profileId,
  onClose,
}: {
  profileId: string;
  onClose: () => void;
}) {
  const { toast } = useToast();
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [initialLoaded, setInitialLoaded] = useState(false);
  const [saving, setSaving] = useState(false);

  // Filter state
  const [framework, setFramework] = useState('');
  const [engine, setEngine] = useState('');
  const [severity, setSeverity] = useState('');
  const [tagFilter, setTagFilter] = useState('');
  const [searchText, setSearchText] = useState('');

  // Load existing profile controls
  useEffect(() => {
    getProfileControls(profileId)
      .then((ids) => {
        setSelectedIds(new Set(ids));
        setInitialLoaded(true);
      })
      .catch(() => setInitialLoaded(true));
  }, [profileId]);

  // Fetch available bundles for framework dropdown
  const { data: bundles } = useQuery<Bundle[]>({
    queryKey: ['bundles'],
    queryFn: listBundles,
  });

  const frameworkOptions = useMemo(() => {
    if (!bundles) return [];
    const names = new Set<string>();
    for (const b of bundles) {
      if (b.id !== DISCOVERY_BUNDLE_ID && b.framework) {
        names.add(b.framework);
      }
    }
    return Array.from(names).sort();
  }, [bundles]);

  // Fetch all controls with filters
  const { data, isLoading } = useQuery({
    queryKey: ['controls', { framework, engine, severity, tag: tagFilter, q: searchText }],
    queryFn: () =>
      listControls({
        framework: framework || undefined,
        engine: engine || undefined,
        severity: severity || undefined,
        tag: tagFilter || undefined,
        q: searchText || undefined,
        page_size: 10000,
      }),
  });

  const controls = useMemo(() => data?.items ?? [], [data]);

  const toggleControl = useCallback((controlId: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(controlId)) next.delete(controlId);
      else next.add(controlId);
      return next;
    });
  }, []);

  // Select-all over the currently rendered rows; DataTable passes the visible
  // ids + the target checked state (replaces the manual toggleAll header).
  const toggleAll = useCallback((ids: string[], checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) ids.forEach((id) => next.add(id));
      else ids.forEach((id) => next.delete(id));
      return next;
    });
  }, []);

  const columns: ColumnDef<ControlEntry>[] = [
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
        row.original.severity ? <SeverityBadge severity={row.original.severity} /> : <span className="muted">{'—'}</span>,
    },
    { id: 'engine', header: 'Engine', accessorFn: (c) => c.engine },
    {
      id: 'frameworks',
      header: 'Frameworks',
      enableSorting: false,
      cell: ({ row }) =>
        row.original.frameworks.map((fw) => (
          <FrameworkChip key={`${fw.bundle_id}-${fw.section}`} bundleName={fw.bundle_name} section={fw.section} />
        )),
    },
    {
      id: 'tags',
      header: 'Tags',
      enableSorting: false,
      cell: ({ row }) => {
        const tags = Array.isArray(row.original.tags) ? row.original.tags : [];
        return tags.length > 0 ? (
          tags.map((t) => (
            <span key={t} className="badge" style={{ fontSize: 11, padding: '1px 6px', marginRight: 'var(--ss-space-xs)', opacity: 0.7 }}>
              {t}
            </span>
          ))
        ) : (
          <span className="muted">{'—'}</span>
        );
      },
    },
  ];

  async function handleSave() {
    setSaving(true);
    try {
      await setProfileControls(profileId, Array.from(selectedIds));
      toast('Profile controls saved', 'success');
      onClose();
    } catch (err) {
      toast((err as Error).message, 'error');
    } finally {
      setSaving(false);
    }
  }

  if (!initialLoaded) return <p>Loading profile controls...</p>;

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 'var(--ss-space-lg)' }}>
        <div>
          <button className="btn btn-sm" onClick={onClose} style={{ marginRight: 'var(--ss-space-md)' }}>
            &larr; Back to profiles
          </button>
          <strong>Edit profile controls</strong>
          <span className="muted" style={{ marginLeft: 'var(--ss-space-md)' }}>
            {selectedIds.size} control{selectedIds.size !== 1 ? 's' : ''} selected
          </span>
        </div>
        <button className="btn btn-primary" onClick={handleSave} disabled={saving}>
          {saving ? 'Saving...' : 'Save'}
        </button>
      </div>

      {/* Filter bar */}
      <div style={{ display: 'flex', gap: 'var(--ss-space-sm)', flexWrap: 'wrap', marginBottom: 'var(--ss-space-lg)' }}>
        <select value={framework} onChange={(e) => setFramework(e.target.value)} style={{ minWidth: 140 }}>
          <option value="">All frameworks</option>
          {frameworkOptions.map((f) => (
            <option key={f} value={f}>{f}</option>
          ))}
        </select>
        <select value={engine} onChange={(e) => setEngine(e.target.value)} style={{ minWidth: 120 }}>
          <option value="">All engines</option>
          {PICKER_ENGINE_OPTIONS.map((e) => (
            <option key={e} value={e}>{e}</option>
          ))}
        </select>
        <select value={severity} onChange={(e) => setSeverity(e.target.value)} style={{ minWidth: 110 }}>
          <option value="">All severities</option>
          {PICKER_SEVERITY_OPTIONS.map((s) => (
            <option key={s} value={s}>{s}</option>
          ))}
        </select>
        <input
          type="text"
          placeholder="Tag..."
          value={tagFilter}
          onChange={(e) => setTagFilter(e.target.value)}
          style={{ width: 120 }}
        />
        <input
          type="text"
          placeholder="Search control ID or name..."
          value={searchText}
          onChange={(e) => setSearchText(e.target.value)}
          style={{ flex: 1, minWidth: 180 }}
        />
      </div>

      {isLoading && <p>Loading controls...</p>}

      {!isLoading && controls.length === 0 && (
        <p className="muted">No controls match the current filters.</p>
      )}

      {/* Selectable DataTable: clicking the row OR its checkbox toggles
          membership. DataTable has no per-row style API, so the legacy
          selected-row tint is dropped \u2014 the checkbox + footer count convey
          selection instead. */}
      {controls.length > 0 && (
        <DataTable
          columns={columns}
          data={controls}
          getRowId={(c) => c.control_id}
          selectable
          selectedIds={selectedIds}
          onToggleRow={(id) => toggleControl(id)}
          onToggleAll={toggleAll}
          onRowClick={(c) => toggleControl(c.control_id)}
          initialSorting={[{ id: 'control_id', desc: false }]}
        />
      )}

      {/* Selected controls summary strip */}
      {selectedIds.size > 0 && (
        <div style={{
          position: 'sticky',
          bottom: 0,
          background: 'var(--ss-bg-raised)',
          borderTop: '1px solid var(--ss-border-default)',
          padding: 'var(--ss-space-md) var(--ss-space-lg)',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginTop: 'var(--ss-space-lg)',
        }}>
          <span>{selectedIds.size} control{selectedIds.size !== 1 ? 's' : ''} selected</span>
          <button className="btn btn-primary" onClick={handleSave} disabled={saving}>
            {saving ? 'Saving...' : 'Save'}
          </button>
        </div>
      )}
    </div>
  );
}
