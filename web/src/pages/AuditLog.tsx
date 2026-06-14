import { useCallback, useEffect, useState, type CSSProperties, type ReactNode } from 'react';
import type { ColumnDef } from '@tanstack/react-table';
import { ScrollText, SearchX } from 'lucide-react';
import { listAuditEvents, type AuditEvent } from '../api/client';
import { useAuth } from '../auth/useAuth';
import { formatAbsolute, formatRelative } from '../lib/time';
import DataTable from '../components/DataTable';
import EmptyState from '../components/EmptyState';
import CodeBlock from '../components/CodeBlock';

// Dedicated Audit Log page (ADR 005 Addendum A5). Promoted from the Settings
// sub-tab to a top-level admin page using the shipped DataTable + detail-drawer
// + token patterns, rendering the real enriched payload (ip / user_agent /
// actor_email / resource_label). No summary tiles — ADR 005 D5 has no count
// endpoint on purpose (expensive on the partitioned table).

const EVENT_TYPE_OPTIONS = [
  '', 'credential.fetch', 'credential.created', 'credential.updated', 'credential.deleted',
  'credential.mapped', 'credential.unmapped', 'credential.test',
  'scan.dispatched', 'scan.completed', 'scan.failed',
  'scan_definition.created', 'scan_definition.updated', 'scan_definition.deleted', 'scan_definition.executed',
  'rule.created', 'rule.updated', 'rule.deleted', 'rule.fired',
  'agent.connected', 'agent.disconnected', 'agent.upgraded', 'agent.key_rotated', 'agent.deleted', 'agent.created',
  'collection.created', 'collection.updated', 'collection.deleted',
];

const RANGE_OPTIONS: { value: string; label: string; ms: number }[] = [
  { value: '24h', label: 'Last 24 hours', ms: 24 * 60 * 60 * 1000 },
  { value: '7d', label: 'Last 7 days', ms: 7 * 24 * 60 * 60 * 1000 },
  { value: '30d', label: 'Last 30 days', ms: 30 * 24 * 60 * 60 * 1000 },
];

// Event-type → pill tint (ADR 005 D7). The per-category tint pairs have no
// design-system token equivalents, so they stay literal; only the shared radius
// is tokenized.
function eventBadgeStyle(eventType: string): CSSProperties {
  const base: CSSProperties = {
    display: 'inline-block', padding: '2px 8px', borderRadius: 'var(--ss-radius-sm)',
    fontSize: 12, fontWeight: 600, whiteSpace: 'nowrap',
  };
  if (eventType.startsWith('credential.')) return { ...base, background: '#dbeafe', color: '#1e40af' };
  if (eventType.startsWith('scan')) return { ...base, background: '#f3f4f6', color: '#374151' };
  if (eventType.startsWith('rule.')) return { ...base, background: '#fef3c7', color: '#92400e' };
  if (eventType.startsWith('agent.')) return { ...base, background: '#d1fae5', color: '#065f46' };
  if (eventType.startsWith('collection.')) return { ...base, background: '#ede9fe', color: '#5b21b6' };
  return { ...base, background: '#f3f4f6', color: '#374151' };
}

const actorBadgeStyle: CSSProperties = {
  display: 'inline-block', padding: '2px 8px', borderRadius: 'var(--ss-radius-sm)',
  fontSize: 11, fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.3px',
  background: 'var(--ss-bg-raised)', color: 'var(--ss-text-secondary)',
};

// Fallback actor label when no enriched actor_email is present.
function actorFallback(ev: AuditEvent): string {
  if (ev.actor_type === 'system') return 'system';
  const id = ev.actor_id ?? '';
  return id ? `${ev.actor_type}:${id.slice(0, 8)}` : ev.actor_type;
}

// Fallback target label when no enriched resource_label is present.
function targetFallback(ev: AuditEvent): string {
  if (!ev.resource_type) return '—';
  const id = ev.resource_id ?? '';
  return id ? `${ev.resource_type}:${id.slice(0, 8)}` : ev.resource_type;
}

