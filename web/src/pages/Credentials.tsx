import { useMemo, useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { KeyRound, Bell, Lock } from 'lucide-react';
import {
  bulkCreateCredentialMappings,
  bulkCreateEndpointMappings,
  bulkCreateAssetMappings,
  createCredentialSource,
  deleteCredentialMapping,
  deleteCredentialSource,
  listAgents,
  listAssetEndpoints,
  listAssets,
  listCollections,
  listCredentialMappings,
  listCredentialSources,
  testCredentialSource,
  updateCredentialSource,
  type CredentialMapping,
  type CredentialSource,
  type CredentialSourceType,
} from '../api/client';
import type { Agent } from '../api/types';
import DataTable from '../components/DataTable';

// Consolidated Credentials surface (P5-b). Three sections, one page:
//
//   - DB / host auth   -> type='static'
//   - Integrations     -> type in (slack, webhook, email, pagerduty)
//   - Vaults           -> type in (aws_secrets_manager, hashicorp_vault, cyberark)
//
// Integrations absorbs the old NotificationChannels page 1:1. Vaults is
// plumbing-only -- the backend persists config JSONB but the resolver
// fetch-path returns 501 until ADR 004 C1+ resolvers ship.

const INTEGRATION_TYPES: CredentialSourceType[] = ['slack', 'webhook', 'email', 'pagerduty'];
const VAULT_TYPES: CredentialSourceType[] = [
  'aws_secrets_manager',
  'hashicorp_vault',
  'cyberark',
];

export default function Credentials() {
  const { data: sources, isLoading, error } = useQuery({
    queryKey: ['credential-sources'],
    queryFn: () => listCredentialSources(),
  });

  const byType = useMemo(() => {
    const g: Record<string, CredentialSource[]> = {};
    for (const s of sources ?? []) (g[s.type] ??= []).push(s);
    return g;
  }, [sources]);

  const staticSources = byType['static'] ?? [];
  const integrationSources = INTEGRATION_TYPES.flatMap((t) => byType[t] ?? []);
  const vaultSources = VAULT_TYPES.flatMap((t) => byType[t] ?? []);

  return (
    <div>
      {isLoading && <p>Loading...</p>}
      {error && <p className="error">{(error as Error).message}</p>}

      <Section
        title="DB / host auth"
        icon={<KeyRound size={20} style={{ color: 'var(--ss-accent-primary)' }} />}
        description="Static credentials used to authenticate compliance scans against databases and hosts."
        sources={staticSources}
        allowedTypes={['static']}
        supportsMappings
      />

      <Section
        title="Integrations"
        icon={<Bell size={20} style={{ color: 'var(--ss-accent-primary)' }} />}
        description="Notification channels and webhooks. Triggered by correlation-rule actions."
        sources={integrationSources}
        allowedTypes={INTEGRATION_TYPES}
      />

      <Section
        title="Vaults"
        icon={<Lock size={20} style={{ color: 'var(--ss-accent-primary)' }} />}
        description="External secret resolvers. AWS Secrets Manager and HashiCorp Vault are live; CyberArk is coming soon."
        sources={vaultSources}
        allowedTypes={VAULT_TYPES}
        supportsMappings
        testableTypes={['aws_secrets_manager', 'hashicorp_vault']}
      />
    </div>
  );
}

interface SectionProps {
  title: string;
  icon: ReactNode;
  description: string;
  sources: CredentialSource[];
  allowedTypes: CredentialSourceType[];
  supportsMappings?: boolean;
  testableTypes?: string[];
}

// Which inline panel is expanded for which source. The legacy table rendered
// these as sibling <tr> rows; DataTable owns its <tbody> and has no row-expand
// API (and we don't widen the locked contract), so the per-row Test / Edit /
// Map panels now render below the table for the single active source.
type ActivePanel =
  | { sourceId: string; kind: 'edit' | 'test' | 'map-collection' | 'map-asset_endpoint' | 'map-asset' }
  | null;

function Section({ title, icon, description, sources, allowedTypes, supportsMappings, testableTypes }: SectionProps) {
  const queryClient = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  const [active, setActive] = useState<ActivePanel>(null);

  const deleteMut = useMutation({
    mutationFn: deleteCredentialSource,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['credential-sources'] }),
  });

  // One mappings query per section (was per-row); filtered per source in the
  // Mappings cell + the active Map panel. Only needed when this section maps.
  const { data: mappings } = useQuery({
    queryKey: ['credential-mappings'],
    queryFn: listCredentialMappings,
    enabled: !!supportsMappings,
  });

  const columns: ColumnDef<CredentialSource>[] = [
    {
      id: 'name',
      header: 'Name',
      accessorFn: (s) => s.name ?? '',
      cell: ({ row }) => row.original.name || <span className="muted">--</span>,
    },
    {
      id: 'type',
      header: 'Type',
      accessorFn: (s) => s.type,
      cell: ({ row }) => <span className={`badge badge-type-${row.original.type}`}>{row.original.type}</span>,
    },
    {
      id: 'config',
      header: 'Config',
      enableSorting: false,
      // Secrets never render plaintext — renderConfigSummary masks passwords and
      // shows only '(set)'/type for everything else.
      cell: ({ row }) => (
        <span style={{ fontFamily: 'monospace', fontSize: 12 }}>{renderConfigSummary(row.original)}</span>
      ),
    },
    {
      id: 'created',
      header: 'Created',
      accessorFn: (s) => s.created_at,
      cell: ({ row }) => <span style={{ fontSize: 12 }}>{new Date(row.original.created_at).toLocaleDateString()}</span>,
    },
    ...(supportsMappings
      ? [{
          id: 'mappings',
          header: 'Mappings',
          enableSorting: false,
          cell: ({ row }: { row: { original: CredentialSource } }) => {
            const mapped = (mappings ?? []).filter((m) => m.credential_source_id === row.original.id);
            return (
              <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
                <span>{mapped.length} mapped</span>
                <select
                  value=""
                  onChange={(e) => {
                    const v = e.target.value as 'collection' | 'asset_endpoint' | 'asset' | '';
                    if (v) setActive({ sourceId: row.original.id, kind: `map-${v}` });
                  }}
                  style={{ fontSize: 12 }}
                >
                  <option value="">Map to...</option>
                  <option value="asset_endpoint">Endpoint</option>
                  <option value="asset">Asset</option>
                  <option value="collection">Collection</option>
                </select>
              </div>
            );
          },
        } as ColumnDef<CredentialSource>]
      : []),
    {
      id: 'actions',
      header: '',
      enableSorting: false,
      cell: ({ row }) => {
        const s = row.original;
        const testable = testableTypes?.includes(s.type) ?? false;
        const editing = active?.sourceId === s.id && active.kind === 'edit';
        return (
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 'var(--ss-space-xs)' }}>
            {testable && (
              <button className="btn btn-sm" onClick={() => setActive({ sourceId: s.id, kind: 'test' })}>
                Test
              </button>
            )}
            <button
              className="btn btn-sm"
              onClick={() => setActive(editing ? null : { sourceId: s.id, kind: 'edit' })}
            >
              {editing ? 'Cancel' : 'Edit'}
            </button>
            <button
              className="btn btn-sm btn-danger"
              onClick={() => {
                if (!window.confirm(`Delete ${s.name || s.type} credential source?`)) return;
                deleteMut.mutate(s.id);
              }}
            >
              Delete
            </button>
          </div>
        );
      },
    },
  ];

  const activeSource = active ? sources.find((s) => s.id === active.sourceId) ?? null : null;
  const activeMapped = activeSource
    ? (mappings ?? []).filter((m) => m.credential_source_id === activeSource.id)
    : [];

  return (
    <section style={{ marginTop: 'var(--ss-space-xl)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-md)' }}>
        {icon}
        <h2 style={{ margin: 0 }}>{title}</h2>
        <button className="btn btn-sm" onClick={() => setShowForm((v) => !v)}>
          {showForm ? 'Cancel' : '+ New'}
        </button>
      </div>
      <p className="muted" style={{ marginTop: 'var(--ss-space-xs)' }}>{description}</p>

      {showForm && (
        <CredentialSourceForm
          allowedTypes={allowedTypes}
          onDone={() => setShowForm(false)}
        />
      )}

      {sources.length === 0 ? (
        <p className="muted" style={{ marginTop: 'var(--ss-space-md)' }}>None configured.</p>
      ) : (
        <div style={{ marginTop: 'var(--ss-space-md)' }}>
          <DataTable
            columns={columns}
            data={sources}
            getRowId={(s) => s.id}
            initialSorting={[{ id: 'name', desc: false }]}
          />
        </div>
      )}

      {activeSource && active?.kind === 'test' && (
        <TestCredentialModal source={activeSource} onClose={() => setActive(null)} />
      )}
      {activeSource && active?.kind === 'edit' && (
        <EditSourceForm source={activeSource} onDone={() => setActive(null)} />
      )}
      {activeSource && active?.kind === 'map-collection' && (
        <MapToCollectionPanel
          sourceId={activeSource.id}
          existingMappings={activeMapped.filter((m) => m.scope_kind === 'collection')}
          onClose={() => setActive(null)}
        />
      )}
      {activeSource && active?.kind === 'map-asset_endpoint' && (
        <MapToEndpointPanel
          sourceId={activeSource.id}
          existingMappings={activeMapped.filter((m) => m.scope_kind === 'asset_endpoint')}
          onClose={() => setActive(null)}
        />
      )}
      {activeSource && active?.kind === 'map-asset' && (
        <MapToAssetPanel
          sourceId={activeSource.id}
          existingMappings={activeMapped.filter((m) => m.scope_kind === 'asset')}
          onClose={() => setActive(null)}
        />
      )}
    </section>
  );
}

