# 01 — Architecture

**Scope:** overall system architecture, technology choices, repository and
package layout, request flows, rendering strategy (HTMX), background work,
configuration, error handling, logging, testing strategy.

Related: [02](02-data-model.md) schema · [03](03-backend-api.md) API ·
[04](04-frontend.md) frontend · [13](13-deployment.md) ops · [14](14-security.md) security.

---

## 1. System diagram

```
                       ┌────────────────────────────────────────────────┐
   Browser             │              Glowworm (single Go binary)       │
┌───────────────┐      │                                                │
│  HTML pages   │◄─────┤  ┌──────────┐   ┌───────────────────────────┐  │
│  (server-     │      │  │ HTTP     │   │ Services (domain layer)   │  │
│   rendered)   │──────┼─►│ server   ├──►│  accounts · transactions  │  │
│               │      │  │ net/http │   │  imports · categoriser    │  │
│  HTMX 2.x     │      │  │ + mux    │   │  transfers · recurring    │  │
│  + app.js     │◄─────┤  │ + mw     │◄──│  audit · timeline · query │  │
│  + widgets.js │      │  └──────────┘   └────────────┬──────────────┘  │
└───────────────┘      │        │                     │                 │
                       │   HTML fragments           ┌─────┴──────┐        │
                       │   JSON (/api/v1)           │ store (SQL)│        │
                       │   NDJSON (chat stream)     └─────┬──────┘        │
                       │                                  │               │
                       │  ┌───────────┐  ┌────────────┐  │  ┌──────────┐ │
                       │  │ ai/       │  │ importer/  │  ▼  │ SQLite   │ │
                       │  │ providers │  │ parsers    │◄───►│ (default)│ │
                       │  └───────────┘  └────────────┘     │ Postgres │ │
                       │  ┌───────────┐  ┌───────────────┐  └──────────┘ │
                       │  │classifier/│  │ jobs (in-proc)│               │
                       │  │ TF-IDF+MLR│  └───────────────┘               │
                       │  └───────────┘                                  │
                       └────────────────────────────────────────────────┘
                                            ▲
                                     Nginx (TLS, static
                                     caching, body size) — optional
```

## 2. Technology decisions (normative)

| Area | Decision | Notes |
|---|---|---|
| Language | **Go ≥ 1.22** | Stdlib `net/http` mux with method+path patterns (`GET /api/v1/accounts/{id}`). No web framework. |
| Frontend | **Server-rendered HTML + HTMX 2.x + small vanilla JS** | No SPA framework, no node build step. HTMX is vendored. |
| Templates | **`html/template`** (stdlib), embedded via `go:embed` | Auto-escaping on. Partials for fragments. |
| Charts | **Client-side SVG** via reused `js/widgets.js` engine | Data passed as JSON in `data-*` attributes (exactly as the prototype does). |
| Database | **SQLite default** via `modernc.org/sqlite` (pure Go, no cgo → trivial cross-compilation). **Postgres optional** via `pgx`. | Single `database/sql` code path; dialect-aware DDL only in migrations. |
| Migrations | **golang-migrate**, embedded `migrations/` dir, auto-run on start | Versioned, forward-only in production; dev may re-create. |
| Data access | **Hand-written SQL** in repository layer | No ORM. One repo file per aggregate. |
| AI | In-process `ai.Provider` interface; adapters per provider | Doc 09. Never a hard dependency. |
| Scheduler | In-process goroutine ticker | Post-import jobs, recurring/transfer re-detection, model retrain. No external queue. |
| Logging | **`log/slog`**, structured, JSON in production, text in dev | Request IDs on every line. |
| Config | Env vars (`GLOWWORM_*`) + optional `glowworm.toml` | Doc 13 §3. |
| Frontend assets | Embedded (`go:embed`), served with cache headers + ETags | Fonts self-hosted [D17]. |

## 3. Repository layout

