# 04 — Frontend

**Scope:** the complete frontend specification. The prototype (`index.html`,
`js/app.js`, `js/widgets.js`, `css/styles.css` in the repo root) is the
**authoritative visual and behavioural spec**; this document maps it onto the
production architecture (server-rendered HTML + HTMX + vanilla JS), lists
every view and interaction, and specifies the two UIs the prototype only
sketches (import review, transfer editing).

Related: [01](01-architecture.md) §5 rendering · [03](03-backend-api.md) §15 fragments ·
[05](05-widgets.md) widget engine · [06](06-import.md) §7 review UI · [08](08-transfers-recurring.md) §5 link UI.

---

## 1. File mapping (prototype → product)

| Prototype file | Product file | Treatment |
|---|---|---|
| `css/styles.css` | `web/static/css/styles.css` | **Copied verbatim.** The only additions allowed are those this doc specifies (§3.1), appended in a clearly marked section. |
| `index.html` | `internal/templates/layout.html` + view templates + partials | Rebuilt server-side; DOM structure, class names, SVG sprite and micro-copy preserved exactly. |
| `js/widgets.js` | `web/static/js/widgets.js` | Reused nearly verbatim (chart SVG builders, `ResizeObserver` redraw, `convertWidgetData`, `widgetEl`, pie legend, injected styles). Small changes: data may carry `currency` for minor-unit formatting; widget actions emit HTMX events instead of mutating local state. |
| `js/app.js` | `web/static/js/app.js` (rewritten, much smaller) | Keeps: theme, privacy, keyboard shortcuts, composer autosize, drag/resize edit mode, toast, modal open/close helpers, brand wiggle. Removed: all mock-data rendering (server does it). |
| `js/data.js` | *(nothing)* | Mock shapes only. Shapes live on in widget/API JSON specs (doc 05, 03). |
| *(prototype fonts link)* | `web/static/fonts/inter-*.woff2` | Self-hosted Inter (400/500/600/700) [D17]; `@font-face` prepended in a marked CSS section. |

Vendor: `web/htmx/htmx.min.js` (HTMX 2.x). No other JS dependencies. No
frontend build step.

## 2. Page skeleton

`layout.html` reproduces the prototype's `<body>` structure:

1. Theme bootstrap inline script (anti-FOUC) — unchanged, reading
   `glowworm-theme` from `localStorage` with `system` fallback.
2. SVG sprite (`#i-dashboard`, `#i-transactions`, … `#i-glowworm`) — copied
   verbatim from the prototype.
3. `.app > .main > header.masthead` — brand (worm SVG + wordmark, wiggle on
   click), `.tabnav` (numbered items 01–06 + settings icon link), period
   selector + custom range popover, privacy toggle, settings link.
