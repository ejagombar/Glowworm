package repos

import (
	"database/sql"

	"github.com/ejagombar/Glowworm/internal/store"
)

type Scanner interface {
	Scan(dest ...any) error
}

func exec(db *store.DB, tx *sql.Tx, q string, args ...any) (sql.Result, error) {
	if tx != nil {
		return tx.Exec(q, args...)
	}
	return db.Exec(q, args...)
}

func row(db *store.DB, tx *sql.Tx, q string, args ...any) *sql.Row {
	if tx != nil {
		return tx.QueryRow(q, args...)
	}
	return db.QueryRow(q, args...)
}

func Row(db *store.DB, tx *sql.Tx, q string, args ...any) *sql.Row { return row(db, tx, q, args...) }

func Exec(db *store.DB, tx *sql.Tx, q string, args ...any) (sql.Result, error) {
	return exec(db, tx, q, args...)
}

var ErrNotFound = sql.ErrNoRows
