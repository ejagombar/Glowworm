package repos

import (
	"database/sql"
	"encoding/json"

	"glowworm/internal/core"
	"glowworm/internal/store"
	"glowworm/internal/util"
)

type Users struct{ DB *store.DB }

func (r *Users) Count() (int, error) {
	var n int
	err := r.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (r *Users) Create(tx *sql.Tx, u *core.User, passwordHash string) error {
	u.ID = util.NewID()
	u.CreatedAt = util.NowStr()
	u.UpdatedAt = u.CreatedAt
	_, err := exec(r.DB, tx, `INSERT INTO users (id, username, display_name, password_hash, created_at, updated_at)
		VALUES (?,?,?,?,?,?)`, u.ID, u.Username, u.DisplayName, passwordHash, u.CreatedAt, u.UpdatedAt)
	return err
}

func (r *Users) ByUsername(username string) (*core.User, error) {
	var u core.User
	err := r.DB.QueryRow(`SELECT id, username, display_name, password_hash FROM users WHERE username = ? COLLATE NOCASE`, username).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *Users) Get(id string) (*core.User, error) {
	var u core.User
	err := r.DB.QueryRow(`SELECT id, username, display_name, password_hash FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *Users) UpdatePassword(id, passwordHash string) error {
	_, err := r.DB.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, util.NowStr(), id)
	return err
}

type Settings struct{ DB *store.DB }

func (r *Settings) GetString(key string) string {
	var v string
	err := r.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil || v == "" {
		return ""
	}
	var s string
	if json.Unmarshal([]byte(v), &s) == nil {
		return s
	}
	return ""
}

func (r *Settings) GetBool(key string) bool {
	var v string
	err := r.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil || v == "" {
		return false
	}
	var b bool
	if json.Unmarshal([]byte(v), &b) == nil {
		return b
	}
	return false
}

func (r *Settings) GetFloat(key string, def float64) float64 {
	var v string
	err := r.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil || v == "" {
		return def
	}
	var f float64
	if json.Unmarshal([]byte(v), &f) == nil {
		return f
	}
	return def
}

func (r *Settings) All() (map[string]any, error) {
	rows, err := r.DB.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		var decoded any
		if json.Unmarshal([]byte(v), &decoded) == nil {
			out[k] = decoded
		} else {
			out[k] = v
		}
	}
	return out, rows.Err()
}

func (r *Settings) Set(tx *sql.Tx, key string, value any) error {
	b, _ := json.Marshal(value)
	if b == nil {
		b = []byte("null")
	}
	_, err := exec(r.DB, tx, `INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(b), util.NowStr())
	return err
}

type AIConfig struct{ DB *store.DB }

type AIConfigRow struct {
	Provider  string
	Models    map[string]string
	BaseURL   string
	KeyCipher string
}

func (r *AIConfig) Get() (*AIConfigRow, error) {
	var row AIConfigRow
	var models string
	err := r.DB.QueryRow(`SELECT provider, models, base_url, key_cipher FROM ai_provider_config WHERE id = 1`).
		Scan(&row.Provider, &models, &row.BaseURL, &row.KeyCipher)
	if err != nil {
		return nil, err
	}
	row.Models = map[string]string{}
	if models != "" {
		_ = json.Unmarshal([]byte(models), &row.Models)
	}
	return &row, nil
}

// SetModels saves the model chosen for each role, and each one's provider
// under "<role>_provider".
func (r *AIConfig) SetModels(tx *sql.Tx, models map[string]string) error {
	m, _ := json.Marshal(models)
	if m == nil || string(m) == "null" {
		m = []byte("{}")
	}
	_, err := exec(r.DB, tx, `UPDATE ai_provider_config SET models=?, updated_at=? WHERE id=1`, string(m), util.NowStr())
	return err
}

// AIProviderKey is one connected provider: its encrypted key and endpoint.
type AIProviderKey struct {
	Provider  string
	KeyCipher string
	BaseURL   string
}

// Keys returns the connected providers by id.
func (r *AIConfig) Keys() (map[string]*AIProviderKey, error) {
	rows, err := r.DB.Query(`SELECT provider, key_cipher, base_url FROM ai_provider_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*AIProviderKey{}
	for rows.Next() {
		k := &AIProviderKey{}
		if err := rows.Scan(&k.Provider, &k.KeyCipher, &k.BaseURL); err != nil {
			return nil, err
		}
		out[k.Provider] = k
	}
	return out, rows.Err()
}

// SetKey connects a provider, or updates the one already connected.
func (r *AIConfig) SetKey(tx *sql.Tx, provider, keyCipher, baseURL string) error {
	_, err := exec(r.DB, tx, `INSERT INTO ai_provider_keys (provider, key_cipher, base_url, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(provider) DO UPDATE SET key_cipher=excluded.key_cipher, base_url=excluded.base_url, updated_at=excluded.updated_at`,
		provider, keyCipher, baseURL, util.NowStr())
	return err
}

// DeleteKey disconnects a provider.
func (r *AIConfig) DeleteKey(tx *sql.Tx, provider string) error {
	_, err := exec(r.DB, tx, `DELETE FROM ai_provider_keys WHERE provider=?`, provider)
	return err
}

func (r *AIConfig) LogCall(capability, provider, model string, prompt, completion, latency int, ok bool, errMsg string) {
	okInt := 0
	if ok {
		okInt = 1
	}
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	_, _ = r.DB.Exec(`INSERT INTO ai_calls (capability, provider, model, prompt_tokens, completion_tokens, latency_ms, ok, error, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, capability, provider, model, prompt, completion, latency, okInt, errMsg, util.NowStr())
}
