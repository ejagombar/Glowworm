# 03 — Backend API

**Scope:** complete HTTP surface — JSON API (`/api/v1`) and HTMX fragment
endpoints — with request/response shapes, query parameters, pagination and
the transaction display-grouping contract.

Related: [02](02-data-model.md) schema · [04](04-frontend.md) fragment wiring ·
[06](06-import.md) · [07](07-categorisation.md) · [10](10-natural-language-query.md) ·
[11](11-audit-history-timeline.md).

---

## 1. Conventions

- JSON API base: `/api/v1`. All bodies JSON unless noted. Content type
  `application/json`.
- Fragment endpoints return HTML partials and are used by HTMX (detected via
  `HX-Request: true`); the same GET routes also serve full pages without it.
- Money fields are integer **minor units**, names end in `_minor`; currency
  code fields accompany them (`currency`).
- Dates: `YYYY-MM-DD`. Timestamps: RFC3339 UTC.
- Auth: session cookie (doc 14). All endpoints require auth unless listed in
  14 §4.
- Errors: doc 01 §8 format.
- **Pagination:** list endpoints accept `limit` (default 50, max 200) and
  `cursor` (opaque). Response envelope:

```json
{ "items": [...], "next_cursor": "..." | null }
```

- **Period filter:** everywhere a data list/aggregate accepts
  `period` (`all|ytd|last-12m|last-3m|last-month|last-day|custom`) or
  explicit `from`/`to` dates — identical semantics to the prototype's
  `periodRange()` (doc 04 §5.3): `last-month` = previous calendar month,
  `ytd` = Jan 1 → today, etc.

## 2. Auth

| Method & path | Purpose |
|---|---|
| `GET /login` | Login page (username + password) — the only sign-in surface; **no account creation here** [D24] |
| `POST /login` | `{username,password}` → session cookie (rate-limited, generic errors — doc 14 §2) |
| `POST /logout` | Clear session |
| `GET /api/v1/auth/session` | `{authenticated, username, display_name}` |
| `PUT /api/v1/auth/password` | Change password (requires current; invalidates other sessions) |
| `POST /api/v1/setup/account` | Create the owner account — **only while no user exists** (setup wizard step 1; claims the instance; doc 12 §3, doc 14 §2). 404 once an account exists. |

## 3. Dashboard & aggregates

### `GET /api/v1/dashboard/summary?period=`

Powers the dashboard stat cards and hero chart.

```json
{
  "available_minor": 379226, "savings_minor": 450000,
  "currency": "GBP",
  "income_month_minor": 245000,
  "income_trend": {"label": "Salary", "date": "2024-06-01"},
  "spend_month_minor": 128450,
  "spend_trend": {"pct_vs_prev": -8, "direction": "down"},
  "balance_series": {
    "scope": "all|available|savings",
    "interval": "month",
    "labels": ["2022-01", "..."],
    "series": [{"name": "Balance", "values": [/* minor units */]}]
  },
  "recent": [ /* transaction display rows, §5.4, limit 7 */ ]
}
```

- `available` = Σ current balances of non-savings accounts; `savings` = Σ of
  savings accounts (prototype `accountTotals`).
- Clicking the Available/Savings cards sets `scope=available|savings` —
  implemented client-side by re-deriving the series (prototype
  `balanceSeriesFor`); the summary returns all three scopes' series when
  `include=scopes`.
- `income_trend.label` = latest recurring income series label ("Salary · 1 May" parity).
- `spend_trend.pct_vs_prev` = this calendar month vs previous, rounded;
  `direction` = `up|down|flat`.

### `GET /api/v1/categories/summary?period=`

Per-category spend for the category cards:

```json
{ "categories": [ {"id":"...","name":"Transport","color":"#4f5bc7","type":"spend","spent_minor":12840} ] }
```
`income`/`internal` categories return `spent_minor: null` (card shows "—").

## 4. Accounts

| Method & path | Notes |
|---|---|
| `GET /api/v1/accounts` | List with masked identifiers, `current_balance_minor`, `tx_count` (respects period) |
| `POST /api/v1/accounts` | Create (audit `account.created`) |
| `GET /api/v1/accounts/{id}` | Full detail **including full identifiers** (edit modal only) |
| `PATCH /api/v1/accounts/{id}` | Update (audit `account.updated` with field diffs) |
| `DELETE /api/v1/accounts/{id}` | Cascade delete (audit `account.deleted`) |
| `GET /api/v1/accounts/{id}/valuations` | Correction list |
| `POST /api/v1/accounts/{id}/valuations` | `{value_minor,date,note}` → valuation + audit `valuation.created`; response includes `drift_minor` (computed vs known) for the UI hint |
| `GET /api/v1/accounts/{id}/series?from&to&interval` | Balance series for charts |

