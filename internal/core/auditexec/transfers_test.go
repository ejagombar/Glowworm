package auditexec_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/auditexec"
	"glowworm/internal/core/transactions"
	"glowworm/internal/core/transfers"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

// ledger is two accounts with the services that change them and the history
// that undoes it: £50 leaves Current (out) and arrives in Savings (in), and
// a second £50 arrives in Savings (in2) that could be the other leg instead.
type ledger struct {
	t      *testing.T
	db     *store.DB
	txs    *transactions.Service
	links  *transfers.Service
	writer *audit.Writer
	engine *audit.Engine
	x      *auditexec.Executor
}

func newLedger(t *testing.T) *ledger {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at) VALUES
			('cur','Current','debit','#000','GBP','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
			('sav','Savings','savings','#000','GBP','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO categories (id, name, color, type, created_at, updated_at) VALUES
			('food','Food','#000','spend','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, category_id, category_source, dedupe_hash, created_at, updated_at) VALUES
			('out','cur','2026-05-20','To savings',-5000,'GBP','expense','food','user','h1','2026-05-20T00:00:00Z','2026-05-20T00:00:00Z'),
			('in','sav','2026-05-20','From current',5000,'GBP','income',NULL,'none','h2','2026-05-20T00:00:00Z','2026-05-20T00:00:00Z'),
			('in2','sav','2026-05-21','From current',5000,'GBP','income',NULL,'none','h3','2026-05-21T00:00:00Z','2026-05-21T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	txRepo := &repos.Transactions{DB: db}
	accRepo := &repos.Accounts{DB: db}
	catRepo := &repos.Categories{DB: db}
	writer := &audit.Writer{DB: db}
	return &ledger{
		t: t, db: db, writer: writer,
		txs:    &transactions.Service{DB: db, Repos: txRepo, Categories: catRepo, Audit: writer},
		links:  &transfers.Service{DB: db, Txs: txRepo, Accounts: accRepo, Audit: writer},
		engine: audit.NewEngine(db, writer),
		x:      &auditexec.Executor{DB: db, Txs: txRepo, Accounts: accRepo, Categories: catRepo},
	}
}

// last is the newest entry in the log.
func (l *ledger) last() int64 {
	l.t.Helper()
	var id int64
	if err := l.db.QueryRow(`SELECT MAX(id) FROM audit_entries`).Scan(&id); err != nil {
		l.t.Fatal(err)
	}
	return id
}

func (l *ledger) undo(id int64) []string {
	l.t.Helper()
	notes, err := l.engine.Undo(id, l.x.Apply)
	if err != nil {
		l.t.Fatalf("undo: %v", err)
	}
	return notes
}

func (l *ledger) redo(id int64) []string {
	l.t.Helper()
	notes, err := l.engine.Redo([]int64{id}, l.x.Apply)
	if err != nil {
		l.t.Fatalf("redo: %v", err)
	}
	return notes
}

func (l *ledger) link(a, b string) string {
	l.t.Helper()
	tr, err := l.links.Link(a, b, core.ActorUser)
	if err != nil {
		l.t.Fatalf("link %s and %s: %v", a, b, err)
	}
	return tr.ID
}

// tx describes a transaction as "type/category/transfer", or "deleted".
func (l *ledger) tx(id string) string {
	l.t.Helper()
	var typ string
	var cat, transfer, deleted sql.NullString
	if err := l.db.QueryRow(`SELECT type, category_id, transfer_id, deleted_at FROM transactions WHERE id = ?`, id).
		Scan(&typ, &cat, &transfer, &deleted); err != nil {
		l.t.Fatal(err)
	}
	if deleted.Valid {
		return "deleted"
	}
	return fmt.Sprintf("%s/%s/%s", typ, cat.String, transfer.String)
}

// sound fails the test if the transfers and the transactions disagree about
// what is linked to what.
func (l *ledger) sound(step string) {
	l.t.Helper()
	var n int
	if err := l.db.QueryRow(`SELECT COUNT(*) FROM transactions t WHERE t.transfer_id IS NOT NULL AND t.deleted_at IS NULL AND NOT EXISTS
		(SELECT 1 FROM transfers r WHERE r.id = t.transfer_id AND t.id IN (r.source_transaction_id, r.destination_transaction_id))`).Scan(&n); err != nil {
		l.t.Fatal(err)
	}
	if n > 0 {
		l.t.Fatalf("%s: %d transactions point at a transfer that isn't theirs", step, n)
	}
	if err := l.db.QueryRow(`SELECT COUNT(*) FROM transfers r WHERE
		(SELECT COUNT(*) FROM transactions t WHERE t.transfer_id = r.id AND t.deleted_at IS NULL
			AND t.id IN (r.source_transaction_id, r.destination_transaction_id)) != 2`).Scan(&n); err != nil {
		l.t.Fatal(err)
	}
	if n > 0 {
		l.t.Fatalf("%s: %d transfers without two live legs", step, n)
	}
}

func (l *ledger) want(step, id, want string) {
	l.t.Helper()
	if got := l.tx(id); got != want {
		l.t.Fatalf("%s: %s is %s, want %s", step, id, got, want)
	}
}

const internal = core.InternalTransferCategoryID

func TestLinkCanBeUndoneAndRedoneAnyNumberOfTimes(t *testing.T) {
	l := newLedger(t)
	id := l.link("out", "in")
	entry := l.last()
	linked := "transfer/" + internal + "/" + id

	for round := 1; round <= 2; round++ {
		l.undo(entry)
		l.sound("undo")
		l.want("undo", "out", "expense/food/")
		l.want("undo", "in", "income//")

		l.redo(entry)
		l.sound("redo")
		// The same transfer, not a new one the entry knows nothing about.
		l.want("redo", "out", linked)
		l.want("redo", "in", linked)
	}
	var how string
	if err := l.db.QueryRow(`SELECT detected || '/' || method FROM transfers WHERE id = ?`, id).Scan(&how); err != nil || how != "manual/manual" {
		t.Fatalf("the transfer came back as %q (%v), want it as it was made", how, err)
	}

	// Once undone, the transactions are free to be linked again.
	l.undo(entry)
	l.link("out", "in2")
	l.sound("link the other way")
}

func TestBreakingALinkCanBeUndoneAndRedone(t *testing.T) {
	l := newLedger(t)
	id := l.link("out", "in")
	if err := l.links.Unlink("out", core.ActorUser); err != nil {
		t.Fatal(err)
	}
	entry := l.last()
	linked := "transfer/" + internal + "/" + id

	for round := 1; round <= 2; round++ {
		l.undo(entry)
		l.sound("undo")
		l.want("undo", "out", linked)
		l.want("undo", "in", linked)

		// Broken again, they're as breaking the link left them.
		l.redo(entry)
		l.sound("redo")
		l.want("redo", "out", "expense//")
		l.want("redo", "in", "income//")
	}
}

func TestLinkIsNotRedoneOverAnotherLink(t *testing.T) {
	l := newLedger(t)
	l.link("out", "in")
	entry := l.last()
	l.undo(entry)
	other := l.link("out", "in2")

	if _, err := l.engine.Redo([]int64{entry}, l.x.Apply); err == nil {
		t.Fatal("redo linked a transaction that is part of another transfer")
	}
	l.sound("refused redo")
	l.want("refused redo", "out", "transfer/"+internal+"/"+other)
	l.want("refused redo", "in", "income//")

	// The same for a link that was broken, when undoing would bring it back.
	if err := l.links.Unlink("out", core.ActorUser); err != nil {
		t.Fatal(err)
	}
	broke := l.last()
	l.link("out", "in")
	if _, err := l.engine.Undo(broke, l.x.Apply); err == nil {
		t.Fatal("undo relinked a transaction that is part of another transfer")
	}
	l.sound("refused undo")
}

func TestLinkIsNotRedoneWithADeletedTransaction(t *testing.T) {
	l := newLedger(t)
	l.link("out", "in")
	entry := l.last()
	l.undo(entry)
	if err := l.txs.Delete("in"); err != nil {
		t.Fatal(err)
	}
	_, err := l.engine.Redo([]int64{entry}, l.x.Apply)
	if err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("redo with a deleted leg: %v", err)
	}
	l.sound("refused redo")
	l.want("refused redo", "out", "expense/food/")
}
