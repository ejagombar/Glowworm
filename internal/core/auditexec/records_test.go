package auditexec_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/ejagombar/Glowworm/internal/core/accounts"
	"github.com/ejagombar/Glowworm/internal/core/categories"
	"github.com/ejagombar/Glowworm/internal/core/transactions"
	"github.com/ejagombar/Glowworm/internal/store/repos"
)

func (l *ledger) accounts() *accounts.Service {
	return &accounts.Service{DB: l.db, Repos: &repos.Accounts{DB: l.db}, Txs: &repos.Transactions{DB: l.db}, Audit: l.writer}
}

func (l *ledger) categories() *categories.Service {
	return &categories.Service{DB: l.db, Repos: &repos.Categories{DB: l.db}, Txs: &repos.Transactions{DB: l.db}, Audit: l.writer}
}

// refused undoes or redoes an entry that must not go through, and returns why.
func (l *ledger) refused(step string, id int64, redo bool) string {
	l.t.Helper()
	var err error
	if redo {
		_, err = l.engine.Redo([]int64{id}, l.x.Apply)
	} else {
		_, err = l.engine.Undo(id, l.x.Apply)
	}
	if err == nil {
		l.t.Fatalf("%s: went through, want it refused", step)
	}
	if strings.Contains(err.Error(), "sql:") || strings.Contains(err.Error(), "constraint") {
		l.t.Fatalf("%s: refused with a database error: %v", step, err)
	}
	return err.Error()
}

// account describes an account's details, or says it is deleted.
func (l *ledger) account(id string) string {
	l.t.Helper()
	var name, color, number, sort, notes string
	var deleted sql.NullString
	if err := l.db.QueryRow(`SELECT name, color, account_number, sort_code, notes, deleted_at FROM accounts WHERE id = ?`, id).
		Scan(&name, &color, &number, &sort, &notes, &deleted); err != nil {
		l.t.Fatal(err)
	}
	if deleted.Valid {
		return "deleted"
	}
	return fmt.Sprintf("%s %s %s %s %q", name, color, number, sort, notes)
}

func TestUndoingAnAccountEditPutsBackWhatItChanged(t *testing.T) {
	l := newLedger(t)
	svc := l.accounts()
	a, err := svc.Create(accounts.Input{Name: "Main", Type: "debit", Color: "#111111", AccountNumber: strp("12345678"), SortCode: strp("11-22-33")})
	if err != nil {
		t.Fatal(err)
	}
	// The first edit changes the name, colour and account number; the second
	// only the notes.
	if _, err := svc.Update(a.ID, accounts.Input{Name: "Joint", Type: "debit", Color: "#222222", AccountNumber: strp("87654321")}); err != nil {
		t.Fatal(err)
	}
	first := l.last()
	if _, err := svc.Update(a.ID, accounts.Input{Name: "Joint", Type: "debit", Notes: strp("bills")}); err != nil {
		t.Fatal(err)
	}
	second := l.last()

	l.undo(first)
	if got, want := l.account(a.ID), `Main #111111 12345678 11-22-33 "bills"`; got != want {
		t.Fatalf("after undoing the first edit: %s, want %s", got, want)
	}
	l.redo(first)
	if got, want := l.account(a.ID), `Joint #222222 87654321 11-22-33 "bills"`; got != want {
		t.Fatalf("after redoing it: %s, want %s", got, want)
	}

	// A field changed again since keeps the later value, and the undo says so.
	if _, err := svc.Update(a.ID, accounts.Input{Name: "Household", Type: "debit"}); err != nil {
		t.Fatal(err)
	}
	notes := l.undo(first)
	if got, want := l.account(a.ID), `Household #111111 12345678 11-22-33 "bills"`; got != want {
		t.Fatalf("after undoing under a later rename: %s, want %s", got, want)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "kept the name") {
		t.Fatalf("notes = %q, want the kept name mentioned", notes)
	}
	_ = second
}

