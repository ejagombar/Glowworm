package server

import (
	"net/http"

	"golang.org/x/crypto/bcrypt"
)

type acctValidationError struct{ Message string }

func (e *acctValidationError) Error() string { return e.Message }

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

func (a *App) apiAccountArchives(w http.ResponseWriter, r *http.Request) error {
	list, err := a.Accounts.Archives()
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": list})
	return nil
}

func (a *App) apiAccountArchiveRestore(w http.ResponseWriter, r *http.Request) error {
	res, err := a.Accounts.RestoreArchive(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, res)
	return nil
}

func (a *App) apiAccountArchivePurge(w http.ResponseWriter, r *http.Request) error {
	if err := a.Accounts.PurgeArchive(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}
