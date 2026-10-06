package accounts

import (
	"testing"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

type fixture struct {
	s                 *Service
	main, savings     string
	groceries         string
	shopTx, outTx     string
	inTx              string
	transferID, batch string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &fixture{s: &Service{DB: db, Repos: &repos.Accounts{DB: db}, Txs: &repos.Transactions{DB: db}, Audit: &audit.Writer{DB: db}}}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	main := &core.Account{Name: "Main Current", Type: core.AccountDebit, Currency: "GBP", AccountNumber: "12345678", SortCode: "12-34-56"}
	savings := &core.Account{Name: "Savings", Type: core.AccountSavings, Currency: "GBP"}
	must(f.s.Repos.Create(nil, main))
	must(f.s.Repos.Create(nil, savings))
	f.main, f.savings = main.ID, savings.ID
	f.groceries = "c-groceries"
	_, err = db.Exec(`INSERT INTO categories (id, name, color, type, created_at, updated_at)
		VALUES ('c-groceries', 'Groceries', '#0e6f51', 'spend', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	must(err)
	f.batch = "batch-1"
	_, err = db.Exec(`INSERT INTO import_batches (id, filename, size_bytes, sha256, mime, account_id, status, created_at)
		VALUES (?, 'june.csv', 10, 'abc', 'csv', ?, 'confirmed', '2026-06-30T00:00:00Z')`, f.batch, f.main)
	must(err)
	_, err = db.Exec(`INSERT INTO import_schemas (id, name, kind, mapping, columns, sample_hash, account_id, created_at, updated_at)
		VALUES ('schema-1', 'Main CSV', 'csv', '{}', 'Date', 'h1', ?, '2026-06-30T00:00:00Z', '2026-06-30T00:00:00Z')`, f.main)
	must(err)

	cat := f.groceries
	shop := &core.Transaction{AccountID: f.main, Date: "2026-06-01", Description: "TESCO", AmountMinor: -1250, Currency: "GBP",
		Type: core.TypeExpense, CategoryID: &cat, CategorySource: core.CategorySourceUser, ImportBatchID: &f.batch}
	out := &core.Transaction{AccountID: f.main, Date: "2026-06-02", Description: "To savings", AmountMinor: -5000, Currency: "GBP",
		Type: core.TypeExpense, CategorySource: core.CategorySourceNone}
	in := &core.Transaction{AccountID: f.savings, Date: "2026-06-02", Description: "From current", AmountMinor: 5000, Currency: "GBP",
		Type: core.TypeIncome, CategorySource: core.CategorySourceNone}
	for _, tx := range []*core.Transaction{shop, out, in} {
		must(f.s.Txs.Create(nil, tx))
	}
	f.shopTx, f.outTx, f.inTx = shop.ID, out.ID, in.ID
	tr := &core.Transfer{SourceTxID: out.ID, DestinationTxID: in.ID, Detected: "manual", Method: "manual"}
	must(f.s.Txs.CreateTransfer(nil, tr, out, in, "c-system-internal"))
	f.transferID = tr.ID
	_, err = f.s.CreateValuation(f.main, ValuationInput{ValueMinor: 100000, Date: "2026-06-03"}, "manual")
	must(err)
	return f
}

func (f *fixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.s.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) txState(t *testing.T, id string) (typ, transferID, catID string) {
	t.Helper()
	if err := f.s.DB.QueryRow(`SELECT type, COALESCE(transfer_id,''), COALESCE(category_id,'') FROM transactions WHERE id = ?`, id).
		Scan(&typ, &transferID, &catID); err != nil {
		t.Fatal(err)
	}
	return
}

func TestDeleteLeavesNothingBehindAndArchiveRestores(t *testing.T) {
	f := newFixture(t)
	arc, err := f.s.Delete(f.main)
	if err != nil {
		t.Fatal(err)
	}
	if arc.TxCount != 2 || arc.Name != "Main Current" {
		t.Fatalf("archive = %+v", arc)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM accounts WHERE id = ?`:                                            0,
		`SELECT COUNT(*) FROM transactions WHERE account_id = ?`:                                0,
		`SELECT COUNT(*) FROM valuations WHERE account_id = ?`:                                  0,
		`SELECT COUNT(*) FROM import_batches WHERE account_id = ?`:                              0,
		`SELECT COUNT(*) FROM import_schemas WHERE account_id = ?`:                              0,
		`SELECT COUNT(*) FROM audit_entries WHERE action = 'account.deleted' AND entity_id = ?`: 1,
	} {
		if got := f.count(t, q, f.main); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	if n := f.count(t, `SELECT COUNT(*) FROM transfers`); n != 0 {
		t.Errorf("transfer link survived the delete")
	}
	if typ, tid, cat := f.txState(t, f.inTx); typ != core.TypeIncome || tid != "" || cat != "" {
		t.Errorf("counterpart leg not unlinked: type=%s transfer=%q cat=%q", typ, tid, cat)
	}
	list, err := f.s.Archives()
	if err != nil || len(list) != 1 {
		t.Fatalf("archives = %v, %v", list, err)
	}

	res, err := f.s.RestoreArchive(arc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Transactions != 2 || res.Transfers != 1 {
		t.Fatalf("restore result = %+v", res)
	}
	a, err := f.s.Repos.Get(f.main)
	if err != nil || a.Name != "Main Current" || a.AccountNumber != "12345678" {
		t.Fatalf("account not restored intact: %+v %v", a, err)
	}
	if typ, _, cat := f.txState(t, f.shopTx); typ != core.TypeExpense || cat != f.groceries {
		t.Errorf("shop tx restored wrong: type=%s cat=%s", typ, cat)
	}
	_, outLink, _ := f.txState(t, f.outTx)
	inType, inLink, inCat := f.txState(t, f.inTx)
	if outLink == "" || outLink != inLink || inType != core.TypeTransfer || inCat != "c-system-internal" {
		t.Errorf("transfer not relinked: out=%q in=%q type=%s cat=%s", outLink, inLink, inType, inCat)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM valuations WHERE account_id = ? AND deleted_at IS NULL`: 1,
		`SELECT COUNT(*) FROM import_batches WHERE account_id = ?`:                    1,
		`SELECT COUNT(*) FROM import_schemas WHERE account_id = ?`:                    1,
	} {
		if got := f.count(t, q, f.main); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	if n := f.count(t, `SELECT COUNT(*) FROM account_archives`); n != 0 {
		t.Errorf("archive should be consumed by the restore")
	}
}

func TestPurgedArchiveIsGoneAndRestoreRecreatesCategories(t *testing.T) {
	f := newFixture(t)
	arc, err := f.s.Delete(f.main)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(`DELETE FROM categories WHERE id = ?`, f.groceries); err != nil {
		t.Fatal(err)
	}
	res, err := f.s.RestoreArchive(arc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CategoriesCreated) != 1 || res.CategoriesCreated[0] != "Groceries" {
		t.Errorf("missing category should be recreated, got %v", res.CategoriesCreated)
	}
	if _, _, cat := f.txState(t, f.shopTx); cat != f.groceries {
		t.Errorf("category not reattached: %q", cat)
	}

	arc, err = f.s.Delete(f.main)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.PurgeArchive(arc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RestoreArchive(arc.ID); err == nil {
		t.Error("a purged archive must not be restorable")
	}
	if n := f.count(t, `SELECT COUNT(*) FROM audit_entries WHERE action = 'account.purged'`); n != 1 {
		t.Errorf("purge audit entries = %d, want 1", n)
	}
}
