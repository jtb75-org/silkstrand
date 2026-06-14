import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Target as TargetIcon, Crosshair } from 'lucide-react';
import { listTargets, createTarget, deleteTarget, listAgents, probeTarget, updateTarget } from '../api/client';
import type { Target, TargetType, CreateTargetRequest, UpdateTargetRequest, Agent } from '../api/types';
import CredentialModal from '../components/CredentialModal';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';

// Supported technologies for the target creation form. The `type` value is
// what the API stores (and what the prober / bundle matcher dispatch on).
const TECHNOLOGIES: {
  value: TargetType;
  label: string;
  defaults: Record<string, unknown>;
}[] = [
  { value: 'postgresql',        label: 'PostgreSQL',
    defaults: { host: '', port: 5432, database: 'postgres', sslmode: 'prefer' } },
  { value: 'aurora_postgresql', label: 'Aurora PostgreSQL',
    defaults: { host: '', port: 5432, database: 'postgres', sslmode: 'require' } },
  { value: 'mssql',             label: 'Microsoft SQL Server',
    defaults: { host: '', port: 1433, database: 'master', encrypt: true } },
  { value: 'mongodb',           label: 'MongoDB',
    defaults: { host: '', port: 27017, auth_source: 'admin', tls: false } },
  { value: 'mysql',             label: 'MySQL / MariaDB',
    defaults: { host: '', port: 3306, database: '' } },
  { value: 'aurora_mysql',      label: 'Aurora MySQL',
    defaults: { host: '', port: 3306, database: '' } },
];

// Discovery target types (ADR 003 R1a). The identifier shape varies —
// CIDR for subnets, an A-B range for host ranges, a bare IP/hostname for
// single hosts. The agent's allowlist YAML is the ultimate gate; these
// type labels just pick the right placeholder.
type DiscoveryType = 'cidr' | 'network_range' | 'host';
const DISCOVERY_TYPES: { value: DiscoveryType; label: string; placeholder: string }[] = [
  { value: 'cidr',          label: 'CIDR',          placeholder: '10.0.0.0/16' },
  { value: 'network_range', label: 'IP range',      placeholder: '10.0.0.1-10.0.0.254' },
  { value: 'host',          label: 'Single host',   placeholder: '10.0.0.5 or db.example.com' },
];

type Kind = 'compliance' | 'discovery';

interface DiscoveryCfg {
  ports: string;
  ratePps: string;
  includeHttpx: boolean;
  includeNuclei: boolean;
}

const DISCOVERY_DEFAULTS: DiscoveryCfg = {
  ports: '',
  ratePps: '',
  includeHttpx: true,
  includeNuclei: true,
};

