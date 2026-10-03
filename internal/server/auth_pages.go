package server

import (
	"fmt"
	"net/http"
	"strings"

	"glowworm/internal/core"
)

var _ = core.ActorUser

type auditEntryView struct {
	ID        int64
	Timestamp string
	Actor     string
	Kind      string // you | ai | import
	Title     string
	Sub       string
	DiffLines []core.DiffLine
	State     string
	Revertible  bool
	Jumpable    bool
	Forgettable bool
	Deletable   bool
}

func toEntryViews(entries []core.AuditEntry) []auditEntryView {
	out := make([]auditEntryView, 0, len(entries))
	for _, e := range entries {
		kind := "you"
		switch e.Actor {
		case "ai":
			kind = "ai"
		case "system":
			if strings.HasPrefix(e.Action, "import") {
				kind = "import"
			} else {
				kind = "ai"
			}
		}
		out = append(out, auditEntryView{
			ID: e.ID, Timestamp: e.Timestamp, Actor: actorLabel(e.Actor, e.ActorDetail), Kind: kind,
			Title: e.Title, Sub: e.Sub, DiffLines: e.DiffLines, State: e.State,
		})
	}
	return out
}

func actorLabel(actor, detail string) string {
	switch actor {
	case "user":
		return "You"
	case "ai":
		if detail != "" {
			return "AI"
		}
		return "AI"
	default:
		return "Glowworm"
	}
}

// seriesJSON builds a line-chart payload for the timeline (minor units).
func seriesJSON(labels []string, values []int64) string {
	return fmt.Sprintf(`{"labels":%s,"series":[{"name":"Balance","color":"#0e6f51","values":%s}]}`,
		jsonSlice(labels), jsonInts(values))
}

// handleLoginPage renders the login view (doc 04 §5.9).
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
	token := CSRFToken(w, r, a.Secret)
	errMsg := ""
	if r.URL.Query().Get("e") == "1" {
		errMsg = "Incorrect username or password."
	}
	data := struct {
		Title     string
		CSRFToken string
		Error     string
		Standalone bool
	}{Title: "Sign in", CSRFToken: token, Error: errMsg, Standalone: true}
	if err := a.Render.Render(w, "login", data); err != nil {
		a.Log.Error("login page", "err", err)
	}
}

// handleLoginSubmit verifies credentials (doc 14 §2).
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
		// Constant-time-ish: still run a bcrypt compare against a dummy hash.
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

// handleLogout clears the session (doc 03 §2).
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if a.Cfg.AuthMode == "password" && !checkCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}
	clearSession(w, r)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// handleSetupPage hosts the first-run wizard (doc 12 §3).
func (a *App) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	if a.Audit.HasUser() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if a.Cfg.SetupToken != "" {
		if r.URL.Query().Get("token") != a.Cfg.SetupToken {
			http.Error(w, "Setup token required. Check the server log for the first-run token.", http.StatusForbidden)
			return
		}
	}
	base, err := a.base(w, r, "dashboard", "Setup")
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	base.SetupOpen = true
	defaults := []struct{ Name, Color string }{
		{"Transport", "#4f5bc7"}, {"Food & Dining", "#c9822e"}, {"Bills & Utilities", "#c0483f"},
		{"Entertainment", "#7a58b8"}, {"Shopping", "#2f7fb0"}, {"Health", "#0e7a4d"},
	}
	wizardCats := make([]core.Category, 0, len(defaults))
	for i, d := range defaults {
		wizardCats = append(wizardCats, core.Category{Name: d.Name, Color: d.Color, SortOrder: i})
	}
	data := struct {
		*viewBase
		WizardCats []core.Category
	}{viewBase: base, WizardCats: wizardCats}
	if err := a.Render.Render(w, "setup", data); err != nil {
		a.Log.Error("setup page", "err", err)
	}
}