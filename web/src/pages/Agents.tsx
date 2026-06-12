import { useEffect, useMemo, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  listAgents, rotateAgentKey, deleteAgent, getAgentDownloads,
  createInstallToken, upgradeAgent, getAgentAllowlist,
  listScans, previewAllowlist,
  type AgentAllowlist, type AllowlistPreview,
} from '../api/client';
import type { Agent, AgentDownloads, Scan } from '../api/types';
import { useAuth } from '../auth/useAuth';
import AgentLogConsole from '../components/AgentLogConsole';

// Single-quote a user-supplied value so it is safe to paste into a shell: an
// allow-target can be a wildcard hostname (*.example.com) which would otherwise
// glob, and free-form input must never be interpreted (spaces, ;, `, etc.).
// Closes the quote, emits an escaped ', reopens — the standard POSIX idiom.
const shQuote = (s: string) => `'${s.replace(/'/g, "'\\''")}'`;

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

  const [installToken, setInstallToken] = useState<{ token: string; expiresAt: string } | null>(null);
  const [newKey, setNewKey] = useState<{ agent: Agent; apiKey: string } | null>(null);
  const [allowlistFor, setAllowlistFor] = useState<Agent | null>(null);
  const [consoleFor, setConsoleFor] = useState<Agent | null>(null);

  // Allowed targets seed the agent's scan-allowlist via --allow-cidr (ADR 013
  // D2). The host file stays the source of truth; this just saves the initial
  // SSH-and-edit step. Each entry → one --allow-cidr flag in the command.
  const [allowCidrs, setAllowCidrs] = useState<string[]>(['']);
  const addAllow = () => setAllowCidrs((a) => [...a, '']);
  const updateAllow = (i: number, v: string) =>
    setAllowCidrs((a) => a.map((x, j) => (j === i ? v : x)));
  const removeAllow = (i: number) => setAllowCidrs((a) => a.filter((_, j) => j !== i));

  const cleanAllow = allowCidrs.map((c) => c.trim()).filter(Boolean);

  // Zone/site label (ADR 013 D10): optional, disambiguates reused private
  // ranges for the overlap heuristic. Server-side metadata — not a curl flag.
  const [zone, setZone] = useState('');

  // Auto-discover on connect (ADR 013 D5). Recurring is opt-in, default Off.
  const [autoDiscover, setAutoDiscover] = useState(true);
  const [discoverSchedule, setDiscoverSchedule] = useState<'off' | 'daily' | 'weekly'>('off');

  // ADR 013 D6: overlap confirmation. Non-null = modal open; the operator can
  // always proceed (overlap is a heuristic, never a block).
  const [overlapPreview, setOverlapPreview] = useState<AllowlistPreview | null>(null);

  const tokenMutation = useMutation({
    mutationFn: () =>
      createInstallToken({
        auto_discover: autoDiscover,
        discover_schedule: discoverSchedule,
        zone: zone.trim() || undefined,
      }),
    onSuccess: (res) => {
      setOverlapPreview(null);
      setInstallToken({ token: res.install_token, expiresAt: res.expires_at });
    },
  });

  // Generate runs the overlap preview first; only ranges actually trigger it.
  const previewMutation = useMutation({
    mutationFn: () => previewAllowlist(cleanAllow, zone.trim() || undefined),
    onSuccess: (res) => {
      if (res.overlaps.length > 0 || res.redundant.length > 0) {
        setOverlapPreview(res); // surface the modal; operator confirms
      } else {
        tokenMutation.mutate(); // clean — mint straight away
      }
    },
  });

  const handleGenerate = () => {
    setInstallToken(null);
    if (cleanAllow.length === 0) {
      tokenMutation.mutate(); // nothing to overlap-check
    } else {
      previewMutation.mutate();
    }
  };

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

  const oneLiner = installToken && apiURL && installScriptURL
    ? `curl -sSL ${installScriptURL} | sudo sh -s -- \\\n  ${[
        `--token=${installToken.token}`,
        `--api-url=${apiURL}`,
        `--name=$(hostname)`,
        ...cleanAllow.map((c) => `--allow-cidr=${shQuote(c)}`),
        `--as-service`,
      ].join(' \\\n  ')}`
    : '';

  return (
    <div>
      <div className="page-header">
        <h1>Agents</h1>
      </div>

      <div className="detail-card" style={{ marginBottom: 24 }}>
        <h2 style={{ marginTop: 0 }}>Install a new agent</h2>
        <p className="muted" style={{ marginTop: 0 }}>
          Generates a one-time install token (valid 1 hour, single use). Paste
          the command on the host that should run the agent. The agent
          registers itself automatically.
        </p>

        <div style={{ margin: '12px 0 16px' }}>
          <label style={{ fontWeight: 600, fontSize: 14 }} htmlFor="agent-zone">
            Zone / site{' '}
            <span className="muted" style={{ fontWeight: 400 }}>(optional)</span>
          </label>
          <input
            id="agent-zone"
            type="text"
            placeholder="office-east"
            value={zone}
            onChange={(e) => setZone(e.target.value)}
            style={{ display: 'block', marginTop: 6, maxWidth: 320 }}
          />
          <p className="muted" style={{ fontSize: 13, marginTop: 6 }}>
            Disambiguates the same private ranges reused at different sites, so the
            overlap check doesn't false-alarm. Stored server-side, not in the command.
          </p>
        </div>

        <div style={{ margin: '12px 0 16px' }}>
          <label style={{ fontWeight: 600, fontSize: 14 }}>
            Allowed targets{' '}
            <span className="muted" style={{ fontWeight: 400 }}>
              (the agent only ever scans these)
            </span>
          </label>
          {allowCidrs.map((c, i) => (
            <div
              key={i}
              style={{ display: 'flex', gap: 8, marginTop: 6, alignItems: 'center' }}
            >
              <input
                type="text"
                placeholder="192.168.0.0/24 · 10.0.0.5 · 10.0.0.10-10.0.0.50 · host.example.com"
                value={c}
                onChange={(e) => updateAllow(i, e.target.value)}
                style={{ flex: '1 1 auto', minWidth: 0 }}
              />
              {allowCidrs.length > 1 && (
                <button
                  type="button"
                  className="btn btn-sm"
                  onClick={() => removeAllow(i)}
                  aria-label="Remove target"
                  style={{ flex: '0 0 auto' }}
                >
                  ×
                </button>
              )}
            </div>
          ))}
          <button type="button" className="btn btn-sm" onClick={addAllow} style={{ marginTop: 8 }}>
            + Add target
          </button>
          <p className="muted" style={{ fontSize: 13, marginTop: 6 }}>
            Seeds the agent's scan allowlist (CIDR, IP, range, or hostname). You can
            edit it later on the host or via the <strong>Allowlist</strong> button below.
            Leave empty to configure it on the host instead.
          </p>
        </div>

        <div style={{ margin: '12px 0 16px' }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 14 }}>
            <input
              type="checkbox"
              checked={autoDiscover}
              onChange={(e) => setAutoDiscover(e.target.checked)}
            />
            <span style={{ fontWeight: 600 }}>Run discovery as soon as it connects</span>
          </label>
          {autoDiscover && (
            <div style={{ marginTop: 6, marginLeft: 24, display: 'flex', alignItems: 'center', gap: 8 }}>
              <span className="muted" style={{ fontSize: 13 }}>then repeat:</span>
              {(['off', 'daily', 'weekly'] as const).map((opt) => (
                <label key={opt} style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 13 }}>
                  <input
                    type="radio"
                    name="discover-schedule"
                    checked={discoverSchedule === opt}
                    onChange={() => setDiscoverSchedule(opt)}
                  />
                  {opt === 'off' ? 'Off' : opt === 'daily' ? 'Daily' : 'Weekly'}
                </label>
              ))}
            </div>
          )}
          <p className="muted" style={{ fontSize: 13, marginTop: 6 }}>
            Discovers across the allowed targets once the agent connects — no scan to set up.
          </p>
        </div>

        <button
          className="btn btn-primary"
          disabled={tokenMutation.isPending || previewMutation.isPending || !apiURL || !installScriptURL}
          onClick={handleGenerate}
        >
          {tokenMutation.isPending || previewMutation.isPending ? 'Generating…' : 'Generate install command'}
        </button>
        {!installScriptURL && (
          <p className="muted" style={{ fontSize: 13 }}>
            Loading the agent release location…
          </p>
        )}
        {tokenMutation.error && (
          <p className="error">{(tokenMutation.error as Error).message}</p>
        )}
        {previewMutation.error && (
          <p className="error">{(previewMutation.error as Error).message}</p>
        )}

        {installToken && oneLiner && (
          <>
            <p className="muted" style={{ marginTop: 16 }}>
              Copy and run on the host (requires sudo). Expires{' '}
              {new Date(installToken.expiresAt).toLocaleString()}.
            </p>
            <CodeBlock content={oneLiner} />
            <p className="muted" style={{ fontSize: 13 }}>
              Drop <code>--as-service</code> to install the binary + credentials only
              (you run silkstrand-agent yourself).
            </p>
          </>
        )}
      </div>

      {downloads && (
        <details style={{ marginBottom: 24 }}>
          <summary style={{ cursor: 'pointer', padding: '6px 0' }}>
            Download binaries directly (advanced)
          </summary>
          <div className="detail-card" style={{ marginTop: 8 }}>
            <ul style={{ margin: 0, paddingLeft: 20 }}>
              {Object.entries(downloads.binaries).map(([platform, url]) => (
                <li key={platform}><a href={url}>{platform}</a></li>
              ))}
            </ul>
          </div>
        </details>
      )}

      {newKey && (
        <div
          className="detail-card"
          style={{ marginBottom: 24, borderColor: '#0f766e' }}
        >
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
      {!isLoading && agents && agents.length === 0 && <p>No agents registered.</p>}

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

      {allowlistFor && (
        <AllowlistModal agent={allowlistFor} onClose={() => setAllowlistFor(null)} />
      )}

      {consoleFor && (
        <ConsoleDrawer agent={consoleFor} onClose={() => setConsoleFor(null)} />
      )}

      {overlapPreview && (
        <div className="modal-backdrop" onClick={() => setOverlapPreview(null)}>
          <div className="form-card" style={{ maxWidth: 560 }} onClick={(e) => e.stopPropagation()}>
            <h3 style={{ marginTop: 0 }}>⚠️ These ranges may already be scanned</h3>
            {overlapPreview.overlaps.length > 0 && (
              <>
                <p style={{ marginBottom: 8 }}>
                  Another agent already discovers across overlapping ranges. If they
                  see the <strong>same network</strong>, you'll scan these hosts twice.
                </p>
                <ul style={{ margin: '0 0 12px', paddingLeft: 18 }}>
                  {overlapPreview.overlaps.map((o, i) => (
                    <li key={i} style={{ marginBottom: 4 }}>
                      <code>{o.cidr}</code> overlaps agent{' '}
                      <strong>{o.conflicts_with.name || o.conflicts_with.id}</strong>{' '}
                      (allowlist <code>{o.conflicts_with.range}</code>
                      {o.conflicts_with.zone ? `, zone ${o.conflicts_with.zone}` : ''})
                    </li>
                  ))}
                </ul>
              </>
            )}
            {overlapPreview.redundant.length > 0 && (
              <>
                <p style={{ marginBottom: 8 }}>Some entries are redundant:</p>
                <ul style={{ margin: '0 0 12px', paddingLeft: 18 }}>
                  {overlapPreview.redundant.map((r, i) => (
                    <li key={i} style={{ marginBottom: 4 }}>{r}</li>
                  ))}
                </ul>
              </>
            )}
            <p className="muted" style={{ fontSize: 13 }}>
              Overlap is a hint, not a rule — the same private range at two sites is
              fine. Add a zone to silence this, or proceed if it's intentional.
            </p>
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
              <button className="btn" onClick={() => setOverlapPreview(null)} disabled={tokenMutation.isPending}>
                Adjust ranges
              </button>
              <button
                className="btn btn-primary"
                disabled={tokenMutation.isPending}
                onClick={() => tokenMutation.mutate()}
              >
                {tokenMutation.isPending ? 'Generating…' : 'Proceed anyway'}
              </button>
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

// CodeBlock — monospace box with a Copy button in the corner.
// Falls back to manual-select when navigator.clipboard isn't available
// (old browsers, non-HTTPS contexts).
function CodeBlock({ content }: { content: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(content);
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    } catch {
      // Ignore — user can still select manually thanks to userSelect: all.
    }
  }
  return (
    <div style={{ position: 'relative' }}>
      <pre
        style={{
          background: '#111', color: '#eee', padding: 12, paddingRight: 64,
          borderRadius: 6, overflowX: 'auto', userSelect: 'all',
          margin: 0,
        }}
      >{content}</pre>
      <button
        type="button"
        onClick={copy}
        className="btn btn-sm"
        style={{
          position: 'absolute', top: 6, right: 6,
          background: '#222', color: '#eee', borderColor: '#333',
        }}
      >
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  );
}
