// Package crypto owns the instance secret key: session cookie signing and
// AES-256-GCM encryption of AI provider API keys at rest (doc 14 §6, doc 09 §4).
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
	"os"
	"path/filepath"
)

// Secret is the 32-byte instance master key.
type Secret struct {
	key []byte
}

// LoadOrCreate reads GLOWWORM_SECRET_KEY or data-dir/secret.key,
// generating and persisting a random key on first boot (0600).
func LoadOrCreate(fromEnv, dataDir string) (*Secret, error) {
	raw := fromEnv
	if raw == "" {
		p := filepath.Join(dataDir, "secret.key")
		if b, err := os.ReadFile(p); err == nil {
			raw = string(b)
		} else {
			buf := make([]byte, 32)
			if _, err := rand.Read(buf); err != nil {
				return nil, err
			}
			hexed := hex.EncodeToString(buf)
			if err := os.MkdirAll(dataDir, 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(p, []byte(hexed), 0o600); err != nil {
				return nil, err
			}
			raw = hexed
		}
	}
	key, err := decode32(raw)
	if err != nil {
		return nil, fmt.Errorf("crypto: %w", err)
	}
	return &Secret{key: key}, nil
}

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
	// Fallback: derive from arbitrary-length secret material.
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

// Sign returns HMAC-SHA256(data) hex — session token signing.
func (s *Secret) Sign(data []byte) string {
	m := hmac.New(sha256.New, s.key)
	m.Write(data)
	return hex.EncodeToString(m.Sum(nil))
}

// VerifySign checks an HMAC hex signature in constant time.
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

// Encrypt seals plaintext with AES-256-GCM (nonce prefixed).
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

// Decrypt opens an AES-256-GCM ciphertext produced by Encrypt.
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
