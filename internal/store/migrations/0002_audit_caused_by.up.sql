-- System changes made because of a user's change (transfer and recurring
-- detection, merchant enrichment, classifier retraining after an import)
-- point at it, so undoing or redoing the user's change takes them along.
ALTER TABLE audit_entries ADD COLUMN caused_by INTEGER;
CREATE INDEX idx_audit_caused_by ON audit_entries(caused_by);
