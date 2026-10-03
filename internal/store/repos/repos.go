// Package repos contains the SQL layer (doc 01 §4). SQL lives here; business
// rules live in core/* services; handlers call services.
//
// Conventions:
//   - Repos hold *store.DB for queries (embedded *sql.DB).
//   - Mutating methods take a *sql.Tx as first argument; services pass their
//     transaction when composing atomic (mutation + audit) writes, or nil for
//     standalone execution.
package repos

import (
	"database/sql"

	"glowworm/internal/store"
)

// DBTX is satisfied by both *sql.DB and *sql.Tx.
type DBTX interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Scanner is satisfied by *sql.Row and *sql.Rows.
type Scanner interface {
	Scan(dest ...any) error
}

// exec runs a mutation on tx when non-nil, else on db.
func exec(db *store.DB, tx *sql.Tx, q string, args ...any) (sql.Result, error) {
	if tx != nil {
		return tx.Exec(q, args...)
	}
	return db.Exec(q, args...)
}

// row runs a single-row query on tx when non-nil, else on db.
func row(db *store.DB, tx *sql.Tx, q string, args ...any) *sql.Row {
	if tx != nil {
		return tx.QueryRow(q, args...)
	}
	return db.QueryRow(q, args...)
}

// Row exposes tx-aware single-row queries to services/executors.
func Row(db *store.DB, tx *sql.Tx, q string, args ...any) *sql.Row { return row(db, tx, q, args...) }

// Exec exposes tx-aware mutations to services/executors.
func Exec(db *store.DB, tx *sql.Tx, q string, args ...any) (sql.Result, error) {
	return exec(db, tx, q, args...)
}

var ErrNotFound = sql.ErrNoRows
