package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"golang.org/x/crypto/bcrypt"

	"glowworm/internal/core/audit"
	"glowworm/internal/core/accounts"
	"glowworm/internal/core/categories"
	"glowworm/internal/core/setup"
	"glowworm/internal/core/transactions"
	"glowworm/internal/store/repos"
)

// repos_ListFilter aliases the repo filter type for handlers.
type repos_ListFilter = repos.ListFilter

func verifyPassword(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// apiError writes the standard error envelope (doc 01 §8).
func apiError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

// writeJSON is the success helper.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// serviceError maps validation/not-found to HTTP responses.
func serviceError(w http.ResponseWriter, err error) bool {
	var ve *accounts.ValidationError
	if errors.As(err, &ve) {
		apiError(w, http.StatusBadRequest, "validation_failed", ve.Message)
		return true
	}
	var ce *categories.ValidationError
	if errors.As(err, &ce) {
		apiError(w, http.StatusBadRequest, "validation_failed", ce.Message)
		return true
	}
	var se *setup.ValidationError
	if errors.As(err, &se) {
		apiError(w, http.StatusBadRequest, "validation_failed", se.Message)
		return true
	}
	var te *transactions.ValidationError
	if errors.As(err, &te) {
		apiError(w, http.StatusBadRequest, "validation_failed", te.Message)
		return true
	}
	if errors.Is(err, transactions.ErrNotFound) {
		apiError(w, http.StatusNotFound, "not_found", "Not found")
		return true
	}
	return false
}

// registerAPI mounts /api/v1 (doc 03).
func (a *App) registerAPI(mux *http.ServeMux) {
	j := func(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := fn(w, r); err != nil {
				if serviceError(w, err) {
					return
				}
				a.Log.Error("api", "path", r.URL.Path, "err", err)
				apiError(w, http.StatusInternalServerError, "internal", "Internal server error")
			}
		}
	}

	// Accounts (doc 03 §4).
	mux.HandleFunc("GET /api/v1/accounts", j(a.apiAccountsList))
	mux.HandleFunc("POST /api/v1/accounts", j(a.apiAccountCreate))
	mux.HandleFunc("GET /api/v1/accounts/{id}", j(a.apiAccountGet))
	mux.HandleFunc("PATCH /api/v1/accounts/{id}", j(a.apiAccountUpdate))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}", j(a.apiAccountDelete))
	mux.HandleFunc("GET /api/v1/accounts/{id}/valuations", j(a.apiValuationsList))
	mux.HandleFunc("POST /api/v1/accounts/{id}/valuations", j(a.apiValuationCreate))

	// Transactions (doc 03 §5).
	mux.HandleFunc("GET /api/v1/transactions", j(a.apiTransactionsList))
	mux.HandleFunc("POST /api/v1/transactions", j(a.apiTransactionCreate))
	mux.HandleFunc("GET /api/v1/transactions/{id}", j(a.apiTransactionGet))
	mux.HandleFunc("PATCH /api/v1/transactions/{id}", j(a.apiTransactionUpdate))
	mux.HandleFunc("DELETE /api/v1/transactions/{id}", j(a.apiTransactionDelete))

	// Categories (doc 03 §6).
	mux.HandleFunc("GET /api/v1/categories", j(a.apiCategoriesList))
	mux.HandleFunc("POST /api/v1/categories", j(a.apiCategoryCreate))
	mux.HandleFunc("PATCH /api/v1/categories/{id}", j(a.apiCategoryUpdate))
	mux.HandleFunc("DELETE /api/v1/categories/{id}", j(a.apiCategoryDelete))

	// Settings + AI (doc 03 §13).
	mux.HandleFunc("GET /api/v1/settings", j(a.apiSettingsGet))
	mux.HandleFunc("PUT /api/v1/settings", j(a.apiSettingsPatch))
	mux.HandleFunc("GET /api/v1/setup/status", j(a.apiSetupStatus))
	mux.HandleFunc("POST /api/v1/setup/account", j(a.apiSetupAccount))
	mux.HandleFunc("POST /api/v1/setup/complete", j(a.apiSetupComplete))

	// Audit (doc 03 §10).
	mux.HandleFunc("GET /api/v1/audit", j(a.apiAuditList))
	mux.HandleFunc("GET /api/v1/audit/{id}", j(a.apiAuditGet))
	mux.HandleFunc("GET /api/v1/audit/{id}/capabilities", j(a.apiAuditCapabilities))
	mux.HandleFunc("POST /api/v1/audit/{id}/revert", j(a.apiAuditRevert))
	mux.HandleFunc("POST /api/v1/audit/{id}/jump-back", j(a.apiAuditJumpBack))
	mux.HandleFunc("POST /api/v1/audit/paths/{path}/restore", j(a.apiAuditRestorePath))
	mux.HandleFunc("DELETE /api/v1/audit/{id}", j(a.apiAuditDelete))

	// Dashboard summary (doc 03 §3).
	mux.HandleFunc("GET /api/v1/dashboard/summary", j(a.apiDashboardSummary))
}

