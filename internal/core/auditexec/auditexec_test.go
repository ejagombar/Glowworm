package auditexec_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ejagombar/Glowworm/internal/core/accounts"
	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/core/auditexec"
	"github.com/ejagombar/Glowworm/internal/core/transactions"
	"github.com/ejagombar/Glowworm/internal/store"
	"github.com/ejagombar/Glowworm/internal/store/repos"
)

// Undoing an older edit puts back only the fields it changed. A field that
// a later edit changed again keeps the later value, and the undo says so;
// redo works the same way.
func TestUndoOlderEditKeepsLaterChanges(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
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

// A dated account value is undone and redone by its own id, so two values
// that look the same don't get mixed up.
func TestUndoValuation(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at)
		VALUES ('a1','Savings','savings','#000','GBP','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	accRepo := &repos.Accounts{DB: db}
	writer := &audit.Writer{DB: db}
	svc := &accounts.Service{DB: db, Repos: accRepo, Txs: &repos.Transactions{DB: db}, Audit: writer}
	engine := audit.NewEngine(db, writer)
	x := &auditexec.Executor{DB: db, Txs: &repos.Transactions{DB: db}, Accounts: accRepo, Categories: &repos.Categories{DB: db}}

	in := accounts.ValuationInput{ValueMinor: 25000000, Date: "2026-06-01"}
	first, err := svc.CreateValuation("a1", in, "manual")
	if err != nil {
		t.Fatal(err)
	}
	var entry int64
	if err := db.QueryRow(`SELECT id FROM audit_entries WHERE action = 'valuation.created' AND entity_id = ?`,
		first.Valuation.ID).Scan(&entry); err != nil {
		t.Fatalf("no entry for the first value: %v", err)
	}
	second, err := svc.CreateValuation("a1", in, "manual")
	if err != nil {
		t.Fatal(err)
	}
	live := func() string {
		vs, err := accRepo.Valuations("a1")
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(vs))
		for i, v := range vs {
			ids[i] = v.ID
		}
		return strings.Join(ids, ",")
	}

	if _, err := engine.Undo(entry, x.Apply); err != nil {
		t.Fatal(err)
	}
	if got := live(); got != second.Valuation.ID {
		t.Errorf("after undoing the first value: %s, want only the second (%s)", got, second.Valuation.ID)
	}
	if _, err := engine.Redo([]int64{entry}, x.Apply); err != nil {
		t.Fatal(err)
	}
	if got := live(); got != first.Valuation.ID+","+second.Valuation.ID {
		t.Errorf("after redo: %s, want both values back", got)
	}
}
