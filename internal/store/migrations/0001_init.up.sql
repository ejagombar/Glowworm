-- Glowworm 0001_init — full schema per docs/design/02-data-model.md

-- 2.1 settings (key-value, JSON values)
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- 2.2 users (single owner in v1)
CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  display_name  TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

-- 3.1 accounts
CREATE TABLE accounts (
  id                   TEXT PRIMARY KEY,
  name                 TEXT NOT NULL,
  type                 TEXT NOT NULL CHECK (type IN ('debit','credit','savings')),
  color                TEXT NOT NULL,
  currency             TEXT NOT NULL,
  opening_balance_minor INTEGER NOT NULL DEFAULT 0,
  opening_balance_date TEXT,
  account_number       TEXT NOT NULL DEFAULT '',
  sort_code            TEXT NOT NULL DEFAULT '',
  iban                 TEXT NOT NULL DEFAULT '',
  bic                  TEXT NOT NULL DEFAULT '',
  notes                TEXT NOT NULL DEFAULT '',
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  deleted_at           TEXT
);

-- 3.2 valuations (point-in-time corrections)
CREATE TABLE valuations (
  id            TEXT PRIMARY KEY,
  account_id    TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  value_minor   INTEGER NOT NULL,
  date          TEXT NOT NULL,
  note          TEXT NOT NULL DEFAULT '',
  source        TEXT NOT NULL CHECK (source IN ('manual','import','ai')),
  drift_minor   INTEGER,
  audit_log_id  INTEGER,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  deleted_at    TEXT
);
CREATE INDEX idx_valuations_account_date ON valuations(account_id, date);

-- 4 categories
CREATE TABLE categories (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  color      TEXT NOT NULL,
  type       TEXT NOT NULL CHECK (type IN ('spend','income','internal')),
  is_system  INTEGER NOT NULL DEFAULT 0,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT,
  UNIQUE (name COLLATE NOCASE)
);

-- 5.1 transactions
CREATE TABLE transactions (
  id                  TEXT PRIMARY KEY,
  account_id          TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  date                TEXT NOT NULL,
  description         TEXT NOT NULL,
  merchant            TEXT,
  amount_minor        INTEGER NOT NULL,
  currency            TEXT NOT NULL,
  type                TEXT NOT NULL CHECK (type IN ('expense','income','transfer')),
  category_id         TEXT REFERENCES categories(id) ON DELETE SET NULL,
  category_source     TEXT NOT NULL DEFAULT 'none'
                        CHECK (category_source IN ('user','local','ai','none')),
  category_confidence REAL,
  source_text         TEXT NOT NULL DEFAULT '',
  destination_text    TEXT NOT NULL DEFAULT '',
  recurring_key       TEXT,
  transfer_id         TEXT,
  import_batch_id     TEXT,
  dedupe_hash         TEXT NOT NULL,
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  deleted_at          TEXT
);
CREATE INDEX idx_tx_account_date ON transactions(account_id, date DESC);
CREATE INDEX idx_tx_category ON transactions(category_id);
CREATE INDEX idx_tx_recurring ON transactions(recurring_key) WHERE recurring_key IS NOT NULL;
CREATE INDEX idx_tx_transfer ON transactions(transfer_id) WHERE transfer_id IS NOT NULL;
CREATE INDEX idx_tx_import ON transactions(import_batch_id) WHERE import_batch_id IS NOT NULL;
CREATE INDEX idx_tx_dedupe ON transactions(account_id, dedupe_hash);

-- 5.2 transfers (linked legs)
CREATE TABLE transfers (
  id                       TEXT PRIMARY KEY,
  source_transaction_id      TEXT NOT NULL UNIQUE REFERENCES transactions(id) ON DELETE CASCADE,
  destination_transaction_id TEXT NOT NULL UNIQUE REFERENCES transactions(id) ON DELETE CASCADE,
  detected                 TEXT NOT NULL CHECK (detected IN ('auto','manual')),
  method                   TEXT NOT NULL DEFAULT '',
  confidence               REAL,
  created_at               TEXT NOT NULL,
  deleted_at               TEXT
);
CREATE INDEX idx_transfers_dest ON transfers(destination_transaction_id);

-- 6.1 recurring_keys
CREATE TABLE recurring_keys (
  key            TEXT PRIMARY KEY,
  label          TEXT NOT NULL,
  cadence        TEXT NOT NULL,
  interval_days  INTEGER,
  amount_minor   INTEGER,
  direction      TEXT NOT NULL CHECK (direction IN ('in','out')),
  first_seen     TEXT NOT NULL,
  last_seen      TEXT NOT NULL,
  next_expected  TEXT,
  confidence     REAL NOT NULL,
  active         INTEGER NOT NULL DEFAULT 1,
  updated_at     TEXT NOT NULL
);

