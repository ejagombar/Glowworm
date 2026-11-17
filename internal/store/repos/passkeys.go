package repos

import (
	"glowworm/internal/store"
	"glowworm/internal/util"
)

// Passkey is a passkey that unlocks Glowworm. Credential is the go-webauthn
// Credential as JSON; this package doesn't look inside it.
type Passkey struct {
	ID           string
	UserID       string
	CredentialID string // base64url, to look it up by
	Credential   string
	Name         string
	CreatedAt    string
	LastUsedAt   string
}

type Passkeys struct{ DB *store.DB }

func (r *Passkeys) ForUser(userID string) ([]*Passkey, error) {
	rows, err := r.DB.Query(`SELECT id, user_id, credential_id, credential, name, created_at, COALESCE(last_used_at, '')
		FROM passkeys WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Passkey
	for rows.Next() {
		var p Passkey
		if err := rows.Scan(&p.ID, &p.UserID, &p.CredentialID, &p.Credential, &p.Name, &p.CreatedAt, &p.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// Any reports whether anyone has a passkey, so the sign-in page offers one.
func (r *Passkeys) Any() (bool, error) {
	var ok bool
	err := r.DB.QueryRow(`SELECT EXISTS (SELECT 1 FROM passkeys)`).Scan(&ok)
	return ok, err
}

func (r *Passkeys) Add(p *Passkey) error {
	p.ID = util.NewID()
	p.CreatedAt = util.NowStr()
	_, err := r.DB.Exec(`INSERT INTO passkeys (id, user_id, credential_id, credential, name, created_at)
		VALUES (?,?,?,?,?,?)`, p.ID, p.UserID, p.CredentialID, p.Credential, p.Name, p.CreatedAt)
	return err
}

// Used stores the credential after a sign-in (its sign count moves on).
func (r *Passkeys) Used(credentialID, credential string) error {
	_, err := r.DB.Exec(`UPDATE passkeys SET credential = ?, last_used_at = ? WHERE credential_id = ?`,
		credential, util.NowStr(), credentialID)
	return err
}

// Delete reports whether there was such a passkey.
func (r *Passkeys) Delete(userID, id string) (bool, error) {
	res, err := r.DB.Exec(`DELETE FROM passkeys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
