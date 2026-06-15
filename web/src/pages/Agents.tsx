import { useEffect, useMemo, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Server, ServerOff } from 'lucide-react';
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
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';
import SummaryChips, { type SummarySegment } from '../components/SummaryChips';
import { isUpdateAvailable } from '../lib/agentVersion';

// Status → label + color for the summary chips. connected/online are healthy,
// disconnected/offline are down, pending is in-between. (model has no "stale".)
const AGENT_STATUS_META: Record<string, { label: string; color: string }> = {
  connected: { label: 'Connected', color: 'var(--ss-success)' },
  online: { label: 'Online', color: 'var(--ss-success)' },
  pending: { label: 'Pending', color: 'var(--ss-warning)' },
  disconnected: { label: 'Disconnected', color: 'var(--ss-danger)' },
  offline: { label: 'Offline', color: 'var(--ss-danger)' },
};
const AGENT_STATUS_ORDER = ['connected', 'online', 'pending', 'disconnected', 'offline'];

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
  const [statusFilter, setStatusFilter] = useState('');
  const [newKey, setNewKey] = useState<{ agent: Agent; apiKey: string } | null>(null);

  // Status counts (client-side, over the full list — agents aren't paginated).
  const statusCounts = useMemo(() => {
    const m: Record<string, number> = {};
    for (const a of agents ?? []) m[a.status] = (m[a.status] ?? 0) + 1;
    return m;
  }, [agents]);

  const statusSegments: SummarySegment[] = useMemo(() => {
    const known = AGENT_STATUS_ORDER.filter((s) => statusCounts[s]);
    const extra = Object.keys(statusCounts).filter((s) => !AGENT_STATUS_ORDER.includes(s));
    return [...known, ...extra].map((s) => ({
      key: s,
      label: AGENT_STATUS_META[s]?.label ?? s,
      count: statusCounts[s],
      color: AGENT_STATUS_META[s]?.color ?? 'var(--ss-text-muted)',
      onClick: () => setStatusFilter((prev) => (prev === s ? '' : s)),
    }));
  }, [statusCounts]);

  const filteredAgents = useMemo(
    () => (statusFilter ? (agents ?? []).filter((a) => a.status === statusFilter) : agents ?? []),
    [agents, statusFilter],
  );
  // One unified detail drawer (General + Allowlist + Console + actions), opened
  // by row-click — replaces the former separate AllowlistModal + ConsoleDrawer.
  const [openAgent, setOpenAgent] = useState<Agent | null>(null);
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
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['agents'] });
      setOpenAgent(null); // the agent is gone — close its drawer if open
    },
  });

  // Shared action handlers (used by both the row actions and the drawer footer).
  // triggerUpgrade keeps the in_container branching: binary → in-place upgrade;
  // container/unknown → the recreate modal. Gate on status===connected at call
  // sites (matches existing behavior).
  const triggerUpgrade = (a: Agent) => {
    if (a.in_container === false) {
      if (confirm(`Upgrade ${a.name} to the latest version? The agent will download the new binary, verify it, and restart.`)) {
        upgradeMutation.mutate(a.id);
      }
    } else {
      setRecreateFor(a);
    }
  };
  const doRotate = (a: Agent) => {
    if (confirm(`Rotate the API key for ${a.name}? The current key keeps working until the agent reconnects with the new one.`)) {
      rotateMutation.mutate(a.id);
    }
  };
  const doDelete = (a: Agent) => {
    if (confirm(`Delete agent ${a.name}? This revokes its key immediately.`)) {
      deleteMutation.mutate(a.id);
    }
  };
  const upgradeLabel = (a: Agent) =>
    a.in_container === false ? 'Upgrade' : a.in_container ? 'Recreate from image' : 'Upgrade…';

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

  // Per-row actions. A plain render fn (not a component) so it closes over the
  // mutations/setters without prop-drilling. Rows are now clickable (open the
  // unified detail drawer), so each action button stopPropagation's to avoid
  // also opening the drawer; the same actions live in the drawer footer too.
  const renderActions = (a: Agent) => (
    <div
      style={{
        display: 'flex',
        justifyContent: 'flex-end',
        flexWrap: 'wrap',
        gap: 'var(--ss-space-xs)',
      }}
    >
      {a.status === 'connected' && (
        <button
          className="btn btn-sm"
          disabled={upgradeMutation.isPending}
          title={a.in_container ? 'Container agents upgrade by recreating from the new image' : a.in_container === false ? undefined : "This agent's deployment mode isn't known yet"}
          onClick={(e) => { e.stopPropagation(); triggerUpgrade(a); }}
        >
          {upgradeLabel(a)}
        </button>
      )}
      <button
        className="btn btn-sm"
        disabled={rotateMutation.isPending}
        onClick={(e) => { e.stopPropagation(); doRotate(a); }}
      >
        Rotate key
      </button>
      <button
        className="btn btn-sm btn-danger"
        disabled={deleteMutation.isPending}
        onClick={(e) => { e.stopPropagation(); doDelete(a); }}
      >
        Delete
      </button>
    </div>
  );

  const columns: ColumnDef<Agent>[] = [
    {
      id: 'name',
      header: 'Name',
      accessorFn: (a) => a.name,
      cell: ({ row }) => {
        const a = row.original;
        return (
          <>
            {a.name}
            {a.zone && (
              <span className="muted" style={{ display: 'block', fontSize: 12 }}>
                zone: {a.zone}
              </span>
            )}
          </>
        );
      },
    },
    {
      id: 'id',
      header: 'ID',
      accessorFn: (a) => a.id,
      cell: ({ row }) => (
        <span className="text-muted" style={{ fontSize: 12 }}>{row.original.id.slice(0, 8)}…</span>
      ),
    },
    {
      id: 'status',
      header: 'Status',
      accessorFn: (a) => a.status,
      cell: ({ row }) => {
        const a = row.original;
        return (
          <>
            {a.status}
            {agentHasRunningScan.has(a.id) && (
              <span
                className="badge badge-completed"
                style={{ marginLeft: 'var(--ss-space-sm)', display: 'inline-flex', alignItems: 'center', gap: 'var(--ss-space-xs)' }}
              >
                <span style={{
                  width: 6, height: 6, borderRadius: '50%',
                  background: 'var(--ss-success)',
                  animation: 'pulse 1.5s ease-in-out infinite',
                }} />
                Running scan
              </span>
            )}
          </>
        );
      },
    },
    {
      id: 'version',
      header: 'Version',
      accessorFn: (a) => a.version ?? '',
      cell: ({ row }) => {
        const a = row.original;
        const latest = downloads?.version;
        // isUpdateAvailable normalizes the leading "v" on both sides (binary
        // agents report no-v, container agents v-prefixed, the tag is v-prefixed)
        // and applies the connected / real-version / not-"latest" guards.
        const updateAvailable = isUpdateAvailable(a.version, latest, a.status);
        return (
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--ss-space-xs)' }}>
            {a.version ?? '—'}
            {updateAvailable && (
              <button
                className="badge"
                title={`Update available: ${latest}. Click to upgrade.`}
                style={{
                  background: 'var(--ss-warning)', color: 'var(--ss-text-on-accent)',
                  border: 'none', cursor: 'pointer',
                }}
                onClick={(e) => { e.stopPropagation(); triggerUpgrade(a); }}
              >
                update available
              </button>
            )}
          </span>
        );
      },
    },
    {
      id: 'last_heartbeat',
      header: 'Last heartbeat',
      accessorFn: (a) => a.last_heartbeat ?? '',
      cell: ({ row }) =>
        row.original.last_heartbeat ? new Date(row.original.last_heartbeat).toLocaleString() : '—',
    },
    {
      id: 'actions',
      header: '',
      enableSorting: false,
      cell: ({ row }) => renderActions(row.original),
    },
  ];

  return (
    <div>
      <div className="page-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
          <Server size={24} style={{ color: 'var(--ss-accent-primary)' }} />
          <h1>Agents</h1>
        </div>
        <button className="btn btn-primary" onClick={() => setShowAdd(true)}>+ Add Agent</button>
      </div>

      {newKey && (
        <div className="detail-card" style={{ marginBottom: 'var(--ss-space-xl)', borderColor: 'var(--ss-accent-primary)' }}>
          <h3 style={{ marginTop: 0 }}>New API key for {newKey.agent.name}</h3>
          <p className="muted">Copy this now — it will not be shown again.</p>
          <pre
            style={{
              background: 'var(--ss-bg-raised)', padding: 'var(--ss-space-md)', borderRadius: 'var(--ss-radius-md)',
              userSelect: 'all', overflowX: 'auto',
            }}
          >{newKey.apiKey}</pre>
          <button className="btn btn-sm" onClick={() => setNewKey(null)}>Dismiss</button>
        </div>
      )}

      {isLoading && <p>Loading…</p>}
      {error && <p className="error">{(error as Error).message}</p>}
      {!isLoading && agents && agents.length === 0 && (
        <EmptyState icon={<ServerOff />} title="No agents registered yet.">
          <button className="btn btn-primary" onClick={() => setShowAdd(true)}>+ Add your first agent</button>
        </EmptyState>
      )}

      {agents && agents.length > 0 && (
        <div style={{ marginBottom: 'var(--ss-space-md)' }}>
          <SummaryChips variant="chips" segments={statusSegments} emptyText="No agents." />
          {statusFilter && (
            <span className="muted" style={{ marginLeft: 'var(--ss-space-md)', fontSize: 'var(--ss-text-body-sm)' }}>
              Filtered: {AGENT_STATUS_META[statusFilter]?.label ?? statusFilter}
              {' · '}
              <button
                className="btn btn-sm"
                style={{ padding: '0 var(--ss-space-xs)' }}
                onClick={() => setStatusFilter('')}
              >
                clear
              </button>
            </span>
          )}
        </div>
      )}

      {agents && agents.length > 0 && filteredAgents.length === 0 && (
        <p className="muted">No agents with that status.</p>
      )}

      {filteredAgents.length > 0 && (
        <DataTable
          columns={columns}
          data={filteredAgents}
          getRowId={(a) => a.id}
          onRowClick={(a) => setOpenAgent(a)}
          initialSorting={[{ id: 'name', desc: false }]}
        />
      )}

      {showAdd && (
        <AddAgentModal
          apiURL={apiURL}
          installScriptURL={installScriptURL}
          binaries={downloads?.binaries}
          onClose={() => setShowAdd(false)}
        />
      )}

      {openAgent && (
        <AgentDetailDrawer
          agent={openAgent}
          onClose={() => setOpenAgent(null)}
          onUpgrade={() => triggerUpgrade(openAgent)}
          onRotate={() => doRotate(openAgent)}
          onDelete={() => doDelete(openAgent)}
          upgradeLabel={upgradeLabel(openAgent)}
          upgrading={upgradeMutation.isPending}
          rotating={rotateMutation.isPending}
          deleting={deleteMutation.isPending}
        />
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
            <p style={{ margin: 'var(--ss-space-md) 0 var(--ss-space-xs)', fontWeight: 600, fontSize: 14 }}>Container (docker)</p>
            <CodeBlock
              content={`curl -sSL ${installScriptURL || 'https://downloads.silkstrand.io/agent/install.sh'} | sudo sh -s -- \\\n  --mode=docker --upgrade --version=${downloads?.version ?? 'latest'}`}
            />
            <p className="muted" style={{ fontSize: 13 }}>
              Credentials, networks, and proxy/CA settings carry over from the
              existing container — no re-bootstrap. Reconnects on the new version in ~30s.
            </p>
            {recreateFor.in_container !== true && (
              <>
                <p style={{ margin: 'var(--ss-space-lg) 0 var(--ss-space-xs)', fontWeight: 600, fontSize: 14 }}>Binary install</p>
                <button
                  className="btn btn-primary btn-sm"
                  disabled={upgradeMutation.isPending}
                  onClick={() => { upgradeMutation.mutate(recreateFor.id); setRecreateFor(null); }}
                >
                  Upgrade in place
                </button>
                <p className="muted" style={{ fontSize: 13, marginTop: 'var(--ss-space-xs)' }}>
                  The agent downloads the new binary, verifies it, and restarts (~30s).
                </p>
              </>
            )}
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 'var(--ss-space-lg)' }}>
              <button className="btn" onClick={() => setRecreateFor(null)}>Close</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// Unified agent detail drawer — consolidates the former separate Console drawer
// + Allowlist modal into one right-side drawer (opened by row-click), with a
// General section and footer actions. Closes on Esc / backdrop / button.
function AgentDetailDrawer({
  agent,
  onClose,
  onUpgrade,
  onRotate,
  onDelete,
  upgradeLabel,
  upgrading,
  rotating,
  deleting,
}: {
  agent: Agent;
  onClose: () => void;
  onUpgrade: () => void;
  onRotate: () => void;
  onDelete: () => void;
  upgradeLabel: string;
  upgrading: boolean;
  rotating: boolean;
  deleting: boolean;
}) {
  // Esc to close — design-system.md § 5.9 (non-destructive dismiss).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer drawer-wide" role="dialog" aria-label={`Agent — ${agent.name}`}>
        <header className="drawer-header">
          <h2>{agent.name}</h2>
          <button type="button" className="btn btn-sm" onClick={onClose}>×</button>
        </header>
        <div className="drawer-body">
          <section>
            <h3>General</h3>
            <dl className="kv">
              <dt>Name</dt>
              <dd>{agent.name}</dd>
              <dt>ID</dt>
              <dd style={{ fontFamily: 'monospace', fontSize: 12 }}>{agent.id}</dd>
              <dt>Status</dt>
              <dd>{agent.status}</dd>
              {agent.version && (<><dt>Version</dt><dd>{agent.version}</dd></>)}
              {agent.zone && (<><dt>Zone</dt><dd>{agent.zone}</dd></>)}
              {agent.last_heartbeat && (
                <><dt>Last heartbeat</dt><dd>{new Date(agent.last_heartbeat).toLocaleString()}</dd></>
              )}
              <dt>Created</dt>
              <dd>{new Date(agent.created_at).toLocaleString()}</dd>
            </dl>
          </section>

          <section style={{ marginTop: 'var(--ss-space-lg)' }}>
            <h3>Scan allowlist</h3>
            <AllowlistSection agent={agent} />
          </section>

          <section style={{ marginTop: 'var(--ss-space-lg)' }}>
            <h3>Agent log</h3>
            <p className="muted" style={{ marginTop: 0 }}>
              Live tail of info-and-above log lines from this agent. Debug lines
              stay in the host log file; lines before you opened this aren't replayed.
            </p>
            <AgentLogConsole filter={{ agentId: agent.id }} />
          </section>

          <section style={{ marginTop: 'var(--ss-space-lg)' }}>
            <h3>Actions</h3>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 'var(--ss-space-xs)' }}>
              {agent.status === 'connected' && (
                <button className="btn btn-sm" disabled={upgrading} onClick={onUpgrade}>
                  {upgradeLabel}
                </button>
              )}
              <button className="btn btn-sm" disabled={rotating} onClick={onRotate}>Rotate key</button>
              <button className="btn btn-sm btn-danger" disabled={deleting} onClick={onDelete}>Delete</button>
            </div>
          </section>
        </div>
      </aside>
    </>
  );
}

