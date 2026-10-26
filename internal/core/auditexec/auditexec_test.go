package auditexec_test

import (
	"fmt"
	"strings"
	"testing"

	"glowworm/internal/core/audit"
	"glowworm/internal/core/auditexec"
	"glowworm/internal/core/transactions"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

// Undoing an older edit puts back only the fields it changed. A field that
// a later edit changed again keeps the later value, and the undo says so;
// redo works the same way.
func TestUndoOlderEditKeepsLaterChanges(t *testing.T) {
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at)
		VALUES ('a1','Main','debit','#000','GBP','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
		VALUES ('t1','a1','2026-05-20','Lidl',-699,'GBP','expense','h','2026-05-20T00:00:00Z','2026-05-20T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	txRepo := &repos.Transactions{DB: db}
	writer := &audit.Writer{DB: db}
	svc := &transactions.Service{DB: db, Repos: txRepo, Categories: &repos.Categories{DB: db}, Audit: writer}
	engine := audit.NewEngine(db, writer)
	x := &auditexec.Executor{DB: db, Txs: txRepo, Accounts: &repos.Accounts{DB: db}, Categories: &repos.Categories{DB: db}}
	last := func() int64 {
		var id int64
		if err := db.QueryRow(`SELECT MAX(id) FROM audit_entries`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	state := func() string {
		tx, err := txRepo.Get("t1")
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%s %s %d", tx.Date, tx.Description, tx.AmountMinor)
	}

	// Edit 1 changes the description and amount; edit 2 changes the amount again.
	if _, err := svc.Edit("t1", transactions.CreateInput{Description: "Lidl GB", AmountMinor: -700}, "user"); err != nil {
		t.Fatal(err)
	}
	edit1 := last()
	if _, err := svc.Edit("t1", transactions.CreateInput{AmountMinor: -1234}, "user"); err != nil {
		t.Fatal(err)
	}

	notes, err := engine.Undo(edit1, x.Apply)
	if err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "2026-05-20 Lidl -1234" {
		t.Errorf("after undoing edit 1: %s, want the description back and the later amount kept", got)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "kept the amount") {
		t.Errorf("notes = %q", notes)
	}

	notes, err = engine.Redo([]int64{edit1}, x.Apply)
	if err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "2026-05-20 Lidl GB -1234" {
		t.Errorf("after redoing edit 1: %s", got)
	}
	if len(notes) != 1 {
		t.Errorf("redo notes = %q", notes)
	}

	// Undoing newest first still restores everything.
	if _, err := engine.Undo(last(), x.Apply); err != nil {
		t.Fatal(err)
	}
	if notes, err := engine.Undo(edit1, x.Apply); err != nil || len(notes) != 0 {
		t.Fatalf("undo in order: notes %q, err %v", notes, err)
	}
	if got := state(); got != "2026-05-20 Lidl -699" {
		t.Errorf("after undoing both: %s", got)
	}
}