Account JSON (list form):

```json
{
  "id": "a1...", "name": "Current account", "type": "debit",
  "color": "#0e6f51", "currency": "GBP",
  "current_balance_minor": 391266,
  "opening_balance_minor": 350000, "opening_balance_date": "2023-01-06",
  "ref_last4": "4211", "has_account_number": true, "has_iban": false,
  "tx_count": 142,
  "corrections_count": 1
}
```

## 5. Transactions [F15]

### 5.1 `GET /api/v1/transactions`

Query params:

| Param | Values |
|---|---|
| `view` | `grouped` (default; all-accounts list) · `statement` (per-account list) · `flat` (raw rows, API/integrations) |
| `account_id` | statement view target |
| `q` | free-text search — matches description, merchant, category name, recurring key, account names (parity with prototype `passesTxFilters` haystack) |
| `category_id`, `type` | `type` ∈ `expense|income|transfer|recurring` (`recurring` filters on `recurring_key IS NOT NULL`, combinable per prototype) |
| `period`/`from`/`to` | period filter |
| `sort` | `date_desc` (default) · `date_asc` · `amount_desc` · `amount_asc` |
| `limit`,`cursor` | pagination |

**Grouping contract (normative — implements prototype `displayRows`):**

- `view=grouped` (no `account_id`): rows where `type='transfer' AND transfer_id
  IS NOT NULL` collapse into **one display row per transfer**. The
  representative leg is the **outbound** leg (`amount < 0`), else the first
  leg. Display row = `{kind:"linked", rep, members:[...legs], source_account,
  destination_account}`. Unlinked/other txs are `kind:"single"` rows.
- `view=statement` (requires `account_id`): **no grouping** — every leg shown
  separately, exactly as on that account's statement, with a `counterparty`
  field (prototype `txCounterparty`):
  - transfer leg with known peer: `"To {destination account name}"` (outbound) / `"From {source account name}"` (inbound);
  - expense: `"To {description}"`; income: `"From {description}"`.
- Both views sort by the representative/own date descending.

Display row shape (grouped/statement):

```json
{
  "kind": "single|linked",
  "id": "t1...",
  "date": "2024-06-01",
  "description": "Transfer to Savings",
  "merchant": null,
  "category": {"id":"c1","name":"Internal transfer","color":"#2e9d8f"},
  "account": {"id":"a1","name":"Current account","color":"#0e6f51"},
  "counterparty": "To Savings",
  "source": "Current account", "destination": "Savings",
  "amount_minor": -50000,
  "type": "transfer",
  "linked": true,
  "transfer_id": "tr1...",
  "recurring": {"key":"rk1","label":"Transfer to Savings"} | null,
  "import_batch_id": null,
  "category_source": "local", "category_confidence": 0.91
}
```

### 5.2 Mutations

| Method & path | Body / notes |
|---|---|
| `POST /api/v1/transactions` | Manual tx create (account_id, date, description, amount_minor, type, category_id?) — audit `transaction.created` |
| `GET /api/v1/transactions/{id}` | Detail incl. legs if linked |
| `PATCH /api/v1/transactions/{id}` | Edit (date, description, amount_minor, type, category_id, account_id). Rules: doc 08 §5 — audit `transaction.edited` |
| `DELETE /api/v1/transactions/{id}` | Unlinks transfer if member; audit `transaction.deleted` |
| `POST /api/v1/transactions/{id}/link` | `{counterpart_id}` → creates transfer (`detected:manual`) — audit `transfer.linked` |
| `POST /api/v1/transactions/{id}/unlink` | Breaks link — audit `transfer.unlink` |
| `GET /api/v1/transactions/{id}/transfer-suggestions` | Candidate counterparts, ordered by (amount match, date proximity) — doc 08 §5.3 |
| `POST /api/v1/transactions/{id}/categorise` | Re-run cascade for one tx |
| `POST /api/v1/transactions/categorise-batch` | `{ids:[...]}` |
| `GET /api/v1/transactions/recurring/{key}` | All txs of a recurring series (ordered by date), plus series metadata |

## 6. Categories

| Method & path | Notes |
|---|---|
| `GET /api/v1/categories` | List (id, name, color, type, is_system) |
| `POST /api/v1/categories` | Create `{name,color,type}` (audit) |
| `PATCH /api/v1/categories/{id}` | Rename/recolor — rename cascades to transactions (audit lists count). System: colour only [D11] |
| `DELETE /api/v1/categories/{id}` | Non-system only; txs → uncategorised (audit) |

