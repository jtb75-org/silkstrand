import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Users as UsersIcon, UserCheck, UserMinus, Trash2, X } from 'lucide-react';
import {
  listUsers, getUser, updateUserStatus, deleteUser,
  updateUserMembershipStatus, removeUserMembership,
} from '../api/client';
import type { User, UserDetail, UserMembership } from '../api/types';
import StatusBadge from '../components/StatusBadge';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';
import Menu from '../components/Menu';

export default function Users() {
  const qc = useQueryClient();
  const { data: users, isLoading, error } = useQuery<User[]>({
    queryKey: ['users'],
    queryFn: listUsers,
  });
  // Single-active below-table detail panel (DataTable has no row-expand API;
  // ADR 018/020 deviation — row click toggles one open panel under the table).
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<User | null>(null);
  const [deleteText, setDeleteText] = useState('');

  const statusMutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: 'active' | 'suspended' }) =>
      updateUserStatus(id, status),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['users'] }),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteUser(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] });
      setDeleteTarget(null);
      setDeleteText('');
      setExpandedId((cur) => (cur === deleteTarget?.id ? null : cur));
    },
  });

  const expandedUser = users?.find((u) => u.id === expandedId) ?? null;

  const columns: ColumnDef<User>[] = [
    { accessorKey: 'email', header: 'Email' },
    {
      accessorKey: 'status',
      header: 'Status',
      cell: ({ row }) => <StatusBadge status={row.original.status} />,
    },
    { accessorKey: 'tenant_count', header: 'Tenants' },
    {
      accessorKey: 'last_login_at',
      header: 'Last login',
      cell: ({ row }) => (
        <span className="text-muted">
          {row.original.last_login_at
            ? new Date(row.original.last_login_at).toLocaleString()
            : '—'}
        </span>
      ),
    },
    {
      accessorKey: 'created_at',
      header: 'Created',
      cell: ({ row }) => new Date(row.original.created_at).toLocaleDateString(),
    },
    {
      id: 'actions',
      header: () => <span className="sr-only">Actions</span>,
      enableSorting: false,
      cell: ({ row }) => {
        const u = row.original;
        const active = u.status === 'active';
        return (
          <div style={{ textAlign: 'right' }}>
            <Menu
              ariaLabel={`Actions for ${u.email}`}
              items={[
                {
                  key: 'status',
                  label: active ? 'Suspend' : 'Reactivate',
                  icon: active ? <UserMinus size={14} /> : <UserCheck size={14} />,
                  onSelect: () =>
                    statusMutation.mutate({ id: u.id, status: active ? 'suspended' : 'active' }),
                },
                {
                  key: 'delete',
                  label: 'Delete user',
                  icon: <Trash2 size={14} />,
                  destructive: true,
                  onSelect: () => { setDeleteTarget(u); setDeleteText(''); },
                },
              ]}
            />
          </div>
        );
      },
    },
  ];

  return (
    <div>
      <div className="page-header">
        <h1>Users</h1>
      </div>

      {isLoading && <p className="text-muted">Loading…</p>}
      {error && <p className="error">Failed to load users: {(error as Error).message}</p>}
      {!isLoading && users && users.length === 0 && (
        <EmptyState icon={<UsersIcon />} title="No users yet." />
      )}

      {users && users.length > 0 && (
        <DataTable<User>
          columns={columns}
          data={users}
          getRowId={(u) => u.id}
          initialSorting={[{ id: 'email', desc: false }]}
          onRowClick={(u) => setExpandedId((cur) => (cur === u.id ? null : u.id))}
        />
      )}

      {expandedUser && (
        <div className="detail-card" style={{ marginTop: 'var(--ss-space-lg)' }}>
          <div
            style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              marginBottom: 'var(--ss-space-md)',
            }}
          >
            <h2 style={{ margin: 0 }}>{expandedUser.email}</h2>
            <button
              className="ss-menu__trigger"
              aria-label="Close details"
              onClick={() => setExpandedId(null)}
            >
              <X size={16} aria-hidden="true" />
            </button>
          </div>
          <UserDetailPanel userId={expandedUser.id} />
        </div>
      )}

      {deleteTarget && (
        <div
          className="modal-backdrop"
          onClick={() => { if (!deleteMutation.isPending) { setDeleteTarget(null); setDeleteText(''); } }}
        >
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h2>Delete user</h2>
            <p>
              This will permanently delete <strong>{deleteTarget.email}</strong> and
              remove them from all tenants. Pending invitations to other tenants
              will also be invalidated. This cannot be undone.
            </p>
            <p>Type <code>{deleteTarget.email}</code> to confirm:</p>
            <input
              autoFocus type="text" value={deleteText}
              onChange={(e) => setDeleteText(e.target.value)}
              placeholder={deleteTarget.email}
            />
            {deleteMutation.error && (
              <p className="error">{(deleteMutation.error as Error).message}</p>
            )}
            <div className="modal-actions">
              <button className="btn" onClick={() => { setDeleteTarget(null); setDeleteText(''); }}
                disabled={deleteMutation.isPending}>Cancel</button>
              <button
                className="btn btn-danger"
                disabled={deleteText !== deleteTarget.email || deleteMutation.isPending}
                onClick={() => deleteMutation.mutate(deleteTarget.id)}
              >
                {deleteMutation.isPending ? 'Deleting...' : 'Delete user'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function UserDetailPanel({ userId }: { userId: string }) {
  const qc = useQueryClient();
  const { data, isLoading, error } = useQuery<UserDetail>({
    queryKey: ['users', userId],
    queryFn: () => getUser(userId),
  });

  const membershipStatus = useMutation({
    mutationFn: ({ tenantId, status }: { tenantId: string; status: 'active' | 'suspended' }) =>
      updateUserMembershipStatus(userId, tenantId, status),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users', userId] });
      qc.invalidateQueries({ queryKey: ['users'] });
    },
  });

  const removeMembership = useMutation({
    mutationFn: (tenantId: string) => removeUserMembership(userId, tenantId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users', userId] });
      qc.invalidateQueries({ queryKey: ['users'] });
    },
  });

  if (isLoading) return <p className="text-muted">Loading details…</p>;
  if (error) return <p className="error">{(error as Error).message}</p>;
  if (!data) return null;

  const membershipColumns: ColumnDef<UserMembership>[] = [
    { accessorKey: 'tenant_name', header: 'Tenant' },
    { accessorKey: 'dc_name', header: 'DC' },
    {
      accessorKey: 'environment',
      header: 'Env',
      cell: ({ row }) => (
        <span className={`env-badge env-${row.original.environment}`}>
          {row.original.environment}
        </span>
      ),
    },
    { accessorKey: 'role', header: 'Role' },
    {
      accessorKey: 'status',
      header: 'Status',
      cell: ({ row }) => <StatusBadge status={row.original.status} />,
    },
    {
      id: 'actions',
      header: () => <span className="sr-only">Actions</span>,
      enableSorting: false,
      cell: ({ row }) => {
        const m = row.original;
        const active = m.status === 'active';
        const busy = membershipStatus.isPending || removeMembership.isPending;
        return (
          <div style={{ textAlign: 'right' }}>
            <Menu
              ariaLabel={`Actions for ${m.tenant_name} membership`}
              items={[
                {
                  key: 'status',
                  label: active ? 'Suspend' : 'Reactivate',
                  icon: active ? <UserMinus size={14} /> : <UserCheck size={14} />,
                  disabled: busy,
                  onSelect: () =>
                    membershipStatus.mutate({
                      tenantId: m.tenant_id,
                      status: active ? 'suspended' : 'active',
                    }),
                },
                {
                  key: 'remove',
                  label: 'Remove from tenant',
                  icon: <Trash2 size={14} />,
                  destructive: true,
                  disabled: busy,
                  onSelect: () => {
                    if (confirm(`Remove ${data.email} from ${m.tenant_name}?`)) {
                      removeMembership.mutate(m.tenant_id);
                    }
                  },
                },
              ]}
            />
          </div>
        );
      },
    },
  ];

  return (
    <div>
      <h3 style={{ margin: '0 0 var(--ss-space-sm)' }}>Tenant memberships</h3>
      {data.memberships.length === 0 ? (
        <p className="text-muted">No memberships.</p>
      ) : (
        <div style={{ marginBottom: 'var(--ss-space-lg)' }}>
          <DataTable<UserMembership>
            columns={membershipColumns}
            data={data.memberships}
            getRowId={(m) => m.tenant_id}
            initialSorting={[{ id: 'tenant_name', desc: false }]}
          />
        </div>
      )}

      {data.pending_invites.length > 0 && (
        <>
          <h3 style={{ margin: '0 0 var(--ss-space-sm)' }}>Pending invitations</h3>
          <ul style={{ margin: 0, paddingLeft: 'var(--ss-space-xl)' }}>
            {data.pending_invites.map((i) => (
              <li key={i.id}>
                {i.role} invite, expires {new Date(i.expires_at).toLocaleDateString()}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}
