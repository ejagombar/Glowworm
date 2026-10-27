-- Grouping rules: transactions whose description is, or contains, some text
-- belong to a named group. A group is only the name its rules share, and
-- transactions.merchant holds it; nothing else sets merchant any more, so
-- the names the AI suggested on the first import are cleared.
CREATE TABLE group_rules (
  id          TEXT PRIMARY KEY,
  group_name  TEXT NOT NULL,
  match       TEXT NOT NULL CHECK (match IN ('is', 'contains')),
  text        TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  deleted_at  TEXT
);

UPDATE transactions SET merchant = NULL WHERE merchant IS NOT NULL;
