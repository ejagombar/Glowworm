package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"glowworm/internal/store"

	"golang.org/x/crypto/bcrypt"

	"glowworm/internal/ai"
	"glowworm/internal/core"
	"glowworm/internal/core/accounts"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/categories"
	"glowworm/internal/core/changes"
	"glowworm/internal/core/grouping"
	"glowworm/internal/core/imports"
	"glowworm/internal/core/setup"
	"glowworm/internal/core/transactions"
	"glowworm/internal/core/transfers"
	"glowworm/internal/crypto"
	"glowworm/internal/importer"
	"glowworm/internal/store/repos"
)

func timeNow() time.Time { return time.Now() }

type repos_ListFilter = repos.ListFilter

func verifyPassword(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func apiError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// An encode error means the client has gone; there's no one to tell.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// maxJSONBody caps a JSON request; uploads go through their own limit.
const maxJSONBody = 2 << 20

// badRequest is a request body the server couldn't read: the client's
// fault, so it's a 400 that says what was wrong, not an internal error.
type badRequest struct{ msg string }

func (e *badRequest) Error() string { return e.msg }

func decodeBody(r *http.Request, v any) error {
	defer func() { _ = r.Body.Close() }()
	err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONBody)).Decode(v)
	var tooBig *http.MaxBytesError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF):
		return &badRequest{"The request was empty"}
	case errors.As(err, &tooBig):
		return &badRequest{"The request is too large"}
	default:
		return &badRequest{"The request couldn't be read: " + err.Error()}
	}
}

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
	var ae *acctValidationError
	if errors.As(err, &ae) {
		apiError(w, http.StatusBadRequest, "validation_failed", ae.Message)
		return true
	}
	var che *changes.ValidationError
	if errors.As(err, &che) {
		apiError(w, http.StatusBadRequest, "validation_failed", che.Message)
		return true
	}
	var ge *grouping.ValidationError
	if errors.As(err, &ge) {
		apiError(w, http.StatusBadRequest, "validation_failed", ge.Message)
		return true
	}
	var ie *imports.ValidationError
	if errors.As(err, &ie) {
		apiError(w, http.StatusBadRequest, "validation_failed", ie.Message)
		return true
	}
	var br *badRequest
	if errors.As(err, &br) {
		apiError(w, http.StatusBadRequest, "bad_request", br.msg)
		return true
	}
	var conflict *transactions.ConflictError
	if errors.As(err, &conflict) {
		apiError(w, http.StatusConflict, "conflict", conflict.Error())
		return true
	}
	if errors.Is(err, transactions.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		apiError(w, http.StatusNotFound, "not_found",
			"That no longer exists: it may have been deleted on another device. Reload to see the latest.")
		return true
	}
	return false
}

