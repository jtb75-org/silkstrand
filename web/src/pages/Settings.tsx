import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { authApi } from '../api/authClient';
import { getToken, listAuditEvents, type AuditEvent } from '../api/client';
import { useAuth } from '../auth/useAuth';
import Team from './Team';
import Credentials from './Credentials';

// P5-b: Settings is now the single Setup surface (per ui-shape.md §Nav
// Band 4). Team is folded in as a tab; the old top-level Team entry is
// gone. Credentials consolidates DB/host auth + Integrations + Vaults.
// Bundles moved to the top-level Compliance page (Level 1).
// Audit log implemented per ADR 005 / O7.

type Tab = 'profile' | 'team' | 'credentials' | 'audit';

export default function Settings() {
  const { user, active } = useAuth();
  const isAdmin = active?.role === 'admin';
  const [tab, setTab] = useState<Tab>('profile');

  return (
    <div>
      <h1>Settings</h1>
      <p className="muted">Signed in as <strong>{user?.email}</strong>.</p>

      <div className="tab-bar" style={{ display: 'flex', gap: 4, borderBottom: '1px solid #e5e7eb', marginTop: 16 }}>
        <TabButton active={tab === 'profile'} onClick={() => setTab('profile')}>Profile</TabButton>
        {isAdmin && <TabButton active={tab === 'team'} onClick={() => setTab('team')}>Team</TabButton>}
        <TabButton active={tab === 'credentials'} onClick={() => setTab('credentials')}>Credentials</TabButton>
        {isAdmin && <TabButton active={tab === 'audit'} onClick={() => setTab('audit')}>Audit</TabButton>}
      </div>

      <div style={{ marginTop: 24 }}>
        {tab === 'profile' && <ProfileTab />}
        {tab === 'team' && isAdmin && <Team />}
        {tab === 'credentials' && <Credentials />}
        {tab === 'audit' && isAdmin && <AuditTab />}
      </div>
    </div>
  );
}

function TabButton({ active, children, onClick }: { active: boolean; children: React.ReactNode; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      style={{
        padding: '8px 16px',
        border: 'none',
        borderBottom: active ? '2px solid #0f766e' : '2px solid transparent',
        background: 'none',
        fontWeight: active ? 600 : 400,
        cursor: 'pointer',
      }}
    >
      {children}
    </button>
  );
}

function ProfileTab() {
  const { user, refresh } = useAuth();

  const displayName = user?.display_name ?? '';
  const [name, setName] = useState(displayName);
  const [syncedName, setSyncedName] = useState(displayName);
  const [nameMsg, setNameMsg] = useState<string | null>(null);
  const [nameBusy, setNameBusy] = useState(false);

  // Resync the editable name when display_name changes upstream (e.g. after a
  // save / tenant switch) — during render via a tracked value, not a
  // set-state-in-effect.
  if (displayName !== syncedName) {
    setSyncedName(displayName);
    setName(displayName);
  }

  async function submitName(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const token = getToken();
    if (!token) return;
    setNameBusy(true);
    setNameMsg(null);
    try {
      await authApi.updateProfile(token, name.trim());
      await refresh();
      setNameMsg('Profile updated.');
    } catch (e) {
      setNameMsg((e as Error).message);
    } finally {
      setNameBusy(false);
    }
  }

  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [msg, setMsg] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setErr(null);
    setMsg(null);
    if (next.length < 8) { setErr('New password must be at least 8 characters.'); return; }
    if (next !== confirm) { setErr('Passwords do not match.'); return; }
    const token = getToken();
    if (!token) return;
    setBusy(true);
    try {
      await authApi.changePassword(token, current, next);
      setMsg('Password updated.');
      setCurrent(''); setNext(''); setConfirm('');
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <section style={{ maxWidth: 420 }}>
        <h2>Profile</h2>
        <form onSubmit={submitName}>
          <label>Display name
            <input
              type="text" maxLength={120}
              value={name} onChange={(e) => setName(e.target.value)}
              placeholder="Shown in members lists and emails"
            />
          </label>
          {nameMsg && <p style={{ color: nameMsg.includes('updated') ? '#065f46' : '#b91c1c' }}>{nameMsg}</p>}
          <button className="btn btn-primary" disabled={nameBusy}>
            {nameBusy ? 'Saving…' : 'Save'}
          </button>
        </form>
      </section>

      <section style={{ marginTop: 24, maxWidth: 420 }}>
        <h2>Change password</h2>
        <form onSubmit={submit}>
          <label>Current password
            <input type="password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
          </label>
          <label>New password
            <input type="password" required minLength={8} value={next} onChange={(e) => setNext(e.target.value)} />
          </label>
          <label>Confirm new password
            <input type="password" required value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </label>
          {err && <p className="error">{err}</p>}
          {msg && <p style={{ color: '#065f46' }}>{msg}</p>}
          <button className="btn btn-primary" disabled={busy}>
            {busy ? 'Saving…' : 'Update password'}
          </button>
        </form>
      </section>
    </>
  );
}

