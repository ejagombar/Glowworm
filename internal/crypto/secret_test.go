package crypto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateKeepsTheKey(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreate("", dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("secret.key mode %v, want 0600", st.Mode().Perm())
	}
	again, err := LoadOrCreate("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Hex() != first.Hex() {
		t.Error("a second start made a different key")
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.key.new")); !os.IsNotExist(err) {
		t.Error("temporary key file left behind")
	}
}

// A key that exists but can't be read must stop the start, not be replaced:
// a new key would lock the saved AI key away and sign everyone out.
func TestLoadOrCreateNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.key")
	// A directory in the key's place: reading it fails with something other
	// than "not found", whoever runs the test.
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate("", dir); err == nil {
		t.Fatal("an unreadable key was accepted")
	}
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		t.Fatal("the unreadable key was replaced")
	}
}

func TestEmptyKeyRejected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(p, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate("", dir); err == nil {
		t.Fatal("an empty key file was accepted")
	}
	if b, _ := os.ReadFile(p); string(b) != "\n" {
		t.Error("the empty key file was rewritten")
	}
	if _, err := Parse("  "); err == nil {
		t.Error("Parse accepted an empty key")
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	s, err := Parse("a passphrase")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := s.Encrypt([]byte("sk-secret"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Decrypt(enc)
	if err != nil || string(got) != "sk-secret" {
		t.Fatalf("decrypt: %q %v", got, err)
	}
	other, _ := Parse("another passphrase")
	if _, err := other.Decrypt(enc); err == nil {
		t.Error("another key decrypted it")
	}
	if !s.VerifySign([]byte("x"), s.Sign([]byte("x"))) || s.VerifySign([]byte("y"), s.Sign([]byte("x"))) {
		t.Error("signature check is wrong")
	}
}