export default function Targets() {
  const queryClient = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  const [credTarget, setCredTarget] = useState<Target | null>(null);
  const [probeBusy, setProbeBusy] = useState<Record<string, boolean>>({});
  const [probeResult, setProbeResult] = useState<Record<string, { ok: boolean; msg: string }>>({});

  // Form state — technology-specific config is a free-form dict that
  // mirrors the config JSON each driver will see on the agent side.
  const [editingId, setEditingId] = useState<string | null>(null);
  const [kind, setKind] = useState<Kind>('compliance');
  const [techType, setTechType] = useState<TargetType>('postgresql');
  const [discoveryType, setDiscoveryType] = useState<DiscoveryType>('cidr');
  const [identifier, setIdentifier] = useState('');
  const [agentId, setAgentId] = useState<string>('');
  const [environment, setEnvironment] = useState('');
  const [config, setConfig] = useState<Record<string, unknown>>(TECHNOLOGIES[0].defaults);
  const [discoveryCfg, setDiscoveryCfg] = useState<DiscoveryCfg>(DISCOVERY_DEFAULTS);

  const { data: targets, isLoading, error } = useQuery<Target[]>({
    queryKey: ['targets'],
    queryFn: listTargets,
  });

  const { data: agents } = useQuery<Agent[]>({
    queryKey: ['agents'],
    queryFn: listAgents,
  });

  function resetForm() {
    setEditingId(null);
    setKind('compliance');
    setTechType('postgresql');
    setDiscoveryType('cidr');
    setIdentifier('');
    setAgentId('');
    setEnvironment('');
    setConfig(TECHNOLOGIES[0].defaults);
    setDiscoveryCfg(DISCOVERY_DEFAULTS);
  }

  const createMutation = useMutation({
    mutationFn: (req: CreateTargetRequest) => createTarget(req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['targets'] });
      setShowForm(false);
      resetForm();
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, req }: { id: string; req: UpdateTargetRequest }) => updateTarget(id, req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['targets'] });
      setShowForm(false);
      resetForm();
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteTarget(id),
    onSuccess: () => { queryClient.invalidateQueries({ queryKey: ['targets'] }); },
  });

  const assignAgentMutation = useMutation({
    mutationFn: ({ id, agentId }: { id: string; agentId: string | undefined }) =>
      updateTarget(id, { agent_id: agentId }),
    onSuccess: () => { queryClient.invalidateQueries({ queryKey: ['targets'] }); },
  });

  function handleTechChange(newType: TargetType) {
    setTechType(newType);
    const tech = TECHNOLOGIES.find((t) => t.value === newType);
    setConfig(tech ? { ...tech.defaults } : {});
  }

  function setConfigField(key: string, value: unknown) {
    setConfig((prev) => ({ ...prev, [key]: value }));
  }

  function buildPayload(): CreateTargetRequest {
    if (kind === 'discovery') {
      const cfg: Record<string, unknown> = {
        include_httpx: discoveryCfg.includeHttpx,
        include_nuclei: discoveryCfg.includeNuclei,
      };
      const ports = discoveryCfg.ports.trim();
      if (ports) cfg.ports = ports;
      const rate = parseInt(discoveryCfg.ratePps, 10);
      if (!isNaN(rate) && rate > 0) cfg.rate_pps = rate;
      return {
        type: discoveryType,
        identifier,
        environment: environment || undefined,
        agent_id: agentId || undefined,
        config: cfg,
      };
    }
    return {
      type: techType,
      identifier,
      environment: environment || undefined,
      agent_id: agentId || undefined,
      config,
    };
  }

  function handleSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const p = buildPayload();
    if (editingId) {
      // Type stays locked on edit (delete + recreate to change shape).
      // Empty-string env or agent_id collapses to "don't change" server-side;
      // clearing them isn't supported in v1.
      updateMutation.mutate({
        id: editingId,
        req: {
          identifier: p.identifier,
          environment: p.environment,
          agent_id: p.agent_id,
          config: p.config,
        },
      });
      return;
    }
    createMutation.mutate(p);
  }

  function handleEdit(t: Target) {
    const tKind: Kind = isDiscoveryType(t.type) ? 'discovery' : 'compliance';
    setEditingId(t.id);
    setKind(tKind);
    setIdentifier(t.identifier);
    setAgentId(t.agent_id ?? '');
    setEnvironment(t.environment ?? '');
    if (tKind === 'compliance') {
      setTechType(t.type as TargetType);
      const cfgObj = (t.config ?? {}) as Record<string, unknown>;
      const tech = TECHNOLOGIES.find((x) => x.value === t.type);
      setConfig({ ...(tech?.defaults ?? {}), ...cfgObj });
    } else {
      setDiscoveryType(t.type as DiscoveryType);
      const cfgObj = (t.config ?? {}) as Record<string, unknown>;
      setDiscoveryCfg({
        ports: typeof cfgObj.ports === 'string' ? cfgObj.ports : '',
        ratePps: typeof cfgObj.rate_pps === 'number' ? String(cfgObj.rate_pps) : '',
        includeHttpx: cfgObj.include_httpx !== false,
        includeNuclei: cfgObj.include_nuclei !== false,
      });
    }
    setShowForm(true);
  }

  async function handleProbe(id: string) {
    setProbeBusy((p) => ({ ...p, [id]: true }));
    setProbeResult((p) => ({ ...p, [id]: { ok: false, msg: 'Testing…' } }));
    try {
      const r = await probeTarget(id);
      const msg = r.ok
        ? (r.detail ? r.detail.slice(0, 80) : 'OK')
        : (r.error || 'Failed');
      setProbeResult((p) => ({ ...p, [id]: { ok: r.ok, msg } }));
    } catch (e) {
      setProbeResult((p) => ({ ...p, [id]: { ok: false, msg: (e as Error).message } }));
    } finally {
      setProbeBusy((p) => ({ ...p, [id]: false }));
    }
  }

  function handleDelete(id: string) {
    if (window.confirm('Delete this target? Credentials and scan history will be removed.')) {
      deleteMutation.mutate(id);
    }
  }

  // Per-row actions. A plain render fn (closes over the mutations/probe state)
  // rather than an inner component. Targets has no selection + no single
  // row-detail action, so rows are non-clickable — the buttons carry their own
  // handlers and need no stopPropagation (no row click to bubble into).
  const renderActions = (t: Target) => (
    <div style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', flexWrap: 'wrap', gap: 'var(--ss-space-xs)' }}>
      {probeResult[t.id] && (
        <span
          style={{ fontSize: 12, color: probeResult[t.id].ok ? 'var(--ss-success)' : 'var(--ss-danger)' }}
          title={probeResult[t.id].msg}
        >
          {probeResult[t.id].ok
            ? '✓ connected'
            : `✗ ${probeResult[t.id].msg.slice(0, 40)}${probeResult[t.id].msg.length > 40 ? '…' : ''}`}
        </span>
      )}
      {!isDiscoveryType(t.type) && (
        <>
          <button
            className="btn btn-sm"
            onClick={() => handleProbe(t.id)}
            disabled={probeBusy[t.id]}
            title="Send a lightweight connection test through the agent"
          >
            {probeBusy[t.id] ? 'Testing…' : 'Test'}
          </button>
          <button className="btn btn-sm" onClick={() => setCredTarget(t)}>Credential</button>
        </>
      )}
      <button className="btn btn-sm" onClick={() => handleEdit(t)}>Edit</button>
      <button className="btn btn-danger btn-sm" onClick={() => handleDelete(t.id)} disabled={deleteMutation.isPending}>
        Delete
      </button>
    </div>
  );

  const columns: ColumnDef<Target>[] = [
    {
      id: 'technology',
      header: 'Technology',
      accessorFn: (t) => techLabel(t.type),
      cell: ({ row }) => <span className="badge badge-type">{techLabel(row.original.type)}</span>,
    },
    { id: 'name', header: 'Name', accessorFn: (t) => t.identifier },
    { id: 'environment', header: 'Environment', accessorFn: (t) => t.environment || '-' },
    {
      // Inline agent reassignment select — interactive cell, non-sortable.
      id: 'agent',
      header: 'Agent',
      enableSorting: false,
      cell: ({ row }) => {
        const t = row.original;
        return (
          <select
            value={t.agent_id ?? ''}
            onChange={(e) => assignAgentMutation.mutate({ id: t.id, agentId: e.target.value || undefined })}
            disabled={assignAgentMutation.isPending}
            title="Assign an agent to this target"
          >
            <option value="">— unassigned —</option>
            {agents?.map((a) => (
              <option key={a.id} value={a.id}>{a.name} ({a.status})</option>
            ))}
          </select>
        );
      },
    },
    {
      id: 'created',
      header: 'Created',
      accessorFn: (t) => t.created_at,
      cell: ({ row }) => new Date(row.original.created_at).toLocaleString(),
    },
    { id: 'actions', header: '', enableSorting: false, cell: ({ row }) => renderActions(row.original) },
  ];

  return (
    <div>
      <div className="page-header">
        <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
          <TargetIcon size={24} style={{ color: 'var(--ss-accent-primary)' }} />
          <h1>Targets</h1>
        </div>
        <button
          className="btn btn-primary"
          onClick={() => {
            if (showForm) {
              setShowForm(false);
              resetForm();
            } else {
              setShowForm(true);
            }
          }}
        >
          {showForm ? 'Cancel' : 'New Target'}
        </button>
      </div>

      {showForm && (
        <form className="form-card" onSubmit={handleSubmit}>
          {editingId && (
            <h3 style={{ marginTop: 0 }}>Edit target</h3>
          )}
          <div className="form-group">
            <label>Kind</label>
            <div style={{ display: 'flex', gap: 'var(--ss-space-lg)' }}>
              <label style={{ fontWeight: 'normal' }}>
                <input
                  type="radio"
                  name="kind"
                  value="compliance"
                  checked={kind === 'compliance'}
                  onChange={() => setKind('compliance')}
                  disabled={!!editingId}
                />{' '}Compliance (database engine)
              </label>
              <label style={{ fontWeight: 'normal' }}>
                <input
                  type="radio"
                  name="kind"
                  value="discovery"
                  checked={kind === 'discovery'}
                  onChange={() => { setKind('discovery'); setIdentifier(''); }}
                  disabled={!!editingId}
                />{' '}Discovery (network range)
              </label>
            </div>
            {editingId && (
              <p className="muted" style={{ fontSize: 13, marginTop: 'var(--ss-space-xs)' }}>
                Kind and type are locked on edit — delete and recreate to change target shape.
              </p>
            )}
            {kind === 'discovery' && (
              <p className="muted" style={{ fontSize: 13, marginTop: 'var(--ss-space-xs)' }}>
                Discovery targets are fed to naabu → httpx → nuclei when the
                Scans page kicks a discovery scan. The agent's allowlist YAML
                is the ultimate gate — anything outside it is refused.
              </p>
            )}
          </div>

          {kind === 'compliance' && (
            <div className="form-group">
              <label htmlFor="tech">Technology</label>
              <select
                id="tech"
                value={techType}
                onChange={(e) => handleTechChange(e.target.value as TargetType)}
                required
                disabled={!!editingId}
              >
                {TECHNOLOGIES.map((t) => (
                  <option key={t.value} value={t.value}>{t.label}</option>
                ))}
              </select>
            </div>
          )}

          {kind === 'discovery' && (
            <div className="form-group">
              <label htmlFor="discovery-type">Discovery target type</label>
              <select
                id="discovery-type"
                value={discoveryType}
                onChange={(e) => setDiscoveryType(e.target.value as DiscoveryType)}
                required
                disabled={!!editingId}
              >
                {DISCOVERY_TYPES.map((t) => (
                  <option key={t.value} value={t.value}>{t.label}</option>
                ))}
              </select>
            </div>
          )}

          <div className="form-group">
            <label htmlFor="identifier">
              {kind === 'discovery' ? 'Target' : 'Name'}
            </label>
            <input
              id="identifier"
              type="text"
              required
              placeholder={
                kind === 'discovery'
                  ? DISCOVERY_TYPES.find((t) => t.value === discoveryType)?.placeholder
                  : 'e.g. studio-prod-postgres'
              }
              value={identifier}
              onChange={(e) => setIdentifier(e.target.value)}
            />
          </div>

          <div className="form-group">
            <label htmlFor="agent">Agent</label>
            <select
              id="agent"
              value={agentId}
              onChange={(e) => setAgentId(e.target.value)}
            >
              <option value="">— none (unassigned) —</option>
              {agents?.map((a) => (
                <option key={a.id} value={a.id}>{a.name} ({a.status})</option>
              ))}
            </select>
          </div>

          <div className="form-group">
            <label htmlFor="environment">Environment</label>
            <input
              id="environment"
              type="text"
              placeholder="e.g. production, staging (optional)"
              value={environment}
              onChange={(e) => setEnvironment(e.target.value)}
            />
          </div>

          {kind === 'compliance' && (
            <ConnectionFields techType={techType} config={config} onChange={setConfigField} />
          )}

          {kind === 'discovery' && (
            <DiscoveryOptions value={discoveryCfg} onChange={setDiscoveryCfg} />
          )}

          {kind === 'compliance' && (
            <p className="muted" style={{ fontSize: 13, marginTop: -6 }}>
              After creating, click <strong>Credential</strong> on the row to set the
              username and password the agent will use to connect.
            </p>
          )}
          {kind === 'discovery' && (
            <p className="muted" style={{ fontSize: 13, marginTop: -6 }}>
              After creating, start a <strong>Discovery</strong> scan from the
              Scans page to populate the Assets inventory for this target.
            </p>
          )}
          <button
            type="submit"
            className="btn btn-primary"
            disabled={createMutation.isPending || updateMutation.isPending}
          >
            {(createMutation.isPending || updateMutation.isPending)
              ? 'Saving…'
              : editingId ? 'Save changes' : 'Create Target'}
          </button>
          {(createMutation.error || updateMutation.error) && (
            <p className="error">
              {((createMutation.error ?? updateMutation.error) as Error).message}
            </p>
          )}
        </form>
      )}

      {isLoading && <p>Loading…</p>}
      {error && <p className="error">Failed to load targets: {(error as Error).message}</p>}
      {!isLoading && !error && targets && targets.length === 0 && (
        <EmptyState icon={<Crosshair />} title="No targets yet.">
          <button className="btn btn-primary" onClick={() => setShowForm(true)}>New Target</button>
        </EmptyState>
      )}
      {targets && targets.length > 0 && (
        <DataTable
          columns={columns}
          data={targets}
          getRowId={(t) => t.id}
          initialSorting={[{ id: 'name', desc: false }]}
        />
      )}

      {credTarget && (
        <CredentialModal target={credTarget} onClose={() => setCredTarget(null)} />
      )}
    </div>
  );
}

