---
name: SilkStrand
colors:
  # Canonical = light theme (default on :root). Dark equivalents in §2.
  background: '#ffffff'
  on-background: '#111827'
  surface: '#ffffff'
  surface-dim: '#f3f4f6'
  surface-bright: '#ffffff'
  surface-container-lowest: '#ffffff'
  surface-container-low: '#f9fafb'
  surface-container: '#f3f4f6'
  surface-container-high: '#f3f4f6'
  surface-container-highest: '#e5e7eb'
  on-surface: '#111827'
  on-surface-variant: '#374151'
  outline: '#d1d5db'
  outline-variant: '#e5e7eb'
  outline-strong: '#9ca3af'
  primary: '#3b82f6'
  on-primary: '#ffffff'
  primary-hover: '#2563eb'
  primary-container: '#93c5fd'
  on-primary-container: '#1d4ed8'
  secondary: '#374151'
  on-secondary: '#ffffff'
  secondary-container: '#f3f4f6'
  on-secondary-container: '#374151'
  error: '#ef4444'
  on-error: '#ffffff'
  error-container: '#fee2e2'
  on-error-container: '#991b1b'
  success: '#10b981'
  success-container: '#d1fae5'
  warning: '#f59e0b'
  warning-container: '#fef3c7'
  info: '#06b6d4'
  info-container: '#cffafe'
typography:
  # Spec'd in docs/design-system.md §4 (semantic, not shirt-sized). NOTE: these
  # typography tokens are NOT yet defined in web/src/index.css — see §6 Gaps.
  h1:
    fontFamily: Inter
    fontSize: 28px
    fontWeight: '600'
    lineHeight: 1.2
    letterSpacing: '0'
  h2:
    fontFamily: Inter
    fontSize: 22px
    fontWeight: '600'
    lineHeight: 1.2
    letterSpacing: '0'
  h3:
    fontFamily: Inter
    fontSize: 18px
    fontWeight: '600'
    lineHeight: 1.2
    letterSpacing: '0'
  h4:
    fontFamily: Inter
    fontSize: 16px
    fontWeight: '600'
    lineHeight: 1.2
    letterSpacing: '0'
  body-lg:
    fontFamily: Inter
    fontSize: 16px
    fontWeight: '400'
    lineHeight: 1.5
    letterSpacing: '0'
  body:
    fontFamily: Inter
    fontSize: 14px
    fontWeight: '400'
    lineHeight: 1.5
    letterSpacing: '0'
  body-sm:
    fontFamily: Inter
    fontSize: 13px
    fontWeight: '400'
    lineHeight: 1.5
    letterSpacing: '0'
  caption:
    fontFamily: Inter
    fontSize: 12px
    fontWeight: '500'
    lineHeight: 1.4
    letterSpacing: 0.04em
  mono:
    fontFamily: JetBrains Mono
    fontSize: 13px
    fontWeight: '400'
    lineHeight: 1.5
    letterSpacing: '0'
rounded:
  sm: 0.25rem      # 4px — inputs, small buttons
  DEFAULT: 0.375rem # 6px — primary buttons, tables
  md: 0.375rem     # 6px
  lg: 0.5rem       # 8px — cards, modals
  full: 9999px     # pills, avatars
spacing:
  unit: 4px
  xs: 4px
  sm: 8px
  md: 12px
  lg: 16px
  xl: 24px
  2xl: 32px
  3xl: 48px
  gutter: 16px
  sidebar: 220px
  content-max: 1200px
---

# Design System: SilkStrand

> Extracted from the tenant frontend (`web/`) as-built: `web/src/index.css`
> (`--ss-*` tokens + legacy classes), shared components, and the authoritative
> spec at `docs/design-system.md` + `docs/adr/018-ui-component-strategy.md`.
> Where code and spec disagree, this document follows the **spec** and flags the
> divergence in §6.

## 1. Visual Theme & Atmosphere

