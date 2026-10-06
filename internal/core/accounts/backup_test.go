package accounts

import (
	"encoding/json"
	"strings"
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
	if len(res.Accounts) != 1 || res.Accounts[0].Transactions != 2 || res.Accounts[0].Transfers != 1 {
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

func TestBackupFileRoundTrip(t *testing.T) {
	f := newFixture(t)
	b, err := f.s.Backup([]string{f.main})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	if !strings.Contains(string(raw), `"format":"glowworm.account-backup"`) || !strings.Contains(string(raw), "Groceries") {
		t.Fatalf("backup is missing format or categories: %s", raw)
	}

	var again Backup
	_ = json.Unmarshal(raw, &again)
	if _, err := f.s.RestoreBackup(&again); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("restore over a live account should be refused, got %v", err)
	}

	arc, err := f.s.Delete(f.main)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.PurgeArchive(arc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(`DELETE FROM categories WHERE id = ?`, f.groceries); err != nil {
		t.Fatal(err)
	}
	var file Backup
	_ = json.Unmarshal(raw, &file)
	res, err := f.s.RestoreBackup(&file)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CategoriesCreated) != 1 || res.CategoriesCreated[0] != "Groceries" {
		t.Errorf("missing category should be recreated, got %v", res.CategoriesCreated)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM transactions WHERE account_id = ? AND deleted_at IS NULL`, f.main); n != 2 {
		t.Errorf("restored %d transactions, want 2", n)
	}
	if _, _, cat := f.txState(t, f.shopTx); cat != f.groceries {
		t.Errorf("category not reattached: %q", cat)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM audit_entries WHERE action IN ('account.purged','account.restored')`); n != 2 {
		t.Errorf("expected purge + restore audit entries, got %d", n)
	}
}

func TestRestoreSkipsAccountsThatStillExist(t *testing.T) {
	f := newFixture(t)
	all, err := f.s.Backup(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(all)
	if _, err := f.s.Delete(f.main); err != nil {
		t.Fatal(err)
	}
	var file Backup
	_ = json.Unmarshal(raw, &file)
	res, err := f.s.RestoreBackup(&file)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Accounts) != 1 || res.Accounts[0].ID != f.main || len(res.Skipped) != 1 || res.Skipped[0] != "Savings" {
		t.Fatalf("want Main restored and Savings skipped, got %+v", res)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM transactions WHERE account_id = ?`, f.savings); n != 1 {
		t.Errorf("savings has %d transactions, want 1", n)
	}
	if _, outLink, _ := f.txState(t, f.outTx); outLink == "" {
		t.Error("transfer to the skipped account should be relinked")
	}
}

func TestValidateBackupRejectsForeignFiles(t *testing.T) {
	for name, b := range map[string]*Backup{
		"wrong format": {Format: "something-else", Version: 1},
		"future":       {Format: BackupFormat, Version: BackupVersion + 1},
		"empty":        {Format: BackupFormat, Version: 1},
		"bad tx date": {Format: BackupFormat, Version: 1, Accounts: []AccountBackup{{
			Account:      BackupAccount{ID: "a", Name: "A", Type: core.AccountDebit, Currency: "GBP"},
			Transactions: []BackupTransaction{{ID: "t", Date: "02/06/2026", Currency: "GBP", Type: core.TypeExpense}},
		}}},
	} {
		if err := ValidateBackup(b); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}
