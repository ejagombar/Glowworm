-- What was actually paid when a transaction wasn't in the account's
-- currency (e.g. €10 on holiday), alongside the converted amount_minor.
ALTER TABLE transactions ADD COLUMN original_amount_minor INTEGER;
ALTER TABLE transactions ADD COLUMN original_currency TEXT;
ALTER TABLE import_staging ADD COLUMN original_amount_minor INTEGER;
ALTER TABLE import_staging ADD COLUMN original_currency TEXT;

-- Daily exchange rates fetched for imports: 1 base = rate quote.
CREATE TABLE fx_rates (
  date  TEXT NOT NULL,
  base  TEXT NOT NULL,
  quote TEXT NOT NULL,
  rate  REAL NOT NULL,
  PRIMARY KEY (date, base, quote)
);
