# ADR 020: Backoffice design-system migration — adopt the tenant `--ss-*` system with a navy/teal palette

**Status:** Accepted
**Date:** 2026-06-14
**Related:** [ADR 018](018-ui-component-strategy.md) (UI component strategy —
headless primitives over a framework; the tenant app executed it), and the
tenant token layer in `web/src/index.css`. This ADR extends that same strategy
to `backoffice/web/`.

---

## Context

The tenant app (`web/`) has migrated to a design-system token layer (`--ss-*`
role tokens), a thin `DataTable` wrapper over `@tanstack/react-table`, a shared
`EmptyState` primitive, and Lucide icons (ADR 018). The **backoffice**
(`backoffice/web/`) has not: its pages use hand-rolled `<table>` markup, literal
hex colors throughout `index.css`, ad-hoc inline action buttons per row, and no
shared token vocabulary.

We want the backoffice to share the tenant's design-system *structure* — so
primitives port unchanged and contributors learn one vocabulary — while keeping
its distinct **navy/teal brand identity** (navy `#0d1b2a` headings, teal
`#1b998b` accent) that visually separates the admin console from the tenant app.

## Decision

1. **Adopt the tenant `--ss-*` token NAMES, with backoffice navy/teal VALUES.**
   `backoffice/web/src/index.css` gains a `:root` token block using the exact
   same token names as `web/` (accent / status / surface / text / border /
   spacing / radius / shadow / motion / typography), but the accent maps to teal
   (`--ss-accent-primary: #1b998b`) and `--ss-text-primary` to navy (`#0d1b2a`).
   Status, spacing, radius, and typography scales mirror the tenant values so the
   two systems stay structurally identical. This makes the shared primitives
   portable with zero edits and keeps the brand split a matter of token values.

2. **Port the shared primitives unchanged:** `DataTable` (the ADR 018 locked
   contract — thin `@tanstack/react-table` wrapper; props `columns` / `data` /
   `initialSorting` / `getRowId` / `onRowClick` / `selectable` / `selectedIds` /
   `onToggleRow` / `onToggleAll`; no per-row style API, no row-expand API) and
   `EmptyState`. They render the existing `.table` / `.ss-empty` classes against
   the new tokens.

3. **New row-action pattern: an ellipsis (kebab) overflow `Menu`.** Backoffice
   rows currently carry inline action buttons. We standardize on a single "⋮"
   overflow menu per row (`Menu` component): accessible (trigger advertises
   `aria-haspopup`/`aria-expanded`; popup is `role="menu"` with `role="menuitem"`
   buttons; Escape + click-outside close and restore focus; ArrowUp/Down/Home/End
   roving focus), with destructive items rendered via `--ss-danger`. It stops
   click propagation so it composes inside a clickable `DataTable` row.

4. **Migrate one page per PR.** Foundation lands first (tokens + primitives, no
   page restyle), then **Users** as the pilot, then DataCenters / Tenants in
   subsequent PRs. Pages keep their existing behavior (confirms, mutations,
   role-gating) across the migration.

### Scope of this PR (PR-1, foundation only)

- ADR (this document).
- `--ss-*` token block in `backoffice/web/src/index.css` (additive; existing page
  rules keep their literal hex and migrate later).
- `DataTable` + `EmptyState` ported into `backoffice/web/src/components/`.
- New `Menu` (kebab) component.
- Add `@tanstack/react-table` + `lucide-react` deps (already in the tenant app).

No existing page is restyled in this PR. The Users pilot follows in PR-2.

## Consequences

- **One design vocabulary across both apps.** Contributors learn `--ss-*` once;
  primitives copy across without rework. The navy/teal split lives entirely in
  token values, not in component code.
- **Accessible, consistent row actions.** The kebab `Menu` replaces per-page
  inline button clusters with one keyboard-navigable pattern.
- **Two token sources to keep aligned.** `web/` and `backoffice/web/` each own a
  `:root` block. They share names but not files; a future shared token package
  could dedupe, but that is out of scope (Principle #8 — minimal deps).
- **Inter not yet bundled in the backoffice.** `--ss-font-sans` starts at the
  system stack; the token name matches the tenant app so Inter can be adopted
  later without touching consumers.
- **Gradual migration.** Until each page is converted, the backoffice mixes
  token-driven primitives with literal-hex legacy CSS. The additive token block
  makes this safe — nothing existing changes appearance until a page opts in.
