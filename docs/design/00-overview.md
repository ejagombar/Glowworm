# 00 — Overview, Goals & Decision Log

**Scope:** product vision, goals/non-goals, feature register (traceability
IDs used across all docs), design principles, resolved decisions, glossary.

---

## 1. Vision

> Building the next generation of open-source finance tracking software. An
> alternative to Firefly III, much more similar to Snoop, whilst being open
> source and using bank statements and exports as opposed to a direct
> connection to the account. Clean, intuitive UI. Smart: AI for automatic
> imports, categorisation, and tracking.

Glowworm is a **self-hosted, single-user (per instance) personal finance
tracker**. The user drops in bank statements; Glowworm figures out the rest.

## 2. Goals

1. **Imports just work.** Any statement format (CSV, **Excel**, PDF, OFX, QIF,
   dividend/interest histories — photos via optional vision support, doc 06
   §2) is detected — first by saved schemas, then by LLM — and confirmed by
   the user with a preview before anything is committed.
2. **Categorisation is cheap and automatic.** A pure-Go local classifier
   handles most transactions offline; AI handles the residue; a human review
   step catches the rest and becomes training data.
3. **Transfers and recurring payments are detected, not typed.** Internal
   transfers never skew spending statistics.
4. **Questions, not reports.** Natural-language questions compile into safe,
   parameterised queries and render as dynamic, editable, pinnable widgets.
5. **Nothing is lost.** Every mutation (user, AI, or system) is a rich,
   reversible audit entry; any past state can be reconstructed and
   reconciled against known balances.
6. **AI agnostic.** OpenAI, Anthropic, Google, Ollama (local), or any
   OpenAI-compatible endpoint. AI is optional — every AI path degrades
   gracefully to a non-AI path.
7. **Privacy-first.** Self-hosted, single binary, no telemetry, embedded
   fonts, masked account identifiers, privacy mode that blurs money.
8. **Simple stack.** Go backend + server-rendered HTML with HTMX + a small
   vanilla-JS layer. No React/Svelte/Vue. No build step for the frontend.

## 3. Non-goals (v1)

- Bank connections / Open Banking / scraping (statements only).
- Multi-user or multi-tenant (single owner per instance; the data model keeps
  this extensible but v1 ships one user).
- Native mobile apps (the web UI is mobile-friendly; the JSON API keeps an
  app possible later).
- Multi-currency conversion (a display-currency setting exists; all accounts
  share it in v1 — field exists for the future).
- Budgets, goals, and forecasting (post-v1 candidates).
- Full git-style branching history (linear audit trail with revert only).

## 4. Design principles

1. **The prototype is the spec.** Looks and interactions are fixed by the
   prototype files; features implicit in the UI are first-class requirements.
2. **Backend owns the data; frontend owns the pixels.** All state lives
   server-side; the browser renders what it is given.
3. **Cheapest correct tool first.** Rules before classifier, classifier
   before AI, AI before human — in every automation.
4. **Every mutation is audited and reversible** (see doc 11).
5. **Widgets are JSON and frontend-agnostic** — they could be rendered as
   HTML/CSS today and native QT tomorrow (see doc 05).
6. **Money is never a float.** Integer minor units end-to-end (doc 02 §2).
7. **AI is an add-on, never a dependency.** With no AI configured the app is
   still fully usable (manual schema mapping, rules + review categorisation,
   no NL queries).

## 5. Feature register

Feature IDs are referenced by later docs (`[F#]`).

| ID | Feature | Spec |
|---|---|---|
| F1 | Setup & configuration walkthrough (first-run wizard) | 12 |
| F2 | AI-assisted importer (format detection, schema preview/correction, named reusable schemas) | 06 |
| F3 | Human review step after import (categories, confidence, bulk actions) | 06 §7 |
| F4 | Automatic categorisation cascade (rules → local → AI → review) | 07 |
| F5 | Automatic transfer detection + manual link/unlink + linked display | 08 |
| F6 | Recurring transaction / bill detection + series viewer | 08 |
| F7 | Dynamic data viewer (NL question → query → widgets) | 10 |
| F8 | Dynamic widget system (JSON-defined, editable, frontend-agnostic) | 05 |
| F9 | Pinned-widget dashboard (drag/resize edit mode) | 04 §6.1, 05 §7 |
| F10 | Accounts with statements, identifiers, opening balances | 02 §3 |
| F11 | Point-in-time valuations ("corrections"), replayed balances | 02 §3.2, 11 §5 |
| F12 | Timeline viewer (state at any date) + reconciliation | 11 §5–6 |
| F13 | Audit history: rich diffs, per-entry revert, **jump-back with collapsed shelved paths, path restore (even after new changes), engine-mediated per-entry Forget/Delete** | 11 |
| F14 | AI provider abstraction, per-capability models, encrypted keys | 09 |
| F15 | Transactions: source/destination model, statement vs grouped views, inline editing | 04 §6.2, 03 §4 |
| F16 | Global period selector affecting all data views | 04 §5.3 |
| F17 | Privacy mode (blur balances) | 04 §8.2 |
| F18 | Themes (light/dark/system) | 04 §8.1 |
| F19 | Powerful JSON API for data & integrations | 03 |
| F20 | Mobile-friendly web UI, keyboard shortcuts | 04 §8–9 |
| F21 | Secure login (public-internet posture); owner account created during setup, not on the login page | 04 §5.9, 12 §3, 14 §2 |