// Types that should default to agent-side testing (on-prem resolvers).
const AGENT_DEFAULT_TYPES: string[] = ['hashicorp_vault', 'cyberark'];
// Types that should default to server-side testing.
const SERVER_DEFAULT_TYPES: string[] = ['aws_secrets_manager', 'static'];

function TestCredentialModal({
  source,
  onClose,
}: {
  source: CredentialSource;
  onClose: () => void;
}) {
  const defaultMode = AGENT_DEFAULT_TYPES.includes(source.type) ? 'agent' : 'server';
  const [mode, setMode] = useState<'server' | 'agent'>(defaultMode);
  const [selectedAgent, setSelectedAgent] = useState<string>('');
  const [testResult, setTestResult] = useState<{
    success: boolean;
    username?: string;
    error?: string;
    hint?: string;
    duration_ms?: number;
  } | null>(null);

  const { data: agents } = useQuery({
    queryKey: ['agents'],
    queryFn: listAgents,
    enabled: mode === 'agent',
  });

  const connectedAgents = (agents ?? []).filter(
    (a: Agent) => a.status === 'connected' || a.status === 'online',
  );

  const testMut = useMutation({
    mutationFn: () =>
      testCredentialSource(source.id, mode === 'agent' ? selectedAgent : undefined),
    onSuccess: (data) => setTestResult(data),
    onError: (e) => setTestResult({ success: false, error: (e as Error).message }),
  });

  const canRun = mode === 'server' || (mode === 'agent' && selectedAgent !== '');

  return (
    <div style={{
      border: '1px solid var(--ss-border-default)',
      borderRadius: 'var(--ss-radius-md)',
      padding: 'var(--ss-space-lg)',
      background: 'var(--ss-bg-surface)',
      marginTop: 'var(--ss-space-xs)',
      marginBottom: 'var(--ss-space-xs)',
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 'var(--ss-space-md)' }}>
        <strong>Test Credential</strong>
        <button className="btn btn-sm" onClick={onClose} style={{ fontSize: 12 }}>Close</button>
      </div>

      <div style={{ marginBottom: 'var(--ss-space-md)' }}>
        <div style={{ fontSize: 13, marginBottom: 6, fontWeight: 500 }}>Test from:</div>
        <label style={{ display: 'block', cursor: 'pointer', marginBottom: 'var(--ss-space-xs)' }}>
          <input
            type="radio"
            name={`test-mode-${source.id}`}
            checked={mode === 'server'}
            onChange={() => { setMode('server'); setTestResult(null); }}
            style={{ marginRight: 6 }}
          />
          Server
          {SERVER_DEFAULT_TYPES.includes(source.type) && <span className="muted" style={{ fontSize: 11, marginLeft: 4 }}>(recommended)</span>}
        </label>
        <label style={{ display: 'block', cursor: 'pointer' }}>
          <input
            type="radio"
            name={`test-mode-${source.id}`}
            checked={mode === 'agent'}
            onChange={() => { setMode('agent'); setTestResult(null); }}
            style={{ marginRight: 6 }}
          />
          Agent
          {AGENT_DEFAULT_TYPES.includes(source.type) && <span className="muted" style={{ fontSize: 11, marginLeft: 4 }}>(recommended for on-prem)</span>}
        </label>
      </div>

      {mode === 'agent' && (
        <div style={{ marginBottom: 'var(--ss-space-md)' }}>
          <label style={{ fontSize: 13, fontWeight: 500 }}>Agent:</label>
          <select
            value={selectedAgent}
            onChange={(e) => setSelectedAgent(e.target.value)}
            style={{ display: 'block', marginTop: 4, width: '100%', maxWidth: 300 }}
          >
            <option value="">Select an agent...</option>
            {connectedAgents.map((a: Agent) => (
              <option key={a.id} value={a.id}>
                {a.name || a.id.slice(0, 8)} (connected)
              </option>
            ))}
          </select>
          {connectedAgents.length === 0 && agents && (
            <p className="muted" style={{ fontSize: 12, marginTop: 4 }}>
              No connected agents available.
            </p>
          )}
        </div>
      )}

      <button
        className="btn btn-primary btn-sm"
        disabled={testMut.isPending || !canRun}
        onClick={() => { setTestResult(null); testMut.mutate(); }}
      >
        {testMut.isPending ? 'Testing...' : 'Run Test'}
      </button>

      {testResult && (
        <div style={{
          marginTop: 'var(--ss-space-md)',
          padding: 'var(--ss-space-sm) var(--ss-space-md)',
          fontSize: 13,
          // No success-bg/danger-bg token exists; keep the light tints literal
          // and token the accent border (--ss-success / --ss-danger).
          background: testResult.success ? '#f0fdf4' : '#fef2f2',
          borderLeft: `3px solid ${testResult.success ? 'var(--ss-success)' : 'var(--ss-danger)'}`,
          borderRadius: 'var(--ss-radius-sm)',
        }}>
          {testResult.success
            ? <>Success{testResult.username ? ` -- username: ${testResult.username}` : ''}</>
            : <>Failed: {testResult.error}</>}
          {testResult.duration_ms != null && (
            <span className="muted" style={{ marginLeft: 8, fontSize: 11 }}>
              ({testResult.duration_ms}ms)
            </span>
          )}
          {testResult.hint && (
            <p className="muted" style={{ fontSize: 11, marginTop: 4 }}>{testResult.hint}</p>
          )}
        </div>
      )}
    </div>
  );
}