export default function AuditLog() {
  const { active } = useAuth();
  // Guard BEFORE mounting the fetching child, so a non-admin who navigates
  // directly to /audit never instantiates the data hooks / fetch effect and
  // therefore never calls listAuditEvents.
  if (active?.role !== 'admin') {
    return (
      <div>
        <PageHeader />
        <EmptyState icon={<ScrollText />} title="The audit log is available to admins only." />
      </div>
    );
  }
  return <AuditLogContent />;
}

// This child holds the data hooks + fetch effect, so it only mounts for admins
// — a non-admin visiting /audit directly never runs listAuditEvents (parity with
// the old Settings sub-tab, which never mounted AuditTab for non-admins).
// Backend role-gating of the endpoint is the open ADR question (ADR 005 OQ#2)
// and intentionally out of scope here.
function AuditLogContent() {
  const [items, setItems] = useState<AuditEvent[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<AuditEvent | null>(null);

  // Filters
  const [eventType, setEventType] = useState('');
  const [resourceSearch, setResourceSearch] = useState('');
  const [range, setRange] = useState('7d');

  const fetchEvents = useCallback(async (cursor?: string) => {
    setLoading(true);
    setError(null);
    try {
      const rangeMs = RANGE_OPTIONS.find((r) => r.value === range)?.ms ?? RANGE_OPTIONS[1].ms;
      const since = new Date(Date.now() - rangeMs).toISOString();
      const result = await listAuditEvents({
        event_type: eventType || undefined,
        resource_id: resourceSearch || undefined,
        since,
        limit: 50,
        cursor,
      });
      if (cursor) {
        setItems((prev) => [...prev, ...result.items]);
      } else {
        setItems(result.items);
      }
      setNextCursor(result.next_cursor);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [eventType, resourceSearch, range]);

  // Fetch-on-dependency-change: fetchEvents synchronously flips loading/error
  // (the intentional "start loading" transition), then sets items after the
  // await — a genuine data fetch, not the derived-state anti-pattern the rule
  // targets. Load-bearing under eslint 10.5.0 / react-hooks 7.1.1 (removal →
  // set-state-in-effect error); a react-query migration would drop the manual
  // effect entirely (tracked separately).
  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { fetchEvents(); }, [fetchEvents]);

  const columns: ColumnDef<AuditEvent>[] = [
    {
      id: 'time',
      header: 'Time',
      accessorFn: (e) => e.occurred_at, // ISO sorts chronologically
      cell: ({ row }) => (
        <div>
          <div>{formatRelative(row.original.occurred_at)}</div>
          <div className="muted" style={{ fontSize: 12 }}>{formatAbsolute(row.original.occurred_at)}</div>
        </div>
      ),
    },
    {
      id: 'event',
      header: 'Event',
      accessorFn: (e) => e.event_type,
      cell: ({ row }) => <span style={eventBadgeStyle(row.original.event_type)}>{row.original.event_type}</span>,
    },
    {
      id: 'actor',
      header: 'Actor',
      accessorFn: (e) => e.payload.actor_email || e.actor_type,
      cell: ({ row }) => {
        const ev = row.original;
        return (
          <div>
            <span style={actorBadgeStyle}>{ev.actor_type}</span>
            <div className="muted" style={{ fontSize: 12, fontFamily: 'monospace', marginTop: 2 }}>
              {ev.payload.actor_email || actorFallback(ev)}
            </div>
          </div>
        );
      },
    },
    {
      id: 'target',
      header: 'Target',
      enableSorting: false,
      cell: ({ row }) => row.original.payload.resource_label || targetFallback(row.original),
    },
    {
      id: 'source_ip',
      header: 'Source IP',
      enableSorting: false,
      cell: ({ row }) => (
        <span style={{ fontFamily: 'monospace', fontSize: 13 }}>{row.original.payload.ip || '—'}</span>
      ),
    },
  ];

  return (
    <div>
      <PageHeader />
      <p className="muted" style={{ marginBottom: 'var(--ss-space-lg)' }}>
        Read-only log of privileged operations.
      </p>

      <div style={{ display: 'flex', gap: 'var(--ss-space-md)', marginBottom: 'var(--ss-space-lg)', flexWrap: 'wrap' }}>
        <select
          value={eventType}
          onChange={(e) => setEventType(e.target.value)}
          style={{ padding: '6px 10px', borderRadius: 'var(--ss-radius-sm)', border: '1px solid var(--ss-border-default)' }}
        >
          <option value="">All event types</option>
          {EVENT_TYPE_OPTIONS.filter(Boolean).map((t) => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
        <select
          value={range}
          onChange={(e) => setRange(e.target.value)}
          style={{ padding: '6px 10px', borderRadius: 'var(--ss-radius-sm)', border: '1px solid var(--ss-border-default)' }}
        >
          {RANGE_OPTIONS.map((r) => (
            <option key={r.value} value={r.value}>{r.label}</option>
          ))}
        </select>
        <input
          type="text"
          placeholder="Resource ID..."
          value={resourceSearch}
          onChange={(e) => setResourceSearch(e.target.value)}
          style={{ padding: '6px 10px', borderRadius: 'var(--ss-radius-sm)', border: '1px solid var(--ss-border-default)', width: 240 }}
        />
      </div>

      {error && <p className="error">{error}</p>}

      {!loading && !error && items.length === 0 ? (
        <EmptyState icon={<SearchX />} title="No audit events found for the selected filters." />
      ) : (
        items.length > 0 && (
          <DataTable
            columns={columns}
            data={items}
            getRowId={(e) => e.id}
            onRowClick={(e) => setSelected(e)}
            initialSorting={[{ id: 'time', desc: true }]}
          />
        )
      )}

      {loading && <p className="muted" style={{ marginTop: 'var(--ss-space-md)' }}>Loading…</p>}

      {nextCursor && !loading && (
        <button
          className="btn btn-secondary"
          onClick={() => fetchEvents(nextCursor)}
          style={{ marginTop: 'var(--ss-space-md)' }}
        >
          Load more
        </button>
      )}

      {selected && <AuditDetailDrawer event={selected} onClose={() => setSelected(null)} />}
    </div>
  );
}

function PageHeader() {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--ss-space-sm)' }}>
      <ScrollText size={24} style={{ color: 'var(--ss-accent-primary)' }} />
      <h1 style={{ margin: 0 }}>Audit Log</h1>
    </div>
  );
}

function Field({ label, value, mono }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <div style={{ display: 'flex', gap: 'var(--ss-space-md)', padding: 'var(--ss-space-xs) 0' }}>
      <span className="muted" style={{ minWidth: 110, fontSize: 12 }}>{label}</span>
      <span style={{ fontFamily: mono ? 'monospace' : undefined, fontSize: 13, wordBreak: 'break-word' }}>{value}</span>
    </div>
  );
}

function AuditDetailDrawer({ event, onClose }: { event: AuditEvent; onClose: () => void }) {
  const p = event.payload;
  return (
    <>
      <div className="drawer-backdrop" onClick={onClose} />
      <aside className="drawer" role="dialog" aria-label="Audit event detail">
        <header className="drawer-header">
          <h2>Audit event</h2>
          <button type="button" className="btn btn-sm" onClick={onClose}>×</button>
        </header>
        <div className="drawer-body">
          <section>
            <h3>General Info</h3>
            <Field label="Time" value={formatAbsolute(event.occurred_at)} />
            <Field label="Event" value={<span style={eventBadgeStyle(event.event_type)}>{event.event_type}</span>} />
            <Field label="Actor type" value={event.actor_type} />
            {p.actor_email && <Field label="Actor email" value={String(p.actor_email)} />}
            {event.actor_id && <Field label="Actor ID" value={event.actor_id} mono />}
            <Field label="Source IP" value={p.ip ? String(p.ip) : '—'} mono />
            {p.user_agent && <Field label="User agent" value={String(p.user_agent)} />}
          </section>

          <section>
            <h3>Target</h3>
            <Field label="Type" value={event.resource_type || '—'} />
            <Field label="Label" value={p.resource_label ? String(p.resource_label) : '—'} />
            <Field label="ID" value={event.resource_id || '—'} mono />
          </section>

          <section>
            <h3>Raw Metadata</h3>
            <CodeBlock content={JSON.stringify(event.payload, null, 2)} />
          </section>
        </div>
      </aside>
    </>
  );
}