func (a *App) registerAPI(mux *http.ServeMux) {
	j := func(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := fn(w, r); err != nil {
				if serviceError(w, err) {
					return
				}
				a.Log.Error("api", "path", r.URL.Path, "err", err)
				apiError(w, http.StatusInternalServerError, "internal",
					"Something went wrong on the server. Try again; if it keeps happening, the server log has the details.")
			}
		}
	}

	mux.HandleFunc("GET /api/v1/accounts", j(a.apiAccountsList))
	mux.HandleFunc("POST /api/v1/accounts", j(a.apiAccountCreate))
	mux.HandleFunc("GET /api/v1/accounts/{id}", j(a.apiAccountGet))
	mux.HandleFunc("PATCH /api/v1/accounts/{id}", j(a.apiAccountUpdate))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}", j(a.apiAccountDelete))
	mux.HandleFunc("GET /api/v1/accounts/archives", j(a.apiAccountArchives))
	mux.HandleFunc("POST /api/v1/accounts/archives/{id}/restore", j(a.apiAccountArchiveRestore))
	mux.HandleFunc("DELETE /api/v1/accounts/archives/{id}", j(a.apiAccountArchivePurge))
	mux.HandleFunc("POST /api/v1/instance/backup", j(a.apiInstanceBackup))
	mux.HandleFunc("POST /api/v1/instance/delete", j(a.apiInstanceDelete))
	mux.HandleFunc("GET /api/v1/accounts/{id}/valuations", j(a.apiValuationsList))
	mux.HandleFunc("POST /api/v1/accounts/{id}/valuations", j(a.apiValuationCreate))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}/valuations/{vid}", j(a.apiValuationDelete))

	mux.HandleFunc("GET /api/v1/transactions", j(a.apiTransactionsList))
	mux.HandleFunc("POST /api/v1/transactions", j(a.apiTransactionCreate))
	mux.HandleFunc("GET /api/v1/transactions/{id}", j(a.apiTransactionGet))
	mux.HandleFunc("PATCH /api/v1/transactions/{id}", j(a.apiTransactionUpdate))
	mux.HandleFunc("DELETE /api/v1/transactions/{id}", j(a.apiTransactionDelete))

	mux.HandleFunc("GET /api/v1/categories", j(a.apiCategoriesList))
	mux.HandleFunc("POST /api/v1/categories", j(a.apiCategoryCreate))
	mux.HandleFunc("PATCH /api/v1/categories/{id}", j(a.apiCategoryUpdate))
	mux.HandleFunc("DELETE /api/v1/categories/{id}", j(a.apiCategoryDelete))

	mux.HandleFunc("GET /api/v1/settings", j(a.apiSettingsGet))
	mux.HandleFunc("PUT /api/v1/settings", j(a.apiSettingsPatch))
	mux.HandleFunc("POST /api/v1/account/password", j(a.apiChangePassword))
	mux.HandleFunc("GET /api/v1/passkeys", j(a.apiPasskeys))
	mux.HandleFunc("POST /api/v1/passkeys/register/begin", j(a.apiPasskeyRegisterBegin))
	mux.HandleFunc("POST /api/v1/passkeys/register/finish", j(a.apiPasskeyRegisterFinish))
	mux.HandleFunc("DELETE /api/v1/passkeys/{id}", j(a.apiPasskeyDelete))
	mux.HandleFunc("POST /api/v1/lock/device", j(a.apiLockDevice))
	mux.HandleFunc("PUT /api/v1/lock/after", j(a.apiLockAfter))

	mux.HandleFunc("GET /api/v1/snapshot", j(a.apiSnapshot))
	mux.HandleFunc("GET /api/v1/reconcile/status", j(a.apiReconcileStatus))

	mux.HandleFunc("GET /api/v1/audit", j(a.apiAuditList))
	mux.HandleFunc("GET /api/v1/audit/{id}", j(a.apiAuditGet))
	mux.HandleFunc("GET /api/v1/audit/{id}/capabilities", j(a.apiAuditCapabilities))
	mux.HandleFunc("POST /api/v1/audit/{id}/revert", j(a.apiAuditRevert))
	mux.HandleFunc("POST /api/v1/audit/{id}/jump-back", j(a.apiAuditJumpBack))
	mux.HandleFunc("POST /api/v1/audit/redo", j(a.apiAuditRedo))
	mux.HandleFunc("POST /api/v1/audit/discard", j(a.apiAuditDiscard))
	mux.HandleFunc("POST /api/v1/audit/paths/{path}/restore", j(a.apiAuditRestorePath))
	mux.HandleFunc("DELETE /api/v1/audit/{id}", j(a.apiAuditDelete))

	mux.HandleFunc("GET /api/v1/dashboard/summary", j(a.apiDashboardSummary))

	mux.HandleFunc("POST /api/v1/imports", j(a.apiImportUpload))
	mux.HandleFunc("GET /api/v1/imports/{id}", j(a.apiImportGet))
	mux.HandleFunc("POST /api/v1/imports/{id}/schema", j(a.apiImportApplySchema))
	mux.HandleFunc("POST /api/v1/imports/{id}/schema/ai-detect", j(a.apiImportAIDetect))
	mux.HandleFunc("POST /api/v1/imports/preview-date", j(a.apiImportPreviewDate))
	mux.HandleFunc("POST /api/v1/imports/{id}/schema/save", j(a.apiImportSaveSchema))
	mux.HandleFunc("GET /api/v1/imports/{id}/categorise/stream", a.apiImportCategoriseStream)
	mux.HandleFunc("GET /api/v1/imports/{id}/categorise/ai/stream", a.apiImportCategoriseAIStream)
	mux.HandleFunc("GET /api/v1/imports/{id}/review", j(a.apiImportReview))
	mux.HandleFunc("PATCH /api/v1/imports/{id}/review", j(a.apiImportReviewPatch))
	mux.HandleFunc("POST /api/v1/imports/{id}/confirm", j(a.apiImportConfirm))
	mux.HandleFunc("PUT /api/v1/imports/{id}/account", j(a.apiImportReviewAccount))
	mux.HandleFunc("POST /api/v1/imports/{id}/cancel", j(a.apiImportCancel))
	mux.HandleFunc("PATCH /api/v1/imports/{id}", j(a.apiImportEdit))
	mux.HandleFunc("GET /api/v1/schemas", j(a.apiSchemasList))
	mux.HandleFunc("DELETE /api/v1/schemas/{id}", j(a.apiSchemaDelete))

	mux.HandleFunc("POST /api/v1/transactions/{id}/link", j(a.apiTransferLink))
	mux.HandleFunc("POST /api/v1/transactions/{id}/unlink", j(a.apiTransferUnlink))
	mux.HandleFunc("GET /api/v1/transactions/{id}/transfer-suggestions", j(a.apiTransferSuggestions))
	mux.HandleFunc("POST /api/v1/transfers/detect", j(a.apiTransferDetect))
	mux.HandleFunc("GET /api/v1/transfers/pending", j(a.apiTransferPending))

	mux.HandleFunc("GET /api/v1/group-rules", j(a.apiGroupRulesList))
	mux.HandleFunc("POST /api/v1/group-rules/preview", j(a.apiGroupRulesPreview))
	mux.HandleFunc("POST /api/v1/group-rules", j(a.apiGroupRulesCreate))
	mux.HandleFunc("DELETE /api/v1/group-rules/{id}", j(a.apiGroupRuleDelete))
	mux.HandleFunc("POST /api/v1/changes/preview", j(a.apiChangePreview))
	mux.HandleFunc("POST /api/v1/changes/apply", j(a.apiChangeApply))

	mux.HandleFunc("GET /api/v1/recurring", j(a.apiRecurringList))
	mux.HandleFunc("GET /api/v1/recurring/{key}/transactions", j(a.apiRecurringSeries))
	mux.HandleFunc("POST /api/v1/recurring/refresh", j(a.apiRecurringRefresh))

	mux.HandleFunc("GET /api/v1/categoriser/status", j(a.apiCategoriserStatus))
	mux.HandleFunc("POST /api/v1/categoriser/retrain", j(a.apiCategoriserRetrain))
	mux.HandleFunc("POST /api/v1/transactions/{id}/categorise", j(a.apiCategoriseTx))
	mux.HandleFunc("POST /api/v1/transactions/{id}/set-category", j(a.apiSetCategory))

	mux.HandleFunc("GET /api/v1/settings/ai", j(a.apiAIGet))
	mux.HandleFunc("PUT /api/v1/settings/ai", j(a.apiAISet))
	mux.HandleFunc("POST /api/v1/settings/ai/test", j(a.apiAITest))
	mux.HandleFunc("POST /api/v1/settings/ai/models", j(a.apiAIModels))

	mux.HandleFunc("GET /api/v1/queries/days", j(a.apiQueryDays))
	mux.HandleFunc("POST /api/v1/query", j(a.apiQuery))
	mux.HandleFunc("POST /api/v1/query/stream", j(a.apiQueryStream))
	mux.HandleFunc("GET /api/v1/conversations", j(a.apiConversationsList))
	mux.HandleFunc("GET /api/v1/conversations/{id}", j(a.apiConversationGet))
	mux.HandleFunc("GET /api/v1/conversations/{id}/messages", j(a.apiConversationMessages))
	mux.HandleFunc("DELETE /api/v1/conversations/{id}", j(a.apiConversationDelete))

	mux.HandleFunc("GET /api/v1/widgets/pinned", j(a.apiWidgetsGet))
	mux.HandleFunc("PUT /api/v1/widgets/pinned", j(a.apiWidgetsReorder))
	mux.HandleFunc("POST /api/v1/widgets/pinned", j(a.apiWidgetPin))
	mux.HandleFunc("PATCH /api/v1/widgets/pinned/{id}", j(a.apiWidgetPatch))
	mux.HandleFunc("DELETE /api/v1/widgets/pinned/{id}", j(a.apiWidgetUnpin))
}

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
	arc, err := a.Accounts.Delete(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true, "archive": arc})
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

