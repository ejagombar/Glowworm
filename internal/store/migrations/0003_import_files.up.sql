-- Retain original upload bytes for the fix-mapping stage (doc 06 §5.1).
CREATE TABLE import_files (
  batch_id TEXT PRIMARY KEY REFERENCES import_batches(id) ON DELETE CASCADE,
  content  BLOB NOT NULL
);
