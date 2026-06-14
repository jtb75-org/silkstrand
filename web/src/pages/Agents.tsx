import { useEffect, useMemo, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  listAgents, rotateAgentKey, deleteAgent, getAgentDownloads,
  upgradeAgent, getAgentAllowlist, listScans,
  type AgentAllowlist,
} from '../api/client';
import type { Agent, AgentDownloads, Scan } from '../api/types';
import { useAuth } from '../auth/useAuth';
import AgentLogConsole from '../components/AgentLogConsole';
import AddAgentModal from '../components/AddAgentModal';
import CodeBlock from '../components/CodeBlock';

export default function Agents() {
  const qc = useQueryClient();
  const { active } = useAuth();
  const apiURL = active?.dc_api_url || '';

  const { data: agents, isLoading, error } = useQuery<Agent[]>({
    queryKey: ['agents'],
    queryFn: listAgents,
  });

  // Scans list for deriving per-agent "running scan" indicator. Invalidated
  // via SSE scan_status events in Layout.tsx so indicators update in real time.
  const { data: scans } = useQuery<Scan[]>({
    queryKey: ['scans'],
    queryFn: listScans,
  });

  const agentHasRunningScan = useMemo(() => {
    const set = new Set<string>();
    if (scans) {
      for (const s of scans) {
        if (s.status === 'running' && s.agent_id) set.add(s.agent_id);
      }
    }
    return set;
  }, [scans]);

  const { data: downloads } = useQuery<AgentDownloads>({
    queryKey: ['agent-downloads'],
    queryFn: getAgentDownloads,
  });

  // Install-script URL comes from the DC's downloads endpoint (single source of
  // truth, driven by the server's AGENT_RELEASES_URL) — never hardcode it, or
  // it drifts from where releases are actually published.
  const installScriptURL = downloads?.install_script ?? '';

  const [showAdd, setShowAdd] = useState(false);
  const [newKey, setNewKey] = useState<{ agent: Agent; apiKey: string } | null>(null);
  const [allowlistFor, setAllowlistFor] = useState<Agent | null>(null);
  const [consoleFor, setConsoleFor] = useState<Agent | null>(null);
  // Container agents can't self-upgrade in place — the action recreates the
  // container from the new image instead (ADR 013 follow-up).
  const [recreateFor, setRecreateFor] = useState<Agent | null>(null);

  const rotateMutation = useMutation({
    mutationFn: (id: string) => rotateAgentKey(id),
    onSuccess: (res, id) => {
      const agent = agents?.find((a) => a.id === id);
      if (agent) setNewKey({ agent, apiKey: res.api_key });
      qc.invalidateQueries({ queryKey: ['agents'] });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteAgent(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['agents'] }),
  });

  const upgradeMutation = useMutation({
    mutationFn: (id: string) => upgradeAgent(id),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ['agents'] });
      // Confirm to the user — without this the click is silent and the
      // agent restart can take ~30s, which feels like nothing happened.
      alert(`Upgrade requested → ${res.version}. Agent will download, verify, and restart in ~30s.`);
    },
    onError: (e) => {
      alert(`Upgrade failed: ${(e as Error).message}`);
    },
  });

  return (
    <div>
      <div className="page-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h1>Agents</h1>
        <button className="btn btn-primary" onClick={() => setShowAdd(true)}>+ Add Agent</button>
      </div>

      {newKey && (
        <div className="detail-card" style={{ marginBottom: 24, borderColor: '#0f766e' }}>
          <h3 style={{ marginTop: 0 }}>New API key for {newKey.agent.name}</h3>
          <p className="muted">Copy this now — it will not be shown again.</p>
          <pre
            style={{
              background: '#f3f4f6', padding: 12, borderRadius: 6,
              userSelect: 'all', overflowX: 'auto',
            }}
          >{newKey.apiKey}</pre>
          <button className="btn btn-sm" onClick={() => setNewKey(null)}>Dismiss</button>
        </div>
      )}

      {isLoading && <p>Loading…</p>}
      {error && <p className="error">{(error as Error).message}</p>}
      {!isLoading && agents && agents.length === 0 && (
        <div className="detail-card">
          <p style={{ marginTop: 0 }}>No agents registered yet.</p>
          <button className="btn btn-primary" onClick={() => setShowAdd(true)}>+ Add your first agent</button>
        </div>
      )}

      {agents && agents.length > 0 && (
        <table className="table">
          <thead>
            <tr>
              <th>Name</th>
              <th>ID</th>
              <th>Status</th>
              <th>Version</th>
              <th>Last heartbeat</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {agents.map((a) => (
              <tr key={a.id}>
                <td>
                  {a.name}
                  {a.zone && (
                    <span className="muted" style={{ display: 'block', fontSize: 12 }}>
                      zone: {a.zone}
                    </span>
                  )}
                </td>
                <td className="text-muted" style={{ fontSize: 12 }}>{a.id.slice(0, 8)}…</td>
                <td>
                  {a.status}
                  {agentHasRunningScan.has(a.id) && (
                    <span
                      className="badge badge-completed"
                      style={{ marginLeft: 8, display: 'inline-flex', alignItems: 'center', gap: 4 }}
                    >
                      <span style={{
                        width: 6, height: 6, borderRadius: '50%',
                        background: 'var(--ss-success, #10b981)',
                        animation: 'pulse 1.5s ease-in-out infinite',
                      }} />
                      Running scan
                    </span>
                  )}
                </td>
                <td>{a.version ?? '—'}</td>
                <td>{a.last_heartbeat ? new Date(a.last_heartbeat).toLocaleString() : '—'}</td>
                <td style={{ textAlign: 'right' }}>
                  <button
                    className="btn btn-sm"
                    style={{ marginRight: 6 }}
                    onClick={() => setConsoleFor(a)}
                    title="Open a live tail of this agent's log stream"
                  >
                    Console
                  </button>
                  <button
                    className="btn btn-sm"
                    style={{ marginRight: 6 }}
                    onClick={() => setAllowlistFor(a)}
                    title="View the scan allowlist this agent most recently reported"
                  >
                    Allowlist
                  </button>
                  {a.status === 'connected' && (
                    a.in_container === false ? (
                      // Positively known binary install → in-place self-upgrade.
                      <button
                        className="btn btn-sm"
                        style={{ marginRight: 6 }}
                        disabled={upgradeMutation.isPending}
                        onClick={() => {
                          if (confirm(`Upgrade ${a.name} to the latest version? The agent will download the new binary, verify it, and restart.`)) {
                            upgradeMutation.mutate(a.id);
                          }
                        }}
                      >
                        Upgrade
                      </button>
                    ) : (
                      // Container (true) → recreate; unknown (null/undefined,
                      // e.g. agents predating mode reporting) → modal offers both
                      // rather than guessing in-place and silently failing.
                      <button
                        className="btn btn-sm"
                        style={{ marginRight: 6 }}
                        title={a.in_container ? 'Container agents upgrade by recreating from the new image' : "This agent's deployment mode isn't known yet"}
                        onClick={() => setRecreateFor(a)}
                      >
                        {a.in_container ? 'Recreate from image' : 'Upgrade…'}
                      </button>
                    )
                  )}
                  <button
                    className="btn btn-sm"
                    style={{ marginRight: 6 }}
                    disabled={rotateMutation.isPending}
                    onClick={() => {
                      if (confirm(`Rotate the API key for ${a.name}? The current key keeps working until the agent reconnects with the new one.`)) {
                        rotateMutation.mutate(a.id);
                      }
                    }}
                  >
                    Rotate key
                  </button>
                  <button
                    className="btn btn-sm btn-danger"
                    disabled={deleteMutation.isPending}
                    onClick={() => {
                      if (confirm(`Delete agent ${a.name}? This revokes its key immediately.`)) {
                        deleteMutation.mutate(a.id);
                      }
                    }}
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {showAdd && (
        <AddAgentModal
          apiURL={apiURL}
          installScriptURL={installScriptURL}
          binaries={downloads?.binaries}
          onClose={() => setShowAdd(false)}
        />
      )}

      {allowlistFor && (
        <AllowlistModal agent={allowlistFor} onClose={() => setAllowlistFor(null)} />
      )}

      {consoleFor && (
        <ConsoleDrawer agent={consoleFor} onClose={() => setConsoleFor(null)} />
      )}

      {recreateFor && (
        <div className="modal-backdrop" onClick={() => setRecreateFor(null)}>
          <div className="form-card" style={{ maxWidth: 600 }} onClick={(e) => e.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>Upgrade {recreateFor.name}</h3>
            {recreateFor.in_container ? (
              <p style={{ marginTop: 0 }}>
                <strong>{recreateFor.name}</strong> runs as a container, so it can't
                swap its own binary in place. Upgrade it by pulling the new image and
                recreating the container — run this on the host where it runs:
              </p>
            ) : (
              <p style={{ marginTop: 0 }}>
                We haven't detected this agent's deployment mode yet (it predates mode
                reporting). Choose the path that matches how it was installed:
              </p>
            )}
            <p style={{ margin: '12px 0 4px', fontWeight: 600, fontSize: 14 }}>Container (docker)</p>
            <CodeBlock
              content={`curl -sSL ${installScriptURL || 'https://downloads.silkstrand.io/agent/install.sh'} | sudo sh -s -- \\\n  --mode=docker --upgrade --version=${downloads?.version ?? 'latest'}`}
            />
            <p className="muted" style={{ fontSize: 13 }}>
              Credentials, networks, and proxy/CA settings carry over from the
              existing container — no re-bootstrap. Reconnects on the new version in ~30s.
            </p>
            {recreateFor.in_container !== true && (
              <>
                <p style={{ margin: '16px 0 6px', fontWeight: 600, fontSize: 14 }}>Binary install</p>
                <button
                  className="btn btn-primary btn-sm"
                  disabled={upgradeMutation.isPending}
                  onClick={() => { upgradeMutation.mutate(recreateFor.id); setRecreateFor(null); }}
                >
                  Upgrade in place
                </button>
                <p className="muted" style={{ fontSize: 13, marginTop: 6 }}>
                  The agent downloads the new binary, verifies it, and restarts (~30s).
                </p>
              </>
            )}
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 16 }}>
              <button className="btn" onClick={() => setRecreateFor(null)}>Close</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// ConsoleDrawer — embeds <AgentLogConsole> in the same right-side drawer
// pattern used by the Allowlist modal. Closes on Esc / backdrop / button.
function ConsoleDrawer({ agent, onClose }: { agent: Agent; onClose: () => void }) {
  // Esc to close — matches the design-system.md § 5.9 "Escape key +
  // backdrop click dismiss non-destructive modals" rule.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer drawer-wide" role="dialog" aria-label={`Agent console — ${agent.name}`}>
        <header className="drawer-header">
          <h2>Console — {agent.name}</h2>
          <button type="button" className="btn btn-sm" onClick={onClose}>×</button>
        </header>
        <div className="drawer-body">
          <p className="muted" style={{ margin: 0 }}>
            Live tail of info-and-above log lines from this agent. Debug
            lines stay in the host log file. Past lines that happened
            before you opened this console are not replayed.
          </p>
          <AgentLogConsole filter={{ agentId: agent.id }} />
        </div>
      </aside>
    </>
  );
}

function AllowlistModal({ agent, onClose }: { agent: Agent; onClose: () => void }) {
  const { data, isLoading, error } = useQuery<AgentAllowlist>({
    queryKey: ['agent-allowlist', agent.id],
    queryFn: () => getAgentAllowlist(agent.id),
    retry: false,
  });

  const notReported = error && /not reported/i.test((error as Error).message);

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer">
        <header className="drawer-header">
          <h2>Allowlist — {agent.name}</h2>
          <button type="button" className="btn btn-sm" onClick={onClose}>×</button>
        </header>
        <div className="drawer-body">
          <p className="muted" style={{ marginTop: 0 }}>
            The scan allowlist lives on the agent host at{' '}
            <code>/etc/silkstrand/scan-allowlist.yaml</code>. SilkStrand cannot
            edit it — this panel shows the most recent snapshot the agent
            reported over the tunnel. Edit the file on the host to change
            what the agent is willing to scan.
          </p>
          {isLoading && <p>Loading…</p>}
          {notReported && (
            <p className="muted">
              This agent has not reported an allowlist yet. It may be running
              an older binary, or have no allowlist file configured.
            </p>
          )}
          {error && !notReported && <p className="error">{(error as Error).message}</p>}
          {data && (
            <>
              <dl className="kv">
                <dt>snapshot hash</dt>
                <dd style={{ fontFamily: 'monospace', fontSize: 12 }}>
                  {data.snapshot_hash.slice(0, 16)}…
                </dd>
                <dt>reported</dt>
                <dd>{new Date(data.reported_at).toLocaleString()}</dd>
                <dt>updated</dt>
                <dd>{new Date(data.updated_at).toLocaleString()}</dd>
                {data.rate_limit_pps > 0 && (
                  <>
                    <dt>rate cap</dt>
                    <dd>{data.rate_limit_pps} pps</dd>
                  </>
                )}
              </dl>
              <section style={{ marginTop: 16 }}>
                <h3>Allow ({data.allow.length})</h3>
                {data.allow.length === 0 ? (
                  <p className="muted">
                    Empty — the agent will refuse every scan directive.
                  </p>
                ) : (
                  <ul style={{ fontFamily: 'monospace', fontSize: 13 }}>
                    {data.allow.map((rule) => (<li key={rule}>{rule}</li>))}
                  </ul>
                )}
              </section>
              {data.deny.length > 0 && (
                <section style={{ marginTop: 16 }}>
                  <h3>Deny ({data.deny.length})</h3>
                  <ul style={{ fontFamily: 'monospace', fontSize: 13 }}>
                    {data.deny.map((rule) => (<li key={rule}>{rule}</li>))}
                  </ul>
                </section>
              )}
            </>
          )}
        </div>
      </aside>
    </>
  );
}