func TestAnAccountIsNotUndoneFromUnderItsTransactions(t *testing.T) {
	l := newLedger(t)
	svc := l.accounts()
	a, err := svc.Create(accounts.Input{Name: "Spare", Type: "debit"})
	if err != nil {
		t.Fatal(err)
	}
	added := l.last()

	// Empty, it goes and comes back.
	l.undo(added)
	if got := l.account(a.ID); got != "deleted" {
		t.Fatalf("after undo: %s", got)
	}
	l.redo(added)

	if _, err := l.db.Exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
		VALUES ('s1',?,'2026-05-20','Coffee',-300,'GBP','expense','hs','2026-05-20T00:00:00Z','2026-05-20T00:00:00Z')`, a.ID); err != nil {
		t.Fatal(err)
	}
	if why := l.refused("undo with a transaction in it", added, false); !strings.Contains(why, "transactions") {
		t.Fatalf("refused with %q, want it to say why", why)
	}
	if got := l.account(a.ID); got == "deleted" {
		t.Fatal("the account went, leaving its transaction behind")
	}
}

func TestWhatIsAlreadyGoneIsNotUndone(t *testing.T) {
	l := newLedger(t)
	accs := l.accounts()
	a, err := accs.Create(accounts.Input{Name: "Spare", Type: "savings"})
	if err != nil {
		t.Fatal(err)
	}
	added := l.last()
	if _, err := accs.Update(a.ID, accounts.Input{Name: "Spare 2", Type: "savings"}); err != nil {
		t.Fatal(err)
	}
	renamed := l.last()
	v, err := accs.CreateValuation(a.ID, accounts.ValuationInput{ValueMinor: 100, Date: "2026-06-01"}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	valued := l.last()
	if err := accs.DeleteValuation(a.ID, v.Valuation.ID); err != nil {
		t.Fatal(err)
	}
	// The value was removed by its own entry: "Updated account value" has
	// nothing left to undo, and must not be left for a redo to bring back.
	l.refused("undo a value that was removed", valued, false)

	// Deleted from Settings, the account is gone from the database.
	if _, err := accs.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	l.refused("undo the rename of a deleted account", renamed, false)
	l.refused("undo adding a deleted account", added, false)

	// The same for a transaction and a category.
	if _, err := l.db.Exec(`DELETE FROM transactions WHERE id = 'in2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`INSERT INTO audit_entries (timestamp, actor, action, entity_type, entity_id, title, sub, diff, before, after, revert, path_id)
		SELECT timestamp, 'user', 'transaction.edited', 'transaction', 'in2', 'Edited', '', '[]', '{"description":"a"}', '{"description":"b"}',
			'{"transaction_id":"in2"}', path_id FROM audit_entries LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	l.refused("undo an edit of a transaction that is gone", l.last(), false)
}

// cat describes a category, or says it is deleted.
func (l *ledger) cat(id string) string {
	l.t.Helper()
	var name, color, desc string
	var deleted sql.NullString
	if err := l.db.QueryRow(`SELECT name, color, description, deleted_at FROM categories WHERE id = ?`, id).
		Scan(&name, &color, &desc, &deleted); err != nil {
		l.t.Fatal(err)
	}
	if deleted.Valid {
		return "deleted"
	}
	return fmt.Sprintf("%s %s %q", name, color, desc)
}

func strp(s string) *string { return &s }

