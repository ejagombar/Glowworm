package auditexec_test

import (
	"database/sql"
	"strings"
	"testing"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/transactions"
)

// in is the account a transaction is in.
func (l *ledger) in(id string) string {
	l.t.Helper()
	var account string
	if err := l.db.QueryRow(`SELECT account_id FROM transactions WHERE id = ?`, id).Scan(&account); err != nil {
		l.t.Fatal(err)
	}
	return account
}

// other adds a third account to move things to.
func (l *ledger) other() {
	l.t.Helper()
	if _, err := l.db.Exec(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at)
		VALUES ('oth','Other','debit','#000','GBP','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		l.t.Fatal(err)
	}
}

func (l *ledger) move(id, account string) int64 {
	l.t.Helper()
	if _, err := l.txs.Edit(id, transactions.CreateInput{AccountID: account}, ""); err != nil {
		l.t.Fatalf("move %s: %v", id, err)
	}
	return l.last()
}

func TestAMoveIsNotUndoneFromUnderATransfer(t *testing.T) {
	l := newLedger(t)
	l.other()
	moved := l.move("in2", "oth")

	// Moved back and forth, it follows.
	l.undo(moved)
	if got := l.in("in2"); got != "sav" {
		t.Fatalf("after undo it is in %s", got)
	}
	l.redo(moved)

	// Linked as a transfer, it stays where the transfer has it: the app
	// won't move a linked transaction either.
	l.link("out", "in2")
	notes := l.undo(moved)
	if got := l.in("in2"); got != "oth" {
		t.Fatalf("a linked transaction was moved to %s", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "account") {
		t.Fatalf("notes = %q, want the kept account mentioned", notes)
	}
	l.sound("undo of a move")
}

func TestAMoveIsNotUndoneIntoADeletedAccount(t *testing.T) {
	l := newLedger(t)
	l.other()
	moved := l.move("in", "oth")
	if _, err := l.accounts().Delete("sav"); err != nil {
		t.Fatal(err)
	}
	notes := l.undo(moved)
	if got := l.in("in"); got != "oth" {
		t.Fatalf("the transaction was moved to %s, an account that is gone", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "account") {
		t.Fatalf("notes = %q, want the kept account mentioned", notes)
	}
}

func TestMovingAnImportBackLeavesWhatWasLinkedSince(t *testing.T) {
	l := newLedger(t)
	l.other()
	for _, q := range []string{
		`INSERT INTO import_batches (id, filename, size_bytes, sha256, mime, account_id, status, created_at)
			VALUES ('b1', 'june.csv', 10, 'abc', 'csv', 'oth', 'confirmed', '2026-06-30T00:00:00Z')`,
		`UPDATE transactions SET import_batch_id = 'b1', account_id = 'oth' WHERE account_id = 'sav'`,
	} {
		if _, err := l.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var entry int64
	err := l.db.WithTx(func(tx *sql.Tx) error {
		var err error
		entry, err = l.writer.Write(tx, audit.Input{Actor: core.ActorUser, Action: "import.edited",
			EntityType: "import_batch", EntityID: "b1", Title: "Moved 2 imported transactions to Other",
			Before: map[string]any{"account_id": "sav"}, After: map[string]any{"account_id": "oth"},
			Revert: map[string]any{"batch_id": "b1", "transaction_ids": []string{"in", "in2"}}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	l.link("out", "in")

	notes := l.undo(entry)
	if got := l.in("in"); got != "oth" {
		t.Fatalf("a linked transaction was moved to %s", got)
	}
	if got := l.in("in2"); got != "sav" {
		t.Fatalf("the other one is in %s, want it moved back", got)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want the one left behind mentioned", notes)
	}
	l.sound("undo of an import move")
	l.redo(entry)
	if got := l.in("in2"); got != "oth" {
		t.Fatalf("after redo it is in %s", got)
	}

	// The account they came from is deleted: there is nowhere to move back to.
	if _, err := l.accounts().Delete("sav"); err != nil {
		t.Fatal(err)
	}
	l.refused("undo into a deleted account", entry, false)
	if got := l.in("in2"); got != "oth" {
		t.Fatalf("it was moved to %s, an account that is gone", got)
	}
	var account string
	if err := l.db.QueryRow(`SELECT account_id FROM import_batches WHERE id = 'b1'`).Scan(&account); err != nil || account != "oth" {
		t.Fatalf("the import is in %q (%v), want oth", account, err)
	}
}
