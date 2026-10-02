# Glowworm — Backend Implementation Requirements

This document lists everything discussed so far that must be implemented in the
backend. The frontend is an HTMX/vanilla-JS skeleton; the backend is a Go
service that owns the data model, persistence, AI integrations, and the public
API.

---

## 1. Tech stack & architecture

- Go backend and core engine.
- Web UI served by the backend (HTMX, no React/Svelte/etc).
- Deployment target: website, mobile-friendly. Keep a future native app possible
  by exposing a clean JSON API.
- Persistence: a real database (SQLite for local/single-user, Postgres for
  hosted). All mutations go through the backend.

---

## 2. Core data model

### 2.1 Accounts

Fields required:

- `id`
- `name`
- `type`: `debit` | `credit` | `savings`
- `color`
- `opening_balance`
- `opening_balance_date`
- `account_number`
- `sort_code`
- `iban`
- `bic`
- `current_balance` (derived, but cached for list views)
- `created_at`
- `updated_at`

Backend rules:

- Store account identifiers verbatim (account number, sort code, IBAN, BIC).
  They are used by the importer and transfer detector to recognise when a
  transaction mentions one of the user's own accounts.
- Do not expose full account numbers/IBANs to the UI; return masked values
  (last 4 digits) plus a flag when a full value exists.
- `opening_balance_date` is the date the opening balance applies to. It is
  separate from the amount and must be editable.

### 2.2 Valuations / corrections (point-in-time account value)

A valuation is a correction to an account's value **at a point in time**, not a
payment.

Fields required:

- `id`
- `account_id`
- `value`
- `date` (the point in time the value applies to)
- `note`
- `created_at`
- `source`: `manual` | `import` | `ai`
- `audit_log_id` (link to the audit entry that created it)

Backend rules:

- Saving a current value always creates a new correction record; it never
  mutates historical values in place.
- The current balance for a date is computed by replaying opening balance +
  transactions + corrections up to that date.
- A correction must be reversible via the audit logger (see §9).
- The default valuation date is today when the UI opens the form.

### 2.3 Transactions

A transaction has a **source** and a **destination**, not just a merchant.

Fields required:

- `id`
- `account_id` (which statement/account this transaction appears on)
- `date`
- `description` (raw text from the statement)
- `amount` (signed, or debit/credit columns)
- `type`: `expense` | `income` | `transfer`
- `category_id` (nullable)
- `source` (free text or account reference)
- `destination` (free text or account reference)
- `merchant` (nullable, AI-detected; not guaranteed for every transaction)
- `recurring_key` (nullable)
- `transfer_id` (nullable — links two legs of a transfer)
- `created_at`, `updated_at`

Backend rules:

- **Normal transaction**: source is an account/card, destination is a merchant.
- **Transfer**: source is one account, destination is another account.
- `description` is always shown; `merchant` is an optional AI enrichment. The UI
  has a description column, not a merchant column.
- The same real-world transfer imported from two statements exists as **two**
  transaction rows (one receiving, one sending) that can be linked.

### 2.4 Transfers (linked legs)

Fields required:

- `id`
- `source_transaction_id`
- `destination_transaction_id`
- `detected`: `auto` | `manual`
- `created_at`

Backend rules:

- On the main transaction list (not filtered by account), the two linked legs
  are presented as one logical transfer with a "linked" tag.
- On a per-account (statement) view, each leg is shown separately exactly as it
  appears on that account's statement.
- Linking can be broken (unlink) and two unrelated transactions can be linked
  manually.
- When a user marks a transaction as a transfer, the backend must suggest
  candidate counterpart transactions, ordered by date proximity and amount
  similarity first.
- Internal transfers must not skew spending/income statistics.

### 2.5 Recurring transactions

Fields required (derived, not user-set):

- `recurring_key`
- optionally: predicted cadence, next expected date, confidence

Backend rules:

- Recurring detection is automatic only. It must not be a user-selectable tag.
- Detection groups transactions by description/merchant + amount + cadence
  (weekly, monthly, etc.).
- Clicking the recurring tag returns all related transactions over time.

### 2.6 Categories

Fields required:

- `id`
- `name`
- `color`
- `type`: `spend` | `income` | `internal` (internal transfers excluded from
  spend stats)
- `created_at`

Backend rules:

- Categories are created during the setup walkthrough and editable later.
- Auto-categorisation assigns a category to imported transactions.

### 2.7 Import schemas

Fields required:

