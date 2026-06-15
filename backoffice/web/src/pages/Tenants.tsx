import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Building2, Ban, CheckCircle2, RefreshCw, Trash2 } from 'lucide-react';
import {
  listTenants,
  listDataCenters,
  createTenant,
  updateTenantStatus,
  retryTenantProvisioning,
  deleteTenant,
} from '../api/client';
import type {
  Tenant,
  DataCenter,
  CreateTenantRequest,
  DCEnvironment,
  TenantInvite,
  InviteRole,
} from '../api/types';
import { worldRegionForGCP, WORLD_REGIONS, type WorldRegion } from '../lib/regions';
import StatusBadge from '../components/StatusBadge';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';
import Menu from '../components/Menu';

type EnvFilter = DCEnvironment | 'all';
type RegionFilter = WorldRegion | 'all';

export default function Tenants() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [showForm, setShowForm] = useState(false);
  const [filterDc, setFilterDc] = useState('');

  // Form filters (for create tenant dropdown)
  const [formEnv, setFormEnv] = useState<EnvFilter>('prod');
  const [formRegion, setFormRegion] = useState<RegionFilter>('all');
  const [formDc, setFormDc] = useState('');

  // Invites (up to 3)
  const [invites, setInvites] = useState<TenantInvite[]>([]);
  // Last created tenant's invite results (shown after submit)
  const [lastCreated, setLastCreated] = useState<Tenant | null>(null);

  // Delete confirmation modal
  const [deleteTarget, setDeleteTarget] = useState<Tenant | null>(null);
  const [deleteConfirmText, setDeleteConfirmText] = useState('');

  const { data: tenants, isLoading, error } = useQuery<Tenant[]>({
    queryKey: ['tenants', { data_center_id: filterDc || undefined }],
    queryFn: () => listTenants(filterDc || undefined),
  });

  const { data: dataCenters } = useQuery<DataCenter[]>({
    queryKey: ['data-centers'],
    queryFn: listDataCenters,
  });

  const createMutation = useMutation({
    mutationFn: (req: CreateTenantRequest) => createTenant(req),
    onSuccess: (tenant) => {
      queryClient.invalidateQueries({ queryKey: ['tenants'] });
      setShowForm(false);
      setFormDc('');
      setInvites([]);
      setLastCreated(tenant);
    },
  });

  const statusMutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: 'active' | 'suspended' }) =>
      updateTenantStatus(id, { status }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tenants'] });
    },
  });

  const retryMutation = useMutation({
    mutationFn: (id: string) => retryTenantProvisioning(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tenants'] });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteTenant(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['tenants'] });
      setDeleteTarget(null);
      setDeleteConfirmText('');
    },
  });

  // Filter DCs for the create form based on env + world region pills
  const filteredDcs = useMemo(() => {
    if (!dataCenters) return [];
    return dataCenters.filter((dc) => {
      if (formEnv !== 'all' && dc.environment !== formEnv) return false;
      if (formRegion !== 'all' && worldRegionForGCP(dc.region) !== formRegion) return false;
      return true;
    });
  }, [dataCenters, formEnv, formRegion]);

  // Derive the currently-valid DC selection. If the selected DC was filtered
  // out by the pills, treat it as unselected without mutating state.
  const effectiveFormDc = filteredDcs.find((d) => d.id === formDc) ? formDc : '';

  function handleCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!effectiveFormDc) return;
    const formData = new FormData(e.currentTarget);

    // Only submit invites that have an email filled in; trim + lowercase.
    const cleanedInvites = invites
      .map((inv) => ({ email: inv.email.trim().toLowerCase(), role: inv.role }))
      .filter((inv) => inv.email !== '');

    createMutation.mutate({
      data_center_id: effectiveFormDc,
      name: formData.get('name') as string,
      invites: cleanedInvites.length > 0 ? cleanedInvites : undefined,
    });
  }

  function addInvite() {
    if (invites.length >= 3) return;
    setInvites([...invites, { email: '', role: 'member' }]);
  }

  function updateInvite(idx: number, patch: Partial<TenantInvite>) {
    setInvites(invites.map((inv, i) => (i === idx ? { ...inv, ...patch } : inv)));
  }

  function removeInvite(idx: number) {
    setInvites(invites.filter((_, i) => i !== idx));
  }

  function handleToggleStatus(tenant: Tenant) {
    const newStatus = tenant.status === 'active' ? 'suspended' : 'active';
    statusMutation.mutate({ id: tenant.id, status: newStatus });
  }

  const columns: ColumnDef<Tenant>[] = [
    { accessorKey: 'name', header: 'Name' },
    {
      accessorKey: 'status',
      header: 'Status',
      cell: ({ row }) => <StatusBadge status={row.original.status} />,
    },
    {
      accessorKey: 'provisioning_status',
      header: 'Provisioning',
      cell: ({ row }) => <StatusBadge status={row.original.provisioning_status} />,
    },
    {
      accessorKey: 'dc_tenant_id',
      header: 'DC Tenant ID',
      cell: ({ row }) => <span className="text-muted">{row.original.dc_tenant_id || '-'}</span>,
    },
    {
      accessorKey: 'created_at',
      header: 'Created',
      cell: ({ row }) => new Date(row.original.created_at).toLocaleString(),
    },
    {
      id: 'actions',
      header: () => <span className="sr-only">Actions</span>,
      enableSorting: false,
      cell: ({ row }) => {
        const t = row.original;
        const active = t.status === 'active';
        const items = [
          {
            key: 'status',
            label: active ? 'Suspend' : 'Activate',
            icon: active ? <Ban size={14} /> : <CheckCircle2 size={14} />,
            disabled: statusMutation.isPending,
            onSelect: () => handleToggleStatus(t),
          },
          ...(t.provisioning_status === 'failed'
            ? [
                {
                  key: 'retry',
                  label: 'Retry provisioning',
                  icon: <RefreshCw size={14} />,
                  disabled: retryMutation.isPending,
                  onSelect: () => retryMutation.mutate(t.id),
                },
              ]
            : []),
          {
            key: 'delete',
            label: 'Delete',
            icon: <Trash2 size={14} />,
            destructive: true,
            onSelect: () => { setDeleteTarget(t); setDeleteConfirmText(''); },
          },
        ];
        return (
          <div style={{ textAlign: 'right' }}>
            <Menu ariaLabel={`Actions for ${t.name}`} items={items} />
          </div>
        );
      },
    },
  ];

  return (
    <div>
      <div className="page-header">
        <h1>Tenants</h1>
        <button className="btn btn-primary" onClick={() => setShowForm(!showForm)}>
          {showForm ? 'Cancel' : 'New Tenant'}
        </button>
      </div>

      {showForm && (
        <form className="form-card" onSubmit={handleCreate}>
          <div className="form-group">
            <label>Environment</label>
            <div className="pill-group">
              <PillButton
                label="Stage"
                active={formEnv === 'stage'}
                onClick={() => setFormEnv('stage')}
              />
              <PillButton
                label="Prod"
                active={formEnv === 'prod'}
                onClick={() => setFormEnv('prod')}
              />
              <PillButton
                label="All"
                active={formEnv === 'all'}
                onClick={() => setFormEnv('all')}
              />
            </div>
          </div>

          <div className="form-group">
            <label>World Region</label>
            <div className="pill-group">
              <PillButton
                label="All"
                active={formRegion === 'all'}
                onClick={() => setFormRegion('all')}
              />
              {WORLD_REGIONS.map((r) => (
                <PillButton
                  key={r}
                  label={r}
                  active={formRegion === r}
                  onClick={() => setFormRegion(r)}
                />
              ))}
            </div>
          </div>

          <div className="form-group">
            <label htmlFor="data_center_id">Data Center</label>
            <select
              id="data_center_id"
              required
              value={effectiveFormDc}
              onChange={(e) => setFormDc(e.target.value)}
            >
              <option value="">
                {filteredDcs.length === 0
                  ? 'No data centers match filters'
                  : 'Select a data center'}
              </option>
              {filteredDcs.map((dc) => (
                <option key={dc.id} value={dc.id}>
                  {dc.name} ({dc.region})
                </option>
              ))}
            </select>
          </div>

          <div className="form-group">
            <label htmlFor="name">Tenant Name</label>
            <input id="name" name="name" type="text" required placeholder="e.g. Acme Corp" />
          </div>

          <div className="form-group">
            <label>Invite users (optional, up to 3)</label>
            {invites.map((inv, i) => (
              <div
                key={i}
                style={{ display: 'flex', gap: 8, marginTop: 8, alignItems: 'center' }}
              >
                <input
                  type="email"
                  placeholder="user@example.com"
                  value={inv.email}
                  onChange={(e) => updateInvite(i, { email: e.target.value })}
                  style={{ flex: '1 1 auto', minWidth: 0 }}
                />
                <select
                  value={inv.role}
                  onChange={(e) => updateInvite(i, { role: e.target.value as InviteRole })}
                  style={{ flex: '0 0 140px' }}
                >
                  <option value="admin">Admin</option>
                  <option value="member">Member</option>
                </select>
                <button
                  type="button"
                  className="btn btn-sm"
                  onClick={() => removeInvite(i)}
                  aria-label="Remove invite"
                  style={{ flex: '0 0 auto' }}
                >
                  ×
                </button>
              </div>
            ))}
            {invites.length < 3 && (
              <button
                type="button"
                className="btn btn-sm"
                onClick={addInvite}
                style={{ marginTop: 8 }}
              >
                + Add user
              </button>
            )}
          </div>

          <button
            type="submit"
            className="btn btn-primary"
            disabled={createMutation.isPending || !effectiveFormDc}
          >
            {createMutation.isPending ? 'Creating...' : 'Create Tenant'}
          </button>
          {createMutation.error && (
            <p className="error">{(createMutation.error as Error).message}</p>
          )}
        </form>
      )}

      {!showForm && (
        <div className="filter-bar">
          <label htmlFor="filter-dc">Filter by Data Center:</label>
          <select
            id="filter-dc"
            value={filterDc}
            onChange={(e) => setFilterDc(e.target.value)}
          >
            <option value="">All</option>
            {dataCenters?.map((dc) => (
              <option key={dc.id} value={dc.id}>
                {dc.name} ({dc.environment})
              </option>
            ))}
          </select>
        </div>
      )}

      {!showForm && lastCreated && lastCreated.invite_results && lastCreated.invite_results.length > 0 && (
        <div className="detail-card" style={{ marginBottom: 16 }}>
          <strong>Invitation results for {lastCreated.name}:</strong>
          <ul style={{ margin: '8px 0 0 20px' }}>
            {lastCreated.invite_results.map((r, i) => (
              <li key={i} style={{ color: r.status === 'invited' ? '#15803d' : '#b91c1c' }}>
                {r.status === 'invited' ? '✓' : '✗'} {r.email} ({r.role})
                {r.error && <span style={{ color: '#64748b' }}> — {r.error}</span>}
              </li>
            ))}
          </ul>
          <button
            type="button"
            className="btn btn-sm"
            style={{ marginTop: 8 }}
            onClick={() => setLastCreated(null)}
          >
            Dismiss
          </button>
        </div>
      )}

      {!showForm && isLoading && <p className="text-muted">Loading…</p>}
      {!showForm && error && <p className="error">Failed to load tenants: {(error as Error).message}</p>}
      {!showForm && !isLoading && tenants && tenants.length === 0 && (
        <EmptyState icon={<Building2 />} title="No tenants found." />
      )}
      {!showForm && tenants && tenants.length > 0 && (
        <DataTable<Tenant>
          columns={columns}
          data={tenants}
          getRowId={(t) => t.id}
          initialSorting={[{ id: 'name', desc: false }]}
          onRowClick={(t) => navigate(`/tenants/${t.id}`)}
        />
      )}

      {deleteTarget && (
        <div
          className="modal-backdrop"
          onClick={() => {
            if (!deleteMutation.isPending) {
              setDeleteTarget(null);
              setDeleteConfirmText('');
            }
          }}
        >
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h2>Delete Tenant</h2>
            <p>
              This will permanently delete tenant <strong>{deleteTarget.name}</strong>,
              deactivate it in the data center, and remove its Clerk organization.
              This cannot be undone.
            </p>
            <p>
              Type <code>{deleteTarget.name}</code> to confirm:
            </p>
            <input
              autoFocus
              type="text"
              value={deleteConfirmText}
              onChange={(e) => setDeleteConfirmText(e.target.value)}
              placeholder={deleteTarget.name}
            />
            {deleteMutation.error && (
              <p className="error">{(deleteMutation.error as Error).message}</p>
            )}
            <div className="modal-actions">
              <button
                className="btn"
                onClick={() => {
                  setDeleteTarget(null);
                  setDeleteConfirmText('');
                }}
                disabled={deleteMutation.isPending}
              >
                Cancel
              </button>
              <button
                className="btn btn-danger"
                disabled={
                  deleteConfirmText !== deleteTarget.name || deleteMutation.isPending
                }
                onClick={() => deleteMutation.mutate(deleteTarget.id)}
              >
                {deleteMutation.isPending ? 'Deleting...' : 'Delete Tenant'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function PillButton({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={`pill ${active ? 'pill-active' : ''}`}
      onClick={onClick}
    >
      {label}
    </button>
  );
}
