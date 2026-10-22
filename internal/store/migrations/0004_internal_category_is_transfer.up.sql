-- Rows put in an internal category on import kept an expense or income type,
-- so they counted as spend and income. A row in Internal transfer is a
-- transfer, linked to its other leg or still waiting for it.
UPDATE transactions SET type = 'transfer'
WHERE type != 'transfer' AND transfer_id IS NULL
  AND category_id IN (SELECT id FROM categories WHERE type = 'internal');
