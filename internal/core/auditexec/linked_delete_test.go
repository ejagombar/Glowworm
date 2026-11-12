package auditexec_test

import (
	"database/sql"
	"strings"
	"testing"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/transactions"
)

// Deleting one side of a transfer leaves the other a transfer waiting for
// its other side, and undoing the delete puts the pair back together.
func TestDeletingALinkedTransaction(t *testing.T) {
	l := newLedger(t)
	id := l.link("out", "in")
	linked := "transfer/" + internal + "/" + id
	if err := l.txs.Delete("out"); err != nil {
		t.Fatal(err)
	}
	entry := l.last()
	l.sound("delete")
	l.want("delete", "in", "transfer/"+internal+"/")

	for round := 1; round <= 2; round++ {
		l.undo(entry)
		l.sound("undo")
		l.want("undo", "out", linked)
		l.want("undo", "in", linked)

		l.redo(entry)
		l.sound("redo")
		l.want("redo", "out", "deleted")
		l.want("redo", "in", "transfer/"+internal+"/")
	}

	// The other side has been linked elsewhere since: the transaction comes
	// back on its own, and the undo says so.
	if _, err := l.db.Exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
		VALUES ('out2','cur','2026-05-20','To savings',-5000,'GBP','expense','h4','2026-05-20T00:00:00Z','2026-05-20T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	other := l.link("out2", "in")
	notes := l.undo(entry)
	l.sound("undo with the other side taken")
	l.want("undo with the other side taken", "out", "transfer/"+internal+"/")
	l.want("undo with the other side taken", "in", "transfer/"+internal+"/"+other)
	if len(notes) != 1 || !strings.Contains(notes[0], "transfer") {
		t.Fatalf("notes = %q", notes)
	}
}

func TestUndoingATransactionThatWasLinkedLater(t *testing.T) {
	l := newLedger(t)
	made, err := l.txs.Create(transactions.CreateInput{AccountID: "cur", Date: "2026-05-22", Description: "To savings", AmountMinor: -5000})
	if err != nil {
		t.Fatal(err)
	}
	entry := l.last()
	id := l.link(made.ID, "in")
	linked := "transfer/" + internal + "/" + id

	l.undo(entry)
	l.sound("undo")
	l.want("undo", made.ID, "deleted")
	l.want("undo", "in", "transfer/"+internal+"/")

	l.redo(entry)
	l.sound("redo")
	l.want("redo", made.ID, linked)
	l.want("redo", "in", linked)
}

func TestUndoingAnImportWhoseRowsWereLinkedLater(t *testing.T) {
	l := newLedger(t)
	if _, err := l.db.Exec(`UPDATE transactions SET import_batch_id = 'b1' WHERE account_id = 'sav'`); err != nil {
		t.Fatal(err)
	}
	var entry int64
	err := l.db.WithTx(func(tx *sql.Tx) error {
		var err error
		entry, err = l.writer.Write(tx, audit.Input{Actor: core.ActorUser, Action: "import.confirmed",
			EntityType: "import_batch", EntityID: "b1", Title: "Imported 2 transactions",
			Revert: map[string]any{"batch_id": "b1"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	id := l.link("out", "in")
	linked := "transfer/" + internal + "/" + id

	l.undo(entry)
	l.sound("undo")
	l.want("undo", "in", "deleted")
	l.want("undo", "in2", "deleted")
	l.want("undo", "out", "transfer/"+internal+"/")

	l.redo(entry)
	l.sound("redo")
	l.want("redo", "in", linked)
	l.want("redo", "out", linked)
	l.want("redo", "in2", "income//")
}

func TestTransactionAlreadyDeletedIsNotUndoneTwice(t *testing.T) {
	l := newLedger(t)
	made, err := l.txs.Create(transactions.CreateInput{AccountID: "cur", Date: "2026-05-22", Description: "Coffee", AmountMinor: -300})
	if err != nil {
		t.Fatal(err)
	}
	created := l.last()
	if err := l.txs.Delete(made.ID); err != nil {
		t.Fatal(err)
	}
	// Undoing the add would mark it undone while the delete still stands, and
	// redoing it later would bring back a transaction the user deleted.
	if _, err := l.engine.Undo(created, l.x.Apply); err == nil {
		t.Fatal("undid adding a transaction that is already deleted")
	}
}
