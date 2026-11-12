# Glowworm — Design Documentation

**Version:** 1.0 · **Status:** Approved for implementation · **Audience:** building agents & maintainers

This directory contains the complete design specification for **Glowworm**, an
open-source, self-hosted personal finance tracker (an alternative to Firefly
III, closer in spirit to Snoop) with AI-assisted importing, categorisation,
and natural-language querying.

The documents in this folder are **normative**. They consolidate every prior
requirements document, resolve the conflicts between them, and extract the
many features that previously existed only implicitly inside the prototype UI.

> **Read this first.** These documents are the specification the first
> version was built from. The implementation has since moved on, and where
> the two disagree **the code is right**. The differences that matter most:
>
> - **Storage** — SQLite only. There is no Postgres option, and the schema
>   is the single migration `internal/store/migrations/0001_init.up.sql`
>   rather than the tables listed in doc 02.
> - **Queries** (docs 05, 10) — the model answers with query specs
>   (`internal/core/query`) that are re-run whenever a widget is shown. The
>   Plan engine and stored snapshot widgets described here no longer exist.
> - **History** (doc 11) — the operations are undo, redo, rewind to an entry
>   and discard. Revert links, forgetting or deleting single entries and
>   restoring a named path were removed.
> - **Categorisation** (doc 07) — runs per group inside the import pipeline
>   (`internal/core/imports/categorise.go`); there is no standalone
>   per-transaction categoriser or retrain endpoint.
> - **API** (doc 03) — the route table in `internal/server/api.go` is the
>   reference; a number of endpoints listed here were never used by the app
>   and have been removed.
> - **Settings** (doc 12) — `transfer_date_window_days` and
>   `ai_daily_token_cap` do not exist.

---

## 1. Source documents & precedence

Where documents conflict, the following precedence applies (highest first):

| Priority | Source | Role |
|---|---|---|
| 1 | **Production UI** — `web/static/` + `internal/render/templates/` | Authoritative for **looks, layout, interactions and micro-behaviour**. (The original static prototype has been retired; "the prototype" in these docs now means this UI.) |
| 2 | **`ui_requirements.md`** | Model corrections that **override** the prototype where they disagree (transaction source/destination model, transfer linking, description-not-merchant column, removal of the filter account dropdown). |
| 3 | **`backend_requirements.md`** | The backend specification: data model, API, categorisation cascade, audit, AI abstraction. |
| 4 | **`Requirements.md`** | The original vision. Older; superseded where 2–3 differ, but still authoritative for goals and the feature list. |

All conflicts found between these sources are recorded in the **Decision Log**
in [`00-overview.md`](00-overview.md) §6. Agents MUST follow the decisions
there rather than re-litigating them.

---

## 2. Document map & reading order

| Doc | Title | Read when |
|---|---|---|
| [00-overview.md](00-overview.md) | Vision, goals, feature register, decision log, glossary | Always, first |
| [01-architecture.md](01-architecture.md) | System architecture, tech stack, repo layout, request flow | Always, second |
| [02-data-model.md](02-data-model.md) | Database schema, money/date conventions, derived data | Backend work |
| [03-backend-api.md](03-backend-api.md) | Full API reference (JSON + HTMX fragments) | Backend & frontend work |
| [04-frontend.md](04-frontend.md) | Frontend spec: design system, views, HTMX wiring, JS modules | Frontend work |
| [05-widgets.md](05-widgets.md) | Widget JSON spec, dataset model, conversions, pinned widgets | Frontend + query work |
| [06-import.md](06-import.md) | Import pipeline: formats, schema detection, review, commit | Import work |
| [07-categorisation.md](07-categorisation.md) | Categorisation cascade: rules → local classifier → AI → review | AI/ML work |
| [08-transfers-recurring.md](08-transfers-recurring.md) | Transfer detection/linking, recurring detection | Engine work |
| [09-ai-integration.md](09-ai-integration.md) | AI provider abstraction, capability contracts, prompts, key security | AI work |
| [10-natural-language-query.md](10-natural-language-query.md) | NL query pipeline, plan schema, SQL compiler, chat UX | Query work |
| [11-audit-history-timeline.md](11-audit-history-timeline.md) | Audit log, diffs, revert, timeline, reconciliation | Core work |
| [12-settings-setup.md](12-settings-setup.md) | Settings & first-run setup walkthrough | UI + backend work |
| [13-deployment.md](13-deployment.md) | Build, config, Docker, NGINX, systemd, backups, upgrades | Ops work |
| [14-security.md](14-security.md) | Threat model, auth, sessions, CSRF, secrets, masking | Always review |
| [15-implementation-plan.md](15-implementation-plan.md) | Build order, milestones, acceptance criteria | Planning & tracking |

**Suggested reading order for any agent:** `00 → 01 → 14`, then the docs for
the subsystem you are building, plus anything they reference. The frontend is
fully specified in `04` + `05`; the backend core in `02` + `03` + `11`; AI in
`07` + `09` + `10`; shipping in `13`.

---

## 3. Normative language

- **MUST / MUST NOT** — non-negotiable requirement.
- **SHOULD** — strong default; deviations need a written reason in the PR.
- **MAY** — optional.

---

## 4. The one-paragraph product summary

Glowworm ingests **bank statements and exports** (CSV, Excel, PDF, OFX, QIF —
photos via optional vision support; no bank connections), detects their format
with an LLM, learns reusable import
schemas, categorises transactions with a cheap local classifier plus AI
fallback, detects internal transfers and recurring bills automatically, and
lets the user ask questions in natural language ("how much did I spend on
transport this year?") which are answered with **frontend-agnostic JSON
widgets** (charts, tables, numbers) that can be pinned to a dashboard. Every
change — human or AI — is recorded in a rich, reversible, git-like **audit
log**, and the state of your money at any past date can be reconstructed and
reconciled. Single Go binary, embedded UI (HTMX + vanilla JS, no SPA
framework), SQLite by default, Postgres supported, AI-provider agnostic
(including fully local via Ollama).

---

## 5. Building with this documentation

- `web/static/css/styles.css` is the stylesheet. Extend it via documented
  additions (see 04 §3) rather than restyling.
- Every doc has a **Scope** header and an **Acceptance criteria** section.
  Agents MUST satisfy the acceptance criteria and SHOULD satisfy the body.
- When a doc says "the prototype does X", verify against the production UI
  before changing behaviour.