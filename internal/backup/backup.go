package backup

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"glowworm/internal/crypto"
	"glowworm/internal/store"
)

const (
	format  = "glowworm.backup"
	version = 1

	manifestName = "manifest.json"
	dbName       = "glowworm.db"
	keyName      = "secret.key"
)

type Manifest struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	CreatedAt  string `json:"created_at"`
	AppVersion string `json:"app_version"`
}

func FileName(t time.Time) string {
	return "glowworm-backup-" + t.Format("2006-01-02-150405") + ".zip"
}

func Write(w io.Writer, db *store.DB, secret *crypto.Secret, appVersion string) error {
	dir, err := os.MkdirTemp("", "glowworm-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	snap := filepath.Join(dir, dbName)
	if err := db.SnapshotTo(snap); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	zw := zip.NewWriter(w)
	m, _ := json.MarshalIndent(Manifest{Format: format, Version: version,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), AppVersion: appVersion}, "", "  ")
	if err := addBytes(zw, manifestName, m); err != nil {
		return err
	}
	f, err := os.Open(snap)
	if err != nil {
		return err
	}
	defer f.Close()
	dw, err := zw.Create(dbName)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dw, f); err != nil {
		return err
	}
	if err := addBytes(zw, keyName, []byte(secret.Hex())); err != nil {
		return err
	}
	return zw.Close()
}

func addBytes(zw *zip.Writer, name string, b []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

type Summary struct {
	Manifest     Manifest
	Owner        string
	Accounts     int
	Transactions int
	ReplacedDir  string
}

func Restore(zipPath, dataDir, dbPath string, force bool) (*Summary, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	release, err := store.LockDataDir(dataDir)
	if errors.Is(err, store.ErrLocked) {
		return nil, errors.New("Glowworm is running on this data directory — stop it first")
	}
	if err != nil {
		return nil, err
	}
	defer release()

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("not a Glowworm backup: %w", err)
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	sum := &Summary{}
	raw, err := readZipFile(files, manifestName)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &sum.Manifest); err != nil || sum.Manifest.Format != format {
		return nil, errors.New("not a Glowworm backup (bad manifest)")
	}
	if sum.Manifest.Version > version {
		return nil, fmt.Errorf("backup format %d is newer than this build supports (%d)", sum.Manifest.Version, version)
	}
	key, err := readZipFile(files, keyName)
	if err != nil {
		return nil, err
	}
	if _, err := crypto.Parse(string(key)); err != nil {
		return nil, fmt.Errorf("backup secret.key is invalid: %w", err)
	}

	if _, err := os.Stat(dbPath); err == nil && !force {
		return nil, fmt.Errorf("%s already exists; rerun with --force to replace it (the current files are moved aside, not deleted)", dbPath)
	}

	tmp := dbPath + ".restoring"
	defer removeDB(tmp)
	if err := extract(files, dbName, tmp); err != nil {
		return nil, err
	}
	if err := verify(tmp, sum); err != nil {
		return nil, err
	}

	if _, err := os.Stat(dbPath); err == nil {
		sum.ReplacedDir = filepath.Join(dataDir, "replaced-"+time.Now().Format("20060102-150405"))
		if err := os.MkdirAll(sum.ReplacedDir, 0o700); err != nil {
			return nil, err
		}
		// The -wal/-shm files belong to the old database: left in place they
		// would be replayed onto the restored one and corrupt it.
		for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", filepath.Join(dataDir, keyName)} {
			if _, err := os.Stat(p); err == nil {
				if err := os.Rename(p, filepath.Join(sum.ReplacedDir, filepath.Base(p))); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dataDir, keyName), key, 0o600); err != nil {
		return nil, err
	}
	// Pages still open from before compare against this; without it they
	// see a different version and offer to reload onto the restored data.
	_ = os.Remove(filepath.Join(dataDir, "data-version"))
	return sum, nil
}

func verify(path string, sum *Summary) error {
	db, err := store.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("backup database can't be opened: %w", err)
	}
	var check string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		db.Close()
		return fmt.Errorf("backup database failed its integrity check: %s %v", check, err)
	}
	var owner sql.NullString
	_ = db.QueryRow(`SELECT username FROM users LIMIT 1`).Scan(&owner)
	sum.Owner = owner.String
	_ = db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE deleted_at IS NULL`).Scan(&sum.Accounts)
	_ = db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE deleted_at IS NULL`).Scan(&sum.Transactions)
	if err := db.Close(); err != nil {
		return err
	}
	// Leaving WAL mode needs the only open connection; the store's pool may
	// hold several, so fold the WAL back into the file on a dedicated one.
	plain, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer plain.Close()
	plain.SetMaxOpenConns(1)
	var mode string
	if err := plain.QueryRow(`PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		return err
	}
	if mode != "delete" {
		return fmt.Errorf("could not finalise the restored database (journal mode %q)", mode)
	}
	return nil
}

func readZipFile(files map[string]*zip.File, name string) ([]byte, error) {
	f, ok := files[name]
	if !ok {
		return nil, fmt.Errorf("not a Glowworm backup (missing %s)", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func extract(files map[string]*zip.File, name, dst string) error {
	f, ok := files[name]
	if !ok {
		return fmt.Errorf("not a Glowworm backup (missing %s)", name)
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func removeDB(path string) {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Remove(p)
	}
}
