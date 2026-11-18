-- Savings: an account's interest rate and tax wrapper, and how often its
-- balance should be brought up to date; goals; and the push subscriptions
-- that out-of-date balance reminders go to. All additive.

-- interest_rate is a yearly ratio (0.045 for 4.5%), NULL when not known.
-- tax_wrapper is '' (none), 'isa', 'lisa', 'pension' or 'other'.
-- balance_reminder_days: NULL follows the reminder settings' default (which
-- covers savings accounts only), 0 never reminds, N reminds once the latest
-- recorded balance is N days old.
ALTER TABLE accounts ADD COLUMN interest_rate REAL;
ALTER TABLE accounts ADD COLUMN tax_wrapper TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN balance_reminder_days INTEGER;

-- A goal is a target amount, by a date or open-ended, over the accounts in
-- goal_accounts; a goal with none counts every savings account.
CREATE TABLE goals (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  target_minor  INTEGER NOT NULL,
  target_date   TEXT,
  color         TEXT NOT NULL DEFAULT '#0e6f51',
  notes         TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  deleted_at    TEXT
);

CREATE TABLE goal_accounts (
  goal_id     TEXT NOT NULL REFERENCES goals(id) ON DELETE CASCADE,
  account_id  TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  PRIMARY KEY (goal_id, account_id)
);

-- One row per device that allowed notifications. The endpoint is the push
-- service's URL for that device; p256dh and auth are its keys.
CREATE TABLE push_subscriptions (
  id            TEXT PRIMARY KEY,
  endpoint      TEXT NOT NULL UNIQUE,
  p256dh        TEXT NOT NULL,
  auth          TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  origin        TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  last_sent_at  TEXT,
  last_error    TEXT NOT NULL DEFAULT ''
);

-- When each account was last reminded about, so a reminder isn't repeated
-- every day.
CREATE TABLE reminder_state (
  account_id    TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  last_sent_at  TEXT NOT NULL
);