## 6. Decision log (conflict resolutions)

These decisions are **final**. Agents MUST NOT reopen them without an
explicit instruction from the owner.

| # | Topic | Decision | Rationale |
|---|---|---|---|
| D1 | Transactions table column | **Description** column, not Merchant. `merchant` is an optional AI-enriched field, never required. | ui_requirements §Merchants |
| D2 | Transaction model | Transactions have **source and destination**. Normal tx: source = account/card, destination = merchant. Transfer: source account → destination account. | ui_requirements |
| D3 | Transfer legs | A real-world transfer imported from two statements is **two transaction rows**, linked by a transfer record. Main (all-accounts) list shows them **as one row** with a "Linked" badge; per-account statement view shows **each leg separately** as it appears on the statement. | ui_requirements |
| D4 | Filter dropdowns | Search / Category / **Type** only. **No account dropdown** in the filter bar — account selection is via the account cards. | ui_requirements |
| D5 | Recurring tag | **Never user-settable.** Detected automatically; clicking the badge opens the series viewer. | ui_requirements + backend §2.5 |
| D6 | Transfer editing | Editing a transaction as a transfer MUST offer an account selector and a counterpart-transaction selector with **suggestions ordered by date proximity / amount similarity**. Links can be broken and created manually. The prototype's simpler inline editor is extended accordingly (see 04 §6.2.4). | ui_requirements |
| D7 | Web UI | **HTMX + server-rendered HTML fragments + vanilla JS.** No SPA framework. The prototype's CSS is kept verbatim; `widgets.js` chart engine is reused. | Requirements + backend §1 |
| D8 | Timeline location | The "Timeline Viewer" is the **Transactions mode of the History view** (as prototyped), with the audit log as the second mode. | Prototype |
| D9 | Bill detection | Folded into **recurring detection**. One "Recurring" badge; no separate bill badge in v1. The user's own categories (e.g. "Bills & Utilities") remain user-owned. | Prototype + backend §6 |
| D10 | Valuation visibility | The account modal's **Current value** row is shown for **all account types** (prototype hides it for non-savings). Styling unchanged. | Backend §2.2 requires corrections universally |
| D11 | Category types | Categories carry `type: spend|income|internal`. `Income` and `Internal transfer` are **system categories** (fixed name, not deletable). User-created categories default to `spend`; the category form gains a small Spend/Income toggle (minimal extension, styled with existing segmented control). | Backend §2.6 |
| D12 | Widget types v1 | `bar`, `line`, `pie`, `table`, `text`, `number`, `transactions` (as prototyped) + `calculator` (specified, optional to ship). | Requirements + prototype |
| D13 | Pinned widget freshness | A pinned widget stores its **query plan**; on dashboard render the backend re-executes the plan so pinned widgets stay current. Chat history keeps snapshot data. | Derived from requirements §Dynamic Widgets |
| D14 | NL query safety | The LLM produces a **validated query plan**, never raw SQL. The backend compiles plans into parameterised SQL from a whitelist. | Security |
| D15 | Money | Integer **minor units** everywhere (DB, API, widgets). Currency code travels alongside. | Backend §14 |
| D16 | Auth | Single user per instance; password auth with session cookie; `AUTH_MODE=none` allowed for localhost/trusted deployments. | 14 |
| D17 | Fonts | Inter is **self-hosted** (woff2, embedded). Google Fonts is not contacted at runtime. | Privacy |
| D18 | Currency setting | One instance-wide display currency (£/$/€ as prototyped). | Prototype settings |
| D19 | Greeting name | The dashboard greeting uses the owner's display name (new small "Your name" field in Settings → Appearance). | Prototype shows "Good morning, Emma" |
| D20 | DB | **SQLite default** (pure-Go driver), **Postgres optional**. No ORM; hand-written SQL in a repository layer. | Backend §1 |
| D21 | Import account | An import targets one account. The importer pre-selects the account when the file contains the user's identifiers; otherwise the user picks. | Backend §4 |
| D22 | Schema reuse order | Saved schema fingerprint match first → LLM detection second → manual mapping last. | Backend §4 |
| D23 | Import format priorities | **Excel (`.xlsx`) is a first-class, prioritised import format — above photos.** Photo/vision import (camera photos, scanned PDFs) is optional and post-v1: useful if possible, never blocking, and only when a multimodal provider is configured. The prototype dropzone copy stays verbatim regardless. | Owner correction: Excel over photos |
| D24 | Account creation | **During setup, not on the login page.** The instance is public-internet facing: no open signup endpoint may exist. The setup wizard's step 1 claims the instance (works only while no user exists; optional `GLOWWORM_SETUP_TOKEN` gate); the login page is username+password only; password reset is host-side CLI. | Owner requirement; security posture (doc 14 §2) |
| D25 | Audit history model | The log is a **branchable timeline** owned by the audit engine: **Jump back** to a point physically reverts + deactivates everything after it (rendered collapsed, expandable, never destroyed); **Restore path** re-applies a shelved path — even after unrelated new changes, which are themselves shelved; **Forget / Delete** remove individual changes (e.g. an AI-made transaction link) from history without touching preceding changes. Per-item availability is decided by the engine's capability matrix (doc 11 §4.3), not by callers. Tombstone meta entries record every history surgery. | Owner requirement |
| D26 | HTMX dropped (implementation finding) | During implementation every interaction resolved to `fetch` + full-page navigation or native form posts; no `hx-*` attributes were ever used. HTMX (vendored runtime, CSRF wiring, static route) is therefore **removed** — the stack is server-rendered `html/template` + vanilla JS, which preserves D7's goals (no SPA framework, no build step) without the unused dependency. Streaming chat uses NDJSON `fetch`, not SSE/HTMX. | Owner decision, 2026-10 |

