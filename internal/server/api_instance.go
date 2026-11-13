package server

import (
	"fmt"
	"net/http"
	"time"

	"glowworm/internal/ai"
	"glowworm/internal/backup"
)

func (a *App) setProviders(cat, queries ai.Provider) {
	a.Cat.Provider = cat
	a.Imports.Provider = cat
	a.Query.Provider = queries
}

func (a *App) confirmOwner(r *http.Request, password string) error {
	if a.Cfg.AuthMode != "password" {
		return nil
	}
	sess, ok := a.session(r)
	if !ok {
		return &acctValidationError{Message: "Not signed in"}
	}
	u, err := a.Repos.Users.Get(sess.UserID)
	if err != nil {
		return err
	}
	return a.confirmPassword(r, password, u.PasswordHash, "Password is incorrect")
}

// confirmPassword checks a password typed to confirm something while signed
// in. Wrong ones count against the address like failed sign-ins do, so a
// borrowed session can't be used to guess the password without limit.
func (a *App) confirmPassword(r *http.Request, password, hash, wrong string) error {
	key := "login:" + limitKey(clientIP(r, a.Cfg.TrustedProxies))
	if a.loginLimiter.Blocked(key) {
		return &acctValidationError{Message: "Too many wrong passwords. Wait 15 minutes and try again."}
	}
	if !verifyPassword(password, hash) {
		a.loginLimiter.Fail(key)
		return &acctValidationError{Message: wrong}
	}
	a.loginLimiter.Reset(key)
	return nil
}

func (a *App) apiInstanceBackup(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Password string `json:"password"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if err := a.confirmOwner(r, in.Password); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, backup.FileName(time.Now())))
	w.Header().Set("Cache-Control", "no-store")
	if err := backup.Write(w, a.DB, a.Secret, a.Version); err != nil {
		a.Log.Error("full backup failed", "err", err)
		return nil
	}
	a.Log.Info("full backup downloaded", "remote", clientIP(r, a.Cfg.TrustedProxies))
	return nil
}

func (a *App) apiInstanceDelete(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if in.Confirm != "DELETE" {
		return &acctValidationError{Message: "Type DELETE to confirm"}
	}
	if err := a.confirmOwner(r, in.Password); err != nil {
		return err
	}
	if err := a.DB.Reset(); err != nil {
		return err
	}
	a.Cat.Model = nil
	a.setProviders(resolveProviders(a.DB, a.Secret, a.Log))
	a.Log.Warn("glowworm account deleted: all data erased", "remote", clientIP(r, a.Cfg.TrustedProxies))
	clearSession(w, r)
	writeJSON(w, map[string]any{"ok": true})
	return nil
}
