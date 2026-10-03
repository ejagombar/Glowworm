// Package store owns the database connection, migrations and transaction helper.
//
// Migrations follow the golang-migrate file convention (NNNN_name.up.sql /
// .down.sql) embedded via go:embed, applied in order inside a transaction
// (doc 01 §2, doc 02 §11). A purpose-built runner is used instead of the
// golang-migrate library to keep the dependency set minimal and cgo-free
// (pure-Go SQLite); the file convention and semantics match.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps the sql.DB handle.
type DB struct {
	*sql.DB
	Dialect string // "sqlite" | "postgres"
}

// Open opens (and creates, for SQLite) the database, applies pending
// migrations and returns the handle.
func Open(dialect, dsn string) (*DB, error) {
	if dialect == "" {
		dialect = "sqlite"
	}
	var db *sql.DB
	var err error
	switch dialect {
	case "sqlite":
		// busy_timeout + foreign keys are also set via PRAGMA below;
		// keep DSN plain so file paths are simple.
		db, err = sql.Open("sqlite", dsn)
	case "postgres":
		err = fmt.Errorf("postgres: dsn %q not supported in this build (set GLOWWORM_DB=sqlite)", dsn)
		db = nil
	default:
		err = fmt.Errorf("unknown dialect %q", dialect)
	}
	if err != nil {
		return nil, err
	}
	// WAL allows concurrent readers; writes serialize via busy_timeout.
	// A small pool avoids hard deadlocks when a tx-holding goroutine issues
	// a read on a second connection (it busy-waits instead of hanging).
	db.SetMaxOpenConns(4)
	s := &DB{DB: db, Dialect: dialect}
	if dialect == "sqlite" {
		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA busy_timeout=5000",
			"PRAGMA foreign_keys=ON",
			"PRAGMA synchronous=NORMAL",
		} {
			if _, err := db.Exec(pragma); err != nil {
				return nil, fmt.Errorf("store: %s: %w", pragma, err)
			}
		}
	}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// WithTx runs fn inside a database transaction; a panic or error rolls back.
func (s *DB) WithTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

type mig struct {
	version int
	name    string
	up      string
	down    string
}

func loadMigrations() ([]mig, error) {
	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	byVersion := map[int]*mig{}
	for _, e := range entries {
		base := strings.TrimSuffix(strings.TrimPrefix(e, "migrations/"), ".sql")
		parts := strings.SplitN(base, "_", 2)
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("store: bad migration name %q", e)
		}
		name := parts[1]
		name = strings.TrimSuffix(name, ".up")
		name = strings.TrimSuffix(name, ".down")
		body, err := migrationsFS.ReadFile(e)
		if err != nil {
			return nil, err
		}
		m := byVersion[v]
		if m == nil {
			m = &mig{version: v, name: name}
			byVersion[v] = m
		}
		if strings.HasSuffix(e, ".up.sql") {
			m.up = string(body)
		} else {
			m.down = string(body)
		}
	}
	out := make([]mig, 0, len(byVersion))
	for _, m := range byVersion {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func (s *DB) migrate() error {
	if _, err := s.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("store: schema_migrations: %w", err)
	}
	var current int
	if err := s.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	all, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range all {
		if m.version <= current {
			continue
		}
		start := time.Now()
		err := s.WithTx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(m.up); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
				m.version, time.Now().UTC().Format(time.RFC3339))
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migration %04d_%s failed: %w", m.version, m.name, err)
		}
		fmt.Printf("store: applied migration %04d_%s (%dms)\n", m.version, m.name, time.Since(start).Milliseconds())
	}
	return nil
}
