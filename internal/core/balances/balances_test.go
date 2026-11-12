package balances

import (
	"testing"

	"glowworm/internal/store"
)

func TestAccountReplay(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO accounts (id, name, type, color, currency, opening_balance_minor, opening_balance_date, created_at, updated_at)
		VALUES ('a1','Current','debit','#0e6f51','GBP',10000,'2024-01-01','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tx := range [][]any{{"t1", "2024-01-05", int64(-4000)}, {"t2", "2024-02-01", int64(3000)}, {"t3", "2024-02-10", int64(-1000)}} {
		if _, err := db.Exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES (?,?,?,'tx',?,?,'expense','h1','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
			tx[0], "a1", tx[1], tx[2], "GBP"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Account(db.DB, "a1", "2024-03-01")
	if err != nil {
		t.Fatal(err)
	}
	if got != 8000 {
		t.Fatalf("balance = %d, want 8000", got)
	}
	got, _ = Account(db.DB, "a1", "2024-01-31")
	if got != 6000 {
		t.Fatalf("balance at 01-31 = %d, want 6000", got)
	}
	if _, err := db.Exec(`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at)
		VALUES ('v1','a1',5000,'2024-02-05','manual','2024-02-05T00:00:00Z','2024-02-05T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	got, _ = Account(db.DB, "a1", "2024-02-28")
	if got != 4000 {
		t.Fatalf("balance after correction = %d, want 4000", got)
	}
	got, _ = Account(db.DB, "a1", "2024-01-31")
	if got != 6000 {
		t.Fatalf("pre-checkpoint balance changed: %d", got)
	}
	if _, err := db.Exec(`UPDATE transactions SET deleted_at = '2024-03-01T00:00:00Z' WHERE id = 't3'`); err != nil {
		t.Fatal(err)
	}
	got, _ = Account(db.DB, "a1", "2024-03-01")
	if got != 5000 {
		t.Fatalf("after soft delete = %d, want 5000", got)
	}
}
