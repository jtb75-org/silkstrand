import { useState } from 'react';
import { useParams, Link } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  getTenant,
  getDataCenter,
  listTenantMembers,
  listTenantInvites,
  createTenantInvite,
  resendTenantInvite,
  deleteTenantInvite,
} from '../api/client';
import type {
  Tenant,
  DataCenter,
  TenantMember,
  TenantPendingInvite,
  InviteRole,
} from '../api/types';
import StatusBadge from '../components/StatusBadge';

type TabKey = 'details' | 'users';

export default function TenantDetail() {
  const { id } = useParams<{ id: string }>();
  const [tab, setTab] = useState<TabKey>('details');

  const { data: tenant, isLoading, error } = useQuery<Tenant>({
    queryKey: ['tenants', id],
    queryFn: () => getTenant(id!),
    enabled: !!id,
  });

  const { data: dc } = useQuery<DataCenter>({
    queryKey: ['data-centers', tenant?.data_center_id],
    queryFn: () => getDataCenter(tenant!.data_center_id),
    enabled: !!tenant?.data_center_id,
  });

  // Pre-fetch the Users-tab data so the tab can show a count badge without a click.
  const { data: members } = useQuery<TenantMember[]>({
    queryKey: ['tenant-members', id],
    queryFn: () => listTenantMembers(id!),
    enabled: !!id,
  });

  const { data: invites } = useQuery<TenantPendingInvite[]>({
    queryKey: ['tenant-invites', id],
    queryFn: () => listTenantInvites(id!),
    enabled: !!id,
  });

  const userCount = (members?.length ?? 0) + (invites?.length ?? 0);

  return (
    <div>
      <Link to="/tenants" className="back-link">Back to Tenants</Link>

      {isLoading && <p>Loading...</p>}
      {error && <p className="error">Failed to load tenant: {(error as Error).message}</p>}

      {tenant && (
        <>
          <div className="page-header">
            <h1>{tenant.name}</h1>
            <StatusBadge status={tenant.status} />
          </div>

          <div className="tabs">
            <button
              className={`tab ${tab === 'details' ? 'tab-active' : ''}`}
              onClick={() => setTab('details')}
            >
              Details
            </button>
            <button
              className={`tab ${tab === 'users' ? 'tab-active' : ''}`}
              onClick={() => setTab('users')}
            >
              Users
              {userCount > 0 && <span className="tab-count">{userCount}</span>}
            </button>
          </div>

          {tab === 'details' && <DetailsTab tenant={tenant} dc={dc} />}
          {tab === 'users' && (
            <UsersTab
              tenantId={tenant.id}
              members={members ?? []}
              invites={invites ?? []}
            />
          )}
        </>
      )}
    </div>
  );
}

function DetailsTab({ tenant, dc }: { tenant: Tenant; dc?: DataCenter }) {
  return (
    <>
      <div className="detail-card">
        <div className="detail-row">
          <span className="detail-label">Status</span>
          <StatusBadge status={tenant.status} />
        </div>
        <div className="detail-row">
          <span className="detail-label">Provisioning</span>
          <StatusBadge status={tenant.provisioning_status} />
        </div>
        <div className="detail-row">
          <span className="detail-label">Data Center</span>
          <span>
            {dc ? (
              <>
                <Link to={`/data-centers/${dc.id}`}>{dc.name}</Link>
                {' '}({dc.region})
                {' '}
                <span className={`env-badge env-${dc.environment}`}>{dc.environment}</span>
              </>
            ) : (
              <code>{tenant.data_center_id}</code>
            )}
          </span>
        </div>
        <div className="detail-row">
          <span className="detail-label">Tenant ID</span>
          <code title="Tenant ID in the data center's database — use this when debugging on the DC side">
            {tenant.dc_tenant_id || '-'}
          </code>
        </div>
        <div className="detail-row">
          <span className="detail-label">Created</span>
          <span>{new Date(tenant.created_at).toLocaleString()}</span>
        </div>
        <div className="detail-row">
          <span className="detail-label">Updated</span>
          <span>{new Date(tenant.updated_at).toLocaleString()}</span>
        </div>
      </div>

      {tenant.config && Object.keys(tenant.config).length > 0 && (
        <>
          <h2>Configuration</h2>
          <pre className="config-json">{JSON.stringify(tenant.config, null, 2)}</pre>
        </>
      )}
    </>
  );
}

