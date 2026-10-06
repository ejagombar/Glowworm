package server

import (
	"errors"
	"fmt"
	"net/http"

	"glowworm/internal/core/setup"
)

func seriesJSON(labels []string, values []int64) string {
	return fmt.Sprintf(`{"labels":%s,"series":[{"name":"Balance","color":"#0e6f51","values":%s}]}`,
		jsonSlice(labels), jsonInts(values))
}

func (a *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if a.Cfg.AuthMode != "password" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if a.Audit.HasUser() {
		if _, ok := readSession(r, a.Secret); ok {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	} else {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	errMsg := ""
	if r.URL.Query().Get("e") == "1" {
		errMsg = "Incorrect username or password."
	}
	base, err := a.base(w, r, "login", "Sign in")
	if err != nil {
		a.Log.Error("login page", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	base.Standalone = true
	data := struct {
		*viewBase
		Error string
	}{viewBase: base, Error: errMsg}
	if err := a.Render.Render(w, "login", data); err != nil {
		a.Log.Error("login page", "err", err)
	}
}

func (a *App) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if a.Cfg.AuthMode != "password" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?e=1", http.StatusFound)
		return
	}
	ip := clientIP(r, a.Cfg.TrustedProxies)
	if !a.loginLimiter.Allow("login:" + ip) {
		http.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}
	username := r.PostFormValue("username")
	password := r.PostFormValue("password")
	user, err := a.Repos.Users.ByUsername(username)
	if err != nil {
		_ = verifyPassword(password, "$2a$12$C6UzMDM.H6dfI/f/IKcEe.6uGRJpDg196n4z1S3z3WBGWl7LuOYeK")
		http.Redirect(w, r, "/login?e=1", http.StatusFound)
		return
	}
	if !verifyPassword(password, user.PasswordHash) {
		http.Redirect(w, r, "/login?e=1", http.StatusFound)
		return
	}
	issueSession(w, r, a.Secret, user.ID)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if a.Cfg.AuthMode == "password" && !checkCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}
	clearSession(w, r)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// The first visit with no owner gets an account form instead of the sign-in
// page. The owner can also come from config or `glowworm user create`.
func (a *App) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	a.renderSetup(w, r, "", "")
}

func (a *App) handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	if !a.setupAllowed(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil || !checkCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}
	ip := clientIP(r, a.Cfg.TrustedProxies)
	if !a.loginLimiter.Allow("setup:" + ip) {
		http.Error(w, "Too many attempts. Try again later.", http.StatusTooManyRequests)
		return
	}
	username := r.PostFormValue("username")
	u, err := a.Setup.CreateAccount(setup.AccountInput{
		Username: username, Password: r.PostFormValue("password"),
		PasswordConfirm: r.PostFormValue("password_confirm"),
	})
	if err != nil {
		var ve *setup.ValidationError
		if errors.As(err, &ve) {
			a.renderSetup(w, r, ve.Message, username)
			return
		}
		a.Log.Error("setup", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	issueSession(w, r, a.Secret, u.ID)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (a *App) setupAllowed(w http.ResponseWriter, r *http.Request) bool {
	if a.Cfg.AuthMode != "password" || a.Audit.HasUser() {
		http.Redirect(w, r, "/", http.StatusFound)
		return false
	}
	if a.Cfg.SetupToken != "" && r.URL.Query().Get("token") != a.Cfg.SetupToken {
		http.Error(w, "Setup token required. Check the server log for the first-run token.", http.StatusForbidden)
		return false
	}
	return true
}

func (a *App) renderSetup(w http.ResponseWriter, r *http.Request, errMsg, username string) {
	if !a.setupAllowed(w, r) {
		return
	}
	base, err := a.base(w, r, "setup", "Create account")
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	base.Standalone = true
	data := struct {
		*viewBase
		Error, Username, Token string
	}{viewBase: base, Error: errMsg, Username: username, Token: r.URL.Query().Get("token")}
	if err := a.Render.Render(w, "setup", data); err != nil {
		a.Log.Error("setup page", "err", err)
	}
}
