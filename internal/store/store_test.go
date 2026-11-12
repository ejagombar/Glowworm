package store

import "testing"

func TestResetMatchesFreshInstall(t *testing.T) {
	db, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	count := func(q string) int {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	fresh := map[string]int{}
	for _, tbl := range []string{"categories", "settings", "audit_paths", "schema_migrations"} {
		fresh[tbl] = count(`SELECT COUNT(*) FROM ` + tbl)
	}
	for _, q := range []string{
		`INSERT INTO users (id, username, password_hash, created_at, updated_at) VALUES ('u1','owner','x','t','t')`,
		`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at) VALUES ('a1','Main','debit','#000','GBP','t','t')`,
		`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES ('t1','a1','2026-06-01','TESCO',-100,'GBP','expense','h','t','t')`,
		`UPDATE settings SET value = '"USD"' WHERE key = 'currency'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q[:30], err)
		}
	}
	if err := db.Reset(); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"users", "accounts", "transactions"} {
		if n := count(`SELECT COUNT(*) FROM ` + tbl); n != 0 {
			t.Errorf("%s has %d rows after reset", tbl, n)
		}
	}
	for tbl, want := range fresh {
		if n := count(`SELECT COUNT(*) FROM ` + tbl); n != want {
			t.Errorf("%s has %d rows after reset, fresh install has %d", tbl, n, want)
		}
	}
	var cur string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key = 'currency'`).Scan(&cur)
	if cur != `"GBP"` {
		t.Errorf("settings not back to defaults: currency = %s", cur)
	}
}