func (a *App) apiValuationDelete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Accounts.DeleteValuation(r.PathValue("id"), r.PathValue("vid")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

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
	if t.TransferID != nil {
		if cp, err := a.Transfers.CounterpartOf(t.ID); err == nil && cp != nil {
			m := map[string]any{
				"id": cp.ID, "date": cp.Date, "description": cp.Description,
				"amount_minor": cp.AmountMinor, "type": cp.Type,
			}
			if cp.Account != nil {
				m["account_name"] = cp.Account.Name
			}
			resp := struct {
				*core.Transaction
				Counterpart *map[string]any `json:"counterpart_tx,omitempty"`
			}{t, &m}
			writeJSON(w, resp)
			return nil
		}
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
	a.regroup()
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
	a.regroup()
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
	notes, err := a.Audit.Undo(id, a.applyEntry)
	if err != nil {
		apiError(w, http.StatusConflict, "conflict", err.Error())
		return nil
	}
	a.afterHistoryChange()
	writeJSON(w, map[string]any{"ok": true, "notes": notes})
	return nil
}

func (a *App) apiAuditRedo(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	notes, err := a.Audit.Redo(in.IDs, a.applyEntry)
	if err != nil {
		apiError(w, http.StatusConflict, "conflict", err.Error())
		return nil
	}
	a.afterHistoryChange()
	writeJSON(w, map[string]any{"ok": true, "notes": notes})
	return nil
}

func (a *App) apiAuditDiscard(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	n, err := a.Audit.Discard(in.IDs)
	if err != nil {
		apiError(w, http.StatusConflict, "conflict", err.Error())
		return nil
	}
	writeJSON(w, map[string]any{"ok": true, "removed": n})
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
	a.afterHistoryChange()
	writeJSON(w, res)
	return nil
}

func (a *App) apiAuditRestorePath(w http.ResponseWriter, r *http.Request) error {
	res, err := a.Audit.RestorePath(r.PathValue("path"), a.applyEntry)
	if err != nil {
		apiError(w, http.StatusConflict, "conflict", err.Error())
		return nil
	}
	a.afterHistoryChange()
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
		"currency":           a.Settings.Get().Currency,
		"income_month_minor": income, "spend_month_minor": spend,
	})
	return nil
}

