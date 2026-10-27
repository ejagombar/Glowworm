package changes_test

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"glowworm/internal/core/audit"
	"glowworm/internal/core/auditexec"
	"glowworm/internal/core/changes"
	"glowworm/internal/core/transactions"
	"glowworm/internal/core/transfers"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

type fixture struct {
	db     *store.DB
	svc    *changes.Service
	engine *audit.Engine
	x      *auditexec.Executor
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	const now = "2026-01-01T00:00:00Z"
	exec(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at) VALUES
		('cur','Current','debit','#000','GBP',?1,?1), ('sav','Savings','savings','#000','GBP',?1,?1)`, now)
	exec(`INSERT INTO categories (id, name, color, type, created_at, updated_at) VALUES
		('groc','Groceries','#000','spend',?1,?1), ('pay','Salary','#000','income',?1,?1)`, now)
	for i, tx := range []struct {
		acct, desc string
		amount     int64
		typ        string
	}{
		{"cur", "TESCO STORES", -1250, "expense"},
		{"cur", "TESCO EXPRESS", -480, "expense"},
		{"cur", "TESCO REFUND", 300, "income"},
		{"cur", "To savings", -10000, "expense"},
		{"sav", "From current", 10000, "income"},
	} {
		exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES (?,?,'2026-03-0'||?,?,?,'GBP',?,'h',?,?)`, fmt.Sprintf("t%d", i), tx.acct, i+1, tx.desc, tx.amount, tx.typ, now, now)
	}
	txRepo := &repos.Transactions{DB: db}
	writer := &audit.Writer{DB: db}
	txs := &transactions.Service{DB: db, Repos: txRepo, Categories: &repos.Categories{DB: db}, Audit: writer}
	trs := &transfers.Service{DB: db, Txs: txRepo, Accounts: &repos.Accounts{DB: db}, Audit: writer,
		Settings: &repos.Settings{DB: db}, InternalCat: txs.InternalCategoryID}
	return &fixture{
		db:     db,
		svc:    &changes.Service{DB: db, Transactions: txs, Transfers: trs, Audit: writer},
		engine: audit.NewEngine(db, writer),
		x: &auditexec.Executor{DB: db, Txs: txRepo, Accounts: &repos.Accounts{DB: db}, Categories: &repos.Categories{DB: db},
			InternalCat: func(*sql.Tx) (string, error) { return txs.InternalCategoryID() }},
	}
}

func (f *fixture) col(t *testing.T, q string) string {
	t.Helper()
	rows, err := f.db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := ""
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out += v + ","
	}
	return out
}

func (f *fixture) causeID(t *testing.T, p changes.Proposal) int64 {
	t.Helper()
	var id int64
	if err := f.db.QueryRow(`SELECT id FROM audit_entries WHERE action = 'change.applied' AND entity_id = ?`, p.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A category change previews what it skips, changes only the rest, and is
// undone and redone as one History entry.
func TestSetCategoryAppliesAndUndoesTogether(t *testing.T) {
	f := setup(t)
	pv, err := f.svc.Draft(changes.Proposal{Action: changes.SetCategory, CategoryID: "groc", IDs: []string{"t0", "t1", "t2"}})
	if err != nil {
		t.Fatal(err)
	}
	if pv.Ready != 2 || pv.Applied {
		t.Fatalf("preview = %+v", pv)
	}
	if after := f.col(t, `SELECT COALESCE(category_id,'-') FROM transactions ORDER BY id`); after != "-,-,-,-,-," {
		t.Fatalf("preview changed data: %s", after)
	}
	got, err := f.svc.Apply(pv.Proposal)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Applied {
		t.Error("not marked applied")
	}
	const cats = `SELECT COALESCE(category_id,'-') FROM transactions ORDER BY id`
	if c := f.col(t, cats); c != "groc,groc,-,-,-," {
		t.Errorf("after apply: %s (the income refund should be skipped)", c)
	}
	if _, err := f.svc.Apply(pv.Proposal); err == nil {
		t.Error("applied twice")
	}
	cause := f.causeID(t, pv.Proposal)
	if _, err := f.engine.Undo(cause, f.x.Apply); err != nil {
		t.Fatal(err)
	}
	if c := f.col(t, cats); c != "-,-,-,-,-," {
		t.Errorf("after undo: %s", c)
	}
	if again, _ := f.svc.Preview(pv.Proposal); again.Applied {
		t.Error("still marked applied after undo")
	}
	if _, err := f.engine.Redo([]int64{cause}, f.x.Apply); err != nil {
		t.Fatal(err)
	}
	if c := f.col(t, cats); c != "groc,groc,-,-,-," {
		t.Errorf("after redo: %s", c)
	}
}

func TestDeleteAndLinkTransfer(t *testing.T) {
	f := setup(t)
	pv, err := f.svc.Draft(changes.Proposal{Action: changes.Delete, IDs: []string{"t0", "t1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Apply(pv.Proposal); err != nil {
		t.Fatal(err)
	}
	const live = `SELECT id FROM transactions WHERE deleted_at IS NULL ORDER BY id`
	if c := f.col(t, live); c != "t2,t3,t4," {
		t.Errorf("after delete: %s", c)
	}
	if _, err := f.engine.Undo(f.causeID(t, pv.Proposal), f.x.Apply); err != nil {
		t.Fatal(err)
	}
	if c := f.col(t, live); c != "t0,t1,t2,t3,t4," {
		t.Errorf("after undoing delete: %s", c)
	}

	if _, err := f.svc.Draft(changes.Proposal{Action: changes.LinkTransfer, IDs: []string{"t0", "t1"}}); err == nil {
		t.Error("linked two payments from the same account")
	}
	link, err := f.svc.Draft(changes.Proposal{Action: changes.LinkTransfer, IDs: []string{"t3", "t4"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Apply(link.Proposal); err != nil {
		t.Fatal(err)
	}
	const types = `SELECT type FROM transactions WHERE id IN ('t3','t4') ORDER BY id`
	if c := f.col(t, types); c != "transfer,transfer," {
		t.Errorf("after link: %s", c)
	}
	unlink, err := f.svc.Draft(changes.Proposal{Action: changes.UnlinkTransfer, IDs: []string{"t3", "t4"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Apply(unlink.Proposal); err != nil {
		t.Fatal(err)
	}
	if c := f.col(t, types); c != "expense,income," {
		t.Errorf("after unlink: %s", c)
	}
	if _, err := f.engine.Undo(f.causeID(t, unlink.Proposal), f.x.Apply); err != nil {
		t.Fatal(err)
	}
	if c := f.col(t, types); c != "transfer,transfer," {
		t.Errorf("after undoing unlink: %s", c)
	}
}

// Two devices pressing Apply together apply the change once.
func TestApplyTwiceAtOnce(t *testing.T) {
	f := setup(t)
	pv, err := f.svc.Draft(changes.Proposal{Action: changes.Delete, IDs: []string{"t0", "t1"}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.svc.Apply(pv.Proposal)
		}(i)
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("want exactly one apply to succeed: %v, %v", errs[0], errs[1])
	}
	if c := f.col(t, `SELECT CAST(COUNT(*) AS TEXT) FROM audit_entries WHERE action = 'change.applied'`); c != "1," {
		t.Errorf("change.applied entries: %s", c)
	}
	if c := f.col(t, `SELECT CAST(COUNT(*) AS TEXT) FROM audit_entries WHERE action = 'transaction.deleted' AND caused_by IS NOT NULL`); c != "2," {
		t.Errorf("deletions tied to the change: %s", c)
	}
}
