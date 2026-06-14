import { useMemo, useState } from 'react';

// Flat-conjunction visual builder for the predicate grammar used by
// asset_sets.predicate and correlation_rules.body.match. Each row is a
// (field, operator, value) triple; rows join with implicit $and.
//
// Anything the flat form cannot represent ($or, $not, nested groups,
// mixed shapes) falls back to the raw-JSON editor with a warning. That
// keeps power users unblocked while the 90% case gets a real form.
//
// Grammar reference: api/internal/rules/predicate.go.

export type Predicate = Record<string, unknown>;

interface Props {
  value: Predicate;
  onChange: (next: Predicate) => void;
}

type ScalarOp = '$eq' | '$ne' | '$regex' | '$cidr' | '$gt' | '$gte' | '$lt' | '$lte';
type SpecialOp = '$in' | '$exists';
type Op = ScalarOp | SpecialOp;

const OPS: { value: Op; label: string }[] = [
  { value: '$eq', label: 'equals' },
  { value: '$ne', label: 'not equals' },
  { value: '$in', label: 'one of (comma-separated)' },
  { value: '$regex', label: 'matches regex' },
  { value: '$cidr', label: 'in CIDR' },
  { value: '$gt', label: '>' },
  { value: '$gte', label: '≥' },
  { value: '$lt', label: '<' },
  { value: '$lte', label: '≤' },
  { value: '$exists', label: 'exists (true/false)' },
];

// Known fields surfaced in the datalist. Users can type free-text for
// technologies.<tag> or future additions.
const KNOWN_FIELDS = [
  'ip',
  'port',
  'hostname',
  'service',
  'version',
  'environment',
  'source',
  'compliance_status',
  'first_seen',
  'last_seen',
  'technologies.postgresql',
  'technologies.mysql',
  'technologies.nginx',
  'cves',
  'cves.severity',
  'cves.id',
];

const NUMERIC_FIELDS = new Set(['port']);

interface Row {
  id: string;
  field: string;
  op: Op;
  value: string; // single input; coerced on serialize
}

export default function PredicateBuilder({ value, onChange }: Props) {
  // Initial mode is decided by whether `value` fits the flat form.
  const initial = useMemo(() => jsonToRows(value), [value]);
  const valueText = useMemo(() => JSON.stringify(value, null, 2), [value]);
  const [rawText, setRawText] = useState(valueText);
  const [syncedText, setSyncedText] = useState(valueText);
  const [rawErr, setRawErr] = useState<string | null>(null);
  const [mode, setMode] = useState<'builder' | 'raw'>(initial ? 'builder' : 'raw');
  const [rows, setRows] = useState<Row[]>(initial ?? []);

  // When `value` changes from outside (e.g. reset on submit), resync the raw
  // editor. Done during render via a tracked signature rather than a
  // set-state-in-effect — same result, one fewer render, no effect.
  if (valueText !== syncedText) {
    setSyncedText(valueText);
    setRawText(valueText);
  }

  function emitRows(next: Row[]) {
    setRows(next);
    onChange(rowsToJSON(next));
  }

  function handleRaw(text: string) {
    setRawText(text);
    try {
      const parsed = JSON.parse(text);
      if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
        setRawErr('predicate must be a JSON object');
        return;
      }
      setRawErr(null);
      onChange(parsed as Predicate);
    } catch (err) {
      setRawErr((err as Error).message);
    }
  }

  function switchToBuilder() {
    const parsed = jsonToRows(value);
    if (!parsed) {
      setRawErr('this predicate uses $or / $not / nested groups — keep editing as raw JSON');
      return;
    }
    setRows(parsed);
    setMode('builder');
  }

  if (mode === 'raw') {
    return (
      <div>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
          <p className="muted" style={{ margin: 0, fontSize: 12 }}>Raw JSON mode</p>
          <button type="button" className="btn btn-small" onClick={switchToBuilder}>
            Back to builder
          </button>
        </div>
        <textarea
          value={rawText}
          onChange={(e) => handleRaw(e.target.value)}
          rows={8}
          style={{ fontFamily: 'monospace', width: '100%' }}
        />
        {rawErr && <p className="error" style={{ marginTop: 4 }}>{rawErr}</p>}
      </div>
    );
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 6 }}>
        <p className="muted" style={{ margin: 0, fontSize: 12 }}>All conditions must match (AND).</p>
        <button type="button" className="btn btn-small" onClick={() => setMode('raw')}>
          Advanced (raw JSON)
        </button>
      </div>
      <table className="table" style={{ marginBottom: 8 }}>
        <thead>
          <tr>
            <th style={{ width: '30%' }}>Field</th>
            <th style={{ width: '25%' }}>Operator</th>
            <th>Value</th>
            <th style={{ width: 1 }}></th>
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 && (
            <tr>
              <td colSpan={4} className="muted" style={{ textAlign: 'center' }}>
                No conditions. Add one below — an empty predicate matches every asset.
              </td>
            </tr>
          )}
          {rows.map((r, i) => (
            <tr key={r.id}>
              <td>
                <input
                  list="predicate-fields"
                  value={r.field}
                  onChange={(e) => emitRows(rows.map((x, j) => (j === i ? { ...x, field: e.target.value } : x)))}
                  placeholder="service"
                  style={{ width: '100%' }}
                />
              </td>
              <td>
                <select
                  value={r.op}
                  onChange={(e) => emitRows(rows.map((x, j) => (j === i ? { ...x, op: e.target.value as Op } : x)))}
                  style={{ width: '100%' }}
                >
                  {OPS.map((o) => (
                    <option key={o.value} value={o.value}>{o.label}</option>
                  ))}
                </select>
              </td>
              <td>
                {r.op === '$exists' ? (
                  <select
                    value={r.value || 'true'}
                    onChange={(e) => emitRows(rows.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))}
                    style={{ width: '100%' }}
                  >
                    <option value="true">true</option>
                    <option value="false">false</option>
                  </select>
                ) : (
                  <input
                    value={r.value}
                    onChange={(e) => emitRows(rows.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))}
                    placeholder={valuePlaceholder(r.op)}
                    style={{ width: '100%' }}
                  />
                )}
              </td>
              <td>
                <button
                  type="button"
                  className="btn btn-small btn-danger"
                  onClick={() => emitRows(rows.filter((_, j) => j !== i))}
                  aria-label="Remove condition"
                >
                  ×
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <datalist id="predicate-fields">
        {KNOWN_FIELDS.map((f) => (<option key={f} value={f} />))}
      </datalist>
      <button
        type="button"
        className="btn btn-small"
        onClick={() => emitRows([...rows, { id: crypto.randomUUID(), field: '', op: '$eq', value: '' }])}
      >
        Add condition
      </button>
    </div>
  );
}