function renderConfigSummary(s: CredentialSource): string {
  const cfg = (s.config ?? {}) as Record<string, unknown>;
  if (s.type === 'static') {
    const t = typeof cfg.type === 'string' ? cfg.type : 'db';
    return `type=${t}, password=${'*'.repeat(8)}`;
  }
  if (s.type === 'webhook') {
    const url = typeof cfg.url === 'string' ? cfg.url : '-';
    return `url=${url}${cfg.secret === '(set)' ? ' + secret' : ''}`;
  }
  if (s.type === 'slack') {
    return cfg.webhook_url === '(set)' ? 'webhook configured' : '--';
  }
  if (s.type === 'aws_secrets_manager') {
    const region = typeof cfg.region === 'string' ? cfg.region : '-';
    const arn = typeof cfg.secret_arn === 'string' ? cfg.secret_arn : '-';
    const truncatedArn = arn.length > 40 ? arn.slice(0, 37) + '...' : arn;
    return `region=${region}, arn=${truncatedArn}`;
  }
  if (s.type === 'hashicorp_vault') {
    const url = typeof cfg.vault_url === 'string' ? cfg.vault_url : '-';
    const path = typeof cfg.secret_path === 'string' ? cfg.secret_path : '-';
    const truncatedPath = path.length > 30 ? path.slice(0, 27) + '...' : path;
    return `url=${url}, path=${truncatedPath}`;
  }
  return Object.entries(cfg)
    .map(([k, v]) => `${k}=${v === '(set)' ? '(set)' : JSON.stringify(v)}`)
    .join(', ') || '--';
}

