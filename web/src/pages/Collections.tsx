import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { ColumnDef } from '@tanstack/react-table';
import { Layers, FolderOpen } from 'lucide-react';
import {
  listCollections,
  createCollection,
  updateCollection,
  deleteCollection,
  previewAdhocCollection,
  type UpsertCollectionRequest,
} from '../api/client';
import type {
  Collection,
  CollectionPreview,
  CollectionScope,
  WidgetKind,
} from '../api/types';
import PredicateBuilder, { type Predicate } from '../components/PredicateBuilder';
import { predicateToEnglish } from '../components/predicateToEnglish';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';

// ADR 006 D5 — Collections replace Asset Sets. Two tabs:
//   · My Collections — every collection the tenant has saved
//   · Dashboard Widgets — subset where is_dashboard_widget=true; per-row
//     widget config (title, widget_kind)
// Inline expand on any row shows a plain-English rendering of the predicate
// (predicateToEnglish) so authors can audit intent without JSON-bashing.

type FormMode = { kind: 'new' } | { kind: 'edit'; c: Collection };
type TabId = 'mine' | 'widgets';

const EXAMPLE_PREDICATE: Predicate = {
  $and: [{ service: 'postgresql' }, { version: { $regex: '^16\\.' } }],
};

export default function Collections() {
  const qc = useQueryClient();
  const [tab, setTab] = useState<TabId>('mine');
  const [mode, setMode] = useState<FormMode | null>(null);

  const { data: all, isLoading, error } = useQuery({
    queryKey: ['collections'],
    queryFn: () => listCollections(),
  });

  const filtered = useMemo(() => {
    if (!all) return [];
    return tab === 'widgets' ? all.filter((c) => c.is_dashboard_widget) : all;
  }, [all, tab]);

  const createMut = useMutation({
    mutationFn: createCollection,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['collections'] });
      setMode(null);
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, req }: { id: string; req: UpsertCollectionRequest }) =>
      updateCollection(id, req),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['collections'] });
      setMode(null);
    },
  });

  const deleteMut = useMutation({
    mutationFn: deleteCollection,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['collections'] }),
  });

  const submitting = createMut.isPending || updateMut.isPending;
  const submitError = createMut.error ?? updateMut.error;

  // Per-row actions (action-only — no row-click). Edit opens the form; Delete
  // confirms then mutates.
  const renderActions = (c: Collection) => (
    <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 'var(--ss-space-xs)' }}>
      <button className="btn btn-small" onClick={() => setMode({ kind: 'edit', c })}>Edit</button>
      <button
        className="btn btn-small btn-danger"
        onClick={() => { if (window.confirm(`Delete collection "${c.name}"?`)) deleteMut.mutate(c.id); }}
        disabled={deleteMut.isPending}
      >
        Delete
      </button>
    </div>
  );

  // The old per-row click toggled an inline query-preview sub-row; DataTable has
  // no row-expand API (and we don't widen the locked contract), so the
  // plain-English predicate becomes an always-visible Query column instead —
  // same predicateToEnglish content, truncated with the full text on hover.
  const columns: ColumnDef<Collection>[] = [
    {
      id: 'name',
      header: 'Name',
      accessorFn: (c) => c.name,
      cell: ({ row }) => {
        const c = row.original;
        return (
          <>
            <strong>{c.name}</strong>
            {c.description && <div className="muted" style={{ fontSize: 12 }}>{c.description}</div>}
          </>
        );
      },
    },
    {
      id: 'type',
      header: 'Type',
      accessorFn: (c) => c.scope,
      cell: ({ row }) => <span className={`badge badge-scope-${row.original.scope}`}>{row.original.scope}</span>,
    },
    ...(tab === 'widgets'
      ? [{
          id: 'widget',
          header: 'Widget',
          enableSorting: false,
          cell: ({ row }: { row: { original: Collection } }) => (
            <>
              {row.original.widget_title || row.original.name}
              <span className="muted" style={{ marginLeft: 'var(--ss-space-xs)' }}>· {row.original.widget_kind || 'list'}</span>
            </>
          ),
        } as ColumnDef<Collection>]
      : []),
    { id: 'count', header: 'Count', enableSorting: false, cell: () => '—' },
    {
      id: 'query',
      header: 'Query',
      enableSorting: false,
      cell: ({ row }) => {
        const eng = predicateToEnglish(row.original.predicate);
        return <code className="muted" title={eng} style={{ fontSize: 12 }}>{eng.length > 80 ? `${eng.slice(0, 80)}…` : eng}</code>;
      },
    },
    {
      id: 'updated',
      header: 'Last Updated',
      accessorFn: (c) => c.updated_at,
      cell: ({ row }) => new Date(row.original.updated_at).toLocaleString(),
    },
    { id: 'actions', header: '', enableSorting: false, cell: ({ row }) => renderActions(row.original) },
  ];

  return (
    <div>
      <div className="page-header" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
          <Layers size={24} style={{ color: 'var(--ss-accent-primary)' }} />
          <h1>Collections</h1>
        </div>
        <button
          className="btn btn-primary"
          onClick={() => setMode(mode ? null : { kind: 'new' })}
        >
          {mode ? 'Cancel' : '+ New'}
        </button>
      </div>

      <div className="tab-bar" role="tablist" style={{ marginBottom: 'var(--ss-space-lg)' }}>
        <button
          role="tab"
          aria-selected={tab === 'mine'}
          className={`btn btn-sm ${tab === 'mine' ? 'btn-primary' : ''}`}
          onClick={() => setTab('mine')}
        >
          My Collections
        </button>
        <button
          role="tab"
          aria-selected={tab === 'widgets'}
          className={`btn btn-sm ${tab === 'widgets' ? 'btn-primary' : ''}`}
          onClick={() => setTab('widgets')}
          style={{ marginLeft: 'var(--ss-space-sm)' }}
        >
          Dashboard Widgets
        </button>
      </div>

      {mode && (
        <CollectionForm
          key={mode.kind === 'edit' ? mode.c.id : 'new'}
          mode={mode}
          submitting={submitting}
          error={submitError ? (submitError as Error).message : null}
          onSubmit={(req) => {
            if (mode.kind === 'edit') updateMut.mutate({ id: mode.c.id, req });
            else createMut.mutate(req);
          }}
        />
      )}

      {isLoading && <p>Loading…</p>}
      {error && <p className="error">{(error as Error).message}</p>}
      {!isLoading && !error && filtered.length === 0 && (
        <EmptyState
          icon={<FolderOpen />}
          title={
            tab === 'widgets'
              ? 'No dashboard widgets yet. Edit a collection and toggle "Show on dashboard".'
              : 'No collections yet. Saved predicates power rules, dashboards, and scans.'
          }
        />
      )}
      {filtered.length > 0 && (
        <DataTable
          columns={columns}
          data={filtered}
          getRowId={(c) => c.id}
          initialSorting={[{ id: 'name', desc: false }]}
        />
      )}
    </div>
  );
}

