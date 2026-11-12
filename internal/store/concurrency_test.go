package store

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
)

// Several clients writing at once queue up for the write lock instead of
// failing with "database is locked", whichever pooled connection they get,
// and every connection enforces foreign keys.
func TestConcurrentWritersQueue(t *testing.T) {
	db, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at)
		VALUES ('a1','Main','debit','#000','GBP','t','t')`); err != nil {
		t.Fatal(err)
	}
	const writers, each = 8, 20
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				// Read, then write: the pattern that fails when a deferred
				// transaction's read snapshot goes stale before it writes.
				errs <- db.WithTx(func(tx *sql.Tx) error {
					var n int
					if err := tx.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&n); err != nil {
						return err
					}
					_, err := tx.Exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
						VALUES (?,'a1','2026-06-01','x',-1,'GBP','expense','h','t','t')`, fmt.Sprintf("t%d-%d", w, i))
					return err
				})
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&n); err != nil || n != writers*each {
		t.Fatalf("%d rows, %v", n, err)
	}
	for i := 0; i < 6; i++ { // more than the pool holds, so every connection is tried
		if _, err := db.Exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES (?,'missing','2026-06-01','x',-1,'GBP','expense','h','t','t')`, fmt.Sprintf("fk%d", i)); err == nil {
			t.Fatal("a connection accepted a transaction for an account that doesn't exist")
		}
	}
}