// Event type → badge colour mapping per ADR 005 D7.
function eventBadgeStyle(eventType: string): React.CSSProperties {
  const base: React.CSSProperties = {
    display: 'inline-block', padding: '2px 8px', borderRadius: 4,
    fontSize: 12, fontWeight: 600, whiteSpace: 'nowrap',
  };
  if (eventType.startsWith('credential.')) return { ...base, background: '#dbeafe', color: '#1e40af' };
  if (eventType.startsWith('scan')) return { ...base, background: '#f3f4f6', color: '#374151' };
  if (eventType.startsWith('rule.')) return { ...base, background: '#fef3c7', color: '#92400e' };
  if (eventType.startsWith('agent.')) return { ...base, background: '#d1fae5', color: '#065f46' };
  if (eventType.startsWith('collection.')) return { ...base, background: '#ede9fe', color: '#5b21b6' };
  return { ...base, background: '#f3f4f6', color: '#374151' };
}

const EVENT_TYPE_OPTIONS = [
  '', 'credential.fetch', 'credential.created', 'credential.updated', 'credential.deleted',
  'credential.mapped', 'credential.unmapped', 'credential.test',
  'scan.dispatched', 'scan.completed', 'scan.failed',
  'scan_definition.created', 'scan_definition.updated', 'scan_definition.deleted', 'scan_definition.executed',
  'rule.created', 'rule.updated', 'rule.deleted', 'rule.fired',
  'agent.connected', 'agent.disconnected', 'agent.upgraded', 'agent.key_rotated', 'agent.deleted', 'agent.created',
  'collection.created', 'collection.updated', 'collection.deleted',
];