-- 6.2 classifier model + rules tier
CREATE TABLE classifier_models (
  id             INTEGER PRIMARY KEY,
  version        INTEGER NOT NULL,
  trained_at     TEXT NOT NULL,
  training_count INTEGER NOT NULL,
  payload        TEXT NOT NULL,
  metrics        TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE category_rules (
  pattern     TEXT PRIMARY KEY,
  category_id TEXT NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
  votes       INTEGER NOT NULL,
  updated_at  TEXT NOT NULL
);

-- 7.1 import_batches
CREATE TABLE import_batches (
  id              TEXT PRIMARY KEY,
  filename        TEXT NOT NULL,
  size_bytes      INTEGER NOT NULL,
  sha256          TEXT NOT NULL,
  mime            TEXT NOT NULL,
  account_id      TEXT REFERENCES accounts(id),
  schema_id       TEXT REFERENCES import_schemas(id),
  status          TEXT NOT NULL CHECK (status IN
                    ('detecting','awaiting_schema','awaiting_review','confirmed','failed','cancelled')),
  detection       TEXT,
  parse_error     TEXT,
  created_at      TEXT NOT NULL,
  confirmed_at    TEXT
);

-- 7.2 import_staging
CREATE TABLE import_staging (
  id                  TEXT PRIMARY KEY,
  batch_id            TEXT NOT NULL REFERENCES import_batches(id) ON DELETE CASCADE,
  line_no             INTEGER NOT NULL,
  date                TEXT NOT NULL,
  description         TEXT NOT NULL,
  amount_minor        INTEGER NOT NULL,
  currency            TEXT NOT NULL,
  source_text         TEXT NOT NULL DEFAULT '',
  destination_text    TEXT NOT NULL DEFAULT '',
  proposed_category_id TEXT REFERENCES categories(id),
  category_source     TEXT NOT NULL DEFAULT 'none' CHECK (category_source IN ('user','local','ai','none')),
  category_confidence REAL,
  needs_review        INTEGER NOT NULL DEFAULT 0,
  duplicate_of        TEXT REFERENCES transactions(id),
  raw                 TEXT NOT NULL DEFAULT '{}',
  created_at          TEXT NOT NULL
);
CREATE INDEX idx_staging_batch ON import_staging(batch_id, line_no);

-- 7.3 import_schemas
CREATE TABLE import_schemas (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  bank_hint    TEXT NOT NULL DEFAULT '',
  kind         TEXT NOT NULL CHECK (kind IN ('csv','xlsx','ofx','qif','pdf')),
  mapping      TEXT NOT NULL,
  columns      TEXT NOT NULL,
  sample_hash  TEXT NOT NULL,
  confidence   REAL NOT NULL DEFAULT 0,
  use_count    INTEGER NOT NULL DEFAULT 0,
  last_used_at TEXT,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

-- 8.1 pinned_widgets
CREATE TABLE pinned_widgets (
  id          TEXT PRIMARY KEY,
  position    INTEGER NOT NULL DEFAULT 0,
  col         INTEGER NOT NULL DEFAULT 8,
  rows        INTEGER NOT NULL DEFAULT 4,
  title       TEXT NOT NULL,
  type        TEXT NOT NULL,
  dataset     TEXT NOT NULL,
  plan        TEXT,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

-- 8.2 conversations / messages
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
  steps           TEXT NOT NULL DEFAULT '[]',
  widget          TEXT,
  dataset         TEXT,
  plan            TEXT,
  created_at      TEXT NOT NULL
);
CREATE INDEX idx_messages_conversation ON messages(conversation_id, created_at);

-- 8.3 audit: branchable timeline (doc 11 §4)
CREATE TABLE audit_paths (
  id             TEXT PRIMARY KEY,
  parent_path_id TEXT REFERENCES audit_paths(id),
  fork_entry_id  INTEGER,
  label          TEXT NOT NULL DEFAULT '',
  is_active      INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL
);

CREATE TABLE audit_entries (
  id          INTEGER PRIMARY KEY,
  timestamp   TEXT NOT NULL,
  actor       TEXT NOT NULL CHECK (actor IN ('user','ai','system')),
  actor_detail TEXT NOT NULL DEFAULT '',
  action      TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  title       TEXT NOT NULL,
  sub         TEXT NOT NULL DEFAULT '',
  diff        TEXT NOT NULL DEFAULT '[]',
  before      TEXT NOT NULL DEFAULT '{}',
  after       TEXT NOT NULL DEFAULT '{}',
  revert      TEXT NOT NULL DEFAULT '{}',
  path_id     TEXT NOT NULL REFERENCES audit_paths(id),
  state       TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','inactive'))
);
CREATE INDEX idx_audit_time ON audit_entries(id DESC);
CREATE INDEX idx_audit_action ON audit_entries(action);
CREATE INDEX idx_audit_entity ON audit_entries(entity_type, entity_id);
CREATE INDEX idx_audit_state ON audit_entries(state, id);

CREATE TABLE audit_reverts (
  entry_id        INTEGER PRIMARY KEY,
  reverted_entry  INTEGER NOT NULL
);

-- 9 AI bookkeeping
CREATE TABLE ai_calls (
  id                INTEGER PRIMARY KEY,
  capability        TEXT NOT NULL,
  provider          TEXT NOT NULL,
  model             TEXT NOT NULL,
  prompt_tokens     INTEGER,
  completion_tokens INTEGER,
  latency_ms        INTEGER,
  ok                INTEGER NOT NULL,
  error             TEXT,
  created_at        TEXT NOT NULL
);

CREATE TABLE ai_provider_config (
  id             INTEGER PRIMARY KEY CHECK (id = 1),
  provider       TEXT NOT NULL DEFAULT '',
  models         TEXT NOT NULL DEFAULT '{}',
  base_url       TEXT NOT NULL DEFAULT '',
  key_cipher     TEXT NOT NULL DEFAULT '',
  updated_at     TEXT NOT NULL
);
