import type { CSSProperties } from 'react';

// SummaryChips — a reusable count summary (ADR 018). Generic over its segments:
// the caller supplies {label, count, color, optional onClick}, so it is NOT tied
// to severity. Renders either a horizontal stacked `bar` (with a legend) or a
// row of `chips`. Reused for Dashboard "Findings by severity", and later the
// Findings severity chips + Agents status chips.

export interface SummarySegment {
  key: string;
  label: string;
  count: number;
  /** Any CSS color — pass a design-system token var, e.g. 'var(--ss-danger)'. */
  color: string;
  /** Optional click (filter / navigate). When set, the segment renders as a button. */
  onClick?: () => void;
}

interface SummaryChipsProps {
  segments: SummarySegment[];
  variant?: 'bar' | 'chips';
  /** Total override; defaults to the sum of segment counts. Drives the empty state. */
  total?: number;
  emptyText?: string;
}

export default function SummaryChips({
  segments,
  variant = 'chips',
  total,
  emptyText = 'No data',
}: SummaryChipsProps) {
  const sum = total ?? segments.reduce((acc, s) => acc + s.count, 0);
  if (sum === 0) return <span className="muted">{emptyText}</span>;

  if (variant === 'bar') {
    const nonZero = segments.filter((s) => s.count > 0);
    return (
      <div>
        <div
          style={{
            display: 'flex',
            gap: 2,
            height: 10,
            borderRadius: 'var(--ss-radius-sm)',
            overflow: 'hidden',
          }}
        >
          {nonZero.map((s) => {
            const style: CSSProperties = { flex: s.count, background: s.color, minWidth: 4 };
            return s.onClick ? (
              <button
                key={s.key}
                type="button"
                onClick={s.onClick}
                title={`${s.label}: ${s.count}`}
                aria-label={`${s.label}: ${s.count}`}
                style={{ ...style, border: 'none', padding: 0, cursor: 'pointer' }}
              />
            ) : (
              <div key={s.key} style={style} title={`${s.label}: ${s.count}`} />
            );
          })}
        </div>
        <div
          style={{
            display: 'flex',
            gap: 'var(--ss-space-md)',
            flexWrap: 'wrap',
            marginTop: 'var(--ss-space-sm)',
          }}
        >
          {segments.map((s) => (
            <Legend key={s.key} segment={s} />
          ))}
        </div>
      </div>
    );
  }

  return (
    <div style={{ display: 'inline-flex', gap: 'var(--ss-space-sm)', flexWrap: 'wrap' }}>
      {segments.filter((s) => s.count > 0).map((s) => (
        <Chip key={s.key} segment={s} />
      ))}
    </div>
  );
}

function Dot({ color }: { color: string }) {
  return (
    <span
      style={{ width: 8, height: 8, borderRadius: '50%', background: color, display: 'inline-block', flexShrink: 0 }}
    />
  );
}

function Legend({ segment: s }: { segment: SummarySegment }) {
  const style: CSSProperties = {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 'var(--ss-space-xs)',
    fontSize: 'var(--ss-text-body-sm)',
  };
  const inner = (
    <>
      <Dot color={s.color} />
      <span className="muted">{s.label}</span>
      <strong>{s.count}</strong>
    </>
  );
  return s.onClick ? (
    <button
      type="button"
      onClick={s.onClick}
      style={{ ...style, background: 'none', border: 'none', cursor: 'pointer', padding: 0 }}
    >
      {inner}
    </button>
  ) : (
    <span style={style}>{inner}</span>
  );
}

function Chip({ segment: s }: { segment: SummarySegment }) {
  const style: CSSProperties = {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 'var(--ss-space-xs)',
    padding: '2px 8px',
    borderRadius: 'var(--ss-radius-full)',
    border: '1px solid var(--ss-border-default)',
    fontSize: 'var(--ss-text-body-sm)',
  };
  const inner = (
    <>
      <Dot color={s.color} />
      <span>{s.label}</span>
      <strong>{s.count}</strong>
    </>
  );
  return s.onClick ? (
    <button type="button" onClick={s.onClick} style={{ ...style, background: 'none', cursor: 'pointer' }}>
      {inner}
    </button>
  ) : (
    <span style={style}>{inner}</span>
  );
}
