package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type Secret struct {
	key []byte
}

// LoadOrCreate returns the instance key: fromEnv when set, else the one in
// dataDir/secret.key, made on first run. A key file that is there but can't
// be read is an error, never a reason to write a new one over it: the saved
// AI key is encrypted with it and every sign-in is signed with it.
func LoadOrCreate(fromEnv, dataDir string) (*Secret, error) {
	if fromEnv != "" {
		return Parse(fromEnv)
	}
	p := filepath.Join(dataDir, "secret.key")
	b, err := os.ReadFile(p)
	switch {
	case err == nil:
		if trimWhitespace(string(b)) == "" {
			return nil, fmt.Errorf("crypto: %s is empty — restore it from a backup, or delete it to start with a new key (sign-ins and the saved AI key are lost)", p)
		}
		return Parse(string(b))
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("crypto: reading the secret key: %w", err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	hexed := hex.EncodeToString(buf)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	// Written whole under another name first: a crash mid-write must not
	// leave a short or empty key behind.
	tmp := p + ".new"
	if err := os.WriteFile(tmp, []byte(hexed), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return Parse(hexed)
}

// Parse reads a key given as 32 bytes of hex or base64; anything else is
// taken as a passphrase and hashed.
func Parse(raw string) (*Secret, error) {
	if trimWhitespace(raw) == "" {
		// Hashing nothing would give a key anyone can work out.
		return nil, errors.New("crypto: the secret key is empty")
	}
	key, err := decode32(raw)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	return &Secret{key: key}, nil
}

func (s *Secret) Hex() string { return hex.EncodeToString(s.key) }

func decode32(raw string) ([]byte, error) {
	raw = trimWhitespace(raw)
	if b, err := hex.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
		return b, nil
	}
	h := sha256.Sum256([]byte(raw))
	return h[:], nil
}

func trimWhitespace(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			out = append(out, c)
		}
	}
	return string(out)
}

func (s *Secret) Sign(data []byte) string {
	m := hmac.New(sha256.New, s.key)
	m.Write(data)
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Secret) VerifySign(data []byte, sigHex string) bool {
	want := s.Sign(data)
	got, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	wantB, _ := hex.DecodeString(want)
	return hmac.Equal(wantB, got)
}

func (s *Secret) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Secret) Encrypt(plaintext []byte) (string, error) {
	g, err := s.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := g.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (s *Secret) Decrypt(ciphertext string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, err
	}
	g, err := s.gcm()
	if err != nil {
		return nil, err
	}
	if len(raw) < g.NonceSize() {
		return nil, errors.New("crypto: ciphertext too short")
	}
	nonce, body := raw[:g.NonceSize()], raw[g.NonceSize():]
	return g.Open(nil, nonce, body, nil)
}