function isDiscoveryType(type: string): boolean {
  return DISCOVERY_TYPES.some((d) => d.value === type);
}

function techLabel(type: string): string {
  const t = TECHNOLOGIES.find((x) => x.value === type);
  if (t) return t.label;
  const d = DISCOVERY_TYPES.find((x) => x.value === type);
  if (d) return d.label;
  return type;
}

function DiscoveryOptions({
  value, onChange,
}: {
  value: DiscoveryCfg;
  onChange: (next: DiscoveryCfg) => void;
}) {
  return (
    <>
      <div className="form-group">
        <label htmlFor="ports">Ports (optional)</label>
        <input
          id="ports"
          type="text"
          placeholder="e.g. 80,443,5432 or 1-1000 (leave blank for naabu defaults)"
          value={value.ports}
          onChange={(e) => onChange({ ...value, ports: e.target.value })}
        />
      </div>
      <div className="form-group">
        <label htmlFor="rate_pps">Rate (packets/sec, optional)</label>
        <input
          id="rate_pps"
          type="number"
          min={1}
          placeholder="agent allowlist caps this — blank uses the cap"
          value={value.ratePps}
          onChange={(e) => onChange({ ...value, ratePps: e.target.value })}
        />
      </div>
      <div className="form-group">
        <label style={{ fontWeight: 'normal' }}>
          <input
            type="checkbox"
            checked={value.includeHttpx}
            onChange={(e) => onChange({ ...value, includeHttpx: e.target.checked })}
          />{' '}Run httpx (service fingerprinting on open HTTP(S) ports)
        </label>
      </div>
      <div className="form-group">
        <label style={{ fontWeight: 'normal' }}>
          <input
            type="checkbox"
            checked={value.includeNuclei}
            onChange={(e) => onChange({ ...value, includeNuclei: e.target.checked })}
          />{' '}Run nuclei (CVE / tech detection templates)
        </label>
      </div>
    </>
  );
}

