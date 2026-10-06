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

type DB struct {
	*sql.DB
	Dialect string
}

func Open(dialect, dsn string) (*DB, error) {
	if dialect == "" {
		dialect = "sqlite"
	}
	var db *sql.DB
	var err error
	switch dialect {
	case "sqlite":
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
	// More than one connection: a goroutine holding a tx that reads via
	// s.DB (not the tx) would deadlock on a single-connection pool.
	db.SetMaxOpenConns(4)
	s := &DB{DB: db, Dialect: dialect}
	if dialect == "sqlite" {
		// These run on one pooled connection only; the others do not enforce
		// foreign keys, so deletes must not rely on ON DELETE CASCADE.
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
}

func loadMigrations() ([]mig, error) {
	entries, err := fs.Glob(migrationsFS, "migrations/*.up.sql")
	if err != nil {
		return nil, err
	}
	out := make([]mig, 0, len(entries))
	for _, e := range entries {
		base := strings.TrimSuffix(strings.TrimPrefix(e, "migrations/"), ".up.sql")
		parts := strings.SplitN(base, "_", 2)
		v, err := strconv.Atoi(parts[0])
		if err != nil || len(parts) != 2 {
			return nil, fmt.Errorf("store: bad migration name %q", e)
		}
		body, err := migrationsFS.ReadFile(e)
		if err != nil {
			return nil, err
		}
		out = append(out, mig{version: v, name: parts[1], up: string(body)})
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
	if latest := all[len(all)-1].version; current > latest {
		return fmt.Errorf("store: database schema is version %d but this build only knows up to %d — it was created by a newer or pre-release build; start with a fresh data directory", current, latest)
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
