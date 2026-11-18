// Package firstrun covers the first visit to a new Glowworm: the token that
// keeps strangers off the setup page, the address to open, and the QR code
// and banner that show it in the terminal.
package firstrun

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Off, as GLOWWORM_SETUP_TOKEN, opens the setup page to anyone who reaches
// it first. Only for an instance nobody else can reach.
const Off = "off"

const settingKey = "setup_token"

// Token is what the setup page asks for while no account exists: the
// configured token, none when that is "off", or otherwise one Glowworm makes
// and keeps until the account is created (see Clear). It's kept in the
// database, so the server and `glowworm setup-url` agree on it, and deleting
// the account brings a new one.
func Token(db *sql.DB, configured string) (string, error) {
	switch configured {
	case Off:
		return "", nil
	case "":
	default:
		return configured, nil
	}
	if t, err := stored(db); err != nil || t != "" {
		return t, err
	}
	v, _ := json.Marshal(rand.Text())
	if _, err := db.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO NOTHING`, settingKey, string(v), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return "", err
	}
	// Read it back: a second process may have saved its own first.
	return stored(db)
}

func stored(db *sql.DB) (string, error) {
	var raw string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, settingKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var t string
	if json.Unmarshal([]byte(raw), &t) != nil {
		return "", nil
	}
	return t, nil
}

// Clear drops the made-up token once the account exists; it's no use after.
func Clear(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM settings WHERE key = ?`, settingKey)
	return err
}
