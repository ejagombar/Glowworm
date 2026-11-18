package server

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"glowworm/internal/clock"
	"glowworm/internal/config"
	"glowworm/internal/core"
	"glowworm/internal/core/accounts"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/auditexec"
	"glowworm/internal/core/categories"
	"glowworm/internal/core/categoriser"
	"glowworm/internal/core/changes"
	"glowworm/internal/core/goals"
	"glowworm/internal/core/grouping"
	"glowworm/internal/core/imports"
	"glowworm/internal/core/notify"
	"glowworm/internal/core/query"
	"glowworm/internal/core/recurring"
	"glowworm/internal/core/settings"
	"glowworm/internal/core/setup"
	"glowworm/internal/core/transactions"
	"glowworm/internal/core/transfers"
	"glowworm/internal/crypto"
	"glowworm/internal/fx"
	"glowworm/internal/render"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"

	staticFS "glowworm/web/static"
)

type App struct {
	Cfg    config.Config
	Clock  *clock.Clock
	Secret *crypto.Secret
	DB     *store.DB
	Render *render.Renderer
	Log    *slog.Logger

	Repos        *Repos
	Accounts     *accounts.Service
	Categories   *categories.Service
	Transactions *transactions.Service
	Settings     *settings.Service
	Setup        *setup.Service
	Audit        *audit.Engine
	Cat          *categoriser.Service
	Imports      *imports.Service
	Transfers    *transfers.Service
	Recurring    *recurring.Service
	Grouping     *grouping.Service
	Changes      *changes.Service
	Query        *query.Service
	Goals        *goals.Service
	Notify       *notify.Service
	Reminders    *notify.Reminders

	AuditWriter  *audit.Writer
	Exec         *auditexec.Executor
	loginLimiter *limiter
	swLogLimiter *limiter
	version      *dataVersion
	// derivedMu runs the jobs that rebuild data derived from transactions
	// (groups, transfers, recurring series, the classifier) one at a time:
	// each reads everything and then rewrites it, so two at once, from two
	// devices saving together, could write stale results or log a new
	// series twice.
	derivedMu sync.Mutex
	Version   string
}

type Repos struct {
	Users        *repos.Users
	Settings     *repos.Settings
	AIConfig     *repos.AIConfig
	Accounts     *repos.Accounts
	Categories   *repos.Categories
	Transactions *repos.Transactions
	Passkeys     *repos.Passkeys
}