func (a *App) apiImportUpload(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, int64(a.Cfg.UploadMaxMB)<<20)
	if err := r.ParseMultipartForm(int64(a.Cfg.UploadMaxMB) << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			apiError(w, http.StatusRequestEntityTooLarge, "file_too_large", fmt.Sprintf("That file is larger than %d MB.", a.Cfg.UploadMaxMB))
		} else {
			apiError(w, http.StatusBadRequest, "upload_incomplete", "The file didn't arrive complete (the connection may have dropped). Try uploading it again.")
		}
		return nil
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		apiError(w, http.StatusBadRequest, "validation_failed", "No file provided")
		return nil
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	var accountID *string
	if v := r.FormValue("account_id"); v != "" {
		accountID = &v
	}
	batch, err := a.Imports.Upload(header.Filename, data, accountID)
	if errors.Is(err, imports.ErrDuplicateFile) {
		apiError(w, http.StatusConflict, "duplicate_file", "This exact file was already imported.")
		return nil
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, batch)
	return nil
}

func (a *App) apiImportGet(w http.ResponseWriter, r *http.Request) error {
	batch, err := a.Imports.Get(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, batch)
	return nil
}

func (a *App) apiImportApplySchema(w http.ResponseWriter, r *http.Request) error {
	batchID := r.PathValue("id")
	var in struct {
		Mapping json.RawMessage `json:"mapping"`
		Save    bool            `json:"save_schema"`
		Name    string          `json:"name"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if _, err := a.Imports.Get(batchID); err != nil {
		return err
	}
	data, err := a.importFileBytes(batchID)
	if err != nil {
		return err
	}
	var m importer.Mapping
	if err := json.Unmarshal(in.Mapping, &m); err != nil {
		apiError(w, http.StatusBadRequest, "validation_failed", "Bad mapping JSON")
		return nil
	}
	b, err := a.Imports.ApplySchemaPublic(batchID, &m, data)
	if err != nil {
		if serviceError(w, err) {
			return nil
		}
		return err
	}
	if in.Save {
		var header []string
		if b.Detection != nil && len(b.Detection.Sample) > 0 {
			header = b.Detection.Sample[0]
		}
		name := in.Name
		if name == "" && b.Detection != nil {
			name = b.Detection.ProposedName
		}
		bank := ""
		if b.Detection != nil {
			bank = b.Detection.BankHint
		}
		if _, err := a.Imports.SaveSchema(name, bank, &m, header, m.Confidence); err != nil {
			return err
		}
	}
	writeJSON(w, b)
	return nil
}

func (a *App) apiImportSaveSchema(w http.ResponseWriter, r *http.Request) error {
	batchID := r.PathValue("id")
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	batch, err := a.Imports.Get(batchID)
	if err != nil {
		return err
	}
	if batch.Detection == nil {
		apiError(w, http.StatusConflict, "import_state", "No detection available")
		return nil
	}
	m := importer.Mapping{
		Kind: importer.Kind(batch.Detection.Kind), Sheet: batch.Detection.Sheet,
		HasHeader: batch.Detection.HasHeader, Delimiter: batch.Detection.Delimiter,
		Columns: batch.Detection.Columns, BankHint: batch.Detection.BankHint,
		Confidence: batch.Detection.Confidence,
	}
	var header []string
	if len(batch.Detection.Sample) > 0 {
		header = batch.Detection.Sample[0]
	}
	schema, err := a.Imports.SaveSchema(in.Name, batch.Detection.BankHint, &m, header, batch.Detection.Confidence)
	if err != nil {
		return err
	}
	writeJSON(w, schema)
	return nil
}

func (a *App) apiImportReview(w http.ResponseWriter, r *http.Request) error {
	batchID := r.PathValue("id")
	rows, confident, needsReview, duplicates, err := a.Imports.Review(batchID)
	if err != nil {
		return err
	}
	batch, err := a.Imports.Get(batchID)
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{
		"batch": batch, "rows": rows,
		"confident": confident, "needs_review": needsReview, "duplicates": duplicates,
		"ai_available": a.Imports.AIAvailable(),
	})
	return nil
}

// apiImportReviewAccount changes the account an import under review goes
// into, and returns the review with duplicates checked against it.
func (a *App) apiImportReviewAccount(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		AccountID string `json:"account_id"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if err := a.Imports.SetReviewAccount(r.PathValue("id"), in.AccountID); err != nil {
		if serviceError(w, err) {
			return nil
		}
		return err
	}
	return a.apiImportReview(w, r)
}

func (a *App) apiImportReviewPatch(w http.ResponseWriter, r *http.Request) error {
	var p imports.ReviewPatch
	if err := decodeBody(r, &p); err != nil {
		return err
	}
	if err := a.Imports.ApplyReview(r.PathValue("id"), p); err != nil {
		if serviceError(w, err) {
			return nil
		}
		return err
	}
	rows, confident, needsReview, duplicates, err := a.Imports.Review(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"rows": rows, "confident": confident, "needs_review": needsReview, "duplicates": duplicates})
	return nil
}

