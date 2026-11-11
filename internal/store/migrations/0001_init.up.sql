CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  display_name  TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

-- Passkeys (Face ID, Touch ID and the like) that unlock Glowworm on a
-- device that has its lock turned on. credential is the go-webauthn
-- Credential as JSON: the public key, sign count and flags.
CREATE TABLE passkeys (
  id             TEXT PRIMARY KEY,
  user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id  TEXT NOT NULL UNIQUE,
  credential     TEXT NOT NULL,
  name           TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  last_used_at   TEXT
);
CREATE INDEX passkeys_user ON passkeys(user_id);

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

CREATE TABLE valuations (
  id            TEXT PRIMARY KEY,
  account_id    TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  value_minor   INTEGER NOT NULL,
  date          TEXT NOT NULL,
  note          TEXT NOT NULL DEFAULT '',
  source        TEXT NOT NULL CHECK (source IN ('manual','import','ai')),
  drift_minor   INTEGER,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  deleted_at    TEXT
);
CREATE INDEX idx_valuations_account_date ON valuations(account_id, date);

CREATE TABLE categories (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  -- What the user means by the category, in their own words. Only the
  -- categorisation AI reads it; the app shows it nowhere but the edit form.
  description TEXT NOT NULL DEFAULT '',
  color      TEXT NOT NULL,
  type       TEXT NOT NULL CHECK (type IN ('spend','income','internal')),
  is_system  INTEGER NOT NULL DEFAULT 0,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT,
  UNIQUE (name COLLATE NOCASE)
);

CREATE TABLE transactions (
  id                  TEXT PRIMARY KEY,
  account_id          TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  date                TEXT NOT NULL,
  description         TEXT NOT NULL,
  merchant            TEXT,
  amount_minor        INTEGER NOT NULL,
  currency            TEXT NOT NULL,
  -- What was actually paid when the transaction wasn't in the account's
  -- currency (e.g. €10 on holiday), alongside the converted amount_minor.
  original_amount_minor INTEGER,
  original_currency   TEXT,
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

CREATE TABLE import_staging (
  id                  TEXT PRIMARY KEY,
  batch_id            TEXT NOT NULL REFERENCES import_batches(id) ON DELETE CASCADE,
  line_no             INTEGER NOT NULL,
  date                TEXT NOT NULL,
  description         TEXT NOT NULL,
  amount_minor        INTEGER NOT NULL,
  currency            TEXT NOT NULL,
  original_amount_minor INTEGER,
  original_currency   TEXT,
  source_text         TEXT NOT NULL DEFAULT '',
  destination_text    TEXT NOT NULL DEFAULT '',
  proposed_category_id TEXT REFERENCES categories(id),
  category_source     TEXT NOT NULL DEFAULT 'none' CHECK (category_source IN ('user','local','ai','none')),
  category_confidence REAL,
  suggested_category_id TEXT REFERENCES categories(id),
  suggested_confidence  REAL,
  needs_review        INTEGER NOT NULL DEFAULT 0,
  duplicate_of        TEXT REFERENCES transactions(id),
  raw                 TEXT NOT NULL DEFAULT '{}',
  created_at          TEXT NOT NULL
);
CREATE INDEX idx_staging_batch ON import_staging(batch_id, line_no);

CREATE TABLE import_schemas (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  bank_hint    TEXT NOT NULL DEFAULT '',
  account_id   TEXT REFERENCES accounts(id) ON DELETE SET NULL,
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

-- Daily exchange rates fetched for imports: 1 base = rate quote.
CREATE TABLE fx_rates (
  date  TEXT NOT NULL,
  base  TEXT NOT NULL,
  quote TEXT NOT NULL,
  rate  REAL NOT NULL,
  PRIMARY KEY (date, base, quote)
);

-- Grouping rules: transactions whose description is, or contains, some text
-- belong to a named group. A group is only the name its rules share, and
-- transactions.merchant holds it; nothing else sets merchant.
CREATE TABLE group_rules (
  id          TEXT PRIMARY KEY,
  group_name  TEXT NOT NULL,
  match       TEXT NOT NULL CHECK (match IN ('is', 'contains')),
  text        TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  deleted_at  TEXT
);

CREATE TABLE pinned_widgets (
  id          TEXT PRIMARY KEY,
  position    INTEGER NOT NULL DEFAULT 0,
  col         INTEGER NOT NULL DEFAULT 8,
  rows        INTEGER NOT NULL DEFAULT 4,
  title       TEXT NOT NULL,
  type        TEXT NOT NULL,
  data        TEXT NOT NULL,
  plan        TEXT,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

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
  created_at      TEXT NOT NULL
);
CREATE INDEX idx_messages_conversation ON messages(conversation_id, created_at);

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
  state       TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active','inactive')),
  -- System changes made because of a user's change (transfer and recurring
  -- detection, merchant enrichment, classifier retraining after an import)
  -- point at it, so undoing or redoing the user's change takes them along.
  caused_by   INTEGER
);
CREATE INDEX idx_audit_time ON audit_entries(id DESC);
CREATE INDEX idx_audit_action ON audit_entries(action);
CREATE INDEX idx_audit_entity ON audit_entries(entity_type, entity_id);
CREATE INDEX idx_audit_state ON audit_entries(state, id);
CREATE INDEX idx_audit_caused_by ON audit_entries(caused_by);

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

