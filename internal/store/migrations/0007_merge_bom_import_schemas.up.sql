-- A CSV starting with a byte-order mark saved its first column name with an
-- invisible U+FEFF in front, so it didn't match the schema saved from the
-- same bank's export without one, and a duplicate was created. Imports now
-- drop the mark; this folds each such duplicate into its twin.
CREATE TEMP TABLE bom_twins AS
SELECT d.id AS dup_id, k.id AS keep_id
FROM import_schemas d
JOIN import_schemas k
  ON k.id <> d.id AND k.kind = d.kind AND k.columns = substr(d.columns, 2)
WHERE substr(d.columns, 1, 1) = char(65279);

UPDATE import_schemas
SET use_count = use_count + (SELECT COALESCE(SUM(d.use_count), 0) FROM import_schemas d
      JOIN bom_twins b ON b.dup_id = d.id WHERE b.keep_id = import_schemas.id),
    last_used_at = (SELECT MAX(x) FROM (
      SELECT import_schemas.last_used_at AS x
      UNION ALL SELECT d.last_used_at FROM import_schemas d
        JOIN bom_twins b ON b.dup_id = d.id WHERE b.keep_id = import_schemas.id)),
    account_id = COALESCE(account_id, (SELECT d.account_id FROM import_schemas d
      JOIN bom_twins b ON b.dup_id = d.id WHERE b.keep_id = import_schemas.id LIMIT 1))
WHERE id IN (SELECT keep_id FROM bom_twins);

UPDATE import_batches SET schema_id = (SELECT keep_id FROM bom_twins WHERE dup_id = schema_id)
WHERE schema_id IN (SELECT dup_id FROM bom_twins);

DELETE FROM import_schemas WHERE id IN (SELECT dup_id FROM bom_twins);

DROP TABLE bom_twins;