interface FormProps {
  mode: FormMode;
  submitting: boolean;
  error: string | null;
  onSubmit: (req: UpsertCollectionRequest) => void;
}

function CollectionForm({ mode, submitting, error, onSubmit }: FormProps) {
  const initial = mode.kind === 'edit' ? mode.c : null;
  const [scope, setScope] = useState<CollectionScope>(initial?.scope ?? 'endpoint');
  const [predicate, setPredicate] = useState<Predicate>(
    initial ? initial.predicate : EXAMPLE_PREDICATE,
  );
  const [widget, setWidget] = useState<boolean>(initial?.is_dashboard_widget ?? false);
  const [widgetKind, setWidgetKind] = useState<WidgetKind>(initial?.widget_kind ?? 'list');
  const [preview, setPreview] = useState<CollectionPreview | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [previewErr, setPreviewErr] = useState<string | null>(null);

  async function handlePreview() {
    setPreviewErr(null);
    setPreview(null);
    setPreviewing(true);
    try {
      const res = await previewAdhocCollection(scope, predicate);
      setPreview(res);
    } catch (err) {
      setPreviewErr((err as Error).message);
    } finally {
      setPreviewing(false);
    }
  }

  function handleSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const fd = new FormData(e.currentTarget);
    const name = (fd.get('name') as string).trim();
    const description = (fd.get('description') as string).trim() || undefined;
    const widget_title = (fd.get('widget_title') as string)?.trim() || undefined;
    onSubmit({
      name,
      description,
      scope,
      predicate,
      is_dashboard_widget: widget,
      widget_kind: widget ? widgetKind : undefined,
      widget_title: widget ? widget_title : undefined,
    });
  }

  return (
    <form className="form-card" onSubmit={handleSubmit}>
      <h3 style={{ marginTop: 0 }}>
        {initial ? `Edit ${initial.name}` : 'New collection'}
      </h3>
      <div className="form-group">
        <label htmlFor="name">Name</label>
        <input id="name" name="name" required defaultValue={initial?.name ?? ''} />
      </div>
      <div className="form-group">
        <label htmlFor="description">Description</label>
        <input
          id="description"
          name="description"
          defaultValue={initial?.description ?? ''}
          placeholder="optional"
        />
      </div>
      <div className="form-group">
        <label htmlFor="scope">Scope</label>
        <select
          id="scope"
          value={scope}
          onChange={(e) => setScope(e.target.value as CollectionScope)}
        >
          <option value="asset">asset</option>
          <option value="endpoint">endpoint</option>
          <option value="finding">finding</option>
        </select>
      </div>
      <div className="form-group">
        <label>Predicate</label>
        <PredicateBuilder value={predicate} onChange={setPredicate} />
      </div>
      <div className="form-group">
        <label>
          <input
            type="checkbox"
            checked={widget}
            onChange={(e) => setWidget(e.target.checked)}
          />{' '}
          Show on Dashboard
        </label>
      </div>
      {widget && (
        <>
          <div className="form-group">
            <label htmlFor="widget_title">Widget title</label>
            <input
              id="widget_title"
              name="widget_title"
              defaultValue={initial?.widget_title ?? ''}
              placeholder={initial?.name ?? 'optional, defaults to name'}
            />
          </div>
          <div className="form-group">
            <label htmlFor="widget_kind">Widget kind</label>
            <select
              id="widget_kind"
              value={widgetKind}
              onChange={(e) => setWidgetKind(e.target.value as WidgetKind)}
            >
              <option value="list">list</option>
              <option value="count">count</option>
              <option value="chart">chart</option>
            </select>
          </div>
        </>
      )}
      <div style={{ display: 'flex', gap: 'var(--ss-space-sm)' }}>
        <button
          type="button"
          className="btn"
          disabled={previewing}
          onClick={handlePreview}
        >
          {previewing ? 'Previewing…' : 'Preview matches'}
        </button>
        <button type="submit" className="btn btn-primary" disabled={submitting}>
          {submitting ? 'Saving…' : initial ? 'Save changes' : 'Save'}
        </button>
      </div>
      {preview && (
        <p className="muted" style={{ marginTop: 'var(--ss-space-sm)' }}>
          Matches {preview.count} {scope}
          {preview.count === 1 ? '' : 's'}.
        </p>
      )}
      {previewErr && <p className="error">{previewErr}</p>}
      {error && <p className="error">{error}</p>}
      <p className="muted" style={{ marginTop: 'var(--ss-space-md)', fontSize: 12 }}>
        Preview: <code>{predicateToEnglish(predicate)}</code>
      </p>
    </form>
  );
}
