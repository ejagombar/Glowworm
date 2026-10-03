# 02 — Data Model

**Scope:** database schema, column semantics, conventions (money, dates,
IDs), derived data and invariants. DDL is SQLite-flavoured; Postgres
differences noted inline.

Related: [03](03-backend-api.md) API shapes · [07](07-categorisation.md) ·
[08](08-transfers-recurring.md) · [11](11-audit-history-timeline.md).

---

## 1. Conventions (normative)

- **IDs:** UUIDv4 strings (`TEXT` PK) for user-facing entities. Audit entries
  use monotonically increasing `INTEGER` (append-only log ordering).
- **Money:** integer **minor units** (`INTEGER`, signed). Column suffix
  `_minor`. Never floats [D15]. Currency ISO-4217 `TEXT` travels alongside
  (per account; instance display currency in settings).
- **Dates & times:** transaction/statement dates are calendar dates
  (`TEXT 'YYYY-MM-DD'` in SQLite / `DATE` in Postgres) — no timezone.
  Timestamps (`created_at`, …) are UTC `TEXT RFC3339` / `TIMESTAMPTZ`.
  "Today" is computed in `GLOWWORM_TIMEZONE`.
- **Booleans:** `INTEGER 0/1` (SQLite) / `BOOL` (PG).
- **JSON columns:** `TEXT` (SQLite) / `JSONB` (PG); document the Go struct.
- **Every mutating service** writes an `audit_entries` row in the same SQL
  transaction as the change [Backend §14].
- **Soft deletion (audit):** the history engine (doc 11 §4) must be able to
  re-apply past changes, so audit-driven reverts of *created* entities set
  `deleted_at` instead of removing rows; live queries and balance replay
  always filter `deleted_at IS NULL`. Hard `DELETE` is reserved for explicit
  confirmed destructive endpoints and engine purges.
- **Naming:** snake_case tables/columns; singular entity names.

## 2. Core reference data

### 2.1 `settings`

Key-value store for app settings (doc 12 has the key catalogue).

```sql
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,            -- JSON-encoded typed value
  updated_at TEXT NOT NULL             -- RFC3339 UTC
);
```

### 2.2 `users`

Single owner (v1) — kept as a table so multi-user remains possible [D16].

```sql
CREATE TABLE users (
  id            TEXT PRIMARY KEY,       -- uuid
  username      TEXT NOT NULL UNIQUE,
  display_name  TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,          -- bcrypt
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
```

## 3. Accounts & valuations

### 3.1 `accounts` [F10]

```sql
CREATE TABLE accounts (
  id                   TEXT PRIMARY KEY,
  name                 TEXT NOT NULL,
  type                 TEXT NOT NULL CHECK (type IN ('debit','credit','savings')),
  color                TEXT NOT NULL,             -- hex, from UI palette
  currency             TEXT NOT NULL,             -- ISO-4217, default = display currency
  opening_balance_minor INTEGER NOT NULL DEFAULT 0,
  opening_balance_date TEXT,                      -- 'YYYY-MM-DD'; NULL => opening balance ignored in replay
  account_number       TEXT NOT NULL DEFAULT '',  -- stored verbatim; masked in list APIs
  sort_code            TEXT NOT NULL DEFAULT '',
  iban                 TEXT NOT NULL DEFAULT '',
  bic                  TEXT NOT NULL DEFAULT '',
  notes                TEXT NOT NULL DEFAULT '',
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  deleted_at           TEXT                    -- history-engine soft delete (doc 11 §4.5)
);
```

Rules:
- Identifiers are stored **verbatim**; the importer and transfer detector use
  them to recognise the user's own accounts inside statement text.
- **Masking [Backend §2.1]:** list/API responses return `last4` (last 4
  digits) + `has_number`/`has_iban` flags. Full values only in
  `GET /api/v1/accounts/{id}` (used by the account edit modal).