// AllowlistSection — the per-agent scan-allowlist viewer (formerly AllowlistModal's
// body), now an inline drawer section. SilkStrand can't edit the allowlist; this
// shows the most recent snapshot the agent reported over the tunnel.
function AllowlistSection({ agent }: { agent: Agent }) {
  const { data, isLoading, error } = useQuery<AgentAllowlist>({
    queryKey: ['agent-allowlist', agent.id],
    queryFn: () => getAgentAllowlist(agent.id),
    retry: false,
  });

  const notReported = error && /not reported/i.test((error as Error).message);

  return (
    <>
      <p className="muted" style={{ marginTop: 0 }}>
        The scan allowlist lives on the agent host at{' '}
        <code>/etc/silkstrand/scan-allowlist.yaml</code>. SilkStrand cannot edit
        it — this shows the most recent snapshot the agent reported over the
        tunnel. Edit the file on the host to change what it's willing to scan.
      </p>
      {isLoading && <p>Loading…</p>}
      {notReported && (
        <p className="muted">
          This agent has not reported an allowlist yet. It may be running an
          older binary, or have no allowlist file configured.
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
          <section style={{ marginTop: 'var(--ss-space-lg)' }}>
            <h3>Allow ({data.allow.length})</h3>
            {data.allow.length === 0 ? (
              <p className="muted">Empty — the agent will refuse every scan directive.</p>
            ) : (
              <ul style={{ fontFamily: 'monospace', fontSize: 13 }}>
                {data.allow.map((rule) => (<li key={rule}>{rule}</li>))}
              </ul>
            )}
          </section>
          {data.deny.length > 0 && (
            <section style={{ marginTop: 'var(--ss-space-lg)' }}>
              <h3>Deny ({data.deny.length})</h3>
              <ul style={{ fontFamily: 'monospace', fontSize: 13 }}>
                {data.deny.map((rule) => (<li key={rule}>{rule}</li>))}
              </ul>
            </section>
          )}
        </>
      )}
    </>
  );
}
