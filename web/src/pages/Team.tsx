import { useEffect, useState, type FormEvent } from 'react';
import { authApi, type TenantMember, type PendingInvite } from '../api/authClient';
import { getToken } from '../api/client';
import { useAuth } from '../auth/useAuth';

export default function Team() {
  const { active, user } = useAuth();
  const isAdmin = active?.role === 'admin';

  const [members, setMembers] = useState<TenantMember[]>([]);
  const [invites, setInvites] = useState<PendingInvite[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);

  const [inviteEmail, setInviteEmail] = useState('');
  const [inviteRole, setInviteRole] = useState<'admin' | 'member'>('member');
  const [inviteBusy, setInviteBusy] = useState(false);
  const [inviteMsg, setInviteMsg] = useState<string | null>(null);

  async function refresh() {
    const token = getToken();
    if (!token) return;
    setLoading(true);
    try {
      const [m, i] = await Promise.all([
        authApi.listMembers(token),
        isAdmin ? authApi.listInvitations(token) : Promise.resolve([] as PendingInvite[]),
      ]);
      setMembers(m);
      setInvites(i);
      setErr(null);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setLoading(false);
    }
  }

  // Refetch whenever the active tenant (or admin status) changes.
  // active.tenant_id captures the TenantSwitcher case where the user
  // stays admin but the data source is different. refresh() synchronously flips
  // loading (the intentional "start loading" transition) then sets
  // members/invites after the await — a genuine fetch-on-dependency-change, not
  // derived state. Deps are narrowed to the real triggers. Both directives are
  // load-bearing under eslint 10.5.0 / react-hooks 7.1.1 (removal → exhaustive-
  // deps 'refresh' warning + set-state-in-effect error); react-query migration
  // tracked separately.
  // eslint-disable-next-line react-hooks/exhaustive-deps, react-hooks/set-state-in-effect
  useEffect(() => { void refresh(); }, [isAdmin, active?.tenant_id]);

  async function submitInvite(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const token = getToken();
    if (!token) return;
    setInviteBusy(true);
    setInviteMsg(null);
    try {
      await authApi.invite(token, inviteEmail.trim().toLowerCase(), inviteRole);
      setInviteMsg(`Invitation sent to ${inviteEmail}.`);
      setInviteEmail('');
      await refresh();
    } catch (e) {
      setInviteMsg((e as Error).message);
    } finally {
      setInviteBusy(false);
    }
  }

  async function remove(userId: string, email: string) {
    if (!confirm(`Remove ${email} from this tenant? They'll lose access immediately.`)) return;
    const token = getToken();
    if (!token) return;
    try {
      await authApi.removeMember(token, userId);
      await refresh();
    } catch (e) {
      alert((e as Error).message);
    }
  }

  async function toggleStatus(m: TenantMember) {
    const token = getToken();
    if (!token) return;
    const next: 'active' | 'suspended' = m.status === 'active' ? 'suspended' : 'active';
    try {
      await authApi.updateMemberStatus(token, m.user_id, next);
      await refresh();
    } catch (e) {
      alert((e as Error).message);
    }
  }

  async function cancelInvite(id: string, email: string) {
    if (!confirm(`Cancel the pending invitation to ${email}?`)) return;
    const token = getToken();
    if (!token) return;
    try {
      await authApi.cancelInvitation(token, id);
      await refresh();
    } catch (e) {
      alert((e as Error).message);
    }
  }

  async function resendInvite(id: string, email: string) {
    const token = getToken();
    if (!token) return;
    try {
      await authApi.resendInvitation(token, id);
      alert(`New invitation sent to ${email}.`);
      await refresh();
    } catch (e) {
      alert((e as Error).message);
    }
  }

  async function changeRole(m: TenantMember, newRole: 'admin' | 'member') {
    const token = getToken();
    if (!token) return;
    if (newRole === m.role) return;
    try {
      await authApi.updateMemberRole(token, m.user_id, newRole);
      await refresh();
    } catch (e) {
      alert((e as Error).message);
    }
  }

  return (
    <div>
      <h1>Team</h1>
      <p className="muted">Manage users in your tenant.</p>

      {isAdmin && (
        <section style={{ marginTop: 'var(--ss-space-xl)' }}>
          <h2>Invite user</h2>
          <form onSubmit={submitInvite} style={{ display: 'flex', gap: 'var(--ss-space-sm)', alignItems: 'center' }}>
            <input
              type="email" required placeholder="user@example.com"
              value={inviteEmail} onChange={(e) => setInviteEmail(e.target.value)}
              style={{ flex: 1 }}
            />
            <select value={inviteRole} onChange={(e) => setInviteRole(e.target.value as 'admin' | 'member')}>
              <option value="member">Member</option>
              <option value="admin">Admin</option>
            </select>
            <button className="btn btn-primary" disabled={inviteBusy}>
              {inviteBusy ? 'Sending…' : 'Send invite'}
            </button>
          </form>
          {inviteMsg && <p style={{ marginTop: 'var(--ss-space-sm)' }}>{inviteMsg}</p>}
        </section>
      )}

      {isAdmin && invites.length > 0 && (
        <section style={{ marginTop: 'var(--ss-space-2xl)' }}>
          <h2>Pending invitations</h2>
          <table className="table">
            <thead>
              <tr>
                <th>Email</th>
                <th>Role</th>
                <th>Sent</th>
                <th>Expires</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {invites.map((i) => (
                <tr key={i.id}>
                  <td>{i.email}</td>
                  <td>{i.role}</td>
                  <td>{new Date(i.created_at).toLocaleDateString()}</td>
                  <td>{new Date(i.expires_at).toLocaleDateString()}</td>
                  <td>
                    <button
                      className="btn btn-sm"
                      style={{ marginRight: 6 }}
                      onClick={() => resendInvite(i.id, i.email)}
                    >
                      Resend
                    </button>
                    <button className="btn btn-sm" onClick={() => cancelInvite(i.id, i.email)}>
                      Cancel
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      <section style={{ marginTop: 'var(--ss-space-2xl)' }}>
        <h2>Members</h2>
        {loading && <p>Loading…</p>}
        {err && <p className="error">{err}</p>}
        {!loading && !err && (
          <table className="table">
            <thead>
              <tr>
                <th>Email</th>
                <th>Role</th>
                <th>Status</th>
                <th>Joined</th>
                {isAdmin && <th></th>}
              </tr>
            </thead>
            <tbody>
              {members.map((m) => (
                <tr key={m.user_id}>
                  <td>{m.email}{user?.id === m.user_id && <span className="muted"> (you)</span>}</td>
                  <td>
                    {isAdmin && user?.id !== m.user_id ? (
                      <select
                        value={m.role}
                        onChange={(e) => changeRole(m, e.target.value as 'admin' | 'member')}
                      >
                        <option value="member">member</option>
                        <option value="admin">admin</option>
                      </select>
                    ) : m.role}
                  </td>
                  <td>
                    {/* Dark success/danger status text — no --ss-* dark-text token, kept literal. */}
                    <span style={{
                      color: m.status === 'active' ? '#065f46' : '#b91c1c',
                      fontWeight: 500,
                    }}>{m.status}</span>
                  </td>
                  <td>{new Date(m.created_at).toLocaleDateString()}</td>
                  {isAdmin && (
                    <td>
                      {user?.id !== m.user_id && (
                        <>
                          <button
                            className="btn btn-sm"
                            style={{ marginRight: 6 }}
                            onClick={() => toggleStatus(m)}
                          >
                            {m.status === 'active' ? 'Suspend' : 'Reactivate'}
                          </button>
                          <button
                            className="btn btn-sm btn-danger"
                            onClick={() => remove(m.user_id, m.email)}
                          >
                            Remove
                          </button>
                        </>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}
