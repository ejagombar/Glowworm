package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"glowworm/internal/core/accounts"
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

func (a *App) apiAccountsBackup(w http.ResponseWriter, r *http.Request) error {
	b, err := a.Accounts.Backup(nil)
	if err != nil {
		return err
	}
	return a.sendBackup(w, b, "all-accounts")
}

func (a *App) apiAccountBackup(w http.ResponseWriter, r *http.Request) error {
	b, err := a.Accounts.Backup([]string{r.PathValue("id")})
	if err != nil {
		return err
	}
	return a.sendBackup(w, b, b.Accounts[0].Account.Name)
}

func (a *App) apiAccountArchiveBackup(w http.ResponseWriter, r *http.Request) error {
	arc, b, err := a.Accounts.ArchiveBackup(r.PathValue("id"))
	if err != nil {
		return err
	}
	return a.sendBackup(w, b, arc.Name)
}

func (a *App) sendBackup(w http.ResponseWriter, b *accounts.Backup, label string) error {
	name := fmt.Sprintf("glowworm-backup-%s-%s.json", slug(label), a.Clock.Today())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(b)
}

var slugJunk = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "account"
	}
	return s
}

func (a *App) apiAccountsRestore(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, int64(a.Cfg.UploadMaxMB)<<20)
	var b accounts.Backup
	if err := decodeBody(r, &b); err != nil {
		return &accounts.ValidationError{Message: "That file isn't a readable backup (expected the JSON file Glowworm downloads)"}
	}
	res, err := a.Accounts.RestoreBackup(&b)
	if err != nil {
		return err
	}
	writeJSON(w, res)
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
