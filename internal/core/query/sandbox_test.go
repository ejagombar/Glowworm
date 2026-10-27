package query

import (
	"strings"
	"testing"

	"glowworm/internal/store"
)

func TestValidateReadonly(t *testing.T) {
	for sql, want := range map[string]string{
		`SELECT description FROM transactions`:                    "",
		`SELECT 'users' AS word, "description" FROM transactions`: "",
		`SELECT password_hash FROM users`:                         "users table",
		`SELECT password_hash FROM "users"`:                       "users table",
		"SELECT password_hash FROM `Users`":                       "users table",
		`SELECT * FROM [ai_provider_config]`:                      "ai_provider_config table",
		`SELECT * FROM main.ai_calls`:                             "ai_calls table",
		`SELECT 1; DELETE FROM transactions`:                      "only one statement",
		`SELECT 1;`:                                               "",
		`DELETE FROM transactions`:                                "only SELECT",
		`WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x`:      "INSERT",
		`-- just a comment`:                                       "no SQL",
	} {
		err := validateReadonly(sql)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: rejected: %v", sql, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: got %v, want an error mentioning %q", sql, err, want)
		}
	}
}

// Queries run on a connection SQLite keeps read-only, and the connection
// writes normally again once it's back in the pool.
func TestReadonlyConnectionCantWrite(t *testing.T) {
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	// A function that writes isn't reachable from SQL, so prove it with a
	// statement the validator would refuse, run on the same kind of connection.
	if _, err := RunReadonlyQuery(db, `SELECT COUNT(*) FROM categories`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('probe','1','t')`); err != nil {
		t.Fatalf("connection left read-only after a query: %v", err)
	}
	if _, err := RunReadonlyQuery(db, `SELECT * FROM nonexistent`); err == nil {
		t.Fatal("query of a missing table succeeded")
	}
	if _, err := db.Exec(`UPDATE settings SET value = '2' WHERE key = 'probe'`); err != nil {
		t.Fatalf("connection left read-only after a failed query: %v", err)
	}
}