func New(cfg config.Config, db *store.DB, secret *crypto.Secret, log *slog.Logger) (*App, error) {
	r, err := render.New()
	if err != nil {
		return nil, err
	}
	clk := clock.New(cfg.Timezone)
	rp := &Repos{
		Users:        &repos.Users{DB: db},
		Settings:     &repos.Settings{DB: db},
		AIConfig:     &repos.AIConfig{DB: db},
		Accounts:     &repos.Accounts{DB: db},
		Categories:   &repos.Categories{DB: db},
		Transactions: &repos.Transactions{DB: db},
		Passkeys:     &repos.Passkeys{DB: db},
	}
	writer := &audit.Writer{DB: db}
	app := &App{
		Cfg: cfg, Clock: clk, Secret: secret, DB: db, Render: r, Log: log,
		Repos:        rp,
		AuditWriter:  writer,
		Audit:        audit.NewEngine(db, writer),
		loginLimiter: newLimiter(5, 15*time.Minute),
		swLogLimiter: newLimiter(20, time.Minute),
		version:      newDataVersion(dataVersionPath(cfg.DataDir)),
	}
	app.Accounts = &accounts.Service{DB: db, Repos: rp.Accounts, Txs: rp.Transactions, Audit: writer, Clock: clk}
	app.Categories = &categories.Service{DB: db, Repos: rp.Categories, Txs: rp.Transactions, Audit: writer}
	app.Transactions = &transactions.Service{DB: db, Repos: rp.Transactions, Categories: rp.Categories, Audit: writer, Clock: clk}
	app.Settings = &settings.Service{DB: db, Repos: rp.Settings, Audit: writer}
	app.Setup = &setup.Service{DB: db, Users: rp.Users, Settings: rp.Settings, Audit: writer}

	provider, queryProvider := resolveProviders(db, secret, log)
	app.Cat = &categoriser.Service{
		DB: db, Categories: rp.Categories, Settings: rp.Settings,
		Audit: writer, Provider: provider,
	}
	if model, err := app.Cat.LoadLatestModel(); err == nil {
		app.Cat.Model = model
	}
	displayCC := rp.Settings.GetString("currency")
	if displayCC == "" {
		displayCC = "GBP"
	}
	app.Imports = &imports.Service{
		DB: db, Accounts: rp.Accounts, Txs: rp.Transactions, Audit: writer,
		Cat: app.Cat, Provider: provider, DisplayCC: displayCC, Log: log,
	}
	if cfg.ExchangeRates != "off" {
		app.Imports.FX = &fx.Frankfurter{DB: db}
	}
	app.Imports.AfterCommit = app.afterImport
	app.Accounts.AfterChange = app.refreshDerived
	app.Transfers = &transfers.Service{
		DB: db, Txs: rp.Transactions, Accounts: rp.Accounts, Audit: writer,
		Settings: rp.Settings, Clock: clk,
	}
	app.Recurring = &recurring.Service{DB: db, Txs: rp.Transactions, Audit: writer}
	app.Grouping = &grouping.Service{DB: db, Audit: writer}
	app.Changes = &changes.Service{DB: db, Transactions: app.Transactions, Transfers: app.Transfers, Audit: writer}
	app.Query = &query.Service{
		DB: db, Provider: queryProvider, Clock: clk,
		Repo: &query.ConvRepo{DB: db},
	}
	app.Goals = &goals.Service{DB: db, Audit: writer, Currency: func() string { return app.Settings.Get().Currency }}
	app.Notify = &notify.Service{DB: db, Secret: secret}
	app.Reminders = &notify.Reminders{
		DB: db, Notify: app.Notify, Book: app.book, Now: clk.TodayTime,
		Log: log.With("job", "reminders"),
	}
	app.startMidnightSummariser(log)
	app.Exec = &auditexec.Executor{
		DB: db, Accounts: rp.Accounts, Categories: rp.Categories, Txs: rp.Transactions,
	}
	if err := app.SeedExamples(); err != nil {
		return nil, fmt.Errorf("seed examples: %w", err)
	}
	if err := app.bootstrapOwner(); err != nil {
		return nil, err
	}
	// Learnt rules and recurring series are derived from transactions;
	// rebuilding them picks up any change a migration or a fix made.
	if err := app.Cat.RefreshRules(); err != nil {
		log.Error("rules refresh", "err", err)
	}
	// Grouping first: recurring series follow the groups.
	app.regroup()
	if _, err := app.Recurring.Detect(); err != nil {
		log.Error("recurring detection", "err", err)
	}
	return app, nil
}

// bootstrapOwner creates the account from owner_username/owner_password when
// no account exists yet; with neither set, the web UI asks on first visit.
func (a *App) bootstrapOwner() error {
	if a.Cfg.OwnerUsername == "" && a.Cfg.OwnerPassword == "" {
		return nil
	}
	if a.Audit.HasUser() {
		return nil
	}
	if _, err := a.Setup.CreateOwner(a.Cfg.OwnerUsername, a.Cfg.OwnerPassword, "", core.ActorSystem, "from config"); err != nil {
		return fmt.Errorf("owner account from config: %w", err)
	}
	a.Log.Info("owner account created from config — you can remove owner_password now", "username", a.Cfg.OwnerUsername)
	return nil
}

func (a *App) applyEntry(tx *sql.Tx, ent *audit.AuditRow, reapply bool) error {
	return a.Exec.Apply(tx, ent, reapply)
}

func (a *App) startMidnightSummariser(log *slog.Logger) {
	go func() {
		for {
			now := time.Now()
			next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
			timer := time.NewTimer(time.Until(next))
			<-timer.C
			a.Query.CatchUpSummaries()
		}
	}()
}

