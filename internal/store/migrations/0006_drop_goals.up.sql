-- Goals are gone from the app: a savings target is a progress widget or a
-- target line now. Their tables, and the triggers that counted their
-- changes towards ledger_version, go with them. Past goal entries stay in
-- the audit log as a record.
DROP TRIGGER IF EXISTS ledger_goal_ins;
DROP TRIGGER IF EXISTS ledger_goal_upd;
DROP TRIGGER IF EXISTS ledger_goal_del;
DROP TRIGGER IF EXISTS ledger_ga_ins;
DROP TRIGGER IF EXISTS ledger_ga_del;
DROP TABLE IF EXISTS goal_accounts;
DROP TABLE IF EXISTS goals;
