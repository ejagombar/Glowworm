package transfers

import (
	"testing"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

func newService(t *testing.T) (*Service, string, string) {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &Service{
		DB: db, Txs: &repos.Transactions{DB: db}, Accounts: &repos.Accounts{DB: db},
		Audit: &audit.Writer{DB: db}, Settings: &repos.Settings{DB: db},
		InternalCat: func() (string, error) { return "c-system-internal", nil },
	}
	current := &core.Account{Name: "Current", Type: core.AccountDebit, Currency: "GBP"}
	broker := &core.Account{Name: "Broker", Type: core.AccountSavings, Currency: "GBP"}
	for _, a := range []*core.Account{current, broker} {
		if err := s.Accounts.Create(nil, a); err != nil {
			t.Fatal(err)
		}
	}
	return s, current.ID, broker.ID
}

func addTx(t *testing.T, s *Service, account, date, desc string, amount int64, typ string) string {
	t.Helper()
	tx := &core.Transaction{AccountID: account, Date: date, Description: desc, AmountMinor: amount, Currency: "GBP",
		Type: typ, CategorySource: core.CategorySourceNone}
	if typ == core.TypeTransfer {
		cat := "c-system-internal"
		tx.CategoryID = &cat
		tx.CategorySource = core.CategorySourceUser
	}
	if err := s.Txs.Create(nil, tx); err != nil {
		t.Fatal(err)
	}
	return tx.ID
}

func linked(t *testing.T, s *Service, id string) bool {
	t.Helper()
	tx, err := s.Txs.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return tx.TransferID != nil && tx.Type == core.TypeTransfer
}

// A transfer marked Internal transfer while only one account was imported
// pairs with its other leg once that account's statement arrives, including
// a transfer of the same amount repeated a few weeks later.
func TestDetectPairsMarkedTransfersWithLaterImport(t *testing.T) {
	s, current, broker := newService(t)
	out1 := addTx(t, s, current, "2026-01-29", "To Broker Ltd", -200000, core.TypeTransfer)
	out2 := addTx(t, s, current, "2026-03-09", "To Broker Ltd", -200000, core.TypeTransfer)
	in1 := addTx(t, s, broker, "2026-01-29", "Deposit", 200000, core.TypeIncome)
	in2 := addTx(t, s, broker, "2026-03-10", "Deposit", 200000, core.TypeIncome)
	div := addTx(t, s, broker, "2026-03-10", "Dividend", 1234, core.TypeIncome)

	n, err := s.Detect()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("linked %d transfers, want 2", n)
	}
	for _, id := range []string{out1, out2, in1, in2} {
		if !linked(t, s, id) {
			t.Errorf("%s not linked", id)
		}
	}
	if linked(t, s, div) {
		t.Error("dividend was linked")
	}
}

// Two same-amount legs within the window are ambiguous and left alone.
func TestDetectLeavesAmbiguousPairs(t *testing.T) {
	s, current, broker := newService(t)
	out := addTx(t, s, current, "2026-01-29", "To Broker Ltd", -5000, core.TypeTransfer)
	addTx(t, s, broker, "2026-01-29", "Deposit", 5000, core.TypeIncome)
	addTx(t, s, broker, "2026-01-30", "Deposit", 5000, core.TypeIncome)

	if _, err := s.Detect(); err != nil {
		t.Fatal(err)
	}
	if linked(t, s, out) {
		t.Error("ambiguous transfer was linked")
	}
}

// A card payment lands on the card a couple of days before or after it
// leaves the current account, and still pairs; a week apart doesn't.
func TestDetectPairsLegsAFewDaysApart(t *testing.T) {
	s, current, card := newService(t)
	out := addTx(t, s, current, "2026-06-24", "American Express - 3773", -77396, core.TypeTransfer)
	in := addTx(t, s, card, "2026-06-22", "PAYMENT RECEIVED - THANK YOU", 77396, core.TypeTransfer)
	far := addTx(t, s, current, "2026-08-30", "American Express - 3773", -16870, core.TypeTransfer)
	addTx(t, s, card, "2026-08-22", "PAYMENT RECEIVED - THANK YOU", 16870, core.TypeTransfer)

	// Pending lists what Detect would link, and links nothing.
	pending, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Source.ID != out || pending[0].Destination.ID != in || linked(t, s, out) {
		t.Fatalf("pending = %+v", pending)
	}
	if _, err := s.Detect(); err != nil {
		t.Fatal(err)
	}
	if !linked(t, s, out) || !linked(t, s, in) {
		t.Error("legs two days apart were not linked")
	}
	if linked(t, s, far) {
		t.Error("legs eight days apart were linked")
	}
}

// Suggestions are other-direction transactions, the likely legs first.
func TestSuggestions(t *testing.T) {
	s, current, card := newService(t)
	out := addTx(t, s, current, "2026-06-24", "American Express", -77396, core.TypeExpense)
	leg := addTx(t, s, card, "2026-06-22", "PAYMENT RECEIVED", 77396, core.TypeIncome)
	addTx(t, s, card, "2026-06-23", "Shop", -77396, core.TypeExpense) // same direction
	other := addTx(t, s, card, "2026-07-10", "Refund", 1200, core.TypeIncome)

	got, err := s.Suggestions(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Tx.ID != leg || !got[0].Close || got[1].Tx.ID != other || got[1].Close {
		for _, g := range got {
			t.Logf("%s %s close=%v", g.Tx.ID, g.Tx.Description, g.Close)
		}
		t.Fatal("unexpected suggestions")
	}
}
