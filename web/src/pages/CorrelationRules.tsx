import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Workflow, GitBranch } from 'lucide-react';
import {
  listCorrelationRules,
  createCorrelationRule,
  updateCorrelationRule,
  deleteCorrelationRule,
  type UpsertRuleRequest,
} from '../api/client';
import type { CorrelationRule } from '../api/types';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';

// CRUD page for ADR 003 D2 correlation rules. Authoring is by raw
// JSONB match + actions — no visual predicate builder in R1.5a (that
// comes later). The API validates the shape on POST/PUT so a bad body
// surfaces a 400.
//
// Update on a rule auto-versions — the server creates a new version
// rather than mutating in place, so "Save changes" increments version.

type FormMode = { kind: 'new' } | { kind: 'edit'; rule: CorrelationRule };

export default function CorrelationRules() {
  const queryClient = useQueryClient();
  const { data: rules, isLoading, error } = useQuery({
    queryKey: ['correlation-rules'],
    queryFn: listCorrelationRules,
  });

  const [mode, setMode] = useState<FormMode | null>(null);

  const createMut = useMutation({
    mutationFn: createCorrelationRule,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['correlation-rules'] });
      setMode(null);
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, req }: { id: string; req: UpsertRuleRequest }) => updateCorrelationRule(id, req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['correlation-rules'] });
      setMode(null);
    },
  });

  const deleteMut = useMutation({
    mutationFn: deleteCorrelationRule,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['correlation-rules'] }),
  });

  // Show only the latest version per name in the list view. API
  // returns every version; we keep history out of sight for v1.
  const latestByName = (rules ?? []).reduce<Record<string, CorrelationRule>>((acc, r) => {
    const prev = acc[r.name];
    if (!prev || r.version > prev.version) acc[r.name] = r;
    return acc;
  }, {});
  const rows = Object.values(latestByName);

  const submitting = createMut.isPending || updateMut.isPending;
  const submitError = createMut.error ?? updateMut.error;

  // Per-row actions (action-only — no row-click). Delete is a soft "Disable".
  const renderActions = (r: CorrelationRule) => (
    <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 'var(--ss-space-xs)' }}>
      <button className="btn btn-small" onClick={() => setMode({ kind: 'edit', rule: r })}>Edit</button>
      <button
        className="btn btn-small btn-danger"
        onClick={() => { if (window.confirm(`Disable rule ${r.name}?`)) deleteMut.mutate(r.id); }}
        disabled={deleteMut.isPending}
      >
        Disable
      </button>
    </div>
  );

  const columns: ColumnDef<CorrelationRule>[] = [
    { id: 'name', header: 'Name', accessorFn: (r) => r.name },
    { id: 'trigger', header: 'Trigger', accessorFn: (r) => r.trigger },
    {
      id: 'actions_summary',
      header: 'Actions',
      enableSorting: false,
      cell: ({ row }) => (row.original.body?.actions ?? []).map((a) => a.type).join(', '),
    },
    { id: 'version', header: 'Version', accessorFn: (r) => r.version },
    { id: 'enabled', header: 'Enabled', accessorFn: (r) => (r.enabled ? 'yes' : 'no') },
    { id: 'actions', header: '', enableSorting: false, cell: ({ row }) => renderActions(row.original) },
  ];

  return (
    <div>
      <div className="page-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
          <Workflow size={24} style={{ color: 'var(--ss-accent-primary)' }} />
          <h1>Correlation Rules</h1>
        </div>
        <button
          className="btn btn-primary"
          onClick={() => setMode(mode ? null : { kind: 'new' })}
        >
          {mode ? 'Cancel' : 'New Rule'}
        </button>
      </div>

      {mode && (
        <RuleForm
          key={mode.kind === 'edit' ? mode.rule.id : 'new'}
          mode={mode}
          submitting={submitting}
          error={submitError ? (submitError as Error).message : null}
          onSubmit={(req) => {
            if (mode.kind === 'edit') updateMut.mutate({ id: mode.rule.id, req });
            else createMut.mutate(req);
          }}
        />
      )}

      {isLoading && <p>Loading…</p>}
      {error && <p className="error">{(error as Error).message}</p>}
      {!isLoading && !error && rows.length === 0 && (
        <EmptyState
          icon={<GitBranch />}
          title="No rules yet. Add one to drive promote-to-compliance, notify, or one-shot scans."
        />
      )}
      {rows.length > 0 && (
        <DataTable
          columns={columns}
          data={rows}
          getRowId={(r) => r.id}
          initialSorting={[{ id: 'name', desc: false }]}
        />
      )}
    </div>
  );
}

