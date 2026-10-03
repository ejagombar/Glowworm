// Package server assembles the HTTP app (doc 01 §5): pages, fragments and
// the JSON API under /api/v1, with static assets embedded and cached.
package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"glowworm/internal/ai"
	"glowworm/internal/clock"
	"glowworm/internal/config"
	"glowworm/internal/core/accounts"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/auditexec"
	"glowworm/internal/core/categories"
	"glowworm/internal/core/categoriser"
	"glowworm/internal/core/imports"
	"glowworm/internal/core/query"
	"glowworm/internal/core/recurring"
	"glowworm/internal/core/settings"
	"glowworm/internal/core/setup"
	"glowworm/internal/core/transactions"
	"glowworm/internal/core/transfers"
	"glowworm/internal/crypto"
	"glowworm/internal/render"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"

	htmxFS "glowworm/web/htmx"
	staticFS "glowworm/web/static"
)

// App wires every layer together.
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
	Query        *query.Service

	AuditWriter  *audit.Writer
	Exec         *auditexec.Executor
	loginLimiter *limiter
}

// Repos bundles the repo structs.
type Repos struct {
	Users        *repos.Users
	Settings     *repos.Settings
	AIConfig     *repos.AIConfig
	Accounts     *repos.Accounts
	Categories   *repos.Categories
	Transactions *repos.Transactions
}

// New builds the app from resolved config.
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
	}
	writer := &audit.Writer{DB: db}
	app := &App{
		Cfg: cfg, Clock: clk, Secret: secret, DB: db, Render: r, Log: log,
		Repos:        rp,
		AuditWriter:  writer,
		Audit:        audit.NewEngine(db, writer),
		loginLimiter: newLimiter(5, 15*time.Minute),
	}
	app.Accounts = &accounts.Service{DB: db, Repos: rp.Accounts, Txs: rp.Transactions, Audit: writer, Clock: clk}
	app.Categories = &categories.Service{DB: db, Repos: rp.Categories, Txs: rp.Transactions, Audit: writer}
	app.Transactions = &transactions.Service{DB: db, Repos: rp.Transactions, Categories: rp.Categories, Audit: writer, Clock: clk}
	app.Settings = &settings.Service{DB: db, Repos: rp.Settings, Audit: writer}
	app.Setup = &setup.Service{DB: db, Users: rp.Users, Settings: rp.Settings, Audit: writer}

	// AI provider from encrypted config (doc 09 §4); GLOWWORM_AI_MOCK=1
	// injects the deterministic mock (tests/dev only).
	provider := a_resolveProvider(db, secret, log)
	if os.Getenv("GLOWWORM_AI_MOCK") == "1" {
		log.Info("using mock AI provider (GLOWWORM_AI_MOCK=1)")
		provider = &ai.Mock{}
	}
	app.Cat = &categoriser.Service{
		DB: db, Txs: rp.Transactions, Categories: rp.Categories, Settings: rp.Settings,
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
		Cat: app.Cat, Provider: provider, DisplayCC: displayCC,
	}
	app.Imports.AfterCommit = func(batchID string) {
		if n, err := app.Transfers.Detect(); err == nil && n > 0 {
			log.Info("transfer detection", "linked", n)
		}
		if n, err := app.Recurring.Detect(); err == nil && n > 0 {
			log.Info("recurring detection", "series", n)
		}
		if err := app.Cat.RefreshRules(); err != nil {
			log.Error("rules refresh", "err", err)
		}
		if _, err := app.Cat.Retrain(); err != nil {
			log.Info("retrain skipped", "reason", err.Error())
		}
	}
	app.Transfers = &transfers.Service{
		DB: db, Txs: rp.Transactions, Accounts: rp.Accounts, Audit: writer,
		Settings: rp.Settings, Clock: clk,
		InternalCat: func() (string, error) {
			c, err := rp.Categories.ByName("Internal transfer")
			if err != nil {
				return "", err
			}
			return c.ID, nil
		},
	}
	app.Recurring = &recurring.Service{DB: db, Txs: rp.Transactions, Audit: writer}
	app.Query = &query.Service{
		DB: db, Provider: provider, Clock: clk,
		Repo: &query.ConvRepo{DB: db},
	}
	app.Exec = &auditexec.Executor{
		DB: db, Accounts: rp.Accounts, Categories: rp.Categories, Txs: rp.Transactions,
		InternalCat: func(tx *sql.Tx) (string, error) {
			c, err := rp.Categories.ByName("Internal transfer")
			if err != nil {
				return "", err
			}
			return c.ID, nil
		},
	}
	return app, nil
}

