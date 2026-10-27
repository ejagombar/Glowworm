package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
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
		db, err = sql.Open("sqlite", sqliteDSN(dsn))
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
		if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// sqliteDSN sets the per-connection pragmas on every pooled connection, not
// just the first: each waits up to 5s for another writer instead of failing
// with "database is locked", and enforces foreign keys. Transactions take the
// write lock when they begin, so two clients writing at once queue up rather
// than one failing when its read turns into a write.
func sqliteDSN(dsn string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
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

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`

func applyMigration(tx *sql.Tx, m mig) error {
	if _, err := tx.Exec(m.up); err != nil {
		return fmt.Errorf("store: migration %04d_%s failed: %w", m.version, m.name, err)
	}
	_, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *DB) migrate() error {
	if _, err := s.Exec(createMigrationsTable); err != nil {
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
		if err := s.WithTx(func(tx *sql.Tx) error { return applyMigration(tx, m) }); err != nil {
			return err
		}
		fmt.Printf("store: applied migration %04d_%s (%dms)\n", m.version, m.name, time.Since(start).Milliseconds())
	}
	return nil
}

func (s *DB) Reset() error {
	all, err := loadMigrations()
	if err != nil {
		return err
	}
	ctx := context.Background()
	conn, err := s.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// With enforcement on, DROP TABLE runs an implicit FK-checked DELETE that
	// fails once a parent is already dropped. The pragma is per connection and
	// can't change inside a transaction, so pin a connection and switch it off.
	var fk int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() {
		// The connection goes back to the pool: if foreign keys can't be
		// switched back on, throw it away rather than leave one unchecked.
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA foreign_keys = %d`, fk)); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := resetSchema(tx, all); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Dropped rows survive in free pages and the WAL until both are rewritten.
	if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

func resetSchema(tx *sql.Tx, all []mig) error {
	rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, n)
	}
	rows.Close()
	for _, t := range tables {
		if _, err := tx.Exec(`DROP TABLE "` + t + `"`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(createMigrationsTable); err != nil {
		return err
	}
	for _, m := range all {
		if err := applyMigration(tx, m); err != nil {
			return err
		}
	}
	return nil
}

func (s *DB) SnapshotTo(path string) error {
	_, err := s.Exec(`VACUUM INTO ?`, path)
	return err
}
