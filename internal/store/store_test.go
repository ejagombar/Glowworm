package store

import (
	"database/sql"
	"testing"
)

func TestResetMatchesFreshInstall(t *testing.T) {
	db, err := Open("sqlite", t.TempDir()+"/t.db")
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

// A database made by the main build that numbered the category description
// migration 5 opens, gets the migrations it missed, and keeps its column.
func TestAdoptsMainsCategoryDescriptionMigration(t *testing.T) {
	path := t.TempDir() + "/t.db"
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	all, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(createMigrationsTable); err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.version > 4 && m.name != "category_description" {
			continue
		}
		if _, err := raw.Exec(m.up); err != nil {
			t.Fatal(err)
		}
		v := m.version
		if v == 10 {
			v = 5
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 't')`, v); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()

	db, err := Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	defer func() { _ = db.Close() }()
	var n, max int
	if err := db.QueryRow(`SELECT COUNT(*), MAX(version) FROM schema_migrations`).Scan(&n, &max); err != nil {
		t.Fatal(err)
	}
	if n != 10 || max != 10 {
		t.Errorf("migrations recorded = %d up to %d, want 10 up to 10", n, max)
	}
	if _, err := db.Exec(`SELECT description FROM categories; SELECT id FROM group_rules`); err != nil {
		t.Errorf("schema incomplete: %v", err)
	}
}