func (a *App) apiImportConfirm(w http.ResponseWriter, r *http.Request) error {
	batchID := r.PathValue("id")
	var in struct {
		AccountID  string `json:"account_id"`
		SchemaName string `json:"schema_name"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	batch, err := a.Imports.Get(batchID)
	if err != nil {
		return err
	}
	accountID := in.AccountID
	if accountID == "" && batch.AccountID != nil {
		accountID = *batch.AccountID
	}
	if accountID == "" {
		apiError(w, http.StatusBadRequest, "validation_failed", "Choose the account to import into")
		return nil
	}
	b, err := a.Imports.Confirm(batchID, accountID, in.SchemaName)
	if err != nil {
		if serviceError(w, err) {
			return nil
		}
		return err
	}
	writeJSON(w, b)
	return nil
}

func (a *App) apiImportCancel(w http.ResponseWriter, r *http.Request) error {
	if err := a.Imports.Cancel(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

// apiImportEdit changes a finished import. Only its account can be changed.
func (a *App) apiImportEdit(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		AccountID string `json:"account_id"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	b, err := a.Imports.MoveToAccount(r.PathValue("id"), in.AccountID)
	if err != nil {
		return err
	}
	writeJSON(w, b)
	return nil
}

func (a *App) apiSchemasList(w http.ResponseWriter, r *http.Request) error {
	schemas, err := a.Imports.ListSchemas()
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": schemas})
	return nil
}

func (a *App) apiSchemaDelete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Imports.DeleteSchema(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) importFileBytes(batchID string) ([]byte, error) {
	var data []byte
	err := a.DB.QueryRow(`SELECT content FROM import_files WHERE batch_id = ?`, batchID).Scan(&data)
	if err != nil {
		return nil, fmt.Errorf("original file not retained for this batch (re-upload to fix the mapping)")
	}
	return data, nil
}

func (a *App) apiTransferLink(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		CounterpartID string `json:"counterpart_id"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	tr, err := a.Transfers.Link(r.PathValue("id"), in.CounterpartID, "user")
	if err != nil {
		return err
	}
	writeJSON(w, tr)
	return nil
}

func (a *App) apiTransferUnlink(w http.ResponseWriter, r *http.Request) error {
	if err := a.Transfers.Unlink(r.PathValue("id"), "user"); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) apiTransferSuggestions(w http.ResponseWriter, r *http.Request) error {
	sugs, err := a.Transfers.Suggestions(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": sugs})
	return nil
}

// apiTransferPending lists the pairs detection would link now.
func (a *App) apiTransferPending(w http.ResponseWriter, r *http.Request) error {
	pairs, err := a.Transfers.Pending()
	if err != nil {
		return err
	}
	if pairs == nil {
		pairs = []*transfers.Pair{}
	}
	writeJSON(w, map[string]any{"items": pairs})
	return nil
}

func (a *App) apiTransferDetect(w http.ResponseWriter, r *http.Request) error {
	n, err := a.Transfers.Detect()
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"linked": n})
	return nil
}

func (a *App) apiRecurringList(w http.ResponseWriter, r *http.Request) error {
	keys, err := a.Repos.Transactions.RecurringKeys()
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": keys})
	return nil
}

func (a *App) apiRecurringSeries(w http.ResponseWriter, r *http.Request) error {
	key := r.PathValue("key")
	meta, rows, err := a.Recurring.Series(key)
	if err != nil {
		return err
	}
	txs := make([]*core.Transaction, 0, len(rows))
	for _, row := range rows {
		if row.Kind == "linked" {
			txs = append(txs, row.Members...)
		} else {
			txs = append(txs, row.Tx)
		}
	}
	writeJSON(w, map[string]any{"series": meta, "transactions": txs, "currency": a.Settings.Get().Currency})
	return nil
}

func (a *App) apiRecurringRefresh(w http.ResponseWriter, r *http.Request) error {
	n, err := a.Recurring.Detect()
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"series": n})
	return nil
}

func (a *App) apiCategoriserStatus(w http.ResponseWriter, r *http.Request) error {
	s := a.Settings.Get()
	var version int
	var trainedAt string
	var trainingCount int
	_ = a.DB.QueryRow(`SELECT version, trained_at, training_count FROM classifier_models ORDER BY version DESC LIMIT 1`).
		Scan(&version, &trainedAt, &trainingCount)
	var rules int
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM category_rules`).Scan(&rules)
	writeJSON(w, map[string]any{
		"model_version": version, "trained_at": trainedAt, "training_count": trainingCount,
		"rules_count": rules,
		"thresholds": map[string]any{
			"local": s.CatLocalThresh, "ai": s.CatAIThresh, "min_examples": s.CatMinExamples,
		},
	})
	return nil
}