- The account colour is assigned from the prototype palette
  (`#0e6f51 #4f5bc7 #c9822e #c0483f #2f7fb0 #7a58b8 #2e9d8f #6b7280`) cycling
  by creation order unless user-chosen.
- **Delete:** hard delete, cascading transactions/valuations/transfers;
  audit entry keeps the full snapshot. UI demands confirmation (prototype
  confirm modal). If transactions exist, the confirm copy states they will be
  deleted too.

### 3.2 `valuations` — point-in-time corrections [F11]

A valuation records the account's **true value at a date**. It is never a
payment and never mutates history in place [Backend §2.2].

```sql
CREATE TABLE valuations (
  id            TEXT PRIMARY KEY,
  account_id    TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  value_minor   INTEGER NOT NULL,
  date          TEXT NOT NULL,            -- 'YYYY-MM-DD'
  note          TEXT NOT NULL DEFAULT '',
  source        TEXT NOT NULL CHECK (source IN ('manual','import','ai')),
  drift_minor   INTEGER,                  -- value_minor − computed balance before this correction (NULL = not computed)
  audit_log_id  INTEGER,                  -- audit_entries.id that created it
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  deleted_at    TEXT                      -- history-engine soft delete (doc 11 §4.5)
);
CREATE INDEX idx_valuations_account_date ON valuations(account_id, date);
```

**Balance replay (checkpoint semantics — normative):**

```
balance(account, D) =
    latest valuation V on account with V.date ≤ D
      ? V.value_minor + Σ tx.amount_minor where V.date < tx.date ≤ D
      : opening_balance_minor + Σ tx.amount_minor where opening_date < tx.date ≤ D
        (if opening_balance_date is NULL: all tx with tx.date ≤ D)
```

Each new correction acts as a checkpoint; earlier history is unaffected.
`current_balance` shown in UIs = `balance(account, today)`, cached on the
account row read path (computed per request; see §7 for scale notes).

Creating a valuation with `drift_minor != 0` records a detected divergence —
this powers the reconciliation status (doc 11 §6).

## 4. Categories

```sql
CREATE TABLE categories (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  color      TEXT NOT NULL,
  type       TEXT NOT NULL CHECK (type IN ('spend','income','internal')),
  is_system  INTEGER NOT NULL DEFAULT 0,     -- 1: cannot rename/delete
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT,                           -- history-engine soft delete (doc 11 §4.5)
  UNIQUE (name COLLATE NOCASE)
);
```

- **System categories** (seeded at first run): `Income` (type `income`) and
  `Internal transfer` (type `internal`). Fixed name, not deletable [D11].
- User-created categories default to `spend`; the category form offers
  Spend/Income [D11].
- Deleting a user category re-assigns its transactions to
  `category_id = NULL` (rendered as "Uncategorised") — never deletes
  transactions. Audit entry lists affected count.
- **Type masking invariant:** transactions of type `expense` may only be
  categorised into `spend` categories; `income` → `income` categories;
  `transfer` → the system `internal` category. Enforced in the service layer
  and the categorisation cascade (doc 07 §2).

## 5. Transactions & transfers

### 5.1 `transactions` [F15]

One row per statement line, on exactly one account.

