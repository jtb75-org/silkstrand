# ADR 018: UI component strategy — headless primitives over a framework

**Status:** Proposed
**Date:** 2026-06-13
**Related:** [`design-system.md`](../design-system.md) (the visual + token source of
truth — this ADR is the component-architecture decision behind executing it).
Precedent: the in-house-auth decision (CLAUDE.md "In-house tenant auth over
Clerk") — we dropped Clerk partly because its embedded UI was too hard to
customize. Same instinct applies to UI frameworks.

---

## Context

Both frontends (`web/` tenant, `backoffice/web/`) are React + TypeScript + **plain
CSS**, no component framework. `design-system.md` already specifies a complete
visual system — `--ss-*` role tokens with light/dark values (§3), **Lucide** icons
with a per-surface map (§6), component looks (§5), and a 4-phase migration (§9) —
but it is **"not yet applied to any shipped UI"**: 0 tokens wired, **451 inline
`style={{}}`** across 18 files, and tables/dialogs/menus are hand-rolled.

The question that prompted this ADR: to clean up the UI and get sort/filter
tables, accessible dialogs, icons, etc. "for free," **should we adopt a component
framework (MUI / Ant / Mantine)?**

Constraints specific to SilkStrand:
- **Principle #8 — single-person sustainability:** boring tech, minimal deps.
- A real **navy/teal brand** + a thorough `design-system.md` we want to *keep*.
- **Two apps** to evolve, not one.
- We have **already been burned** by hard-to-customize third-party UI (Clerk).
- We **already use TanStack Query** — TanStack's headless model is familiar.

## Decision

### D1. No full component framework (MUI / Ant / Mantine / Chakra)

A full kit shapes the entire component tree, styling approach, and theming; it
imposes a rebrand fight against our existing brand + `design-system.md`, adds
heavy dependencies against Principle #8, and is a large, invasive migration across
**two** apps with real lock-in. The "free components" do not justify that. (Ant is
the worst for rebrand; MUI/Mantine are customizable-but-fighty.)

### D2. Adopt *headless* primitives, styled with our own tokens + CSS

Get the hard behavior/a11y for free; keep our markup, brand, and `design-system.md`
styling. Three focused, tree-shakeable, replaceable libraries — not a framework:

- **`@tanstack/react-table`** — table behavior (sort, filter, pagination, column
  model) as hooks; we render `<table>` and style it per `design-system.md` §5.5.
  Same family as our `@tanstack/react-query` → zero new paradigm. Kills the
  sharpest pain (sort/filter on Assets/Findings/Scans) directly.
- **Radix UI Primitives** — unstyled, accessible Dialog / DropdownMenu / Tabs /
  Popover / Tooltip / Combobox; we style with tokens. (Alternative considered:
  **React Aria** — deeper a11y, more verbose. Choose Radix for ergonomics as a
  solo maintainer; revisit React Aria only if we hit a Radix limit.)
- **`lucide-react`** — icons. **Already the documented choice** (`design-system.md`
  §6: Lucide, outline, 1.5 stroke, `color: inherit`).

### D3. Execute the documented token foundation (`design-system.md` §3 / §9 Phase 1)

Wire the `--ss-*` tokens — non-breaking (new code uses them; existing untouched).
The doc targets a shared `packages/design-tokens/tokens.css` imported by both apps.
Start tokens **in-app** to avoid a premature workspace restructure; promote to a
shared package once both apps are actively consuming them.

### D4. Incremental, page-by-page — no big-bang (aligns to `design-system.md` §9)

Each PR migrates one page to tokens + the headless primitives + design-system
classes, sweeping that page's inline styles and promoting repeated card/modal/
drawer scaffolds into reusable classes. The 451 inline styles are retired as a
*byproduct* of this migration, not a separate sweep.

### D5. Both apps, shared conventions

Tenant and backoffice adopt the same primitives + tokens. Thin **styled wrappers**
(`<DataTable>`, `<Dialog>`, `<Menu>`, `<Tabs>`, `<Icon>`) live alongside each app
(or a shared package later) so pages consume *our* API, not the libraries directly
— keeping the libraries swappable.

### D6. Spike first

Before the sweep, prove the stack on one real, data-heavy page: convert its table
to `@tanstack/react-table`, wire `--ss-*` tokens on that page, and swap its icons
to `lucide-react`. Validates ergonomics + bundle impact + brand fit before
committing.

## Consequences

- Get sort/filter/pagination, accessible dialogs/menus, and icons **without
  lock-in or a rebrand fight**; full control of markup + brand retained.
- Adds **3 focused deps** (react-table, radix, lucide) — all headless,
  tree-shakeable, individually replaceable. Consistent with "minimal deps": these
  are libraries behind our own wrappers, not a framework that owns the app.
- The token-wiring + inline-style cleanup happen as part of the same migration —
  the supportability win you were after.
- Not zero-effort: we still style components ourselves (the point — we own the
  look). Far less effort than hand-rolling accessible tables/menus, far less
  lock-in than a framework.

## Resolved decisions

- **Why headless, not a framework** — D1.
- **Why Radix over React Aria** — ergonomics for a solo maintainer; escalate to
  React Aria only on a concrete a11y gap.
- **Why TanStack Table** — same family as react-query, headless, solves the
  sharpest pain.
- **Why Lucide** — already mandated by `design-system.md` §6.
- **Why wrappers (D5)** — keep the libraries swappable; pages depend on our API.

## Scope / phasing (slots into `design-system.md` §9)

- **P0 — spike:** one data-heavy page → TanStack Table + `--ss-*` tokens + Lucide.
  Prove the stack.
- **P1 — token foundation:** wire `--ss-*` (`design-system.md` §3). Non-breaking.
- **P2 — primitives + wrappers:** add Radix + Lucide; build the styled wrappers.
- **P3 — page-by-page migration** in the `design-system.md` §9 order; inline styles
  retired per page.
- **P4 — delete legacy CSS;** optional dark mode (`design-system.md` §2 / §9
  Phase 2).

## Open questions (carry from `design-system.md` §10)

- Toast library vs handwritten notifications (a Radix Toast would fit D2).
- Shared `packages/design-tokens` vs per-app tokens (D3 starts per-app).
- Dark-mode timing (tokens make it cheap; sequence after the sweep).
