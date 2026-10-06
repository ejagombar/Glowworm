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

func (r *AIConfig) Set(tx *sql.Tx, provider string, models map[string]string, baseURL, keyCipher string) error {
	m, _ := json.Marshal(models)
	if m == nil {
		m = []byte("{}")
	}
	_, err := exec(r.DB, tx, `UPDATE ai_provider_config SET provider=?, models=?, base_url=?, key_cipher=?, updated_at=? WHERE id=1`,
		provider, string(m), baseURL, keyCipher, util.NowStr())
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