4. `<main class="content" id="content">` — view sections (server renders the
   **active** view's markup; other views are separate pages).
5. Global overlays: setup wizard modal, widget drawer + backdrop, recurring
   series modal, account modal, confirm modal, toast — as partials included
   by layout, filled/triggered via HTMX where their content is dynamic
   (account modal body, recurring series list).

**Masthead (normative):** identical to prototype. Period `<select>` options:
`all` (All time, default on first load), `ytd`, `last-12m` (**default
afterwards**, persisted in settings), `last-3m`, `last-month`, `last-day`,
`custom` → shows `#customRange` popover (from/to date inputs + Apply).

## 3. Design system summary (from `css/styles.css`)

- **Tokens:** CSS custom properties for surfaces (`--bg`, `--surface`,
  `--surface-2/3`), text (`--text`, `--text-2`, `--muted`, `--muted-2`),
  lines, brand (`--primary #0b5d43`, `--accent`), semantic colours
  (`--income`, `--expense`, `--transfer`, `--bill`, `--info` + soft variants),
  chart colours, shadows, radii (`--r-xs 2px`, `--r 3px`, `--r-lg 4px`),
  `--masthead-h: 56px`, fonts (`--font` Inter, `--mono` for numerals).
- **Dark theme:** `[data-theme="dark"]` overrides the same tokens; toggled by
  `data-theme` on `<html>`; never a second stylesheet.
- **Look:** restrained geometric fintech — hairline rules, sharp corners,
  dense-but-readable 13.5px base, grid-paper background (24px, 3% alpha),
  tabular-nums mono for all monetary values, section headings uppercase with
  an 8px square marker, stat cards separated by 1px lines (not gaps).
- **Motion:** 0.14–0.22s ease transitions; view fade; modal pop; toast in;
  worm loader animation; `prefers-reduced-motion` disables all of it.

### 3.1 Permitted CSS additions (only these)

1. `@font-face` declarations for self-hosted Inter.
2. Styles for the **import review** UI and **transfer counterpart selector**
   (built strictly from existing primitives: `.panel`, `.tx-table`,
   `.badge`, `.chip`, `.field`, `.segmented`, `.steps-list`, `.cat-colors`).
3. `.badge-needsreview` (bill-soft/warning tone) for review rows.
4. Any addition MUST be appended under a `/* ===== v1 additions ===== */`
   banner and MUST reuse tokens (no new hard-coded colours).

## 4. Client JS modules

```
web/static/js/
├── htmx.min.js      (vendored)
├── widgets.js       (chart/widget engine — from prototype)
└── app.js           (glue, ~600 lines target)
```

`app.js` responsibilities:

1. **Theme** — `applyTheme/initTheme` (prototype logic unchanged).
2. **Privacy toggle** — `body.privacy` class + button/icon swap (unchanged).
3. **Keyboard shortcuts** (§9).
4. **Modals/drawer/toast** — `open/close` helpers using the prototype's
   classes; content injected by HTMX or static; `Esc` + backdrop-click close.
5. **Composer autosize** (queries textarea; Enter sends, Shift+Enter newline).
6. **Chat streaming client** — fetch NDJSON from `POST /queries/messages/stream`,
   render `steps`/`text`/`widget` events into the pending assistant message,
   worm-out → reveal choreography (doc 10 §6).
7. **Pinned-grid edit mode** — pointer-based drag-swap + edge-resize exactly
   as prototyped (`GRID_GAP=12`, `GRID_ROW=72`, col clamp 2..12, rows 1..8);
   on drop/resize, `PUT /api/v1/widgets/pinned` with the new order/sizes
   (debounced); the server re-renders `#pinnedGrid` (or client reorders
   optimistically and reconciles on response).
8. **Widget drawer** — title/type/pinned fields; Apply calls
   `convertWidgetData` client-side for instant preview, then HTMX-patches the
   pinned widget (server re-derives data from dataset [D13]) and re-renders.
9. **Custom range popover** logic (prototype `openCustomRange/applyCustomRange`
   → sets period and triggers HTMX refresh of the current view).
10. **Brand wiggle**, `data-view-link` handling (plain navigation now).

## 5. Views (normative behaviour per view)

### 5.1 Dashboard (`/`)

Components and behaviour — all as prototyped:

1. **Greeting** — `Good {morning|afternoon|evening}, {display_name}` (server
   time-of-day; name from settings [D19]; if unset, name is omitted).
2. **Overview stat grid** (4 cards, hairline-separated):
   - *Available balance* (Σ non-savings balances) and *Savings* (Σ savings) —
     `clickable`: selecting one scopes the hero chart (`data-balance-view`),
     selected card gets the primary outline; clicking again returns to all
     accounts. Trend line: "Show graph".
   - *Income this month* — Σ income this calendar month; trend row
     `Salary · 1 May` parity (latest recurring income label + date).
   - *Spent this month* — Σ expense (excl. transfers) this month; trend row
     `8% vs last month` with `up`/`down` class.
3. **Balance overview panel** — line chart (prototype `lineChartSVG`) over
   the period; label `All accounts · {period label lowercased}`; scope badge
   `+3.2% this year` (computed vs 1 Jan).
4. **Pinned widgets** — grid (12 col × 72px rows, 12px gap) rendered
   server-side from `pinned_widgets` with fresh plan data [D13]. Header:
   `Pinned widgets` + hint (`Saved from your questions`) + `Edit` button.
   Edit mode (§7.2): drag-swap, edge-resize, dashed outlines, `Done`.
5. **Recent activity** — 7 display rows (grouped view, §5.2 semantics),
   `View all` → `/transactions`. Row: coloured initial-icon (⇄ for
   transfers), description, `category · account` sub-line (+ badges), signed
   amount (pos green / neg red / `⇄ £x` blue for transfers), recurring badge
   (clickable → recurring modal).

### 5.2 Transactions (`/transactions`)

1. **Accounts grid** — one `.account-card` per account: dot, name, type
   badge (Debit/Credit/Savings), balance (red if negative), `···· last4 · N
   transactions` (count within period). Click selects → statement view;
   click again deselects. Selected card: primary border + outline.
2. **Section subheading** — `All accounts · transfers linked` or
   `{Account name} · statement view`.
3. **Filters** (`.filters.panel`) — search input, category select
   (non-system categories), type select (`All types`, Expenses, Income,
   Transfers, **Recurring**) [D4: no account dropdown]. Any change → HTMX
   `GET /transactions` swapping the table region (search debounced 250ms).
4. **Table** — two modes (doc 03 §5.1 contract):
   - *All accounts:* Date (d MMM), Description (+ badges), Category (dot +
     name), Account (linked rows: `Source → Destination`), Amount
     (`+£`/`−£`/`⇄ £`), row actions (edit icon, hover-visible).
   - *Statement view (`account-view` class):* Date, Description, Category,
     **To / From** (counterparty), Amount, actions. Each transfer leg shown
     separately.
   - Linked rows carry `tr.linked` + `Linked` badge; recurring badge is a
     `<button class="badge-btn" data-recurring-key>` (not user-settable [D5]).
5. **Inline editor** (§5.2.4) — opens beneath the row (`tr.editing` primary
   tint; editor row surface-2).
6. Empty state: `No transactions match your filters.`

#### 5.2.4 Inline transaction editor (extended per [D6])

Existing fields (prototype): Date, Description (grow), Category, Account,
Amount, Save/Cancel. **Additions:**

1. A **Type** segmented control (`Expense | Income | Transfer`) — visible for
   non-linked rows.
2. When **Transfer** is chosen:
   - Category locks to `Internal transfer` (disabled select),
   - Account select stays (this leg's account),
   - A **counterpart transaction** select appears, populated by
     `GET /transactions/{id}/transfer-suggestions`, grouped by account, each
     option labelled `{date} · {description} · {amount}` — suggestions sorted
     by date proximity then amount similarity [D6].
   - If the row is currently linked, show a `Break link` ghost button
     (danger text) that unlinks first, keeping both transactions.
3. Saving a transfer edit calls link/unlink endpoints as needed; audit
   entries record both legs.
4. Linked rows (viewing from the grouped list): Category/Account disabled as
   prototyped; editing amount/date updates that leg only — if the amounts
   diverge the linked badge gains a `⚠` title ("Amounts differ — review the
   other leg"). Breaking the link is always available.

### 5.3 Period selector (global) [F16]

- Affects: hero chart + stat cards, recent activity, transactions, category
  card amounts, recurring modal totals, pinned widget re-render, chat
  `transactions` widget contents (via plan time range only when the plan is
  period-bound — plans store explicit ranges; the period selector does **not**
  rewrite chat history).
- Implementation: changing the select reloads the current view via HTMX with
  `?period=...`; `custom` opens the popover; Apply sets `period=custom&from&to`.
- Not affected: timeline slider (own date domain), settings, wizard.

### 5.4 Queries (`/queries`) [F7]

Layout exactly as prototyped: collapsible left **History** sidebar
(`#queriesSidebar`, 232px) listing conversations grouped per day
(`Today`/`Yesterday`/date + message count; click scrolls to day separator);
main column with `#queryMessages`, composer (autosize textarea + send
button), suggestion chips (Transport spend / Where did my money go? / Balance
over time / List transactions — copy from prototype).

Message rendering (client, prototype parity):

- Day separators (`.msg-day`).
- User bubble (`.msg.user .msg-bubble`).
- Assistant: `.msg-text`, then `How I worked this out` `<details class="msg-reason">`
  with `.reason-step` ticks (steps arrive progressively via stream), then the
  widget card (`.widget`, pinnable via `#i-pin`, settings icon opens drawer).
- Pending state: worm loader (`glowworm-loader`); on completion the loader
  gets `worm-out` then the answer reveals (unless `prefers-reduced-motion`).
- Pin button state `pinned-on` when already pinned.

### 5.5 Categories (`/categories`)

- Grid of `.category-card`: dot, name, `System` badge for system categories,
  edit icon (hidden for `Internal transfer`), spent amount in period
  (`—` for income/internal), sub-line (`Incoming` / `Between your own accounts`).
- **Form** (`.category-form.panel`, hidden until add/edit): title, system
  note (`System category — its name is fixed.`), Name (disabled for system),
  Colour swatches (8 palette circles), **Type** segmented control
  (`Spend | Income`) shown only for new categories [D11], Delete button
  (hidden for system), Save/Cancel. Save → HTMX re-render grid + transaction
  filter options; rename cascades (audit).

### 5.6 Import (`/import`) [F2/F3]

- **Dropzone** (verbatim prototype): drag/drop + browse, multiple files,
  copy: "CSV, PDF, OFX, QIF, camera photos, dividend & interest payment
  history… anything goes." (Copy stays verbatim — the UI is final. Excel is
  a v1-priority format; camera photos are the optional vision path, doc 06 §2.)
- **Saved schemas** list: `.schema-card` logo initials (coloured), name +
  `{confidence}% match` (income-green), mono column list, chevron button.
- **How it works** `.steps-list` panel (1–4 copy verbatim).
- **Import flow** — a staged wizard rendered into the two-col area's left
  column (or a wide modal on small screens). Full UX in doc 06 §7. Stages:
  1. *Detecting* — thinking-card with spinner, "Reading your statement…".
  2. *Schema preview* — detected schema (name proposal, bank hint,
     confidence badge, column mapping table editable via selects), sample
     rows (2–3 parsed transactions as `.tx-table`), buttons: `Looks right` /
     `Fix mapping` / `Cancel`.
  3. *Fix mapping* — same table with editable role selects per column +
     date-format/decimal separators; `Re-parse` re-runs staging.
  4. *Review* — the review table (doc 06 §7): all staged rows with proposed
     category chips, source badges (`local`/`ai`/`needs review`), confidence,
     inline category change, `Apply to similar` per row, `Accept N confident`,
     duplicate marking, account header ("Importing into: {account}"), `Import
     N transactions` primary button + `Cancel`.
  5. *Done* — success panel (`N transactions imported` + toast + audit link).

### 5.7 History (`/history`) [F12/F13]

- **Segmented mode switch** (full-width, equal halves): `Transactions` |
  `Audit log` (prototype `.history-mode`).
- **Transactions (timeline) mode:**
  - `.timeline-controls` panel: clock icon, `Viewing state on` label, large
    date, chip `Latest` / `N months back`; range slider spanning the data's
    month range (min = earliest tx month, max = current month), custom-styled
    with `--fill` percentage; ≤7 evenly-spread tick labels.
  - 3 stat cards: *Balance at this date* (total across accounts), *Reconciled*
    (`In sync` green / `N divergences` / `Unverified` — doc 11 §6),
    *Transactions in period* (count up to date).
  - *Account balances at this date* — rows (`.timeline-account`): dot, name,
    balance; click scopes the chart to that account.
  - *Balance over time* — line chart up to the selected date (all or selected
    account). Slider input → HTMX `GET /history/snapshot?date=...` swapping
    stats + accounts + chart data (debounced ~80ms).
- **Audit log mode:** filter chips (`All / By you / By AI / Imports`) →
  `#history-list` rows (`.history-item`): icon tile (Y green / sparkle
  transfer-blue / import bill-amber), `who`-coloured actor + title, sub,
  mono diff block (`diff-add` green `+ …`, `diff-del` red strikethrough
  `− …`), timestamp, action cluster.
- **Per-entry actions** (doc 11 §4.3 — only what the engine permits):
  - `Restore` button (prototype label/copy) = **Jump back** — make this
    point the present; everything after it shelves; confirm modal first
    ("This will undo {n} changes made after this point.").
  - `⋯` menu (`.widget-btn`): `Revert change` (undo just this entry),
    `Keep effect, remove record` (Forget), `Delete from history`
    (danger, confirm modal). Unavailable operations are hidden; blocked
    reasons surface as tooltips.
- **Collapsed groups:** after a jump back, the superseded entries render as
  one inline collapsed block (`.msg-reason`-style `<details>`): header
  "{n} hidden actions · jumped back to {date}", expandable to rows rendered
  muted; the group carries its own `⋯` → `Restore these actions`, which
  re-applies the original path even after newer changes (doc 11 §4.2).

### 5.8 Settings (`/settings`)

12-col grid of `.setting-block`s (as prototyped):

1. **Accounts** (full width): heading + note ("Valuation updates are stored
   as corrections, not payments."), `Add account`, grid of
   `.settings-account-card` (dot, name, type badge, balance, `···· last4 ·
   £X at opening · opened {date} · N corrections`). Click → account modal.
2. **AI provider**: Provider select (OpenAI (GPT) / Anthropic (Claude) /
   Google (Gemini) / Ollama (local) / Custom endpoint), API key
   (`type=password`, masked display), Model select (Auto/Latest + configured
   per-capability overrides), `Test connection` ghost button (result toast).
   Save → `PUT /api/v1/settings/ai` (audit).
3. **Appearance**: Theme (Light/Dark/System — applies instantly),
   Currency (£ GBP / $ USD / € EUR), **Your name** [D19].
4. **Getting started**: "Re-run the setup walkthrough and rebuild your
   categories." + `Run setup walkthrough` button.

#### Account modal (`.account-modal`)

Fields (verbatim prototype): Name; Type select (Debit/Credit/Savings);
Opening balance (currency unit field); Opening balance date; Account number
+ Sort code (2-col); IBAN + BIC (2-col); **Current value** + valuation date
(`.field-hint`: "Set the value of this account at a point in time. It is
saved as a correction in the audit log so it can be undone later." — shown
for **all** types [D10]). Footer: Delete account (danger, confirm modal) /
Save changes / Cancel. After save with a changed value → new valuation
(`source: manual`) + drift recorded + audit; toast `Account updated`.

### 5.9 Login page (`/login`) [D24]

Not in the prototype (it assumes a signed-in session) — specified here,
built strictly from existing primitives:

- Full-viewport `--bg` with the grid-paper background; one centred card
  (`.panel`, max-width 360px): worm brand mark + "Glowworm" wordmark,
  `Sign in` heading.
- Fields: Username (`.field`), Password (`.field type=password`); full-width
  primary `Sign in` button; inline error text (`--expense`) — generic
  "Incorrect username or password." (never reveals which factor failed).
- **No account creation on this page** [D24]: the owner account is created
  during setup (wizard step 1, doc 12 §3). Password recovery is out-of-band:
  `glowworm reset-password` on the host (doc 01 §2).
- While no user exists, every route redirects to `/setup`, where the wizard
  opens with the account-creation step. Afterwards `/setup` redirects to
  `/`.

## 6. Modals & overlays (global)

| Overlay | Behaviour |
|---|---|
| **Setup wizard** | 6 steps, progress bars, worm hero, **account creation (step 1 [D24])**, categories chips (remove ✕ / add), dropzone, provider picks (`selected` state), success check. Footer: Back / Skip for now / Continue→Finish. Content and completion are server-backed (doc 12 §3). |
| **Recurring series modal** | Title = series label; sub `{n} transactions · {period} · auto-detected series`; header total (`Total spent/received/transferred` + signed amount); list of rows grouped for transfer series; backdrop/Esc close. |
| **Account modal** | §5.8. |
| **Confirm modal** | Title, message, Cancel/OK(danger). Used for account delete, category delete, jump back, breaking links. |
| **Widget drawer** | Right sheet 380px: Title, Chart type select (bar/line/pie/table/transactions/number/text), `Pinned to dashboard` switch, Cancel/Apply. Apply converts data client-side + persists. |
| **Toast** | Bottom-centre dark pill with check icon; triggered by `HX-Trigger` events or direct JS; 2.6s. |

## 7. Widgets (rendering contract)

Full JSON/data spec in doc 05. Frontend contract:

- Server renders `<article class="widget" data-widget-id data-widget-col
  data-widget-rows>` with `.widget-head` (title + pin/settings actions) and
  `.widget-body` containing the presentation HTML produced by `widgets.js`
  conventions: chart hosts carry `data-chart-type` and JSON
  `data-chart-data`; the engine redraws on resize.
- Default spans (col/rows): number/text/pie 4/2–3, bar/line/table 8/4,
  transactions 12/4 (prototype `spanCol/spanRows`).
- Widget actions: pin (chat only, `pinned-on` state), settings (drawer).
- Edit-mode chrome: `.resize-handle` (rh-w/rh-e/rh-s), ghost, drop-target.

## 8. Cross-cutting client behaviours

### 8.1 Theme [F18]
Settings select → `localStorage('glowworm-theme')` + instant `applyTheme`;
system option listens to `matchMedia` changes (prototype logic verbatim).

### 8.2 Privacy mode [F17]
Eye toggle → `body.privacy`: CSS blurs monetary text (stat values, balances,
amounts, num widgets, table num cells) and chart y-labels/pie totals, but not
chart shapes or transaction rows. Server never sends different data (masking
is handled by auth/masking rules, doc 14 §7).

### 8.3 Iconography
Inline SVG sprite only (`<use href="#i-...">`), 1.8 stroke, `currentColor`.
The worm mascot SVG (sprite `#i-glowworm` + brand loader variant) is copied
verbatim.

## 9. Keyboard shortcuts (normative)

| Keys | Action |
|---|---|
| `Cmd/Ctrl + K` | Navigate to Queries and focus composer |
| `1`…`6` | Dashboard / Queries / Transactions / Categories / Import / History |
| `0` | Settings |
| `Esc` | Close confirm modal → wizard → drawer → recurring modal → account modal (in that priority) |
| `Enter` (composer) | Send; `Shift+Enter` newline |

Digits ignored while typing in inputs/textareas (prototype `closest('input,
textarea, select, [contenteditable]')` guard).

## 10. Responsive behaviour (from prototype CSS — must hold)

- ≤1100px: stats grid 2-col; overview stacks.
- ≤900px: widgets span 12; two-col import layout stacks; settings blocks 6-col.
- ≤760px: brand wordmark hidden; period select narrows; view heads stack;
  category grid 1-col; settings full-width; the Account column hides in the
  grouped transactions table (`nth-child(4)` rule) — statement view keeps all
  columns.
- The queries sidebar collapses to the expand chip; chat uses full height
  (`calc(100vh − masthead − 60px)`, min 460px).

## 11. Accessibility

- All interactive icons have `aria-label`s (prototype already does).
- Modals: `role="dialog" aria-modal="true"` + labelled by heading; focus
  moves to first field on open, returns on close; `Esc` closes.
- Worm loader has `role="status"` + `.sr-only` text ("Glowworm is thinking…").
- Charts expose `role="img"` + `aria-label`; data tables remain real tables.
- Colour contrast comes from the existing tokens (WCAG AA on both themes).

## 12. Asset & caching strategy

- `GET /static/*` — immutable hashed filenames in release builds
  (`styles.{hash}.css`), `Cache-Control: public, max-age=31536000, immutable`;
  `index`/pages `no-cache`.
- `htmx.min.js` vendored and hashed too. Fonts: `font-display: swap`.

## 13. Acceptance criteria

1. Side-by-side with the prototype, every view matches pixel-for-pixel at
   1280px, 900px and 390px widths, in light and dark themes.
2. All prototype interactions work against live data (period selector,
   account selection, filters, inline editor incl. transfer flow, recurring
   modal, category CRUD, import flow, chat with streaming + widget pin/drawer,
   dashboard edit mode drag/resize, timeline slider, audit filters, settings,
   wizard, privacy, theme, shortcuts).
3. No mock data ships; empty states render for fresh instances.
4. No JS console errors on any view; graceful degradation with JS disabled is
   not required (HTMX requires JS) but links degrade to full page loads.