function AuditTab() {
  const [items, setItems] = useState<AuditEvent[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<string | null>(null);

  // Filters
  const [eventType, setEventType] = useState('');
  const [resourceSearch, setResourceSearch] = useState('');

  const fetchEvents = useCallback(async (cursor?: string) => {
    setLoading(true);
    setError(null);
    try {
      const since = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000).toISOString();
      const result = await listAuditEvents({
        event_type: eventType || undefined,
        resource_id: resourceSearch || undefined,
        since,
        limit: 50,
        cursor,
      });
      if (cursor) {
        setItems(prev => [...prev, ...result.items]);
      } else {
        setItems(result.items);
      }
      setNextCursor(result.next_cursor);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [eventType, resourceSearch]);

  // Fetch-on-dependency-change: fetchEvents synchronously flips loading/error
  // (the intentional "start loading" transition), then sets items after the
  // await — a genuine data fetch, not the derived-state anti-pattern the rule
  // targets. (A react-query migration would remove the manual effect entirely;
  // tracked separately.)
  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { fetchEvents(); }, [fetchEvents]);

  function formatTime(iso: string): string {
    const d = new Date(iso);
    return d.toLocaleString();
  }

  function formatActor(ev: AuditEvent): string {
    if (ev.actor_type === 'system') return 'system';
    const id = ev.actor_id ?? '';
    return `${ev.actor_type}:${id.slice(0, 8)}`;
  }

  function formatResource(ev: AuditEvent): string {
    if (!ev.resource_type) return '-';
    const id = ev.resource_id ?? '';
    return `${ev.resource_type}:${id.slice(0, 8)}`;
  }

  return (
    <section>
      <h2>Audit log</h2>
      <p className="muted" style={{ marginBottom: 16 }}>
        Read-only log of privileged operations. Showing the last 7 days.
      </p>

      <div style={{ display: 'flex', gap: 12, marginBottom: 16, flexWrap: 'wrap' }}>
        <select value={eventType} onChange={e => setEventType(e.target.value)}
          style={{ padding: '6px 10px', borderRadius: 4, border: '1px solid #d1d5db' }}>
          <option value="">All event types</option>
          {EVENT_TYPE_OPTIONS.filter(Boolean).map(t => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
        <input
          type="text" placeholder="Resource ID..."
          value={resourceSearch} onChange={e => setResourceSearch(e.target.value)}
          style={{ padding: '6px 10px', borderRadius: 4, border: '1px solid #d1d5db', width: 240 }}
        />
      </div>

      {error && <p className="error">{error}</p>}

      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 14 }}>
        <thead>
          <tr style={{ borderBottom: '2px solid #e5e7eb', textAlign: 'left' }}>
            <th style={{ padding: '8px 12px' }}>Timestamp</th>
            <th style={{ padding: '8px 12px' }}>Event Type</th>
            <th style={{ padding: '8px 12px' }}>Actor</th>
            <th style={{ padding: '8px 12px' }}>Resource</th>
            <th style={{ padding: '8px 12px' }}>Details</th>
          </tr>
        </thead>
        <tbody>
          {items.map(ev => (
            <>
              <tr key={ev.id} style={{ borderBottom: '1px solid #f3f4f6' }}>
                <td style={{ padding: '8px 12px', whiteSpace: 'nowrap' }}>{formatTime(ev.occurred_at)}</td>
                <td style={{ padding: '8px 12px' }}>
                  <span style={eventBadgeStyle(ev.event_type)}>{ev.event_type}</span>
                </td>
                <td style={{ padding: '8px 12px', fontFamily: 'monospace', fontSize: 12 }}>{formatActor(ev)}</td>
                <td style={{ padding: '8px 12px', fontFamily: 'monospace', fontSize: 12 }}>{formatResource(ev)}</td>
                <td style={{ padding: '8px 12px' }}>
                  <button
                    onClick={() => setExpandedId(expandedId === ev.id ? null : ev.id)}
                    style={{ background: 'none', border: 'none', color: '#0f766e', cursor: 'pointer', fontSize: 12 }}
                  >
                    {expandedId === ev.id ? 'Hide' : 'Show'}
                  </button>
                </td>
              </tr>
              {expandedId === ev.id && (
                <tr key={`${ev.id}-detail`}>
                  <td colSpan={5} style={{ padding: '8px 12px 16px', background: '#f9fafb' }}>
                    <pre style={{ margin: 0, fontSize: 12, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                      {JSON.stringify(ev.payload, null, 2)}
                    </pre>
                  </td>
                </tr>
              )}
            </>
          ))}
          {items.length === 0 && !loading && (
            <tr><td colSpan={5} style={{ padding: 24, textAlign: 'center', color: '#9ca3af' }}>
              No audit events found for the selected filters.
            </td></tr>
          )}
        </tbody>
      </table>

      {loading && <p className="muted" style={{ marginTop: 12 }}>Loading...</p>}

      {nextCursor && !loading && (
        <button
          className="btn btn-secondary"
          onClick={() => fetchEvents(nextCursor)}
          style={{ marginTop: 12 }}
        >
          Load more
        </button>
      )}
    </section>
  );
}