```
glowworm/
├── cmd/glowworm/main.go          # entrypoint: config, store open, server start
├── internal/
│   ├── config/                   # env+file config, defaults, validation
│   ├── server/                   # HTTP: router, middleware, handlers
│   │   ├── middleware.go         # logging, recovery, request-id, auth, csrf
│   │   ├── pages.go              # full-page handlers (one per view)
│   │   ├── fragments.go          # HTMX fragment handlers
│   │   └── api/                  # /api/v1 JSON handlers (thin; call services)
│   ├── render/                   # template loading, fragment/page renderer,
│   │   └── helpers.go            # money/date formatting, badge helpers
│   ├── templates/                # *.html (layout, views, partials) [embed]
│   ├── store/                    # db open, migrations runner, tx helper
│   │   ├── migrations/           # 0001_init.up.sql / .down.sql ...  [embed]
│   │   └── repos/                # accounts.go, transactions.go, ...
│   ├── core/                     # domain services (one package per concern)
│   │   ├── accounts/             # incl. valuations
│   │   ├── transactions/
│   │   ├── categories/
│   │   ├── transfers/
│   │   ├── recurring/
│   │   ├── imports/
│   │   ├── categoriser/          # cascade orchestrator
│   │   ├── audit/                # audit writer + history engine (doc 11 §4)
│   │   ├── timeline/             # snapshot/series computation, reconcile
│   │   ├── query/                # NL query pipeline + plan compiler
│   │   ├── widgets/              # widget types, dataset→presentation derive
│   │   └── setup/                # first-run walkthrough state
│   ├── classifier/               # pure-Go TF-IDF + multinomial LR (doc 07)
│   ├── importer/                 # csv/, ofx/, qif/, pdf/ parsers + sniffing
│   ├── ai/                       # provider interface, adapters, prompts
│   ├── money/                    # minor-unit type, parsing, formatting
│   └── jobs/                     # scheduler wiring
├── web/
│   ├── static/                   # css/styles.css (verbatim copy), js/, fonts/ [embed]
│   └── htmx/htmx.min.js          # vendored HTMX 2.x
├── docs/design/                  # this documentation
├── deploy/                       # Dockerfile, compose.yaml, nginx.conf, systemd unit
└── glowworm.example.toml
```

## 4. Layering rules

1. **Handlers** (`server/`) parse/validate input, call **services**, render
   output. No SQL, no business rules.
2. **Services** (`core/`) own business rules and invariants; they open store
   transactions via `store.WithTx` and write audit entries **in the same SQL
   transaction** as the mutation [Backend §14].
3. **Repos** (`store/repos/`) contain SQL only. They never call services.
4. **Engines** (`importer/`, `classifier/`, `ai/`, `query/`) are pure-ish
   libraries: deterministic where possible, injected dependencies, no global
   state.
5. Every service method that mutates data MUST produce an audit entry (doc 11
   §3 lists the catalogue). Audit writing is part of the service, not the
   handler.

## 5. Rendering strategy (HTML + HTMX)

### 5.1 Pages

One URL per view, rendered by `layout.html` + a view template:

| Route | View |
|---|---|
| `GET /` | Dashboard |
| `GET /queries` | Queries (AI chat) |
| `GET /transactions` | Transactions |
| `GET /categories` | Categories |
| `GET /import` | Import |
| `GET /history` | History (timeline + audit modes) |
| `GET /settings` | Settings |
| `GET /login`, `POST /login`, `POST /logout` | Auth |
| `GET /setup` | First-run wizard page/modal host |

The layout contains the SVG sprite, the masthead (brand, tab nav, period
selector, privacy toggle, settings link) and `<main id="content">` —
mirroring the prototype's DOM structure exactly (class names identical).

### 5.2 Fragments

Handlers detect HTMX via the `HX-Request: true` header. The **same URL**
returns the full page for browsers and just the relevant fragment for HTMX
calls. Fragments are `<div>`/`<tbody>`/`<tr>` partials. Rules:

- Fragments MUST be valid standalone HTML for their swap target.
- Responses to HTMX mutations MAY carry `HX-Trigger` events (e.g.
  `tx-updated`, `widget-pinned`) that other parts of the page listen to via
  `htmx:afterSwap` / custom JS listeners to refresh dependent regions.
- Never send `HX-Redirect` for data operations; return fragments.

### 5.3 Key HTMX interactions (full map in doc 04 §7)

| Interaction | Verb + target | Swap |
|---|---|---|
| Transactions filters/search | `GET /transactions?...` | `#tx-table-wrap` (tbody + counts) |
| Select account card | `GET /transactions?account={id}` | accounts grid + table + subheading |
| Inline tx editor open/save | `GET /transactions/{id}/edit` `PUT /api fragment` | editor `<tr>` / restored row |
| Category form save | `POST /categories` | category grid + filter options |
| Import steps | `POST /imports` etc. | step container per stage |
| Timeline slider | `GET /history/snapshot?date=...` | snapshot stats + accounts + chart host |
| Audit filter chips | `GET /history?mode=audit&filter=...` | `#history-list` |
| Chat send | `POST /queries/messages` → NDJSON stream (JS) | messages container |
| Widget drawer apply | `PUT /widgets/pinned/{id}` | widget card(s) |
| Pinned grid edit ops | `PATCH /widgets/pinned/{id}` | grid |
| Account modal | `GET /settings/accounts/{id}/form`, `POST/PATCH` | modal body + cards |

### 5.4 What stays client-side (vanilla JS)

Exactly the behaviours the prototype implements in JS and that do not need
the server:

- Theme apply/switch (`localStorage` + `data-theme`, with the anti-FOUC
  inline script in `<head>`), system-preference listener.
- Privacy toggle (class on `<body>`), client-only.
- Widget rendering: `widgets.js` engine — SVG charts, `ResizeObserver`
  redraws, conversion between widget types (drawer), dataset→presentation.