func TestUndoingACategoryEditPutsBackWhatItChanged(t *testing.T) {
	l := newLedger(t)
	svc := l.categories()
	c, err := svc.Create(categories.Input{Name: "Pets", Color: "#111111", Description: strp("vet, food")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(c.ID, categories.Input{Name: "Animals", Color: "#222222"}); err != nil {
		t.Fatal(err)
	}
	first := l.last()
	if _, err := svc.Update(c.ID, categories.Input{Name: "Animals", Description: strp("vet, food, toys")}); err != nil {
		t.Fatal(err)
	}

	l.undo(first)
	if got, want := l.cat(c.ID), `Pets #111111 "vet, food, toys"`; got != want {
		t.Fatalf("after undoing the first edit: %s, want %s", got, want)
	}
	l.redo(first)
	if got, want := l.cat(c.ID), `Animals #222222 "vet, food, toys"`; got != want {
		t.Fatalf("after redoing it: %s, want %s", got, want)
	}

	// The old name can't come back once another category has it.
	if _, err := svc.Create(categories.Input{Name: "pets"}); err != nil {
		t.Fatal(err)
	}
	notes := l.undo(first)
	if got, want := l.cat(c.ID), `Animals #111111 "vet, food, toys"`; got != want {
		t.Fatalf("after undoing with the name taken: %s, want %s", got, want)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "name") {
		t.Fatalf("notes = %q, want the kept name mentioned", notes)
	}
}

func TestADeletedCategoryDoesNotHoldItsName(t *testing.T) {
	l := newLedger(t)
	svc := l.categories()
	old, err := svc.Create(categories.Input{Name: "Pets", Color: "#111111"})
	if err != nil {
		t.Fatal(err)
	}
	added := l.last()

	// Undone and added again, it's a new category under the same name.
	l.undo(added)
	fresh, err := svc.Create(categories.Input{Name: "Pets", Color: "#222222"})
	if err != nil {
		t.Fatalf("adding a category again after undoing it: %v", err)
	}
	if fresh.ID == old.ID {
		t.Fatal("the new category is the old one")
	}
	// The old one can't come back while its name is in use...
	l.refused("redo over a category with the same name", added, true)
	if got := l.cat(old.ID); got != "deleted" {
		t.Fatalf("the old category is %s", got)
	}
	// ...and does once the name is free, under the name it had.
	if _, err := svc.Update(fresh.ID, categories.Input{Name: "Animals"}); err != nil {
		t.Fatal(err)
	}
	l.redo(added)
	if got, want := l.cat(old.ID), `Pets #111111 ""`; got != want {
		t.Fatalf("after redo: %s, want %s", got, want)
	}

	// Deleting and adding again works the same way, as does a rename.
	if err := svc.Delete(old.ID); err != nil {
		t.Fatal(err)
	}
	deleted := l.last()
	if _, err := svc.Update(fresh.ID, categories.Input{Name: "Pets"}); err != nil {
		t.Fatalf("renaming to a deleted category's name: %v", err)
	}
	l.refused("undo a delete over a category with the same name", deleted, false)
	if err := svc.Delete(fresh.ID); err != nil {
		t.Fatal(err)
	}
	l.undo(deleted)
	if got, want := l.cat(old.ID), `Pets #111111 ""`; got != want {
		t.Fatalf("after undoing the delete: %s, want %s", got, want)
	}

	// A category deleted by its own entry isn't "un-added" as well.
	if err := svc.Delete(old.ID); err != nil {
		t.Fatal(err)
	}
	l.refused("undo adding a category that was deleted", added, false)
}

func TestATransactionInADeletedCategoryCanStillBeEdited(t *testing.T) {
	l := newLedger(t)
	svc := l.categories()
	c, err := svc.Create(categories.Input{Name: "Pets"})
	if err != nil {
		t.Fatal(err)
	}
	added := l.last()
	if _, err := l.txs.Edit("out", transactions.CreateInput{CategoryID: &c.ID}, ""); err != nil {
		t.Fatal(err)
	}

	// Undoing "Added category" takes the category away from under it.
	l.undo(added)
	if _, err := l.txs.Edit("out", transactions.CreateInput{Description: "Vet"}, ""); err != nil {
		t.Fatalf("editing a transaction whose category was undone: %v", err)
	}
	// It is still the category's, for when the category comes back.
	l.redo(added)
	l.want("category back", "out", "expense/"+c.ID+"/")

	// A category that never existed is still refused.
	if _, err := l.txs.Edit("out", transactions.CreateInput{CategoryID: strp("nope")}, ""); err == nil {
		t.Fatal("an unknown category was accepted")
	}
}