- `id`
- `name`
- `bank` / source hint
- `columns` (detected column mapping)
- `confidence`
- `saved_at`

Backend rules:

- Schemas are detected from an uploaded file, shown to the user for
  confirmation with example transactions, then named and saved for reuse.
- On later imports, saved schemas are checked first before falling back to LLM
  detection.

### 2.8 Audit log

See §9. Every mutation must create an audit entry with enough information to
reconstruct and revert the change.

---

## 3. Account management API

- `GET /api/accounts` — list accounts with masked identifiers and current
  balance.
- `POST /api/accounts` — create account (name, type, opening balance, opening
  balance date, account number, sort code, IBAN, BIC).
- `PATCH /api/accounts/{id}` — update account.
- `DELETE /api/accounts/{id}` — delete account.
- `GET /api/accounts/{id}` — full account detail for the edit form (full
  identifiers only where appropriate).
- `POST /api/accounts/{id}/valuations` — record a point-in-time valuation.
  Body: `{ value, date, note }`. Creates a correction + audit entry.
- `GET /api/accounts/{id}/valuations` — list corrections.

All account mutations must be audit-logged.

---

## 4. AI-assisted importer

Backend responsibilities:

- Accept statement files: CSV, PDF, OFX, QIF, and any other format.
- Use the configured LLM to detect the file format and column mapping.
- Return a schema preview with a couple of parsed example transactions for the
  user to verify.
- Allow the user to correct the detected column mapping.
- Allow the user to name and save the corrected schema.
- The AI should propose a schema name; the user can override it.
- On future imports, check saved schemas first, then fall back to LLM detection.
- Parse into the transaction model with `account_id`, `date`, `description`,
  `amount`, and raw source/destination where available.
- Use account identifiers (account number, sort code, IBAN, BIC) to detect when
  a statement line references the user's own accounts.

Suggested endpoints:

- `POST /api/imports` — upload file, returns `import_id` + detected schema +
  preview rows.
- `PATCH /api/imports/{id}/schema` — save corrected schema mapping.
- `POST /api/imports/{id}/confirm` — commit parsed transactions.
- `GET /api/schemas`, `POST /api/schemas`, `PATCH /api/schemas/{id}`,
  `DELETE /api/schemas/{id}`.

---

## 5. Transfer detection & linking

Backend responsibilities:

