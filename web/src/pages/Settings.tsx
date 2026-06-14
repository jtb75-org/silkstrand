import { useState, type FormEvent } from 'react';
import { authApi } from '../api/authClient';
import { getToken } from '../api/client';
import { useAuth } from '../auth/useAuth';
import Team from './Team';
import Credentials from './Credentials';

// P5-b: Settings is now the single Setup surface (per ui-shape.md §Nav
// Band 4). Team is folded in as a tab; the old top-level Team entry is
// gone. Credentials consolidates DB/host auth + Integrations + Vaults.
// Bundles moved to the top-level Compliance page (Level 1).
// Audit log promoted to its own top-level admin page (ADR 005 Addendum A5);
// the former Settings → Audit sub-tab is gone.

type Tab = 'profile' | 'team' | 'credentials';

export default function Settings() {
  const { user, active } = useAuth();
  const isAdmin = active?.role === 'admin';
  const [tab, setTab] = useState<Tab>('profile');

  return (
    <div>
      <h1>Settings</h1>
      <p className="muted">Signed in as <strong>{user?.email}</strong>.</p>

      <div className="tab-bar" style={{ display: 'flex', gap: 'var(--ss-space-xs)', borderBottom: '1px solid var(--ss-border-default)', marginTop: 'var(--ss-space-lg)' }}>
        <TabButton active={tab === 'profile'} onClick={() => setTab('profile')}>Profile</TabButton>
        {isAdmin && <TabButton active={tab === 'team'} onClick={() => setTab('team')}>Team</TabButton>}
        <TabButton active={tab === 'credentials'} onClick={() => setTab('credentials')}>Credentials</TabButton>
      </div>

      <div style={{ marginTop: 'var(--ss-space-xl)' }}>
        {tab === 'profile' && <ProfileTab />}
        {tab === 'team' && isAdmin && <Team />}
        {tab === 'credentials' && <Credentials />}
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
          {/* Dark success/danger status text — no --ss-* dark-text token exists, kept literal. */}
          {nameMsg && <p style={{ color: nameMsg.includes('updated') ? '#065f46' : '#b91c1c' }}>{nameMsg}</p>}
          <button className="btn btn-primary" disabled={nameBusy}>
            {nameBusy ? 'Saving…' : 'Save'}
          </button>
        </form>
      </section>

      <section style={{ marginTop: 'var(--ss-space-xl)', maxWidth: 420 }}>
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
          {/* Dark success text — no --ss-* dark-text token exists, kept literal. */}
          {msg && <p style={{ color: '#065f46' }}>{msg}</p>}
          <button className="btn btn-primary" disabled={busy}>
            {busy ? 'Saving…' : 'Update password'}
          </button>
        </form>
      </section>
    </>
  );
}
