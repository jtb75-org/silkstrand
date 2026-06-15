import { useCallback, useEffect, useId, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { MoreVertical } from 'lucide-react';

export interface MenuItem {
  key: string;
  label: string;
  onSelect: () => void;
  /** Optional leading Lucide icon, sized by the caller (e.g. `<Trash2 size={14} />`). */
  icon?: ReactNode;
  /** Render in danger styling (red) — for destructive actions like delete. */
  destructive?: boolean;
  disabled?: boolean;
}

interface MenuProps {
  items: MenuItem[];
  /** Accessible label for the trigger (e.g. "Actions for jane@acme.io"). */
  ariaLabel?: string;
}

// Menu — the design-system overflow / kebab row-action menu (ADR 020). A "⋮"
// trigger button toggles a popup of action items. Accessible: the trigger
// advertises aria-haspopup / aria-expanded, the popup is role="menu" with
// role="menuitem" buttons, Escape and click-outside close it (returning focus
// to the trigger), and ArrowUp/Down/Home/End move focus between items.
// Destructive items render via `--ss-danger`. Intended to live in a clickable
// table row — it stops propagation so opening the menu never triggers row click.
export default function Menu({ items, ariaLabel = 'Actions' }: MenuProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popupRef = useRef<HTMLDivElement>(null);
  const menuId = useId();

  const close = useCallback((focusTrigger: boolean) => {
    setOpen(false);
    if (focusTrigger) triggerRef.current?.focus();
  }, []);

  // Close on outside click and on Escape (Escape returns focus to the trigger).
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        close(true);
      }
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open, close]);

  // When opening, move focus to the first enabled item.
  useEffect(() => {
    if (!open) return;
    const first = popupRef.current?.querySelector<HTMLButtonElement>(
      'button.ss-menu__item:not(:disabled)',
    );
    first?.focus();
  }, [open]);

  // ArrowUp/Down/Home/End roving focus within the popup.
  const onPopupKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) return;
    e.preventDefault();
    const buttons = Array.from(
      popupRef.current?.querySelectorAll<HTMLButtonElement>(
        'button.ss-menu__item:not(:disabled)',
      ) ?? [],
    );
    if (buttons.length === 0) return;
    const idx = buttons.indexOf(document.activeElement as HTMLButtonElement);
    let next = 0;
    if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = buttons.length - 1;
    else if (e.key === 'ArrowDown') next = idx < buttons.length - 1 ? idx + 1 : 0;
    else next = idx > 0 ? idx - 1 : buttons.length - 1;
    buttons[next]?.focus();
  };

  return (
    <div
      ref={rootRef}
      className="ss-menu"
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        // Keep row-level keyboard activation from firing when interacting here.
        if (e.target === e.currentTarget) return;
      }}
    >
      <button
        ref={triggerRef}
        type="button"
        className="ss-menu__trigger"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={ariaLabel}
        onClick={() => setOpen((v) => !v)}
      >
        <MoreVertical size={16} aria-hidden="true" />
      </button>
      {open && (
        <div
          ref={popupRef}
          id={menuId}
          className="ss-menu__popup"
          role="menu"
          aria-label={ariaLabel}
          onKeyDown={onPopupKeyDown}
        >
          {items.map((item) => (
            <button
              key={item.key}
              type="button"
              role="menuitem"
              disabled={item.disabled}
              className={
                item.destructive ? 'ss-menu__item ss-menu__item--danger' : 'ss-menu__item'
              }
              onClick={() => {
                close(false);
                item.onSelect();
              }}
            >
              {item.icon && (
                <span className="ss-menu__icon" aria-hidden="true">
                  {item.icon}
                </span>
              )}
              {item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