function valuePlaceholder(op: Op): string {
  switch (op) {
    case '$in': return 'postgresql, mysql';
    case '$cidr': return '10.0.0.0/16';
    case '$regex': return '^16\\.';
    case '$gt':
    case '$gte':
    case '$lt':
    case '$lte': return '42 or 2026-01-01T00:00:00Z';
    default: return 'postgresql';
  }
}

// ────────── serialize / parse ──────────

function rowsToJSON(rows: Row[]): Predicate {
  const terms = rows
    .filter((r) => r.field.trim() !== '')
    .map(rowToTerm);
  if (terms.length === 0) return {};
  if (terms.length === 1) return terms[0];
  return { $and: terms };
}

function rowToTerm(r: Row): Predicate {
  const val = coerceValue(r);
  if (r.op === '$eq') return { [r.field]: val };
  return { [r.field]: { [r.op]: val } };
}

function coerceValue(r: Row): unknown {
  if (r.op === '$exists') return r.value !== 'false';
  if (r.op === '$in') {
    return r.value.split(',').map((s) => {
      const t = s.trim();
      if (NUMERIC_FIELDS.has(r.field) && /^-?\d+(\.\d+)?$/.test(t)) return Number(t);
      return t;
    });
  }
  if (NUMERIC_FIELDS.has(r.field) && /^-?\d+(\.\d+)?$/.test(r.value.trim())) {
    return Number(r.value.trim());
  }
  return r.value;
}

// jsonToRows returns rows if `p` fits the flat-conjunction form, else null.
function jsonToRows(p: Predicate): Row[] | null {
  if (!p || typeof p !== 'object' || Array.isArray(p)) return null;
  const keys = Object.keys(p);
  if (keys.length === 0) return [];
  // Top-level $and with array of simple terms.
  if (keys.length === 1 && keys[0] === '$and') {
    const arr = p.$and;
    if (!Array.isArray(arr)) return null;
    const rows: Row[] = [];
    for (const term of arr) {
      const r = termToRow(term as Predicate);
      if (!r) return null;
      rows.push(r);
    }
    return rows;
  }
  // Single term.
  const r = termToRow(p);
  return r ? [r] : null;
}

function termToRow(t: Predicate): Row | null {
  if (!t || typeof t !== 'object' || Array.isArray(t)) return null;
  const entries = Object.entries(t);
  if (entries.length !== 1) return null;
  const [field, raw] = entries[0];
  if (field.startsWith('$')) return null; // $or / $not / etc — not flat.
  // Bare scalar → $eq.
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
    return { id: crypto.randomUUID(), field, op: '$eq', value: formatValue(raw) };
  }
  // Operator object with a single $op key.
  const opEntries = Object.entries(raw as Record<string, unknown>);
  if (opEntries.length !== 1) return null;
  const [op, val] = opEntries[0];
  if (!OPS.some((o) => o.value === op)) return null;
  return { id: crypto.randomUUID(), field, op: op as Op, value: formatValue(val) };
}

function formatValue(v: unknown): string {
  if (Array.isArray(v)) return v.map((x) => String(x)).join(', ');
  if (typeof v === 'boolean') return v ? 'true' : 'false';
  if (v === null || v === undefined) return '';
  return String(v);
}