## 7. Imports [F2/F3]

Full flow in doc 06. Endpoints:

| Method & path | Purpose |
|---|---|
| `POST /api/v1/imports` | multipart upload (`file`, optional `account_id`). Creates batch, kicks detection. Returns `{import_id, status:"detecting"}` |
| `GET /api/v1/imports/{id}` | Batch status + detection result |
| `PATCH /api/v1/imports/{id}/schema` | Submit corrected column mapping (`{mapping, name?, save_schema?}`) → re-parse + stage |
| `POST /api/v1/imports/{id}/schema/save` | `{name}` — name & save the confirmed schema (AI name pre-filled) |
| `GET /api/v1/imports/{id}/review` | Staged rows + per-row category proposal/source/confidence/needs_review + aggregates |
| `PATCH /api/v1/imports/{id}/review` | Bulk ops: `{set_category:{row_ids,category_id}}`, `{accept_confident:true}`, `{mark_duplicates:[row_ids]}` |
| `POST /api/v1/imports/{id}/confirm` | Commit (audit `import.confirmed`) → triggers detection jobs + retrain |
| `POST /api/v1/imports/{id}/cancel` | Discard batch (audit `import.cancelled`) |
| `GET /api/v1/schemas` / `POST` / `PATCH /{id}` / `DELETE /{id}` | Saved schema CRUD |

## 8. Widgets & dashboard pins [F8/F9]

| Method & path | Purpose |
|---|---|
| `GET /api/v1/widgets/pinned` | Ordered pinned widgets (rendered `data` + dataset + plan) |
| `PUT /api/v1/widgets/pinned` | Replace full ordering/size map `{items:[{id,position,col,rows}]}` (drag/resize persistence) |
| `POST /api/v1/widgets/pinned` | Pin a widget `{title,type,dataset,plan?,col?,rows?}` (from chat) — audit `widget.pinned` |
| `PATCH /api/v1/widgets/pinned/{id}` | Update title/type/col/rows (type change → server re-derives `data` from dataset) — audit `widget.updated` |
| `DELETE /api/v1/widgets/pinned/{id}` | Unpin — audit `widget.unpinned` |

Widget JSON contract: doc 05.

## 9. Natural-language query & chat [F7]

Doc 10 has the full pipeline. Endpoints:

| Method & path | Purpose |
|---|---|
| `POST /api/v1/query` | `{text, conversation_id?}` → **synchronous** full response (used for API consumers & fallback) |
| `POST /api/v1/query/stream` | Same, **NDJSON stream** (UI default) |
| `GET /api/v1/conversations` | List (id, title, updated_at, message_count) |
| `GET /api/v1/conversations/{id}` | Messages (grouped by day client-side) |
| `DELETE /api/v1/conversations/{id}` | Delete conversation |
| `PATCH /api/v1/conversations/{id}` | Rename (title) |

## 10. Audit & history [F13]

Doc 11 is normative.

| Method & path | Purpose |
|---|---|
| `GET /api/v1/audit?actor=&action=&entity_type=&entity_id=&limit=&cursor=` | List (rich entries incl. diff, `state`, `path_id` and collapsed-group info; doc 11 §4) |
| `GET /api/v1/audit/{id}` | Full entry |
| `GET /api/v1/audit/{id}/capabilities` | Engine verdict `{revert, jump_back, forget, delete, reason?}` — the UI renders only what the engine permits (doc 11 §4.3) |
| `POST /api/v1/audit/{id}/revert` | Undo this entry — audit `audit.reverted` (new entry) |
| `POST /api/v1/audit/{id}/jump-back` | **Jump back to this point**: shelves (reverts + deactivates) every change after it; they stay expandable and restorable — audit `audit.jumpback` (doc 11 §4.2) |
| `POST /api/v1/audit/paths/{path}/restore` | **Restore a shelved path**: reverts the current future, re-applies the original path (skipping previously reverted entries) — audit `audit.path.activated` (doc 11 §4.2) |
| `DELETE /api/v1/audit/{id}?mode=forget\|undo` | Engine-mediated history surgery — Forget (keep effect, remove record) or Delete (undo effect + scrub); tombstone `audit.entry.deleted`; availability per §4.3 matrix |
| `GET /api/v1/snapshot?date=YYYY-MM-DD` | State at date: per-account balance, totals, tx count up to date |
| `GET /api/v1/timeline?from&to&interval&account_id` | Balance series (+ per-account series) |
| `GET /api/v1/reconcile/status?date=` | Reconciliation status per account (doc 11 §6) |
| `POST /api/v1/reconcile` | `{account_id,date,value_minor}` → creates valuation (`source:manual`), returns drift |