```sql
CREATE TABLE transactions (
  id                  TEXT PRIMARY KEY,
  account_id          TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  date                TEXT NOT NULL,             -- 'YYYY-MM-DD' (statement date)
  description         TEXT NOT NULL,             -- raw text from the statement; always shown [D1]
  merchant            TEXT,                      -- optional AI-detected destination/payer [D1]
  amount_minor        INTEGER NOT NULL,          -- signed: negative = money out of this account
  currency            TEXT NOT NULL,
  type                TEXT NOT NULL CHECK (type IN ('expense','income','transfer')),
  category_id         TEXT REFERENCES categories(id) ON DELETE SET NULL,
  category_source     TEXT NOT NULL DEFAULT 'none'
                        CHECK (category_source IN ('user','local','ai','none')),
  category_confidence REAL,
  source_text         TEXT NOT NULL DEFAULT '',  -- raw 'source' if the statement provides one
  destination_text    TEXT NOT NULL DEFAULT '',  -- raw 'destination' if the statement provides one
  recurring_key       TEXT,                      -- set only by the recurring detector [D5]
  transfer_id         TEXT,                      -- denormalised FK into transfers
  import_batch_id     TEXT,                      -- NULL for manually created txs
  dedupe_hash         TEXT NOT NULL,             -- doc 06 §8
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  deleted_at          TEXT                       -- history-engine soft delete (doc 11 §4.5)
);
CREATE INDEX idx_tx_account_date ON transactions(account_id, date DESC);
CREATE INDEX idx_tx_category ON transactions(category_id);
CREATE INDEX idx_tx_recurring ON transactions(recurring_key) WHERE recurring_key IS NOT NULL;
CREATE INDEX idx_tx_transfer ON transactions(transfer_id) WHERE transfer_id IS NOT NULL;
CREATE INDEX idx_tx_import ON transactions(import_batch_id) WHERE import_batch_id IS NOT NULL;
CREATE INDEX idx_tx_dedupe ON transactions(account_id, dedupe_hash);
```

Semantics:
- **Source/destination resolution** (for the To/From column, `txCounterparty`
  parity) is computed at read time by the API layer:
  - `expense`: source = the account; destination = `merchant` ?? `description`.
  - `income`: source = `merchant` ?? `description`; destination = the account.
  - `transfer`: both ends are accounts via the linked legs (doc 08 §4).
  `source_text`/`destination_text` preserve the raw statement wording when
  present and take precedence for display in the statement view.
- `type` is stored (editable in the inline editor). Changing type to
  `transfer` without linking is rejected — use the link flow (doc 08 §5).
- `category_source='user'` rows are the **training data** for the local
  classifier [Backend §2.3].
- Unique-ish dedupe: `dedupe_hash = sha256(account_id ‖ date ‖ amount_minor ‖
  normalised(description))` — used to detect re-imported statements (doc 06 §8).

### 5.2 `transfers` — linked legs [F5]

```sql
CREATE TABLE transfers (
  id                       TEXT PRIMARY KEY,
  source_transaction_id      TEXT NOT NULL UNIQUE REFERENCES transactions(id) ON DELETE CASCADE, -- outbound (amount < 0)
  destination_transaction_id TEXT NOT NULL UNIQUE REFERENCES transactions(id) ON DELETE CASCADE, -- inbound (amount > 0)
  detected                 TEXT NOT NULL CHECK (detected IN ('auto','manual')),
  method                   TEXT NOT NULL DEFAULT '',   -- identifier | description | amount-date | manual
  confidence               REAL,
  created_at               TEXT NOT NULL,
  deleted_at               TEXT                        -- history-engine soft delete (doc 11 §4.5)
);
CREATE INDEX idx_transfers_dest ON transfers(destination_transaction_id);
```

Invariants (service-enforced, additionally asserted by a nightly integrity check):
1. `source_transaction_id != destination_transaction_id`.
2. The two legs belong to **different accounts**.
3. Source leg `amount_minor < 0`, destination leg `amount_minor > 0`, and
   `|amounts|` SHOULD be equal (equality is enforced for auto-detection;
   manual links MAY differ — flagged in audit).
4. Both legs have `type='transfer'` and `category_id` = the system `internal`
   category, `category_source='none'`.
5. `transactions.transfer_id` always equals the `transfers.id` on both legs.
6. A transaction may be in at most one transfer.

Linked transfers are **excluded** from spending/income statistics [Backend §2.4].

## 6. Detection artefacts

### 6.1 `recurring_keys` [F6]

Metadata for detected series (the transactions themselves carry `recurring_key`).