SilkStrand is a CIS-compliance scanner for enterprise security teams, and its
interface is deliberately **"quietly powerful."** It reads lightweight, secure,
and technical-but-approachable — calm and precise rather than loud. It pointedly
rejects the security-product cliché kit: no hacker-green terminals, no neon
alert colors, no padlocks/flames/shields, no heavy gradients. The canvas is
near-white (`#ffffff` surfaces over a faint `#f5f5f8` body), neutrals are cool
(slate-grays, never warm tans), and a single calm brand blue carries all
interactivity. The one decorative flourish is a thin blue **woven-thread wave
texture** on deep navy, used full-bleed *only* behind auth screens and as a
≤20% overlay behind empty dashboards — never behind tables, forms, or chrome.

Density is **information-first**: operators scan long lists of assets, findings,
and scan runs, so the layout favors comfortable-but-compact rows (a 42px table
row is the target — not 60px "breathing room"), a fixed 220px sidebar, and a
1200px left-aligned content column. Whitespace comes from a strict 4px grid, not
from oversized padding. The system ships **light + dark themes** with
system-preference detection; light is canonical and dark is defined token-for-
token (page `#0f172a`, surfaces `#1f2937`) but currently inert until the theme
switch is wired. The net feel is an enterprise control plane that trusts the
operator with dense data while staying visually unhurried.

## 2. Color Palette & Roles

Tokens are named by **role, never by hue** (`--ss-bg-surface`, not `--ss-slate-900`).
Accent and status hues are shared across themes; surfaces/text/borders flip.

### Accent & Interactive
- **Brand Blue** `#3b82f6` (`--ss-accent-primary`) — primary buttons, links, active nav, focus ring. The single interactive color.
- **Brand Blue Hover** `#2563eb` (`--ss-accent-hover`) — hover/pressed.
- **Brand Blue Strong** `#1d4ed8` (`--ss-accent-subtle`) — emphasis on tinted containers.
- **Sky Soft** `#93c5fd` (`--ss-accent-soft`) — subtle accent fills.

### Primary Foundation (light / dark)
- **Base** `#ffffff` / `#0f172a` (`--ss-bg-base`) — page background.
- **Surface** `#ffffff` / `#1f2937` (`--ss-bg-surface`) — cards, panels.
- **Subtle** `#f9fafb` / `#111827` (`--ss-bg-subtle`) — sidebar, muted panels.
- **Raised** `#f3f4f6` / `#374151` (`--ss-bg-raised`) — hovered rows, raised surfaces.

### Typography & Text Hierarchy (light / dark)
- **Primary text** `#111827` / `#f9fafb` (`--ss-text-primary`).
- **Secondary text** `#374151` / `#d1d5db` (`--ss-text-secondary`).
- **Muted text** `#6b7280` / `#9ca3af` (`--ss-text-muted`) — captions, table headers, help.
- **On-accent** `#ffffff` (`--ss-text-on-accent`).
- Borders: **subtle** `#e5e7eb`, **default** `#d1d5db`, **strong** `#9ca3af` (dark: `#1f2937` / `#374151` / `#4b5563`).

### Functional States (foreground + container)
- **Success** `#10b981` on `#d1fae5` — passing controls, connected agents.
- **Warning** `#f59e0b` on `#fef3c7` — queued/running, attention.
- **Danger** `#ef4444` on `#fee2e2` — failures, destructive actions, errors.
- **Info** `#06b6d4` on `#cffafe` — neutral informational.

Status backgrounds become low-opacity overlays in dark mode (e.g. `rgba(16,185,129,.15)`).
**Color is never the sole signal** — every status badge pairs its color with a label word.

## 3. Typography Rules

**Primary family:** `Inter, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`
— a neutral, highly legible humanist sans that suits dense tabular data.
**Monospace:** `"JetBrains Mono", ui-monospace, "SF Mono", Menlo, Consolas, monospace`
for IDs, CVEs, command lines, and log consoles.