## 11. Categoriser

| Method & path | Purpose |
|---|---|
| `GET /api/v1/categoriser/status` | `{model_version, trained_at, training_count, rules_count, thresholds}` |
| `POST /api/v1/categoriser/retrain` | Refit local model (audit `classifier.retrained`) |
| `GET /api/v1/transactions?needs_review=1` | Review queue (category_source=none or needs_review) |

## 12. Recurring

| Method & path | Purpose |
|---|---|
| `GET /api/v1/recurring` | Series list (key, label, cadence, amount, first/last seen, next_expected, active) |
| `GET /api/v1/recurring/{key}` | Series + member transactions (ordered by date, grouped legs if transfers) |
| `POST /api/v1/recurring/refresh` | Force re-detection |

## 13. Settings & AI config [F14]

Doc 12 catalogue. Endpoints:

| Method & path | Purpose |
|---|---|
| `GET /api/v1/settings` | All settings (theme, currency, display name, thresholds…) |
| `PUT /api/v1/settings` | Patch settings (audit where data-affecting, doc 12 §4) |
| `GET /api/v1/settings/ai` | Provider config — **masked** key (`sk-••••••••`), models per capability |
| `PUT /api/v1/settings/ai` | Update provider/models/key (audit `settings.ai.updated`; key re-encrypted) |
| `POST /api/v1/settings/ai/test` | Connectivity test: tiny completion; returns `{ok, latency_ms, error?}` |
| `POST /api/v1/setup/complete` | Mark walkthrough complete (audit `setup.completed`) |
| `GET /api/v1/setup/status` | `{completed, has_categories, has_accounts, has_transactions, ai_configured}` |

## 14. Health

`GET /healthz` → `{"status":"ok"}` (no auth). `GET /readyz` → checks DB
migration state.

## 15. Fragment endpoints (HTMX)

Fragments reuse service calls; they exist **only** where the UI needs a
partial swap. Non-exhaustive list (mapping to interactions in doc 04 §7):

| Method & path | Returns |
|---|---|
| `GET /transactions` (HX) | `#tx-region` (accounts grid + filters + table) |
| `GET /transactions/{id}/edit-form` | Inline editor `<tr>` (incl. type=transfer branch with suggestion `<select>` options) |
| `PUT /transactions/{id}` (HX form post) | Updated `<tr>` (or linked row) + `HX-Trigger: tx-updated` |
| `POST /transactions/{id}/link-form-suggestions` | `<option>` list for counterpart selector (driven by transfer-suggestions) |
| `GET /categories` (HX with form post) | Category grid + filter options fragment |
| `GET /settings/accounts/{id}/form` | Account modal body |
| `POST /settings/accounts` / `PATCH /settings/accounts/{id}` | Modal body (success → cards fragment + `HX-Trigger: accounts-changed`) |
| `POST /settings/accounts/{id}/valuations` | Modal body with drift hint |
| `GET /history/snapshot?date=` | Snapshot stats + timeline accounts + chart data host |
| `GET /history/audit-list?filter=` | `#history-list` rows (incl. collapsed groups) |
| `POST /audit/{id}/{action}` (HTMX; action ∈ revert\|jump-back\|restore-path\|delete) | `#history-list` re-render + `HX-Trigger: audit-updated` (availability per doc 11 §4.3; blocked reasons render inline) |
| `POST /imports` | Import step container (detection spinner → detected schema) |
| `GET /imports/{id}/review-fragment` | Review table fragment |
| `POST /queries/messages` | User bubble `<div>` (assistant reply arrives via NDJSON stream) |
| `GET /widgets/pinned-grid` | Re-rendered pinned grid |
| `PUT /widgets/pinned/{id}` (drawer apply) | Updated widget card(s) + toast trigger |

Fragment handlers MUST NOT bypass services (no direct SQL).

## 16. Acceptance criteria

1. Every endpoint in §2–14 exists, is authenticated (except health/login),
   validates input, and writes audit entries for mutations.
2. `view=grouped|statement|flat` behaviour matches the prototype `displayRows`
   semantics (tests cover mixed transfer/income/expense ledgers).
3. Pagination, period filtering and error shapes are uniform across lists.
4. No endpoint returns full account numbers/IBANs except `GET /accounts/{id}`.