// ---------------------------------------------------------------
// Edit form for existing credential sources
// ---------------------------------------------------------------

function EditSourceForm({
  source,
  onDone,
}: {
  source: CredentialSource;
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const [err, setErr] = useState<string | null>(null);

  const updateMut = useMutation({
    mutationFn: ({ name, config }: { name: string; config: Record<string, unknown> }) =>
      updateCredentialSource(source.id, { name, config }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['credential-sources'] });
      onDone();
    },
    onError: (e) => setErr((e as Error).message),
  });

  function handleSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setErr(null);
    const fd = new FormData(e.currentTarget);
    const name = (fd.get('edit_name') as string).trim();

    if (source.type === 'static') {
      const username = (fd.get('edit_username') as string).trim();
      const password = (fd.get('edit_password') as string).trim();
      if (!username) {
        setErr('Username is required.');
        return;
      }
      // Blank password = keep existing (backend preserves)
      updateMut.mutate({
        name,
        config: { username, password: password || '' },
      });
      return;
    }

    // Non-static: gather config from form
    const config: Record<string, unknown> = {};
    switch (source.type) {
      case 'webhook': {
        config.url = (fd.get('webhook_url') as string).trim();
        const secret = (fd.get('webhook_secret') as string).trim();
        if (secret) config.secret = secret;
        break;
      }
      case 'slack': {
        const url = (fd.get('slack_url') as string).trim();
        if (url) config.webhook_url = url;
        break;
      }
      case 'email': {
        config.smtp_host = (fd.get('smtp_host') as string).trim();
        config.smtp_user = (fd.get('smtp_user') as string).trim();
        config.smtp_password = (fd.get('smtp_password') as string).trim();
        config.from = (fd.get('email_from') as string).trim();
        break;
      }
      case 'pagerduty': {
        config.routing_key = (fd.get('pd_routing_key') as string).trim();
        break;
      }
      case 'aws_secrets_manager': {
        config.region = (fd.get('aws_region') as string).trim();
        config.secret_arn = (fd.get('aws_secret_arn') as string).trim();
        const roleArn = (fd.get('aws_role_arn') as string).trim();
        if (roleArn) config.role_arn = roleArn;
        config.secret_key_username = (fd.get('aws_key_username') as string).trim() || 'username';
        config.secret_key_password = (fd.get('aws_key_password') as string).trim() || 'password';
        break;
      }
      case 'hashicorp_vault': {
        config.vault_url = (fd.get('vault_url') as string).trim();
        config.auth_method = 'token';
        const tok = (fd.get('vault_token') as string).trim();
        if (tok) config.token = tok;
        config.secret_path = (fd.get('vault_secret_path') as string).trim();
        config.secret_key_username = (fd.get('vault_key_username') as string).trim() || 'username';
        config.secret_key_password = (fd.get('vault_key_password') as string).trim() || 'password';
        const ns = (fd.get('vault_namespace') as string).trim();
        if (ns) config.namespace = ns;
        config.tls_skip_verify = (fd.get('vault_tls_skip') as string) === 'on';
        break;
      }
      default:
        break;
    }
    updateMut.mutate({ name, config });
  }

  const cfg = (source.config ?? {}) as Record<string, unknown>;

  return (
    <form className="form-card" style={{ marginTop: 8 }} onSubmit={handleSubmit}>
      <div className="form-group">
        <label htmlFor="edit_name">Name</label>
        <input id="edit_name" name="edit_name" type="text" defaultValue={source.name} />
      </div>

      {source.type === 'static' && (
        <>
          <div className="form-group">
            <label htmlFor="edit_username">Username</label>
            <input id="edit_username" name="edit_username" type="text" required
              defaultValue={(source.config as Record<string, unknown>)?.username as string ?? ''} />
          </div>
          <div className="form-group">
            <label htmlFor="edit_password">Password (leave blank to keep existing)</label>
            <input id="edit_password" name="edit_password" type="password" />
          </div>
        </>
      )}
      {source.type === 'webhook' && (
        <>
          <Field name="webhook_url" label="URL" type="url" defaultValue={cfg.url as string} required />
          <Field name="webhook_secret" label="Signing secret (blank = keep)" type="password" />
        </>
      )}
      {source.type === 'slack' && (
        <Field name="slack_url" label="Slack webhook URL (blank = keep)" type="url" />
      )}
      {source.type === 'email' && (
        <>
          <Field name="smtp_host" label="SMTP host" defaultValue={cfg.smtp_host as string} required />
          <Field name="smtp_user" label="SMTP user" defaultValue={cfg.smtp_user as string} required />
          <Field name="smtp_password" label="SMTP password (blank = keep)" type="password" />
          <Field name="email_from" label="From address" type="email" defaultValue={cfg.from as string} required />
        </>
      )}
      {source.type === 'pagerduty' && (
        <Field name="pd_routing_key" label="Routing key (blank = keep)" type="password" />
      )}
      {source.type === 'aws_secrets_manager' && (
        <>
          <Field name="aws_region" label="AWS region" defaultValue={cfg.region as string} required />
          <Field name="aws_secret_arn" label="Secret ARN" defaultValue={cfg.secret_arn as string} required />
          <Field name="aws_role_arn" label="Role ARN (optional)" defaultValue={cfg.role_arn as string} />
          <Field name="aws_key_username" label="Username key" defaultValue={(cfg.secret_key_username as string | undefined) ?? 'username'} />
          <Field name="aws_key_password" label="Password key" defaultValue={(cfg.secret_key_password as string | undefined) ?? 'password'} />
        </>
      )}
      {source.type === 'hashicorp_vault' && (
        <>
          <Field name="vault_url" label="Vault URL" defaultValue={cfg.vault_url as string} required />
          <Field name="vault_token" label="Token (blank = keep existing)" type="password" />
          <Field name="vault_secret_path" label="Secret path" defaultValue={cfg.secret_path as string} required />
          <Field name="vault_key_username" label="Username key" defaultValue={(cfg.secret_key_username as string | undefined) ?? 'username'} />
          <Field name="vault_key_password" label="Password key" defaultValue={(cfg.secret_key_password as string | undefined) ?? 'password'} />
          <Field name="vault_namespace" label="Namespace (optional)" defaultValue={cfg.namespace as string} />
          <div className="form-group">
            <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <input type="checkbox" name="vault_tls_skip" defaultChecked={!!cfg.tls_skip_verify} />
              Skip TLS verification
            </label>
          </div>
        </>
      )}

      {err && <p className="error">{err}</p>}
      <button className="btn btn-primary" disabled={updateMut.isPending}>
        {updateMut.isPending ? 'Saving...' : 'Save'}
      </button>
    </form>
  );
}