// applyEntry adapts the audit engine's hook to the executor.
func (a *App) applyEntry(tx *sql.Tx, ent *audit.AuditRow, reapply bool) error {
	return a.Exec.Apply(tx, ent, reapply)
}

// a_resolveProvider builds the AI provider from the encrypted config row.
func a_resolveProvider(db *store.DB, secret *crypto.Secret, log *slog.Logger) ai.Provider {
	var provider, models, baseURL, keyCipher string
	err := db.QueryRow(`SELECT provider, models, base_url, key_cipher FROM ai_provider_config WHERE id = 1`).
		Scan(&provider, &models, &baseURL, &keyCipher)
	if err != nil || provider == "" {
		return nil
	}
	modelMap := map[string]string{}
	_ = json.Unmarshal([]byte(models), &modelMap)
	apiKey := ""
	if keyCipher != "" {
		if plain, err := secret.Decrypt(keyCipher); err == nil {
			apiKey = string(plain)
		} else {
			log.Error("ai key decryption failed", "err", err)
		}
	}
	p, err := ai.New(provider, modelMap["default"], baseURL, apiKey)
	if err != nil {
		log.Error("ai provider", "err", err)
		return nil
	}
	return p
}

// Router builds the full route table (doc 01 §5.1, doc 03).
func (a *App) Router() http.Handler {
	mux := http.NewServeMux()

	// Static + vendored assets (strip the URL mount prefix, then read from
	// the embedded FS root).
	mux.Handle("GET /static/{path...}", a.staticHandlerFS(staticFS.FS, "/static/"))
	mux.Handle("GET /htmx/{path...}", a.staticHandlerFS(htmxFS.FS, "/htmx/"))

	// Health.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Pages.
	mux.HandleFunc("GET /{$}", a.page(a.handleDashboard))
	mux.HandleFunc("GET /transactions", a.page(a.handleTransactions))
	mux.HandleFunc("GET /queries", a.page(a.handleQueries))
	mux.HandleFunc("GET /categories", a.page(a.handleCategories))
	mux.HandleFunc("GET /import", a.page(a.handleImport))
	mux.HandleFunc("GET /history", a.page(a.handleHistory))
	mux.HandleFunc("GET /settings", a.page(a.handleSettings))
	mux.HandleFunc("GET /login", a.handleLoginPage)
	mux.HandleFunc("POST /login", a.handleLoginSubmit)
	mux.HandleFunc("POST /logout", a.handleLogout)
	mux.HandleFunc("GET /setup", a.handleSetupPage)

	// JSON API v1.
	a.registerAPI(mux)

	// Auth + CSRF gates.
	var handler http.Handler = mux
	if a.Cfg.AuthMode == "password" {
		handler = a.withAuth(handler)
	}
	handler = securityHeaders(handler)
	handler = withLogging(a.Log, handler)
	return handler
}

// page wraps a page handler with the common pre-render step.
func (a *App) page(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			a.Log.Error("page", "path", r.URL.Path, "err", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
	}
}

// withAuth gates pages/API behind a valid session (doc 14 §2).
func (a *App) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/login" || strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/htmx/") || path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		// Setup claim: while no user exists, everything goes to /setup.
		if !a.Audit.HasUser() {
			if path == "/setup" || strings.HasPrefix(path, "/api/v1/setup/") {
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
		if _, ok := readSession(r, a.Secret); !ok {
			if strings.HasPrefix(path, "/api/") {
				http.Error(w, `{"error":{"code":"auth_required","message":"Login required"}}`, http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		// CSRF on unsafe methods (doc 14 §3).
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
		next.ServeHTTP(w, r)
	})
}

// staticHandlerFS serves embedded assets with immutable caching + ETags
// (doc 04 §12). strip is the URL mount prefix to remove before the FS lookup.
func (a *App) staticHandlerFS(fsys fs.FS, strip string) http.Handler {
	etags := map[string]string{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, strip)
		if path == "" || strings.Contains(path, "..") {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		sum := sha256.Sum256(data)
		etag := hex.EncodeToString(sum[:8])
		if etags[path] == "" {
			etags[path] = etag
		}
		// ETag revalidation (304s keep it cheap) — deliberately NOT
		// `immutable`: a self-hosted app updated in place must never serve a
		// browser-cached stale app.js (documented deviation from doc 04 §12,
		// which specified immutable caching).
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		switch {
		case strings.HasSuffix(path, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(path, ".js"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case strings.HasSuffix(path, ".woff2"):
			w.Header().Set("Content-Type", "font/woff2")
		case strings.HasSuffix(path, ".svg"):
			w.Header().Set("Content-Type", "image/svg+xml")
		}
		w.Write(data)
	})
}
