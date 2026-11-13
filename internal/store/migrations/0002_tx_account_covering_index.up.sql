-- Balances, per-account counts and the per-day sums behind the charts all read
-- (account, date, deleted, amount) and nothing else. With those four columns
-- in the index SQLite answers them from the index alone instead of visiting
-- every transaction row.
DROP INDEX idx_tx_account_date;
CREATE INDEX idx_tx_account_date ON transactions(account_id, date DESC, deleted_at, amount_minor);