// --- accounts ---

func (a *App) apiAccountsList(w http.ResponseWriter, r *http.Request) error {
	list, err := a.Accounts.List(nil)
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": list})
	return nil
}

func (a *App) apiAccountGet(w http.ResponseWriter, r *http.Request) error {
	acct, err := a.Accounts.Get(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, acct)
	return nil
}

func (a *App) apiAccountCreate(w http.ResponseWriter, r *http.Request) error {
	var in accounts.Input
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	acct, err := a.Accounts.Create(in)
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, acct)
	return nil
}

func (a *App) apiAccountUpdate(w http.ResponseWriter, r *http.Request) error {
	var in accounts.Input
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	acct, err := a.Accounts.Update(r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, acct)
	return nil
}

func (a *App) apiAccountDelete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Accounts.Delete(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) apiValuationsList(w http.ResponseWriter, r *http.Request) error {
	vals, err := a.Accounts.Valuations(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": vals})
	return nil
}

func (a *App) apiValuationCreate(w http.ResponseWriter, r *http.Request) error {
	var in accounts.ValuationInput
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	res, err := a.Accounts.CreateValuation(r.PathValue("id"), in, "manual")
	if err != nil {
		return err
	}
	writeJSON(w, res)
	return nil
}

// --- transactions ---

func (a *App) txFilter(r *http.Request) (repos_ListFilter, string) {
	q := r.URL.Query()
	f := repos_ListFilter{
		Q:          q.Get("q"),
		CategoryID: q.Get("category_id"),
		Type:       q.Get("type"),
		Sort:       q.Get("sort"),
	}
	if v := q.Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	if v := q.Get("from"); v != "" {
		f.From = v
	}
	if v := q.Get("to"); v != "" {
		f.To = v
	}
	return f, q.Get("account_id")
}

func (a *App) apiTransactionsList(w http.ResponseWriter, r *http.Request) error {
	f, accountID := a.txFilter(r)
	rows, err := a.Repos.Transactions.DisplayRows(f, accountID)
	if err != nil {
		return err
	}
	type rowOut struct {
		Kind string `json:"kind"`
		Tx   any    `json:"tx"`
	}
	items := make([]rowOut, 0, len(rows))
	for _, row := range rows {
		if row.Kind == "linked" {
			items = append(items, rowOut{Kind: "linked", Tx: row.Rep})
		} else {
			items = append(items, rowOut{Kind: "single", Tx: row.Tx})
		}
	}
	writeJSON(w, map[string]any{"items": items})
	return nil
}

func (a *App) apiTransactionGet(w http.ResponseWriter, r *http.Request) error {
	t, err := a.Transactions.Get(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, t)
	return nil
}

func (a *App) apiTransactionCreate(w http.ResponseWriter, r *http.Request) error {
	var in transactions.CreateInput
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	t, err := a.Transactions.Create(in)
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, t)
	return nil
}

func (a *App) apiTransactionUpdate(w http.ResponseWriter, r *http.Request) error {
	var in transactions.CreateInput
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	t, err := a.Transactions.Edit(r.PathValue("id"), in, "user")
	if err != nil {
		return err
	}
	writeJSON(w, t)
	return nil
}