### Hierarchy & Weights
Semantic scale (px): `caption 12 · body-sm 13 · body 14 (default) · body-lg 16 ·
h4 16 · h3 18 · h2 22 · h1 28`. Weights: regular **400** (body), medium **500**
(buttons, labels, badges), semibold **600** (headings), bold **700**. Line
heights: tight **1.2** (headings), normal **1.5** (body), loose **1.7** (prose).

### Spacing Principles
Table headers and form labels are **uppercase 12px, letter-spacing ~0.04em,
muted color** — the one place caps are used. Everything else is **sentence case**.
Letter-spacing is otherwise neutral (`0`); headings are not tracked-tight.

## 4. Component Stylings

### Buttons
Inline-flex, `gap 8px`, `padding 8px 14px`, `radius 6px`, weight medium, with a
`120ms ease` color transition and a 2px brand-blue `:focus-visible` ring offset
2px. Three variants: **primary** (solid brand blue, white text), **secondary**
(transparent, default border, raised-bg on hover), **danger** (solid red). A
`-sm` modifier drops to `4px 10px / 12px`. Labels are **sentence case, verb-first**
("Sign in", "Rotate key", "Delete tenant") — never "Yes"/"Submit". Disabled = `.55` opacity.

### Cards & Containers
`surface` background, `1px` subtle border, `radius 8px`, `padding 24px`. **Never
nest cards** — use a dividing border for sub-panels. Shadows (`sm/md/lg`) are
used sparingly in light mode and almost never in dark (prefer a border highlight).

### Navigation (Sidebar)
Fixed **220px**, `subtle` background, right border. Nav items are flex rows with
a 16–20px Lucide icon + label, `radius 6px`. Active item: a 12%-accent tint
background, brand-blue text, and a **3px brand-blue left border**. Topbar holds
the product wordmark on the left and right-aligned actions (tenant switcher, user menu).

### Tables (the workhorse surface)
Full-width, collapsed borders. Headers: uppercase 12px muted, bottom border.
Cells: 12px padding, 14px body, subtle bottom border, **42px target row height**
(comfortable, not spacious). Rows hover to `raised`. **Action columns live on the
right, right-aligned**; rows may be clickable for navigation, but any control
inside a row must `stopPropagation()` (click **and** keydown) so it doesn't
trigger row activation. Migrated pages use a shared `DataTable` (TanStack-backed,
ADR 018) with page-owned-Set selection, required `getRowId`, and select-all over
post-sort rows; action-only tables omit selection but still supply `getRowId`.