function ConnectionFields({
  techType, config, onChange,
}: {
  techType: TargetType;
  config: Record<string, unknown>;
  onChange: (key: string, value: unknown) => void;
}) {
  // Shared host+port row appears for every DB engine.
  const hostPort = (
    <>
      <div className="form-group">
        <label htmlFor="host">Host</label>
        <input
          id="host"
          type="text"
          required
          placeholder="e.g. 10.0.0.5 or db.example.internal"
          value={(config.host as string) || ''}
          onChange={(e) => onChange('host', e.target.value)}
        />
      </div>
      <div className="form-group">
        <label htmlFor="port">Port</label>
        <input
          id="port"
          type="number"
          required
          value={(config.port as number) ?? ''}
          onChange={(e) => onChange('port', Number(e.target.value))}
        />
      </div>
    </>
  );

  switch (techType) {
    case 'postgresql':
    case 'aurora_postgresql':
      return (
        <>
          {hostPort}
          <div className="form-group">
            <label htmlFor="database">Database</label>
            <input
              id="database"
              type="text"
              value={(config.database as string) || ''}
              onChange={(e) => onChange('database', e.target.value)}
            />
          </div>
          <div className="form-group">
            <label htmlFor="sslmode">SSL mode</label>
            <select
              id="sslmode"
              value={(config.sslmode as string) || 'prefer'}
              onChange={(e) => onChange('sslmode', e.target.value)}
            >
              <option value="disable">disable</option>
              <option value="allow">allow</option>
              <option value="prefer">prefer</option>
              <option value="require">require</option>
              <option value="verify-ca">verify-ca</option>
              <option value="verify-full">verify-full</option>
            </select>
          </div>
        </>
      );

    case 'mssql':
      return (
        <>
          {hostPort}
          <div className="form-group">
            <label htmlFor="database">Database</label>
            <input
              id="database"
              type="text"
              value={(config.database as string) || ''}
              onChange={(e) => onChange('database', e.target.value)}
            />
          </div>
          <div className="form-group">
            <label>
              <input
                type="checkbox"
                checked={Boolean(config.encrypt)}
                onChange={(e) => onChange('encrypt', e.target.checked)}
              />
              {' '}Encrypt connection (TLS)
            </label>
          </div>
        </>
      );

    case 'mongodb':
      return (
        <>
          {hostPort}
          <div className="form-group">
            <label htmlFor="auth_source">Auth database</label>
            <input
              id="auth_source"
              type="text"
              value={(config.auth_source as string) || 'admin'}
              onChange={(e) => onChange('auth_source', e.target.value)}
            />
          </div>
          <div className="form-group">
            <label>
              <input
                type="checkbox"
                checked={Boolean(config.tls)}
                onChange={(e) => onChange('tls', e.target.checked)}
              />
              {' '}TLS
            </label>
          </div>
        </>
      );

    case 'mysql':
    case 'aurora_mysql':
      return (
        <>
          {hostPort}
          <div className="form-group">
            <label htmlFor="database">Database</label>
            <input
              id="database"
              type="text"
              value={(config.database as string) || ''}
              onChange={(e) => onChange('database', e.target.value)}
            />
          </div>
        </>
      );

    default:
      return null;
  }
}