```sql
CREATE TABLE recurring_keys (
  key            TEXT PRIMARY KEY,          -- stable hash: doc 08 §6
  label          TEXT NOT NULL,             -- normalised merchant/description
  cadence        TEXT NOT NULL,             -- weekly | fortnightly | monthly | quarterly | annually | irregular
  interval_days  INTEGER,
  amount_minor   INTEGER,                   -- representative (median) amount, signed relative to direction
  direction      TEXT NOT NULL CHECK (direction IN ('in','out')),
  first_seen     TEXT NOT NULL,
  last_seen      TEXT NOT NULL,
  next_expected  TEXT,
  confidence     REAL NOT NULL,
  active         INTEGER NOT NULL DEFAULT 1,
  updated_at     TEXT NOT NULL
);
```

`recurring_key` values are content-addressed (hash of label + amount bucket +
cadence) so re-detection is stable across runs. Recurring is **never**
user-settable [D5]; user edits to a member transaction's description can drop
it out of the series (detector is idempotent and re-runs).

### 6.2 Classifier model

```sql
CREATE TABLE classifier_models (
  id             INTEGER PRIMARY KEY,
  version        INTEGER NOT NULL,
  trained_at     TEXT NOT NULL,
  training_count INTEGER NOT NULL,
  payload        TEXT NOT NULL,   -- JSON: vocab, idf, weights, category ids, config (doc 07 §5)
  metrics        TEXT NOT NULL DEFAULT '{}'  -- JSON: held-out accuracy, per-class counts
);
CREATE TABLE category_rules (        -- rules tier (doc 07 §3)
  pattern     TEXT PRIMARY KEY,      -- normalised description
  category_id TEXT NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
  votes       INTEGER NOT NULL,      -- count of user-confirmed occurrences
  updated_at  TEXT NOT NULL
);
```

## 7. Imports

### 7.1 `import_batches`

```sql
CREATE TABLE import_batches (
  id              TEXT PRIMARY KEY,
  filename        TEXT NOT NULL,
  size_bytes      INTEGER NOT NULL,
  sha256          TEXT NOT NULL,
  mime            TEXT NOT NULL,
  account_id      TEXT REFERENCES accounts(id),   -- chosen/duuggested target account
  schema_id       TEXT REFERENCES import_schemas(id),
  status          TEXT NOT NULL CHECK (status IN
                    ('detecting','awaiting_schema','awaiting_review','confirmed','failed','cancelled')),
  detection       TEXT,          -- JSON: detector output (doc 06 §5)
  parse_error     TEXT,
  created_at      TEXT NOT NULL,
  confirmed_at    TEXT
);
```

### 7.2 `import_staging` — rows awaiting review

```sql
CREATE TABLE import_staging (
  id                  TEXT PRIMARY KEY,
  batch_id            TEXT NOT NULL REFERENCES import_batches(id) ON DELETE CASCADE,
  line_no             INTEGER NOT NULL,           -- original position in the file
  date                TEXT NOT NULL,
  description         TEXT NOT NULL,
  amount_minor        INTEGER NOT NULL,
  currency            TEXT NOT NULL,
  source_text         TEXT NOT NULL DEFAULT '',
  destination_text    TEXT NOT NULL DEFAULT '',
  proposed_category_id TEXT REFERENCES categories(id),
  category_source     TEXT NOT NULL DEFAULT 'none' CHECK (category_source IN ('user','local','ai','none')),
  category_confidence REAL,
  needs_review        INTEGER NOT NULL DEFAULT 0, -- 1 => shown as "needs review" in the UI
  duplicate_of        TEXT REFERENCES transactions(id),  -- dedupe hit
  raw                 TEXT NOT NULL DEFAULT '{}',        -- original row JSON
  created_at          TEXT NOT NULL
);
CREATE INDEX idx_staging_batch ON import_staging(batch_id, line_no);
```

### 7.3 `import_schemas` [F2]