## 7. Scale assumptions

Single instance, one owner: ≤ 20 accounts, ≤ 100k transactions, ≤ 5k
categories-history rows, audit ≤ 1M entries. SQLite handles this comfortably;
every balance/aggregate is computed by indexed SQL (no materialised caches in
v1 — see 11 §5.4 for the scaling escape hatch).

## 8. Glossary

| Term | Meaning |
|---|---|
| **Account** | A user-owned financial account (debit, credit, savings) that statements are imported into. |
| **Transaction (tx)** | One row from one statement. Has a source and a destination, a signed amount, a type (`expense|income|transfer`), and appears on exactly one account. |
| **Leg** | One side of a transfer: the transaction on the sending account (outbound, negative) or receiving account (inbound, positive). |
| **Linked transfer** | Two legs joined by a transfer record; displayed as one row outside statement views. |
| **Recurring series** | An auto-detected group of transactions with the same merchant/description, similar amount, and regular cadence; identified by `recurring_key`. |
| **Valuation (correction)** | A point-in-time statement of an account's true value. Never a payment; used as a checkpoint when replaying balances. |
| **Import schema** | A named, reusable mapping of statement columns → Glowworm fields, detected by AI and confirmed by the user. |
| **Import batch** | One upload; holds staged rows in review before commit. |
| **Category source** | Who assigned a category: `user`, `local` (rules or local classifier), `ai`, `none`. |
| **Widget** | A JSON-defined presentation unit (bar, line, pie, table, text, number, transactions, calculator) rendered by the client widget engine. |
| **Dataset** | The canonical tabular result behind a widget (columns + rows + meta), from which any presentation can be derived. |
| **Plan** | A validated, structured description of a data question (metric, entity, time range, grouping) that compiles deterministically to SQL. |
| **Pinned widget** | A widget persisted server-side with its plan, shown on the dashboard. |
| **Audit entry** | An immutable, diff-rich record of one mutation with revert data. |
| **Snapshot** | The reconstructed state of all accounts at a past date. |
| **Reconciliation** | Recording a known statement balance for an account/date and comparing it to the computed balance. |
| **Privacy mode** | UI state that blurs monetary values while preserving chart shapes. |
| **Statement view** | The per-account transaction table showing each leg exactly as the bank reports it, with a To/From counterparty column. |