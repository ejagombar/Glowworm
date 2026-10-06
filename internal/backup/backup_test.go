package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"glowworm/internal/crypto"
	"glowworm/internal/store"
)

func seededInstance(t *testing.T) (dir string, secret *crypto.Secret) {
	t.Helper()
	dir = t.TempDir()
	secret, err := crypto.LoadOrCreate("", dir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open("sqlite", filepath.Join(dir, "glowworm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO users (id, username, password_hash, created_at, updated_at) VALUES ('u1','owner','x','t','t')`,
		`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at) VALUES ('a1','Main','debit','#000','GBP','t','t')`,
		`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES ('t1','a1','2026-06-01','TESCO',-100,'GBP','expense','h','t','t')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return dir, secret
}

func writeBackup(t *testing.T, dir string, secret *crypto.Secret) string {
	t.Helper()
	db, err := store.Open("sqlite", filepath.Join(dir, "glowworm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := filepath.Join(t.TempDir(), "b.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(f, db, secret, "test"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return path
}

func TestRestoreRoundTrip(t *testing.T) {
	src, secret := seededInstance(t)
	zipPath := writeBackup(t, src, secret)

	dst := t.TempDir()
	dbPath := filepath.Join(dst, "glowworm.db")
	sum, err := Restore(zipPath, dst, dbPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Owner != "owner" || sum.Accounts != 1 || sum.Transactions != 1 || sum.ReplacedDir != "" {
		t.Fatalf("summary = %+v", sum)
	}
	key, _ := os.ReadFile(filepath.Join(dst, "secret.key"))
	if string(key) != secret.Hex() {
		t.Error("secret.key not restored")
	}
	for _, leftover := range []string{dbPath + "-wal", dbPath + ".restoring"} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("%s left behind", leftover)
		}
	}
	db, err := store.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var desc string
	if err := db.QueryRow(`SELECT description FROM transactions WHERE id = 't1'`).Scan(&desc); err != nil || desc != "TESCO" {
		t.Errorf("restored data missing: %q %v", desc, err)
	}
	db.Close()
}

func TestRestoreSafety(t *testing.T) {
	src, secret := seededInstance(t)
	zipPath := writeBackup(t, src, secret)
	dbPath := filepath.Join(src, "glowworm.db")

	release, err := store.LockDataDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(zipPath, src, dbPath, true); err == nil || !strings.Contains(err.Error(), "running") {
		t.Errorf("restore must refuse while the data dir is locked, got %v", err)
	}
	release()

	if _, err := Restore(zipPath, src, dbPath, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("restore over existing data needs --force, got %v", err)
	}
	sum, err := Restore(zipPath, src, dbPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sum.ReplacedDir, "glowworm.db")); err != nil {
		t.Errorf("previous database should be moved aside: %v", err)
	}

	bogus := filepath.Join(t.TempDir(), "bogus.zip")
	os.WriteFile(bogus, []byte("not a zip"), 0o600)
	if _, err := Restore(bogus, t.TempDir(), filepath.Join(t.TempDir(), "x.db"), false); err == nil {
		t.Error("a non-backup file must be rejected")
	}
}