```sql
CREATE TABLE import_schemas (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,             -- AI-proposed, user-overridable
  bank_hint    TEXT NOT NULL DEFAULT '',
  kind         TEXT NOT NULL CHECK (kind IN ('csv','xlsx','ofx','qif','pdf')),
  mapping      TEXT NOT NULL,             -- JSON column mapping (doc 06 §5.2)
  columns      TEXT NOT NULL,             -- JSON: ordered source column names
  sample_hash  TEXT NOT NULL,             -- fingerprint: normalised header/kind hash
  confidence   REAL NOT NULL DEFAULT 0,
  use_count    INTEGER NOT NULL DEFAULT 0,
  last_used_at TEXT,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
```

## 8. Widgets, chat, audit

### 8.1 `pinned_widgets` [F9]

```sql
CREATE TABLE pinned_widgets (
  id          TEXT PRIMARY KEY,
  position    INTEGER NOT NULL DEFAULT 0,
  col         INTEGER NOT NULL DEFAULT 8,   -- grid span (2..12)
  rows        INTEGER NOT NULL DEFAULT 4,   -- grid rows (1..8)
  title       TEXT NOT NULL,
  type        TEXT NOT NULL,                -- bar|line|pie|table|text|number|transactions|calculator
  dataset     TEXT NOT NULL,               -- JSON canonical dataset (doc 05 §3)
  plan        TEXT,                        -- JSON query plan; NULL for static widgets
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
```

On dashboard render the backend re-executes `plan` (if present) and derives
`data` for the widget's current `type` from the fresh dataset [D13].

### 8.2 `conversations` / `messages` [F7]

```sql
CREATE TABLE conversations (
  id         TEXT PRIMARY KEY,
  title      TEXT NOT NULL DEFAULT 'New conversation',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE messages (
  id              TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  role            TEXT NOT NULL CHECK (role IN ('user','assistant')),
  text            TEXT NOT NULL DEFAULT '',
  steps           TEXT NOT NULL DEFAULT '[]',  -- JSON array of reasoning-step strings
  widget          TEXT,                        -- JSON widget (with dataset) as rendered
  dataset         TEXT,                        -- canonical dataset snapshot
  plan            TEXT,                        -- plan used (assistant messages)
  created_at      TEXT NOT NULL
);
CREATE INDEX idx_messages_conversation ON messages(conversation_id, created_at);
```

### 8.3 `audit_entries` + revert bookkeeping [F13]

Append-only. Entries are **never mutated** [Backend §14].

```sql
CREATE TABLE audit_entries (
  id          INTEGER PRIMARY KEY,           -- monotonic seq (AUTOINCREMENT)
  timestamp   TEXT NOT NULL,                 -- RFC3339 UTC
  actor       TEXT NOT NULL CHECK (actor IN ('user','ai','system')),
  actor_detail TEXT NOT NULL DEFAULT '',     -- e.g. model name for AI, "import:Monzo CSV" for system
  action      TEXT NOT NULL,                 -- catalogue in doc 11 §3
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  title       TEXT NOT NULL,                 -- short human summary ("Edited schema “Monzo CSV”")
  sub         TEXT NOT NULL DEFAULT '',      -- detail line ("Renamed column and saved for reuse")
  diff        TEXT NOT NULL DEFAULT '[]',    -- JSON [{line, cls: diff-add|diff-del}] — UI-renderable rich diff
  before      TEXT NOT NULL DEFAULT '{}',    -- JSON entity snapshot before
  after       TEXT NOT NULL DEFAULT '{}',    -- JSON entity snapshot after
  revert      TEXT NOT NULL DEFAULT '{}',    -- JSON revert payload (doc 11 §4)
  path_id     TEXT NOT NULL REFERENCES audit_paths(id),  -- timeline path this entry belongs to (doc 11 §4.1)
  state       TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','inactive'))
                -- engine-managed (doc 11 §4): content above is immutable; only the
                -- audit engine transitions state, atomically, mirrored by meta entries
);
CREATE INDEX idx_audit_time ON audit_entries(id DESC);
CREATE INDEX idx_audit_action ON audit_entries(action);
CREATE INDEX idx_audit_entity ON audit_entries(entity_type, entity_id);
CREATE INDEX idx_audit_state ON audit_entries(state, id);

CREATE TABLE audit_paths (                  -- branchable timeline (doc 11 §4.1) [D25]
  id             TEXT PRIMARY KEY,
  parent_path_id TEXT REFERENCES audit_paths(id),   -- NULL for the root path
  fork_entry_id  INTEGER,                   -- entry after which this path forked (NULL for root)
  label          TEXT NOT NULL DEFAULT '',  -- e.g. "Main", auto "Jump back · 2 Jun"
  is_active      INTEGER NOT NULL DEFAULT 0,-- exactly one active path
  created_at     TEXT NOT NULL
);

CREATE TABLE audit_reverts (                -- which entry reverted which
  entry_id        INTEGER PRIMARY KEY,      -- the revert action entry
  reverted_entry  INTEGER NOT NULL
);
```