func (a *App) apiCategoriserRetrain(w http.ResponseWriter, r *http.Request) error {
	model, err := a.Cat.Retrain()
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"version": model.Version, "training_count": model.TrainingCount})
	return nil
}

func (a *App) apiCategoriseTx(w http.ResponseWriter, r *http.Request) error {
	t, err := a.Repos.Transactions.Get(r.PathValue("id"))
	if err != nil {
		return err
	}
	res, err := a.Cat.Categorise(nil, []*core.Transaction{t})
	if err != nil {
		return err
	}
	writeJSON(w, res)
	return nil
}

func (a *App) apiSetCategory(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		CategoryID *string `json:"category_id"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if err := a.Transactions.SetCategory(r.PathValue("id"), in.CategoryID); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) apiAIGet(w http.ResponseWriter, r *http.Request) error {
	row, err := a.Repos.AIConfig.Get()
	if err != nil {
		return err
	}
	masked := ""
	if row.KeyCipher != "" {
		masked = "sk-••••••••••••••••"
	}
	writeJSON(w, map[string]any{
		"provider": row.Provider, "models": row.Models,
		"base_url": row.BaseURL, "key_masked": masked, "has_key": row.KeyCipher != "",
	})
	return nil
}

func (a *App) apiAISet(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Provider string            `json:"provider"`
		APIKey   string            `json:"api_key"`
		BaseURL  *string           `json:"base_url"`
		ClearKey bool              `json:"clear_key"`
		Models   map[string]string `json:"models"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	row, err := a.Repos.AIConfig.Get()
	if err != nil {
		return err
	}
	baseURL := row.BaseURL
	if in.BaseURL != nil {
		baseURL = *in.BaseURL
	}
	keyCipher := row.KeyCipher
	if in.APIKey != "" {
		cipher, err := a.Secret.Encrypt([]byte(in.APIKey))
		if err != nil {
			return err
		}
		keyCipher = cipher
	}
	if in.ClearKey {
		keyCipher = ""
	}
	if in.Provider == "" {
		keyCipher = ""
	}
	models := row.Models
	if in.Models != nil {
		models = in.Models
	}
	if err := a.Repos.AIConfig.Set(nil, in.Provider, models, baseURL, keyCipher); err != nil {
		return err
	}
	if err := a.DB.WithTx(func(tx *sql.Tx) error {
		_, err := a.AuditWriter.Write(tx, audit.Input{
			Actor: "user", Action: "settings.ai.updated",
			EntityType: "ai_provider_config", EntityID: "1",
			Title:  "Updated AI provider",
			Sub:    maskedProvider(in.Provider),
			After:  map[string]any{"provider": in.Provider, "models": models, "base_url": baseURL, "has_key": in.APIKey != ""},
			Revert: map[string]any{"provider": row.Provider, "models": row.Models, "base_url": row.BaseURL, "key_cipher": row.KeyCipher},
		})
		return err
	}); err != nil {
		return err
	}
	key := decodeKey(a.Secret, keyCipher)
	cat, _ := ai.New(in.Provider, modelFor(models, modelRoleCategorisation), baseURL, key)
	queries, _ := ai.New(in.Provider, modelFor(models, modelRoleQueries), baseURL, key)
	a.setProviders(cat, queries)
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) apiAIModels(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Provider string `json:"provider"`
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	row, err := a.Repos.AIConfig.Get()
	if err != nil {
		return err
	}
	provider := in.Provider
	if provider == "" && row != nil {
		provider = row.Provider
	}
	baseURL := in.BaseURL
	if baseURL == "" && row != nil {
		baseURL = row.BaseURL
	}
	apiKey := in.APIKey
	if apiKey == "" && row != nil {
		apiKey = decodeKey(a.Secret, row.KeyCipher)
	}
	needsKey := provider == "openai" || provider == "anthropic" || provider == "google"
	if needsKey && apiKey == "" {
		writeJSON(w, map[string]any{"ok": true, "source": "needs-key", "provider": provider, "models": ai.FallbackModels(provider)})
		return nil
	}
	models, err := ai.ListModels(provider, baseURL, apiKey, 10*time.Second)
	source := "live"
	if err != nil || len(models) == 0 {
		if err == nil {
			err = &ai.ErrUnavailable{Reason: "provider returned no models"}
		}
		models = ai.FallbackModels(provider)
		source = "fallback"
	}
	out := map[string]any{"ok": true, "source": source, "provider": provider, "models": models}
	if source == "fallback" && apiKey != "" {
		out["error"] = err.Error()
	}
	writeJSON(w, out)
	return nil
}

