import type { ReactNode } from 'react';

interface EmptyStateProps {
  /** Lucide icon element, e.g. `<Users />`. Sized/colored via the `.icon` class. */
  icon: ReactNode;
  /** One-line explanation of what this surface contains (design-system §5.7). */
  title: string;
  /** Optional secondary line or a primary action (e.g. a "Create" button). */
  children?: ReactNode;
  className?: string;
}

// EmptyState — the design-system §5.7 empty state as a shared primitive: a muted
// outline icon, a one-line title, and optional body/action. Uses the existing
// `.ss-empty` class + tokens; intentionally narrow (not a card, no page copy).
// Ported to the backoffice per ADR 020.
export default function EmptyState({ icon, title, children, className }: EmptyStateProps) {
  return (
    <div className={className ? `ss-empty ${className}` : 'ss-empty'}>
      <span className="icon" aria-hidden="true">{icon}</span>
      <span>{title}</span>
      {children}
    </div>
  );
}