CREATE TABLE import_files (
  batch_id TEXT PRIMARY KEY REFERENCES import_batches(id) ON DELETE CASCADE,
  content  BLOB NOT NULL
);

CREATE TABLE chat_day_summaries (
  day           TEXT PRIMARY KEY,
  summary       TEXT NOT NULL,
  message_count INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE TABLE account_archives (
  id          TEXT PRIMARY KEY,
  account_id  TEXT NOT NULL,
  name        TEXT NOT NULL,
  tx_count    INTEGER NOT NULL,
  deleted_at  TEXT NOT NULL,
  payload     TEXT NOT NULL
);
CREATE INDEX idx_account_archives_deleted ON account_archives(deleted_at DESC);

INSERT INTO categories (id, name, color, type, is_system, sort_order, created_at, updated_at) VALUES
  ('c-system-income',    'Income',            '#0e7a4d', 'income',   1, 0, '1970-01-01T00:00:00Z', '1970-01-01T00:00:00Z'),
  ('c-system-internal',  'Internal transfer', '#2e9d8f', 'internal', 1, 0, '1970-01-01T00:00:00Z', '1970-01-01T00:00:00Z');

INSERT INTO settings (key, value, updated_at) VALUES
  ('theme',                    '"system"',    '1970-01-01T00:00:00Z'),
  ('currency',                 '"GBP"',       '1970-01-01T00:00:00Z'),
  ('user_name',                '""',          '1970-01-01T00:00:00Z'),
  ('default_period',           '"last-12m"',  '1970-01-01T00:00:00Z'),
  ('privacy_default',          'false',       '1970-01-01T00:00:00Z'),
  ('setup_completed',          'false',       '1970-01-01T00:00:00Z'),
  ('cat_local_threshold',      '0.75',        '1970-01-01T00:00:00Z'),
  ('cat_ai_threshold',         '0.8',         '1970-01-01T00:00:00Z'),
  ('cat_min_examples',         '3',           '1970-01-01T00:00:00Z'),
  ('transfer_threshold',       '0.7',         '1970-01-01T00:00:00Z'),
  ('transfer_date_window_days','4',           '1970-01-01T00:00:00Z'),
  ('ai_daily_token_cap',       '0',           '1970-01-01T00:00:00Z');

INSERT INTO audit_paths (id, parent_path_id, fork_entry_id, label, is_active, created_at) VALUES
  ('main', NULL, NULL, 'Main', 1, '1970-01-01T00:00:00Z');

INSERT INTO ai_provider_config (id, provider, models, base_url, key_cipher, updated_at) VALUES
  (1, '', '{}', '', '', '1970-01-01T00:00:00Z');