func maskedProvider(p string) string {
	if p == "" {
		return "AI disabled"
	}
	return p
}

func decodeKey(secret *crypto.Secret, cipher string) string {
	if cipher == "" {
		return ""
	}
	plain, err := secret.Decrypt(cipher)
	if err != nil {
		return ""
	}
	return string(plain)
}

func (a *App) apiAITest(w http.ResponseWriter, r *http.Request) error {
	row, err := a.Repos.AIConfig.Get()
	if err != nil {
		return err
	}
	if row.Provider == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "No provider configured"})
		return nil
	}
	// Test each distinct model once; both are usually the same.
	tested := map[string]bool{}
	latency := 0
	for _, role := range []string{modelRoleQueries, modelRoleCategorisation} {
		model := modelFor(row.Models, role)
		if tested[model] {
			continue
		}
		tested[model] = true
		p, err := ai.New(row.Provider, model, row.BaseURL, decodeKey(a.Secret, row.KeyCipher))
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return nil
		}
		start := timeNow()
		_, _, err = p.CompleteStructured(&ai.StructuredRequest{
			Capability: "schema_naming",
			PromptJSON: map[string]any{"columns": []string{"Date", "Description", "Amount"}, "bank_hint": "Test"},
		})
		ms := int(timeNow().Sub(start).Milliseconds())
		if err != nil {
			modelLabel := model
			if modelLabel == "" {
				modelLabel = ai.EffectiveModel(p)
			}
			writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("%s (model: %s)", err, modelLabel)})
			return nil
		}
		a.Repos.AIConfig.LogCall("schema_naming", row.Provider, model, 0, 0, ms, true, "")
		if ms > latency {
			latency = ms
		}
	}
	writeJSON(w, map[string]any{"ok": true, "latency_ms": latency})
	return nil
}