func (a *App) apiTransactionDelete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Transactions.Delete(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

// --- categories ---

func (a *App) apiCategoriesList(w http.ResponseWriter, r *http.Request) error {
	cats, err := a.Categories.List(r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": cats})
	return nil
}

func (a *App) apiCategoryCreate(w http.ResponseWriter, r *http.Request) error {
	var in categories.Input
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	c, err := a.Categories.Create(in)
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, c)
	return nil
}

func (a *App) apiCategoryUpdate(w http.ResponseWriter, r *http.Request) error {
	var in categories.Input
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	c, err := a.Categories.Update(r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, c)
	return nil
}

func (a *App) apiCategoryDelete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Categories.Delete(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

// --- settings / setup ---

func (a *App) apiSettingsGet(w http.ResponseWriter, r *http.Request) error {
	m, err := a.Settings.All()
	if err != nil {
		return err
	}
	writeJSON(w, m)
	return nil
}

func (a *App) apiSettingsPatch(w http.ResponseWriter, r *http.Request) error {
	var m map[string]any
	if err := decodeBody(r, &m); err != nil {
		return err
	}
	if err := a.Settings.Patch(m); err != nil {
		return err
	}
	out, err := a.Settings.All()
	if err != nil {
		return err
	}
	writeJSON(w, out)
	return nil
}

func (a *App) apiSetupStatus(w http.ResponseWriter, r *http.Request) error {
	st, err := a.Setup.Status()
	if err != nil {
		return err
	}
	writeJSON(w, st)
	return nil
}

func (a *App) apiSetupAccount(w http.ResponseWriter, r *http.Request) error {
	var in setup.AccountInput
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	u, err := a.Setup.CreateAccount(in)
	if err != nil {
		return err
	}
	issueSession(w, r, a.Secret, u.ID)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, u)
	return nil
}

func (a *App) apiSetupComplete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Setup.Complete(); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

// --- audit ---

func (a *App) apiAuditList(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v)
	}
	var cursor int64
	if v := q.Get("cursor"); v != "" {
		cursor, _ = strconv.ParseInt(v, 10, 64)
	}
	entries, next, err := a.Audit.List(audit.ListOptions{
		Actor: q.Get("actor"), Action: q.Get("action"),
		EntityType: q.Get("entity_type"), EntityID: q.Get("entity_id"),
		Limit: limit, Cursor: cursor,
	})
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": entries, "next_cursor": next})
	return nil
}

func (a *App) apiAuditGet(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "validation_failed", "Bad id")
		return nil
	}
	ent, err := a.Audit.Get(id)
	if err != nil {
		return err
	}
	caps, _ := a.Audit.Capabilities(id)
	writeJSON(w, map[string]any{"entry": ent, "capabilities": caps})
	return nil
}

func (a *App) apiAuditCapabilities(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "validation_failed", "Bad id")
		return nil
	}
	caps, err := a.Audit.Capabilities(id)
	if err != nil {
		return err
	}
	writeJSON(w, caps)
	return nil
}

func (a *App) apiAuditRevert(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "validation_failed", "Bad id")
		return nil
	}
	if err := a.Audit.Revert(id, a.applyEntry); err != nil {
		apiError(w, http.StatusConflict, "conflict", err.Error())
		return nil
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) apiAuditJumpBack(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "validation_failed", "Bad id")
		return nil
	}
	res, err := a.Audit.JumpBack(id, a.applyEntry)
	if err != nil {
		apiError(w, http.StatusConflict, "conflict", err.Error())
		return nil
	}
	writeJSON(w, res)
	return nil
}

func (a *App) apiAuditRestorePath(w http.ResponseWriter, r *http.Request) error {
	res, err := a.Audit.RestorePath(r.PathValue("path"), a.applyEntry)
	if err != nil {
		apiError(w, http.StatusConflict, "conflict", err.Error())
		return nil
	}
	writeJSON(w, res)
	return nil
}

func (a *App) apiAuditDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "validation_failed", "Bad id")
		return nil
	}
	mode := r.URL.Query().Get("mode")
	if mode == "forget" {
		if err := a.Audit.Forget(id); err != nil {
			apiError(w, http.StatusConflict, "conflict", err.Error())
			return nil
		}
	} else {
		if err := a.Audit.Delete(id, a.applyEntry); err != nil {
			apiError(w, http.StatusConflict, "conflict", err.Error())
			return nil
		}
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

// --- dashboard summary (doc 03 §3) ---

func (a *App) apiDashboardSummary(w http.ResponseWriter, r *http.Request) error {
	counts, _ := a.Repos.Transactions.CountByAccount("", "")
	list, err := a.Accounts.List(counts)
	if err != nil {
		return err
	}
	var available, savings int64
	for _, acct := range list {
		if acct.Type == "savings" {
			savings += acct.CurrentBalanceMinor
		} else {
			available += acct.CurrentBalanceMinor
		}
	}
	income, spend := a.monthTotals()
	writeJSON(w, map[string]any{
		"available_minor": available, "savings_minor": savings,
		"currency":        a.Settings.Get().Currency,
		"income_month_minor": income, "spend_month_minor": spend,
	})
	return nil
}