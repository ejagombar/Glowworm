package ledger

import (
	"context"
	"database/sql"
	"sync"
)

// Beginner is what Cached reads through: a *sql.DB or *sql.Conn.
type Beginner interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

type cached struct {
	version int64
	today   string
	book    *Book
}

var (
	cacheMu sync.Mutex
	cache   = map[any]cached{}
)

// Cached is Load, kept between calls while nothing it reads has changed:
// triggers bump ledger_version on every change to transactions,
// valuations, accounts, goals and the reminder setting, whoever makes it.
// The version and the data are read in one read transaction, so a book is
// never filed under a version newer than its data. key names the database
// (its *sql.DB), since several can be open at once.
//
// A Book is read-only once loaded, so callers share it.
func Cached(ctx context.Context, key any, db Beginner, today string) (*Book, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT n FROM ledger_version WHERE id = 1`).Scan(&version); err != nil {
		return nil, err
	}
	cacheMu.Lock()
	c, ok := cache[key]
	cacheMu.Unlock()
	if ok && c.version == version && c.today == today {
		return c.book, nil
	}
	book, err := Load(ctx, tx, today)
	if err != nil {
		return nil, err
	}
	cacheMu.Lock()
	cache[key] = cached{version: version, today: today, book: book}
	cacheMu.Unlock()
	return book, nil
}