func (a *App) apiSnapshot(w http.ResponseWriter, r *http.Request) error {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = a.Clock.Today()
	}
	balances, err := balancesAll(a.DB, date)
	if err != nil {
		return err
	}
	list, err := a.Accounts.List(nil)
	if err != nil {
		return err
	}
	type accBal struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Type    string `json:"type"`
		Color   string `json:"color"`
		Balance int64  `json:"balance_minor"`
	}
	items := []accBal{}
	var total int64
	for _, acct := range list {
		b := balances[acct.ID]
		items = append(items, accBal{ID: acct.ID, Name: acct.Name, Type: acct.Type, Color: acct.Color, Balance: b})
		total += b
	}
	var txCount int64
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM transactions WHERE deleted_at IS NULL AND date <= ?`, date).Scan(&txCount)
	status, label, hint := a.reconcileSummary(date)
	writeJSON(w, map[string]any{
		"date": date, "accounts": items, "total_minor": total,
		"currency": a.Settings.Get().Currency, "tx_count": txCount,
		"reconciled": map[string]any{"status": status, "label": label, "hint": hint},
	})
	return nil
}

func balancesAll(db *store.DB, date string) (map[string]int64, error) {
	ids := []string{}
	rows, err := db.Query(`SELECT id FROM accounts WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	out := map[string]int64{}
	for _, id := range ids {
		var opening int64
		var since any
		if err := db.QueryRow(`SELECT opening_balance_minor, opening_balance_date FROM accounts WHERE id = ?`, id).Scan(&opening, &since); err != nil {
			continue
		}
		balance := opening
		sinceStr := ""
		if sd, ok := since.(string); ok {
			sinceStr = sd
		}
		var val int64
		var valDate string
		err := db.QueryRow(`SELECT value_minor, date FROM valuations WHERE account_id = ? AND date <= ? AND deleted_at IS NULL
			ORDER BY date DESC, created_at DESC LIMIT 1`, id, date).Scan(&val, &valDate)
		if err == nil {
			balance = val
			sinceStr = valDate
		}
		q := `SELECT COALESCE(SUM(amount_minor),0) FROM transactions WHERE account_id = ? AND deleted_at IS NULL AND date <= ?`
		args := []any{id, date}
		if sinceStr != "" {
			q += ` AND date > ?`
			args = append(args, sinceStr)
		}
		var sum any
		_ = db.QueryRow(q, args...).Scan(&sum)
		if v, ok := sum.(int64); ok {
			balance += v
		}
		out[id] = balance
	}
	return out, nil
}

func (a *App) apiReconcileStatus(w http.ResponseWriter, r *http.Request) error {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = a.Clock.Today()
	}
	status, label, hint := a.reconcileSummary(date)
	writeJSON(w, map[string]any{"status": status, "label": label, "hint": hint})
	return nil
}

func (a *App) apiGroupRulesList(w http.ResponseWriter, r *http.Request) error {
	rules, err := a.Grouping.List()
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": rules})
	return nil
}

func (a *App) apiGroupRulesPreview(w http.ResponseWriter, r *http.Request) error {
	var d grouping.Draft
	if err := decodeBody(r, &d); err != nil {
		return err
	}
	p, err := a.Grouping.Preview(d)
	if err != nil {
		return err
	}
	writeJSON(w, p)
	return nil
}

func (a *App) apiGroupRulesCreate(w http.ResponseWriter, r *http.Request) error {
	var d grouping.Draft
	if err := decodeBody(r, &d); err != nil {
		return err
	}
	created, err := a.Grouping.Create(d)
	if err != nil {
		return err
	}
	a.afterGroupRulesChange()
	writeJSON(w, map[string]any{"created": created})
	return nil
}

func (a *App) apiGroupRuleDelete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Grouping.Delete(r.PathValue("id")); err != nil {
		return err
	}
	a.afterGroupRulesChange()
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

// apiChangePreview lists what a change drafted in Queries would do.
func (a *App) apiChangePreview(w http.ResponseWriter, r *http.Request) error {
	var p changes.Proposal
	if err := decodeBody(r, &p); err != nil {
		return err
	}
	pv, err := a.Changes.Preview(p)
	if err != nil {
		return err
	}
	writeJSON(w, pv)
	return nil
}

// apiChangeApply makes a change drafted in Queries, once the user presses
// Apply on its preview.
func (a *App) apiChangeApply(w http.ResponseWriter, r *http.Request) error {
	var p changes.Proposal
	if err := decodeBody(r, &p); err != nil {
		return err
	}
	pv, err := a.Changes.Apply(p)
	if err != nil {
		return err
	}
	// Deletions can end a recurring series; edits can change what's grouped.
	a.afterHistoryChange()
	writeJSON(w, pv)
	return nil
}