function MapToCollectionPanel({
  sourceId,
  existingMappings,
  onClose,
}: {
  sourceId: string;
  existingMappings: CredentialMapping[];
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { data: collections } = useQuery({
    queryKey: ['collections'],
    queryFn: () => listCollections(),
  });
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const endpointCollections = (collections ?? []).filter((c) => c.scope === 'endpoint');

  const bulkMut = useMutation({
    mutationFn: (ids: string[]) => bulkCreateCredentialMappings(sourceId, ids),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['credential-mappings'] });
      onClose();
    },
  });

  const unmapMut = useMutation({
    mutationFn: deleteCredentialMapping,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['credential-mappings'] }),
  });

  function toggle(id: string) {
    setSelected((prev) => {
      const n = new Set(prev);
      if (n.has(id)) n.delete(id); else n.add(id);
      return n;
    });
  }

  return (
    <div className="form-card" style={{ marginTop: 8 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between' }}>
        <strong>Map credential to endpoint-scoped collections</strong>
        <button className="btn btn-sm" onClick={onClose}>Close</button>
      </div>
      {endpointCollections.length === 0 && (
        <p className="muted">No endpoint-scoped collections defined.</p>
      )}
      <ul style={{ listStyle: 'none', padding: 0, margin: '8px 0' }}>
        {endpointCollections.map((c) => {
          const mapping = existingMappings.find((m) => m.collection_id === c.id);
          const already = !!mapping;
          return (
            <li key={c.id} style={{ padding: '4px 0', display: 'flex', gap: 8, alignItems: 'center' }}>
              <label style={{ display: 'flex', gap: 8, alignItems: 'center', flex: 1 }}>
                <input
                  type="checkbox"
                  disabled={already}
                  checked={already || selected.has(c.id)}
                  onChange={() => toggle(c.id)}
                />
                <span>{c.name}</span>
                {already && <span className="muted">(mapped)</span>}
              </label>
              {already && (
                <button
                  className="btn btn-sm btn-danger"
                  disabled={unmapMut.isPending}
                  onClick={() => unmapMut.mutate(mapping.id)}
                >
                  Unmap
                </button>
              )}
            </li>
          );
        })}
      </ul>
      {bulkMut.error && <p className="error">{(bulkMut.error as Error).message}</p>}
      <div style={{ display: 'flex', gap: 8 }}>
        <button
          className="btn btn-primary"
          disabled={selected.size === 0 || bulkMut.isPending}
          onClick={() => bulkMut.mutate(Array.from(selected))}
        >
          {bulkMut.isPending ? 'Mapping...' : `Map ${selected.size} collection(s)`}
        </button>
        {unmapMut.isPending && <span className="muted">Unmapping...</span>}
      </div>
    </div>
  );
}

function MapToEndpointPanel({
  sourceId,
  existingMappings,
  onClose,
}: {
  sourceId: string;
  existingMappings: CredentialMapping[];
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [search, setSearch] = useState('');
  const { data: endpoints } = useQuery({
    queryKey: ['asset-endpoints', { q: search }],
    queryFn: () => listAssetEndpoints({ q: search || undefined, page: 1, page_size: 50 }),
    enabled: search.length >= 1,
  });
  const [selected, setSelected] = useState<Set<string>>(new Set());

  // Resolve mapped endpoint UUIDs to host:port labels.
  const mappedEndpointIds = existingMappings
    .filter((m) => m.scope_kind === 'asset_endpoint' && m.asset_endpoint_id)
    .map((m) => m.asset_endpoint_id!);
  const { data: mappedEndpointLabels } = useQuery({
    queryKey: ['mapped-endpoint-labels', mappedEndpointIds],
    queryFn: async () => {
      const all = await listAssetEndpoints({ page_size: 500 });
      const map = new Map<string, string>();
      for (const ep of all.items) {
        map.set(ep.id, `${ep.host || ep.ip}:${ep.port}`);
      }
      return map;
    },
    enabled: mappedEndpointIds.length > 0,
  });

  const bulkMut = useMutation({
    mutationFn: (ids: string[]) => bulkCreateEndpointMappings(sourceId, ids),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['credential-mappings'] });
      onClose();
    },
  });

  const unmapMut = useMutation({
    mutationFn: deleteCredentialMapping,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['credential-mappings'] }),
  });

  const items = endpoints?.items ?? [];
  const mappedIds = new Set(existingMappings.map((m) => m.asset_endpoint_id).filter(Boolean));

  return (
    <div className="form-card" style={{ marginTop: 8 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between' }}>
        <strong>Map credential to endpoints</strong>
        <button className="btn btn-sm" onClick={onClose}>Close</button>
      </div>
      {existingMappings.filter(m => m.scope_kind === 'asset_endpoint').length > 0 && (
        <>
          <p style={{ margin: '8px 0 4px', fontWeight: 600, fontSize: 13 }}>Current mappings</p>
          <ul style={{ listStyle: 'none', padding: 0, margin: '0 0 12px' }}>
            {existingMappings.filter(m => m.scope_kind === 'asset_endpoint' && m.asset_endpoint_id).map(m => (
              <li key={m.id} style={{ padding: '4px 0', display: 'flex', gap: 8, alignItems: 'center' }}>
                <span style={{ flex: 1 }}>{mappedEndpointLabels?.get(m.asset_endpoint_id!) ?? `${m.asset_endpoint_id?.slice(0, 8)}…`} (mapped)</span>
                <button
                  className="btn btn-sm btn-danger"
                  disabled={unmapMut.isPending}
                  onClick={() => unmapMut.mutate(m.id)}
                >
                  Unmap
                </button>
              </li>
            ))}
          </ul>
        </>
      )}
      <p style={{ margin: '8px 0 4px', fontWeight: 600, fontSize: 13 }}>Add mapping</p>
      <input
        type="search"
        placeholder="Search endpoints by host, IP, service..."
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        style={{ width: '100%' }}
      />
      {search.length >= 1 && items.length === 0 && (
        <p className="muted" style={{ marginTop: 8 }}>No endpoints found.</p>
      )}
      {items.length > 0 && (
        <ul style={{ listStyle: 'none', padding: 0, margin: '8px 0', maxHeight: 300, overflowY: 'auto' }}>
          {items.map((ep) => {
            const already = mappedIds.has(ep.id);
            const mapping = already ? existingMappings.find((m) => m.asset_endpoint_id === ep.id) : null;
            return (
              <li key={ep.id} style={{ padding: '4px 0', display: 'flex', gap: 8, alignItems: 'center' }}>
                <label style={{ display: 'flex', gap: 8, alignItems: 'center', flex: 1 }}>
                  <input
                    type="checkbox"
                    disabled={already}
                    checked={already || selected.has(ep.id)}
                    onChange={() => {
                      setSelected((prev) => {
                        const n = new Set(prev);
                        if (n.has(ep.id)) n.delete(ep.id); else n.add(ep.id);
                        return n;
                      });
                    }}
                  />
                  <span>{ep.host || ep.ip}:{ep.port}</span>
                  {ep.service && <span className="muted">({ep.service})</span>}
                  {already && <span className="muted">(mapped)</span>}
                </label>
                {already && mapping && (
                  <button
                    className="btn btn-sm btn-danger"
                    disabled={unmapMut.isPending}
                    onClick={() => unmapMut.mutate(mapping.id)}
                  >
                    Unmap
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {bulkMut.error && <p className="error">{(bulkMut.error as Error).message}</p>}
      <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
        <button
          className="btn btn-primary"
          disabled={selected.size === 0 || bulkMut.isPending}
          onClick={() => bulkMut.mutate(Array.from(selected))}
        >
          {bulkMut.isPending ? 'Mapping...' : `Map ${selected.size} endpoint(s)`}
        </button>
        {unmapMut.isPending && <span className="muted">Unmapping...</span>}
      </div>
    </div>
  );
}

function MapToAssetPanel({
  sourceId,
  existingMappings,
  onClose,
}: {
  sourceId: string;
  existingMappings: CredentialMapping[];
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [search, setSearch] = useState('');
  const { data: assets } = useQuery({
    queryKey: ['assets', { q: search }],
    queryFn: () => listAssets({ q: search || undefined, page: 1, page_size: 50 }),
    enabled: search.length >= 1,
  });
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const bulkMut = useMutation({
    mutationFn: (ids: string[]) => bulkCreateAssetMappings(sourceId, ids),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['credential-mappings'] });
      onClose();
    },
  });

  const unmapMut = useMutation({
    mutationFn: deleteCredentialMapping,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['credential-mappings'] }),
  });

  const items = assets?.items ?? [];
  const mappedIds = new Set(existingMappings.map((m) => m.asset_id).filter(Boolean));

  return (
    <div className="form-card" style={{ marginTop: 8 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between' }}>
        <strong>Map credential to assets</strong>
        <button className="btn btn-sm" onClick={onClose}>Close</button>
      </div>
      <input
        type="search"
        placeholder="Search assets by hostname, IP..."
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        style={{ width: '100%', marginTop: 8 }}
      />
      {search.length >= 1 && items.length === 0 && (
        <p className="muted" style={{ marginTop: 8 }}>No assets found.</p>
      )}
      {items.length > 0 && (
        <ul style={{ listStyle: 'none', padding: 0, margin: '8px 0', maxHeight: 300, overflowY: 'auto' }}>
          {items.map((a) => {
            const already = mappedIds.has(a.id);
            const mapping = already ? existingMappings.find((m) => m.asset_id === a.id) : null;
            return (
              <li key={a.id} style={{ padding: '4px 0', display: 'flex', gap: 8, alignItems: 'center' }}>
                <label style={{ display: 'flex', gap: 8, alignItems: 'center', flex: 1 }}>
                  <input
                    type="checkbox"
                    disabled={already}
                    checked={already || selected.has(a.id)}
                    onChange={() => {
                      setSelected((prev) => {
                        const n = new Set(prev);
                        if (n.has(a.id)) n.delete(a.id); else n.add(a.id);
                        return n;
                      });
                    }}
                  />
                  <span>{a.hostname || a.primary_ip || a.ip || '-'}</span>
                  {a.resource_type && <span className="muted">({a.resource_type})</span>}
                  {already && <span className="muted">(mapped)</span>}
                </label>
                {already && mapping && (
                  <button
                    className="btn btn-sm btn-danger"
                    disabled={unmapMut.isPending}
                    onClick={() => unmapMut.mutate(mapping.id)}
                  >
                    Unmap
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
      {bulkMut.error && <p className="error">{(bulkMut.error as Error).message}</p>}
      <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
        <button
          className="btn btn-primary"
          disabled={selected.size === 0 || bulkMut.isPending}
          onClick={() => bulkMut.mutate(Array.from(selected))}
        >
          {bulkMut.isPending ? 'Mapping...' : `Map ${selected.size} asset(s)`}
        </button>
        {unmapMut.isPending && <span className="muted">Unmapping...</span>}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------
// New-source form. Slim but covers every allowed type.
// ---------------------------------------------------------------

function CredentialSourceForm({
  allowedTypes,
  onDone,
}: {
  allowedTypes: CredentialSourceType[];
  onDone: () => void;
}) {
  const queryClient = useQueryClient();
  const [type, setType] = useState<CredentialSourceType>(allowedTypes[0]);
  const [err, setErr] = useState<string | null>(null);

  const createMut = useMutation({
    mutationFn: createCredentialSource,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['credential-sources'] });
      onDone();
    },
    onError: (e) => setErr((e as Error).message),
  });

  function handleSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setErr(null);
    const fd = new FormData(e.currentTarget);
    const name = (fd.get('source_name') as string | null)?.trim() ?? '';
    const config: Record<string, unknown> = {};
    switch (type) {
      case 'static': {
        const username = (fd.get('static_username') as string).trim();
        const password = (fd.get('static_password') as string).trim();
        if (!username || !password) {
          setErr('Username and password are required.');
          return;
        }
        config.username = username;
        config.password = password;
        break;
      }
      case 'webhook': {
        config.url = (fd.get('webhook_url') as string).trim();
        const secret = (fd.get('webhook_secret') as string).trim();
        if (secret) config.secret = secret;
        break;
      }
      case 'slack': {
        const url = (fd.get('slack_url') as string).trim();
        if (url) config.webhook_url = url;
        break;
      }
      case 'email': {
        config.smtp_host = (fd.get('smtp_host') as string).trim();
        config.smtp_user = (fd.get('smtp_user') as string).trim();
        config.smtp_password = (fd.get('smtp_password') as string).trim();
        config.from = (fd.get('email_from') as string).trim();
        break;
      }
      case 'pagerduty': {
        config.routing_key = (fd.get('pd_routing_key') as string).trim();
        break;
      }
      case 'aws_secrets_manager': {
        config.region = (fd.get('aws_region') as string).trim();
        config.secret_arn = (fd.get('aws_secret_arn') as string).trim();
        const roleArn = (fd.get('aws_role_arn') as string).trim();
        if (roleArn) config.role_arn = roleArn;
        config.secret_key_username = (fd.get('aws_key_username') as string).trim() || 'username';
        config.secret_key_password = (fd.get('aws_key_password') as string).trim() || 'password';
        if (!config.region || !config.secret_arn) {
          setErr('Region and Secret ARN are required.');
          return;
        }
        break;
      }
      case 'hashicorp_vault': {
        config.vault_url = (fd.get('vault_url') as string).trim();
        config.auth_method = 'token';
        const tok = (fd.get('vault_token') as string).trim();
        if (tok) config.token = tok;
        config.secret_path = (fd.get('vault_secret_path') as string).trim();
        config.secret_key_username = (fd.get('vault_key_username') as string).trim() || 'username';
        config.secret_key_password = (fd.get('vault_key_password') as string).trim() || 'password';
        const ns = (fd.get('vault_namespace') as string).trim();
        if (ns) config.namespace = ns;
        config.tls_skip_verify = (fd.get('vault_tls_skip') as string) === 'on';
        if (!config.vault_url || !config.secret_path) {
          setErr('Vault URL and secret path are required.');
          return;
        }
        if (!tok) {
          setErr('Token is required.');
          return;
        }
        break;
      }
      case 'cyberark': {
        config.app_id = (fd.get('cyberark_app_id') as string).trim();
        const key = (fd.get('cyberark_api_key') as string).trim();
        if (key) config.api_key = key;
        break;
      }
    }
    createMut.mutate({ name, type, config });
  }

  return (
    <form className="form-card" onSubmit={handleSubmit}>
      <div className="form-group">
        <label htmlFor="source_name">Name</label>
        <input id="source_name" name="source_name" type="text" placeholder="e.g. studio-mssql-sa" />
      </div>
      {allowedTypes.length > 1 && (
        <div className="form-group">
          <label>Type</label>
          <select value={type} onChange={(e) => setType(e.target.value as CredentialSourceType)}>
            {allowedTypes.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
        </div>
      )}
      {type === 'static' && (
        <>
          <Field name="static_username" label="Username" required />
          <Field name="static_password" label="Password" type="password" required />
        </>
      )}
      {type === 'webhook' && (
        <>
          <Field name="webhook_url" label="URL" type="url" required />
          <Field name="webhook_secret" label="Signing secret (optional)" type="password" />
        </>
      )}
      {type === 'slack' && (
        <Field name="slack_url" label="Slack webhook URL" type="url" required />
      )}
      {type === 'email' && (
        <>
          <Field name="smtp_host" label="SMTP host" required />
          <Field name="smtp_user" label="SMTP user" required />
          <Field name="smtp_password" label="SMTP password" type="password" required />
          <Field name="email_from" label="From address" type="email" required />
        </>
      )}
      {type === 'pagerduty' && (
        <Field name="pd_routing_key" label="Routing key" type="password" required />
      )}
      {type === 'aws_secrets_manager' && (
        <>
          <Field name="aws_region" label="AWS region" required defaultValue="us-east-1" />
          <Field name="aws_secret_arn" label="Secret ARN" required />
          <Field name="aws_role_arn" label="Role ARN (optional, for cross-account)" />
          <Field name="aws_key_username" label="Username key in secret JSON" defaultValue="username" />
          <Field name="aws_key_password" label="Password key in secret JSON" defaultValue="password" />
        </>
      )}
      {type === 'hashicorp_vault' && (
        <>
          <Field name="vault_url" label="Vault URL" required defaultValue="http://127.0.0.1:8200" />
          <Field name="vault_token" label="Token" type="password" required />
          <Field name="vault_secret_path" label="Secret path (e.g. secret/data/mssql-creds)" required />
          <Field name="vault_key_username" label="Username key in secret" defaultValue="username" />
          <Field name="vault_key_password" label="Password key in secret" defaultValue="password" />
          <Field name="vault_namespace" label="Namespace (optional)" />
          <div className="form-group">
            <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <input type="checkbox" name="vault_tls_skip" />
              Skip TLS verification (dev / self-signed certs)
            </label>
          </div>
        </>
      )}
      {type === 'cyberark' && (
        <>
          <Field name="cyberark_app_id" label="App ID" required />
          <Field name="cyberark_api_key" label="API key" type="password" required />
        </>
      )}
      {err && <p className="error">{err}</p>}
      <button className="btn btn-primary" disabled={createMut.isPending}>
        {createMut.isPending ? 'Creating...' : 'Create'}
      </button>
    </form>
  );
}

function Field({
  name,
  label,
  type = 'text',
  required,
  defaultValue,
}: {
  name: string;
  label: string;
  type?: string;
  required?: boolean;
  defaultValue?: string;
}) {
  return (
    <div className="form-group">
      <label htmlFor={name}>{label}</label>
      <input id={name} name={name} type={type} required={required} defaultValue={defaultValue} />
    </div>
  );
}
