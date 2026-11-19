package store

import (
	"database/sql"
	"testing"
)

// A database at 0005 with goals, accounts and balances comes through 0006:
// the goal tables and their triggers go, everything else stays, and
// ledger_version still counts balance changes.
func TestDropGoalsMigration(t *testing.T) {
	path := t.TempDir() + "/t.db"
	raw, err := sql.Open("sqlite", sqliteDSN(path))
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
		if m.version > 5 {
			break
		}
		tx, err := raw.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := applyMigration(tx, m); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	const ts = "2026-11-01T00:00:00Z"
	for _, q := range []string{
		`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at) VALUES ('isa', 'ISA', 'savings', '#123456', 'GBP', '` + ts + `', '` + ts + `')`,
		`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at) VALUES ('v1', 'isa', 500000, '2026-11-01', 'manual', '` + ts + `', '` + ts + `')`,
		`INSERT INTO goals (id, name, target_minor, created_at, updated_at) VALUES ('g1', 'House', 2500000, '` + ts + `', '` + ts + `')`,
		`INSERT INTO goal_accounts (goal_id, account_id) VALUES ('g1', 'isa')`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	one := func(q string) int {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if n := one(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('goals', 'goal_accounts') OR name LIKE 'ledger_goal_%' OR name LIKE 'ledger_ga_%'`); n != 0 {
		t.Errorf("%d goal tables or triggers left", n)
	}
	if n := one(`SELECT COUNT(*) FROM accounts`) + one(`SELECT COUNT(*) FROM valuations`); n != 2 {
		t.Errorf("accounts and valuations: %d rows, want 2", n)
	}
	before := one(`SELECT n FROM ledger_version`)
	if _, err := db.Exec(`UPDATE valuations SET value_minor = 510000 WHERE id = 'v1'`); err != nil {
		t.Fatal(err)
	}
	if after := one(`SELECT n FROM ledger_version`); after != before+1 {
		t.Errorf("ledger_version %d -> %d after a balance change", before, after)
	}
}