## 9. AI bookkeeping

```sql
CREATE TABLE ai_calls (
  id                INTEGER PRIMARY KEY,
  capability        TEXT NOT NULL,     -- schema_detection|categorisation|query_planning|merchant_extraction|schema_naming
  provider          TEXT NOT NULL,
  model             TEXT NOT NULL,
  prompt_tokens     INTEGER,
  completion_tokens INTEGER,
  latency_ms        INTEGER,
  ok                INTEGER NOT NULL,
  error             TEXT,
  created_at        TEXT NOT NULL
);
CREATE TABLE ai_provider_config (      -- single row per instance
  id             INTEGER PRIMARY KEY CHECK (id = 1),
  provider       TEXT NOT NULL DEFAULT '',   -- '' = AI disabled
  models         TEXT NOT NULL DEFAULT '{}', -- JSON per-capability model overrides
  base_url       TEXT NOT NULL DEFAULT '',
  key_cipher     TEXT NOT NULL DEFAULT '',   -- AES-GCM ciphertext of the API key (doc 14 §6)
  updated_at     TEXT NOT NULL
);
```

### 8.4 History-engine invariants

- Exactly one `audit_paths` row has `is_active = 1`.
- An entry's `state='active'` iff it belongs to the active chain (ancestors
  up to each fork point + the active path) — doc 11 §4.1.
- Engine operations (revert / jump-back / restore-path / forget / delete)
  update `state`, path flags and `deleted_at` **in one transaction** with
  their tombstone meta entry. Entry *content* is never rewritten.

## 10. Derived data & performance notes

- Balances, series, category aggregates, dashboard totals: **computed by
  indexed SQL on demand** (doc 11 §5). At the stated scale (≤100k txs) every
  query is single-digit milliseconds in SQLite.
- Escape hatch (only if profiling demands): a `daily_balances(account_id,
  date, balance_minor)` cache maintained transactionally. NOT built in v1 —
  record in this doc if ever added.
- `VACUUM`/`ANALYZE`: run `ANALYZE` after large imports.

## 11. Migration policy

- golang-migrate, embedded; `0001_init` creates everything above.
- Every schema change gets a new numbered pair `.up.sql`/`.down.sql`.
- Migrations run automatically on boot (before serving). A failed migration
  aborts startup with a clear log.
- Seed data inserted in `0002_seed` (system categories, default settings
  rows, palette constant embedded in code not SQL).

## 12. Acceptance criteria

1. The DDL above exists as migration `0001_init` (plus seeds) and applies
   cleanly on SQLite and Postgres.
2. All invariants marked normative are enforced either by CHECK/FK constraints
   or by service-layer assertions with tests.
3. Balance replay formula is implemented in one function and unit-tested
   against hand-computed ledgers (including multi-correction histories).
4. Masking: no list endpoint returns full account numbers/IBANs.