package server

import (
	"net/http"

	"golang.org/x/crypto/bcrypt"
)

// acctValidationError is user-facing (mapped to 400 in serviceError).
type acctValidationError struct{ Message string }

func (e *acctValidationError) Error() string { return e.Message }

// apiChangePassword lets the signed-in owner change their password
// (doc 14: >=10 chars, current password verified).
func (a *App) apiChangePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	sess, ok := readSession(r, a.Secret)
	if !ok {
		return &acctValidationError{Message: "Not signed in"}
	}
	u, err := a.Repos.Users.Get(sess.UserID)
	if err != nil {
		return err
	}
	if !verifyPassword(in.Current, u.PasswordHash) {
		return &acctValidationError{Message: "Current password is incorrect"}
	}
	if len(in.New) < 10 {
		return &acctValidationError{Message: "New password must be at least 10 characters"}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.New), 12)
	if err != nil {
		return err
	}
	if err := a.Repos.Users.UpdatePassword(u.ID, string(hash)); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}