### Inputs & Forms
Labels **above** inputs, left-aligned, sentence case; required = red asterisk in
the label (never placeholder-only). Inputs: surface bg, default border, `radius
4px`, `8px 10px` padding; focus shows the 2px brand-blue ring + accent border.
**Validation fires on submit, not on blur** — no surprise-validation as the user
types. Errors state *what* and *how to fix* ("Password must be at least 8
characters."), never raw or apologetic.

### Status Badges
Pill (`radius 999px`), `2px 8px`, caption size, medium weight, always a
container-bg + matching foreground + a **text label**. Variants: success /
danger / warning / info / neutral, plus domain badges (scan status
queued/pending/running/completed/failed, control PASS/FAIL/ERROR/NA, CVE
severity, allowlist status).

### Modals / Dialogs
Centered over a `rgba(0,0,0,.45)` backdrop, surface panel, `radius 8px`, `lg`
shadow, max-width 520px. Primary action on the **right**, optional destructive
action to its left — never two green buttons. Escape + backdrop dismiss
non-destructive modals; **irreversible** actions require **type-to-confirm**
(type the exact name of the thing being deleted).

### Empty States
Shared `<EmptyState />` (`.ss-empty`): centered, muted, a 40px outline icon, a
one-line explanation, and a primary action that creates the first item. Never
render an empty table with blank rows.

### Log Console (domain-specific)
A deliberately dark panel (`#0f172a`) even in light mode — monospace, JetBrains
Mono 12.5px, connection pills (connected/reconnecting/error/idle) and per-line
level badges (info/warn/error/debug). The "monospace feel without hacker-green."

## 5. Layout Principles

- **Structure:** 220px fixed sidebar + topbar; content `max-width 1200px`,
  left-aligned. Forms cap at 560px when they are the primary content.
- **Whitespace:** strict **4px grid**; `lg (16px)` inside cards, `xl (24px)`
  between sections, `3xl (48px)` between top-level page sections. No off-scale values.
- **Page rhythm:** page title + optional muted subtitle, a right-aligned primary
  action, then optional KPI/banner card, then the card/table/form.
- **Responsive:** breakpoints `sm 640 · md 768 · lg 1024 · xl 1280`. Sidebar
  collapses to a drawer below `md`. Tenant pages usable on tablet; admin
  desktop-first. Respect `prefers-reduced-motion` (disable transform transitions).
- **A11y floor:** WCAG AA body contrast (4.5:1); visible focus ring on every
  interactive element; tab order follows visual order, no positive `tabindex`.

## 6. Design System Notes for Stitch Generation

### Language to use
Describe screens as a **calm, dense enterprise control plane**: near-white
surfaces, cool slate neutrals, one brand blue for all interactivity, compact
42px table rows, generous-but-disciplined 4px-grid spacing. Say "quietly
powerful," "technical but approachable." **Avoid** hacker-green, neon, gradients,
padlock/shield/flame iconography, playful copy, and exclamation marks.

### Color references (light, canonical)
Brand Blue `#3b82f6` (primary) · Hover `#2563eb` · Ink `#111827` (text) · Slate
`#374151`/`#6b7280` (secondary/muted) · Surface `#ffffff` · Subtle `#f9fafb` ·
Raised `#f3f4f6` · Border `#e5e7eb`/`#d1d5db`. Status: Success `#10b981`,
Warning `#f59e0b`, Danger `#ef4444`, Info `#06b6d4` (each over its pale container).

### Icons
Lucide, outline, 1.5 stroke, monochrome, inherit color. 16px inline, 20px nav,
40px empty states. Surface map: Dashboard `layout-dashboard`, Agents `server`,
Targets `target`, Scans `radar`, Team `users`, Settings `settings`.

### Component prompts (examples)
1. "A dense assets table: uppercase-muted 12px headers, 42px rows, a right-aligned
   action column, severity pills (success/warning/danger) with text labels, brand-blue
   row hover. White surface, thin `#e5e7eb` row dividers, no card nesting."
2. "A right-side detail drawer (min 480px) over a 25%-black backdrop: header with
   title + close, scrollable body with key/value rows in 13px, section sub-headers in
   muted slate. Calm, no shadows beyond a soft left edge."
3. "An empty state: centered 40px outline `server` icon in muted gray, one line
   'No agents registered.', and a primary brand-blue button 'Generate install command'."

### Incremental iteration
Migration is **one page per PR** (Dashboard → Agents → Targets → Scans → Team →
Settings). Keep the `DataTable` selection contract fixed; tokenize hex/spacing to
`--ss-*`; use concrete px for type sizes until the typography tokens land (below).

### ⚠️ Known gaps & divergences (as-built vs. spec)
- **Typography tokens are spec'd but not implemented.** `docs/design-system.md §4`
  defines `--ss-text-*`, `--ss-font-*`, `--ss-leading-*`, but **none exist in
  `web/src/index.css`** (which has only color/spacing/radius/shadow/motion). A
  reference to `var(--ss-text-body-sm)` fell back to invalid and was replaced with a
  concrete `13px` (PRs #409/#411). Closing this gap (adding the type scale to the
  token layer) is the recommended next design-system task.
- **Dark theme is defined but inert** — values exist under `[data-theme="dark"]`
  but no theme switch / pre-mount script is wired (spec §9 Phase 2).
- **`.ss-*` adoption is partial.** Legacy classes (`.btn`, `.table`, `.badge-*`,
  `.form-group`) still ship alongside the token-based system; migration is in progress.
- **Two frontends.** This document covers the **tenant** app (`web/`). The
  **backoffice** admin (`backoffice/web/`) shares these tokens but uses a distinct
  **navy/teal** theme — extract it separately if needed.