func (a *App) Router() http.Handler {
	mux := http.NewServeMux()

	assets := loadStatic(staticFS.FS)
	static := assets.handler("/static/")
	mux.Handle("GET /static/{path...}", static)
	mux.HandleFunc("GET /static/splash/{name}", func(w http.ResponseWriter, r *http.Request) {
		data, ok := staticFS.Splash(r.PathValue("name"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		_, _ = w.Write(data)
	})
	mux.Handle("GET /sw.js", assets.serviceWorker())
	mux.HandleFunc("POST /sw-log", a.handleSWLog)
	// Browsers and iOS ask for these at the root, whatever the page links to.
	for path, file := range map[string]string{
		"/manifest.webmanifest":             "manifest.webmanifest",
		"/favicon.ico":                      "icons/favicon-32.png",
		"/apple-touch-icon.png":             "icons/apple-touch-icon.png",
		"/apple-touch-icon-precomposed.png": "icons/apple-touch-icon.png",
	} {
		file := file
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/static/" + file
			static.ServeHTTP(w, r2)
		})
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("GET /{$}", a.page(a.handleDashboard))
	mux.HandleFunc("GET /transactions", a.page(a.handleTransactions))
	mux.HandleFunc("GET /transactions/rows", a.page(a.handleTransactionsRows))
	mux.HandleFunc("GET /queries", a.page(a.handleQueries))
	mux.HandleFunc("GET /savings", a.page(a.handleSavings))
	mux.HandleFunc("GET /import", a.page(a.handleImport))
	mux.HandleFunc("GET /history", a.page(a.handleHistory))
	mux.HandleFunc("GET /history/pane", a.page(a.handleHistoryPane))
	mux.HandleFunc("GET /settings", a.page(a.handleSettings))
	mux.HandleFunc("GET /login", a.handleLoginPage)
	mux.HandleFunc("POST /login", a.handleLoginSubmit)
	mux.HandleFunc("POST /login/passkey/begin", a.handlePasskeyLoginBegin)
	mux.HandleFunc("POST /login/passkey/finish", a.handlePasskeyLoginFinish)
	mux.HandleFunc("POST /logout", a.handleLogout)
	mux.HandleFunc("GET /unlock", a.handleUnlockPage)
	mux.HandleFunc("GET /setup", a.handleSetupPage)
	mux.HandleFunc("POST /setup", a.handleSetupSubmit)

	a.registerAPI(mux)

	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"version": a.version.String()})
	})

	handler := a.version.middleware(mux)
	if a.Cfg.AuthMode == "password" {
		handler = a.withAuth(handler)
	}
	handler = securityHeaders(handler)
	handler = withLogging(a.Log, handler)
	return handler
}

func (a *App) page(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			a.Log.Error("page", "path", r.URL.Path, "err", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
	}
}

// publicPaths are served without a session: health checks, the service worker
// and its diagnostics, and the icons and manifest a home screen app fetches.
var publicPaths = map[string]bool{
	"/healthz": true, "/sw.js": true, "/sw-log": true, "/manifest.webmanifest": true,
	"/favicon.ico": true, "/apple-touch-icon.png": true, "/apple-touch-icon-precomposed.png": true,
}

