# Glowworm — Design Documentation

**Version:** 1.0 · **Status:** Approved for implementation · **Audience:** building agents & maintainers

This directory contains the complete design specification for **Glowworm**, an
open-source, self-hosted personal finance tracker (an alternative to Firefly
III, closer in spirit to Snoop) with AI-assisted importing, categorisation,
and natural-language querying.

The documents in this folder are **normative**. They consolidate every prior
requirements document, resolve the conflicts between them, and extract the
many features that previously existed only implicitly inside the prototype UI.

---

## 1. Source documents & precedence

Where documents conflict, the following precedence applies (highest first):

| Priority | Source | Role |
|---|---|---|
| 1 | **Prototype UI** — `index.html`, `js/app.js`, `js/widgets.js`, `js/data.js`, `css/styles.css` (repo root) | Authoritative for **looks, layout, interactions and micro-behaviour**. The production UI must reproduce this pixel-for-pixel and behaviour-for-behaviour. |
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

- The prototype's `css/styles.css` is **copied verbatim** into the product.
  Never restyle; extend only via documented additions (see 04 §3).
- `js/widgets.js` (the chart/widget engine) is reused nearly verbatim.
- `js/data.js` is mock data only — it defines *shapes*, never ships.
- Every doc has a **Scope** header and an **Acceptance criteria** section.
  Agents MUST satisfy the acceptance criteria and SHOULD satisfy the body.
- When a doc says "the prototype does X", verify against the actual files in
  the repo root before changing behaviour.