- Automatically detect transfers between the user's own accounts.
- Match using:
  - Amount equality (opposite signs).
  - Date proximity.
  - Account identifiers in the description (account number, sort code, IBAN).
  - Known transfer descriptions ("Transfer to Savings", "Payment from
    Current", etc.).
- Create `transfer` records linking the two transaction legs.
- Exclude linked transfers from spend/income statistics.

Manual operations:

- `POST /api/transfers/link` — link two transaction ids.
- `POST /api/transfers/{id}/unlink` — break a link.
- `GET /api/transactions/{id}/transfer-suggestions` — return candidate
  counterpart transactions for linking, sorted by date/amount similarity.

Transaction editing rules:

- When a transaction is edited and its type is set to `transfer`, the UI shows
  an account selector and a counterpart transaction selector. The suggestions
  endpoint above drives the counterpart list.

---

## 6. Recurring transaction detection

Backend responsibilities:

- Detect recurring transactions automatically (bills, direct debits, salaries,
  subscriptions).
- Group by description/merchant + normalised amount + cadence.
- Assign a stable `recurring_key`.
- Recurring must not be manually settable by the user.
- Provide an endpoint to fetch all transactions for a recurring key over time:
  `GET /api/transactions/recurring/{key}`.

---

## 7. Automatic categorisation

Backend responsibilities:

- Assign categories to imported transactions using the configured LLM.
- Respect user-defined categories; do not invent categories that do not exist.
- Allow manual override by the user.
- Categories of type `internal`/transfer must not be suggested for normal
  spend transactions.

Suggested endpoints:

- `GET /api/categories`, `POST /api/categories`, `PATCH /api/categories/{id}`,
  `DELETE /api/categories/{id}`.
- `POST /api/transactions/{id}/categorise` — AI categorise a single transaction.
- `POST /api/transactions/categorise-batch` — categorise a batch after import.

---

## 8. Dynamic data viewer & widgets

Backend responsibilities:

- Accept natural-language questions such as
  "How much have I spent on Transport over the past year?"
- Use the configured LLM to:
  - Interpret the question.
  - Detect entities such as `Transport` (category), time range, and account
    scope.
  - Produce a database query.
- Return a structured response containing:
  - The thought process / highlights.
  - One or more widget definitions as JSON.
- Support widget types: `bar`, `line`, `pie`, `table`, `text`, `number`,
  `calculator`.
- Widget JSON must be frontend-agnostic and transferable; the frontend renders
  it, and the user can change settings (e.g. bar → line) without the backend
  re-querying.
- Pinned widgets must persist server-side and be returned on the dashboard.

Suggested endpoints:

- `POST /api/query` — natural-language query → `{ steps, widgets }`.
- `GET /api/widgets/pinned`, `PUT /api/widgets/pinned` — persist pinned
  dashboard widgets.
- `PATCH /api/widgets/{id}` — update a pinned widget's settings.

---

## 9. Audit logger & history

Backend responsibilities:

- Log **every** mutation, whether made by the user or the AI.
- Each audit entry must be a rich, diff-like record showing exactly what
  changed (old value → new value), not just a text note.
- Each entry must carry enough data to be reversible.
- Support navigating history and returning to any previous point in time.
- Changes should behave like git diffs: reversible, inspectable, ordered.

Entry fields required:

- `id`
- `timestamp`
- `actor`: `user` | `ai` | `system`
- `action` (e.g. `account.created`, `account.valuation.correction`,
  `transfer.link`, `transfer.unlink`, `transaction.edit`, `category.created`,
  `schema.saved`, `import.confirmed`)
- `entity_type`, `entity_id`
- `before` / `after` (structured diff)
- `revert_data` (what is needed to undo the change)

Suggested endpoints:

- `GET /api/audit` — list/filter audit entries (by actor, action, entity).
- `GET /api/audit/{id}` — full diff for an entry.
- `POST /api/audit/{id}/revert` — undo that change.
- `GET /api/snapshot?date=...` — reconstruct state at a point in time.

Concrete examples that must be logged:

- Account valuation correction: `before` = previous value, `after` = new value,
  plus the valuation date.
- Account created/updated/deleted.
- Transfer linked/unlinked.
- Transaction edited (including category/type/amount/date changes).
- Category created/updated/deleted.
- Import confirmed.
- Schema saved/edited.

---

## 10. Timeline viewer & reconciliation

Backend responsibilities:

- Support reconstructing the state of all accounts at any past date.
- Provide balance series over time.
- Support reconciliation: compare the computed balance at a date against the
  statement/known balance, and surface divergences.
- Divergences should be actionable (open the relevant transactions/corrections).

Suggested endpoints:

- `GET /api/timeline` — balance series for the selected accounts/range.
- `GET /api/snapshot?date=...` — balances and state at a date.
- `POST /api/reconcile` — mark an account reconciled at a date/balance and
  record the result.

---

## 11. AI provider abstraction

Backend responsibilities:

- Support multiple providers: OpenAI (GPT), Anthropic (Claude), Google
  (Gemini), Ollama (local), and custom endpoints.
- Store provider configuration and API keys securely (encrypted at rest, never
  returned in full to the UI).
- Provide a single internal interface for all LLM calls (schema detection,
  categorisation, natural-language query planning, merchant detection).
- Support selecting a model per provider.

Suggested endpoints:

- `GET /api/settings/ai` — current provider config (masked secrets).
- `PUT /api/settings/ai` — update provider/model/API key.

---

## 12. Settings

Backend responsibilities:

- Persist user settings: theme, currency, privacy preference, AI provider.
- Settings changes are audit-logged where they affect data (AI provider config
  changes should be logged; pure theme/currency preference may be exempt).

---

## 13. Setup & configuration walkthrough

Backend responsibilities:

- On first run, guide the user through:
  - Creating initial categories.
  - Importing a first statement.
  - Choosing an AI provider.
- Persist the completed-walkthrough flag so it is not shown again (but can be
  re-run from Settings).

Suggested endpoint:

- `POST /api/setup/complete` — mark walkthrough complete.

---

## 14. Non-functional requirements

- All monetary values stored as integers (minor units) or fixed-point decimals;
  never floats.
- All dates stored as ISO dates; all timestamps UTC.
- Every mutation is transactional (write + audit entry atomically).
- API responses are JSON for data operations and HTML fragments for HTMX where
  needed.
- Full identifiers (IBAN/account number) are masked in list responses and only
  returned to the owning user where necessary.
- The audit log is append-only; historical entries are never mutated.