function UsersTab({
  tenantId,
  members,
  invites,
}: {
  tenantId: string;
  members: TenantMember[];
  invites: TenantPendingInvite[];
}) {
  const queryClient = useQueryClient();
  const [showInvite, setShowInvite] = useState(false);
  const [email, setEmail] = useState('');
  const [role, setRole] = useState<InviteRole>('member');

  function invalidate() {
    queryClient.invalidateQueries({ queryKey: ['tenant-members', tenantId] });
    queryClient.invalidateQueries({ queryKey: ['tenant-invites', tenantId] });
  }

  const createMutation = useMutation({
    mutationFn: () => createTenantInvite(tenantId, { email: email.trim().toLowerCase(), role }),
    onSuccess: () => {
      setEmail('');
      setRole('member');
      setShowInvite(false);
      invalidate();
    },
  });

  const resendMutation = useMutation({
    mutationFn: (inviteId: string) => resendTenantInvite(tenantId, inviteId),
    onSuccess: invalidate,
  });

  const revokeMutation = useMutation({
    mutationFn: (inviteId: string) => deleteTenantInvite(tenantId, inviteId),
    onSuccess: invalidate,
  });

  const empty = members.length === 0 && invites.length === 0;

  return (
    <>
      <div className="section-header">
        <h2>Users</h2>
        <button className="btn btn-primary btn-sm" onClick={() => setShowInvite(!showInvite)}>
          {showInvite ? 'Cancel' : 'Invite User'}
        </button>
      </div>

      {showInvite && (
        <form
          className="detail-card"
          style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 16 }}
          onSubmit={(e) => {
            e.preventDefault();
            if (email.trim()) createMutation.mutate();
          }}
        >
          <input
            type="email"
            required
            placeholder="user@example.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            style={{ flex: '1 1 auto', minWidth: 0 }}
          />
          <select
            value={role}
            onChange={(e) => setRole(e.target.value as InviteRole)}
            style={{ flex: '0 0 140px' }}
          >
            <option value="admin">Admin</option>
            <option value="member">Member</option>
          </select>
          <button
            type="submit"
            className="btn btn-primary btn-sm"
            disabled={createMutation.isPending || !email.trim()}
            style={{ flex: '0 0 auto' }}
          >
            {createMutation.isPending ? 'Sending...' : 'Send Invite'}
          </button>
        </form>
      )}

      {createMutation.error && (
        <p className="error">{(createMutation.error as Error).message}</p>
      )}

      {empty && <p>No users or pending invites for this tenant.</p>}

      {!empty && (
        <table className="table">
          <thead>
            <tr>
              <th>Email</th>
              <th>Role</th>
              <th>Status</th>
              <th>Since / Expires</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {members.map((m) => (
              <tr key={`m-${m.user_id}`}>
                <td>{m.email}</td>
                <td>{m.role}</td>
                <td><StatusBadge status={m.status} /></td>
                <td>
                  <span className="row-muted">
                    joined {new Date(m.created_at).toLocaleDateString()}
                  </span>
                </td>
                <td></td>
              </tr>
            ))}
            {invites.map((inv) => (
              <tr key={`i-${inv.id}`}>
                <td>{inv.email}</td>
                <td>{inv.role}</td>
                <td><StatusBadge status="invited" /></td>
                <td>
                  <span className="row-muted">
                    expires {new Date(inv.expires_at).toLocaleDateString()}
                  </span>
                </td>
                <td>
                  <button
                    className="btn btn-sm"
                    onClick={() => resendMutation.mutate(inv.id)}
                    disabled={resendMutation.isPending}
                  >
                    Resend
                  </button>
                  <button
                    className="btn btn-danger btn-sm"
                    style={{ marginLeft: 6 }}
                    onClick={() => revokeMutation.mutate(inv.id)}
                    disabled={revokeMutation.isPending}
                  >
                    Revoke
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {(resendMutation.error || revokeMutation.error) && (
        <p className="error">
          {((resendMutation.error || revokeMutation.error) as Error).message}
        </p>
      )}
    </>
  );
}
