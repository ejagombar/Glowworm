-- ledger_version counts changes to everything balances over time are
-- worked out from, so the app can keep what it worked out until the count
-- moves. Triggers bump it, so every writer counts: requests, imports,
-- background jobs and undo alike.
CREATE TABLE ledger_version (
  id  INTEGER PRIMARY KEY CHECK (id = 1),
  n   INTEGER NOT NULL
);
INSERT INTO ledger_version (id, n) VALUES (1, 0);

CREATE TRIGGER ledger_tx_ins AFTER INSERT ON transactions BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_tx_upd AFTER UPDATE ON transactions BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_tx_del AFTER DELETE ON transactions BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_val_ins AFTER INSERT ON valuations BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_val_upd AFTER UPDATE ON valuations BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_val_del AFTER DELETE ON valuations BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_acc_ins AFTER INSERT ON accounts BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_acc_upd AFTER UPDATE ON accounts BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_acc_del AFTER DELETE ON accounts BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_goal_ins AFTER INSERT ON goals BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_goal_upd AFTER UPDATE ON goals BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_goal_del AFTER DELETE ON goals BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_ga_ins AFTER INSERT ON goal_accounts BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_ga_del AFTER DELETE ON goal_accounts BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_set_ins AFTER INSERT ON settings WHEN NEW.key = 'reminder_default_days'
  BEGIN UPDATE ledger_version SET n = n + 1; END;
CREATE TRIGGER ledger_set_upd AFTER UPDATE ON settings WHEN NEW.key = 'reminder_default_days'
  BEGIN UPDATE ledger_version SET n = n + 1; END;

-- The per-day sums balances over time are worked out from also split by
-- type; with it in the index they're read from the index alone. The leading
-- columns are unchanged, so other queries plan as before.
DROP INDEX idx_tx_account_date;
CREATE INDEX idx_tx_account_date ON transactions(account_id, date DESC, deleted_at, amount_minor, type);