- Pinned-grid edit mode: drag-swap, edge-resize, ghost/drop-target visuals
  (prototype logic; persisted via HTMX calls).
- Chat composer autosize; worm loader animation; message entrance animation
  (respecting `prefers-reduced-motion`).
- Keyboard shortcuts: `Cmd/Ctrl+K` → focus query; `0–6` → views; `Esc` →
  close modal/drawer (doc 04 §9).
- Toasts (copy unchanged; content driven by `HX-Trigger` events).

### 5.5 The chat stream

The assistant reply is streamed as **NDJSON** over a `POST /queries/messages/stream`
fetch (doc 10 §6). Event kinds: `steps`, `text`, `widget`, `done`, `error`.
This keeps the prototype's "worm loader → wriggle out → reveal" choreography
while allowing progressive display of reasoning steps.

## 6. Background jobs

A single scheduler goroutine (ticker, 30s) drains a job queue table-free
in-memory queue + periodic sweeps:

| Job | Trigger | Work |
|---|---|---|
| `import.process` | upload accepted | parse → stage rows → run categorisation cascade → notify |
| `import.confirm` | user confirms | commit rows, detect transfers, detect recurring, retrain classifier, audit |
| `categoriser.retrain` | after import confirm; manual endpoint; nightly sweep | refit local model (doc 07 §5) |
| `detection.sweep` | nightly + after any transaction mutation | recurring detection refresh; missed transfer candidates (cheap pass) |
| `ai.retry` | failed AI batches with retryable errors | bounded retries |

All jobs are idempotent and logged. Job failures surface in the UI as
toasts/banners on next relevant request (status row in `job_state` table or
in-memory + audit entry).

## 7. Configuration

Full list in doc 13 §3. Summary:

```
GLOWWORM_ADDR           (default 127.0.0.1:8080)
GLOWWORM_DATA_DIR       (default ./data)
GLOWWORM_DB             sqlite | postgres (+ GLOWWORM_DB_DSN for pg)
GLOWWORM_TIMEZONE       (default system local)
GLOWWORM_SECRET_KEY     hex/base64 32B; auto-generated & persisted if absent
GLOWWORM_AUTH_MODE      password | none   (default password)
GLOWWORM_LOG_FORMAT     text | json
GLOWWORM_UPLOAD_MAX_MB  (default 25)
```

## 8. Error handling

- JSON API errors: proper HTTP status + body `{"error":{"code":"...","message":"...","details":...}}`.
  Machine codes: `validation_failed`, `not_found`, `conflict`, `ai_unavailable`, `import_state`, `auth_required`.
- HTML/HTMX errors: render the app's inline error partial (styled `panel` with
  danger text); for 5xx, toast event `app-error`.
- Panics: recovered by middleware → 500 + log with stack; never crash the server.
- Validation errors list field+message so forms can re-render with messages.

## 9. Logging

- `slog` with keys: `req_id`, `method`, `path`, `status`, `dur_ms`, `user`, `actor`.
- AI calls always log capability/provider/model/latency/tokens to the
  `ai_calls` table (doc 09 §8) — that is data, not just logs.
- No secrets, no raw API keys, no full account numbers in logs (masked).

## 10. Testing strategy

| Level | Tooling | Focus |
|---|---|---|
| Unit | `go test` | money math, date parsing, normalisation, classifier train/predict round-trip, plan compiler (table-driven), transfer/recurring detectors on fixture ledgers, history engine (revert/jump-back/restore-path/delete round-trips) |
| Parser tests | `go test` with fixture files | CSV dialects, OFX/QIF, PDFs (pre-extracted text) |
| Store | `go test` against temp SQLite | repo SQL, migration up/down |
| Integration | `httptest` + temp DB | full flows: import→review→commit→detect→audit; auth+CSRF |
| Golden fragments | `go test` | key templates render stable HTML (tx table, widget card, audit row) |
| Frontend | manual checklist (04 §10) + optional Playwright smoke | visual fidelity vs prototype, keyboard shortcuts, drag/resize |

CI: `go vet`, `staticcheck`, `gofmt`, tests with `-race`. Acceptance = all
green + golden templates unchanged unless a doc change dictates it.

## 11. Acceptance criteria

1. `go build ./cmd/glowworm` produces a single static binary; `go:embed`
   covers templates, CSS, JS, fonts, migrations.
2. Running the binary serves the UI at `GLOWWORM_ADDR` with no external
   services; with `GLOWWORM_AI` unset all non-AI features work.
3. Every page/fragment of the prototype's DOM structure is reproduced with
   identical class names; `css/styles.css` is byte-identical to the prototype
   (plus only documented additions).
4. All data mutations go through services that write audit entries atomically.
5. `sqlite` and (with DSN) `postgres` both pass the store test-suite.
6. The NDJSON chat stream, HTMX fragment flows, and JSON API all function
   without any JS framework and without a node build step.