const EXAMPLE_BODY = `{
  "match": { "service": "postgresql", "version": { "$regex": "^16\\\\." } },
  "actions": [
    { "type": "suggest_target", "bundle_id": "cis-postgresql-16" }
  ]
}`;

interface FormProps {
  mode: FormMode;
  submitting: boolean;
  error: string | null;
  onSubmit: (req: UpsertRuleRequest) => void;
}

function RuleForm({ mode, submitting, error, onSubmit }: FormProps) {
  const initial = mode.kind === 'edit' ? mode.rule : null;
  const [bodyText, setBodyText] = useState(
    initial ? JSON.stringify(initial.body, null, 2) : EXAMPLE_BODY,
  );
  const [parseErr, setParseErr] = useState<string | null>(null);

  function handleSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    const name = (fd.get('name') as string).trim();
    const trigger = fd.get('trigger') as 'asset_discovered' | 'asset_event';
    const enabled = fd.get('enabled') !== null;
    let parsed;
    try {
      parsed = JSON.parse(bodyText);
    } catch (err) {
      setParseErr('body is not valid JSON: ' + (err as Error).message);
      return;
    }
    setParseErr(null);
    onSubmit({ name, trigger, enabled, body: parsed });
  }

  return (
    <form className="form-card" onSubmit={handleSubmit}>
      <h3 style={{ marginTop: 0 }}>{initial ? `Edit ${initial.name} (v${initial.version})` : 'New rule'}</h3>
      <div className="form-group">
        <label htmlFor="name">Name</label>
        <input
          id="name"
          name="name"
          required
          defaultValue={initial?.name ?? ''}
          readOnly={!!initial}
          placeholder="cis-postgres-16-suggest"
        />
        {initial && (
          <p className="muted" style={{ fontSize: 12 }}>
            Name is the identity — editing creates a new version; keep the same name.
          </p>
        )}
      </div>
      <div className="form-group">
        <label htmlFor="trigger">Trigger</label>
        <select id="trigger" name="trigger" defaultValue={initial?.trigger ?? 'asset_discovered'}>
          <option value="asset_discovered">asset_discovered</option>
          <option value="asset_event">asset_event</option>
        </select>
      </div>
      <div className="form-group">
        <label>
          <input
            type="checkbox"
            name="enabled"
            defaultChecked={initial ? initial.enabled : true}
          />
          {' '}Enabled
        </label>
      </div>
      <div className="form-group">
        <label htmlFor="body">Body (JSON: match + actions)</label>
        <textarea
          id="body"
          name="body"
          rows={10}
          value={bodyText}
          onChange={(e) => setBodyText(e.target.value)}
          style={{ fontFamily: 'monospace', width: '100%' }}
        />
        <p className="muted" style={{ fontSize: 12 }}>
          Actions: <code>suggest_target</code>, <code>auto_create_target</code>,{' '}
          <code>notify</code> (requires channel), <code>run_one_shot_scan</code> (requires bundle_id + agent_id).
        </p>
      </div>
      <button type="submit" className="btn btn-primary" disabled={submitting}>
        {submitting ? 'Saving…' : initial ? 'Save new version' : 'Save'}
      </button>
      {parseErr && <p className="error">{parseErr}</p>}
      {error && <p className="error">{error}</p>}
    </form>
  );
}