// handleSWLog writes the service worker's diagnostics (what happened to page
// loads while the server was out of reach) to the server log. Capped, and it
// only ever logs, so it needs no session; each address gets a few posts a
// minute, so it can't be used to fill the log either.
func (a *App) handleSWLog(w http.ResponseWriter, r *http.Request) {
	if !a.swLogLimiter.Allow(limitKey(clientIP(r, a.Cfg.TrustedProxies))) {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	var in struct {
		UA     string `json:"ua"`
		Events []struct {
			T int64  `json:"t"`
			E string `json:"e"`
		} `json:"events"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if len(in.Events) > 100 {
		in.Events = in.Events[len(in.Events)-100:]
	}
	for _, e := range in.Events {
		msg := e.E
		if len(msg) > 300 {
			msg = msg[:300]
		}
		a.Log.Info("sw", "at", time.UnixMilli(e.T).Format(time.RFC3339), "event", msg, "ua", truncate(in.UA, 120))
	}
	w.WriteHeader(http.StatusNoContent)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func dataVersionPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "data-version")
}

// session reads the signed-in session, checking its user still exists and
// hasn't changed password since it signed in.
func (a *App) session(r *http.Request) (*Session, bool) {
	sess, ok := readSession(r, a.Secret)
	if !ok {
		return nil, false
	}
	u, err := a.Repos.Users.Get(sess.UserID)
	if err != nil {
		return nil, false
	}
	fp := passwordFingerprint(u.PasswordHash)
	if subtle.ConstantTimeCompare([]byte(fp), []byte(sess.Fingerprint)) != 1 {
		return nil, false
	}
	return sess, true
}

func (a *App) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/login" || strings.HasPrefix(path, "/login/passkey/") || strings.HasPrefix(path, "/static/") || publicPaths[path] {
			next.ServeHTTP(w, r)
			return
		}
		if !a.Audit.HasUser() {
			if path == "/setup" {
				next.ServeHTTP(w, r)
				return
			}
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		if a.Audit.HasUser() && path == "/setup" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		sess, ok := a.session(r)
		if !ok {
			if strings.HasPrefix(path, "/api/") {
				http.Error(w, `{"error":{"code":"auth_required","message":"Login required"}}`, http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if !checkCSRF(r) {
				if strings.HasPrefix(path, "/api/") {
					http.Error(w, `{"error":{"code":"csrf","message":"Missing or invalid CSRF token"}}`, http.StatusForbidden)
					return
				}
				http.Error(w, "Invalid CSRF token", http.StatusForbidden)
				return
			}
		}
		clearLockCookies(w, r)
		renewSession(w, r, a.Secret, sess)
		next.ServeHTTP(w, r)
	})
}

// afterImport runs the follow-on detection and retraining, then ties the
// system entries written since the import
// to it, so undoing the import undoes them too.
func (a *App) afterImport(batchID string) {
	// Held through LinkCause, so only this import's follow-on entries are
	// tied to it, not another device's written meanwhile.
	a.derivedMu.Lock()
	defer a.derivedMu.Unlock()
	a.refreshDerivedLocked()
	var cause int64
	_ = a.DB.QueryRow(`SELECT COALESCE(MAX(id),0) FROM audit_entries WHERE action = 'import.confirmed' AND entity_id = ?`, batchID).Scan(&cause)
	if err := a.AuditWriter.LinkCause(cause); err != nil {
		a.Log.Error("link import side effects", "err", err)
	}
}

// afterHistoryChange rebuilds what is derived from the data after an undo,
// rewind, redo or discard. It writes no audit entries: the change being
// undone or redone is already in the log.
func (a *App) afterHistoryChange() {
	a.derivedMu.Lock()
	defer a.derivedMu.Unlock()
	if err := a.Cat.RefreshRules(); err != nil {
		a.Log.Error("rules refresh", "err", err)
	}
	a.regroupLocked()
	if _, err := a.Recurring.Detect(); err != nil {
		a.Log.Error("recurring detection", "err", err)
	}
	a.Cat.RetrainQuiet()
}

// afterGroupRulesChange finds recurring series again, as they follow the
// groups a rule change has just moved transactions between.
func (a *App) afterGroupRulesChange() {
	a.derivedMu.Lock()
	defer a.derivedMu.Unlock()
	if _, err := a.Recurring.Detect(); err != nil {
		a.Log.Error("recurring detection", "err", err)
	}
}

// regroup puts transactions in their groups by the grouping rules, after
// anything that adds transactions or changes their descriptions.
func (a *App) regroup() {
	a.derivedMu.Lock()
	defer a.derivedMu.Unlock()
	a.regroupLocked()
}

func (a *App) regroupLocked() {
	if err := a.Grouping.Apply(); err != nil {
		a.Log.Error("grouping", "err", err)
	}
}

func (a *App) refreshDerived() {
	a.derivedMu.Lock()
	defer a.derivedMu.Unlock()
	a.refreshDerivedLocked()
}

func (a *App) refreshDerivedLocked() {
	a.regroupLocked()
	if n, err := a.Transfers.Detect(); err == nil && n > 0 {
		a.Log.Info("transfer detection", "linked", n)
	}
	if n, err := a.Recurring.Detect(); err == nil && n > 0 {
		a.Log.Info("recurring detection", "series", n)
	}
	if err := a.Cat.RefreshRules(); err != nil {
		a.Log.Error("rules refresh", "err", err)
	}
	if _, err := a.Cat.Retrain(); err != nil {
		a.Log.Info("retrain skipped", "reason", err.Error())
	}
}
