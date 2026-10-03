// Package transactions implements the transaction service (doc 02 §5.1,
// doc 03 §5): CRUD with audit, the display-row contract, and category
// assignment with type masking.
package transactions

import (
	"database/sql"
	"errors"
	"fmt"

	"glowworm/internal/clock"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

// Service owns transaction rules.
type Service struct {
	DB         *store.DB
	Repos      *repos.Transactions
	Categories *repos.Categories
	Audit      *audit.Writer
	Clock      *clock.Clock
}

// ErrNotFound mirrors repos.
var ErrNotFound = repos.ErrNotFound

// ValidationError is user-facing.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// Display returns display rows for the transactions table (doc 03 §5.1).
func (s *Service) Display(f repos.ListFilter, accountID string) ([]*repos.DisplayRow, error) {
	return s.Repos.DisplayRows(f, accountID)
}

// InternalCategoryID resolves the system "Internal transfer" category.
func (s *Service) InternalCategoryID() (string, error) {
	c, err := s.Categories.ByName("Internal transfer")
	if err != nil {
		return "", fmt.Errorf("system category missing: %w", err)
	}
	return c.ID, nil
}

// eligibleCategory validates type masking (doc 02 §4): expense→spend,
// income→income, transfer→internal.
func (s *Service) eligibleCategory(txType string, categoryID *string) error {
	if categoryID == nil || *categoryID == "" {
		return nil
	}
	c, err := s.Categories.Get(*categoryID)
	if err != nil {
		return &ValidationError{Message: "Unknown category"}
	}
	switch txType {
	case core.TypeExpense:
		if c.Type != core.CategorySpend {
			return &ValidationError{Message: "Expenses can only use spend categories"}
		}
	case core.TypeIncome:
		if c.Type != core.CategoryIncome {
			return &ValidationError{Message: "Income can only use income categories"}
		}
	case core.TypeTransfer:
		internal, _ := s.InternalCategoryID()
		if *categoryID != internal {
			return &ValidationError{Message: "Transfers are always the Internal transfer category"}
		}
	}
	return nil
}

// CreateInput is a manual transaction create/edit payload (doc 03 §5.2).
type CreateInput struct {
	AccountID   string  `json:"account_id"`
	Date        string  `json:"date"`
	Description string  `json:"description"`
	AmountMinor int64   `json:"amount_minor"`
	Currency    string  `json:"currency"`
	Type        string  `json:"type"`
	CategoryID  *string `json:"category_id"`
	Merchant    *string `json:"merchant"`
}

// Validate checks create input.
func (in *CreateInput) Validate() error {
	if in.AccountID == "" {
		return &ValidationError{Message: "Account is required"}
	}
	if len(in.Date) != 10 {
		return &ValidationError{Message: "Date is required (YYYY-MM-DD)"}
	}
	if in.Description == "" {
		return &ValidationError{Message: "Description is required"}
	}
	if in.AmountMinor == 0 {
		return &ValidationError{Message: "Amount is required"}
	}
	switch in.Type {
	case core.TypeExpense, core.TypeIncome:
	case "":
		in.Type = core.TypeExpense
	default:
		return &ValidationError{Message: "Type must be expense, income or transfer"}
	}
	if in.Type == core.TypeTransfer {
		return &ValidationError{Message: "Create the two legs first, then link them as a transfer"}
	}
	return nil
}

// Create adds a manual transaction (audit transaction.created).
func (s *Service) Create(in CreateInput) (*core.Transaction, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if err := s.eligibleCategory(in.Type, in.CategoryID); err != nil {
		return nil, err
	}
	if in.Currency == "" {
		var cc string
		_ = s.DB.QueryRow(`SELECT value FROM settings WHERE key='currency'`).Scan(&cc)
		if cc == "" {
			cc = `"GBP"`
		}
		in.Currency = trimQuotes(cc)
	}
	t := &core.Transaction{
		AccountID: in.AccountID, Date: in.Date, Description: in.Description,
		Merchant: in.Merchant, AmountMinor: in.AmountMinor, Currency: in.Currency,
		Type: in.Type, CategoryID: in.CategoryID,
		CategorySource: core.CategorySourceUser,
	}
	if in.CategoryID == nil {
		t.CategorySource = core.CategorySourceNone
	}
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Repos.Create(tx, t); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "transaction.created",
			EntityType: "transaction", EntityID: t.ID,
			Title: "Added transaction", Sub: t.Description,
			After:  s.snapshot(t),
			Revert: map[string]any{"transaction_id": t.ID},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Repos.Get(t.ID)
}

// Edit applies a partial update (audit transaction.edited with field diffs).
func (s *Service) Edit(id string, in CreateInput, categorySource string) (*core.Transaction, error) {
	t, err := s.Repos.Get(id)
	if err != nil {
		return nil, err
	}
	if t.TransferID != nil {
		if in.Type != "" && in.Type != core.TypeTransfer {
			return nil, &ValidationError{Message: "Break the transfer link before changing its type"}
		}
		if in.CategoryID != nil && *in.CategoryID != deref(t.CategoryID) {
			return nil, &ValidationError{Message: "Transfers are always the Internal transfer category"}
		}
	}
	before := s.snapshot(t)
	// Apply fields (only provided ones for partial updates: empty strings mean
	// "keep" except explicit pointers).
	if in.Date != "" {
		t.Date = in.Date
	}
	if in.Description != "" {
		t.Description = in.Description
	}
	if in.Merchant != nil {
		t.Merchant = in.Merchant
	}
	if in.AmountMinor != 0 || in.Date != "" || in.Description != "" {
		if in.AmountMinor != 0 {
			t.AmountMinor = in.AmountMinor
		}
	}
	if in.Currency != "" {
		t.Currency = in.Currency
	}
	if in.CategoryID != nil {
		t.CategoryID = in.CategoryID
		t.CategorySource = categorySource
		if categorySource == "" {
			t.CategorySource = core.CategorySourceUser
		}
		t.CategoryConfidence = nil
	}
	if in.Type != "" && in.Type != t.Type {
		if t.TransferID != nil {
			return nil, &ValidationError{Message: "Break the transfer link before changing its type"}
		}
		t.Type = in.Type
	}
	if err := s.eligibleCategory(t.Type, t.CategoryID); err != nil {
		return nil, err
	}
	if len(t.Date) != 10 {
		return nil, &ValidationError{Message: "Date is required (YYYY-MM-DD)"}
	}
	if t.Description == "" {
		return nil, &ValidationError{Message: "Description is required"}
	}
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Repos.Update(tx, t); err != nil {
			return err
		}
		after := s.snapshot(t)
		diff := fieldDiffs(before, after)
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "transaction.edited",
			EntityType: "transaction", EntityID: t.ID,
			Title: "Edited transaction", Sub: t.Description,
			Diff: diff, Before: before, After: after,
			Revert: map[string]any{"transaction_id": t.ID, "fields": before},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Repos.Get(id)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func trimQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// Delete soft-deletes (audit transaction.deleted) — the history engine can
// restore it (doc 11 §4.5).
func (s *Service) Delete(id string) error {
	t, err := s.Repos.Get(id)
	if err != nil {
		return err
	}
	return s.DB.WithTx(func(tx *sql.Tx) error {
		// Unlink first if it is a transfer leg.
		if t.TransferID != nil {
			if err := s.Repos.DeleteTransfer(tx, *t.TransferID); err != nil {
				return err
			}
		}
		if err := s.Repos.SoftDelete(id); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "transaction.deleted",
			EntityType: "transaction", EntityID: id,
			Title: "Deleted transaction", Sub: t.Description,
			Before: s.snapshot(t),
			Revert: map[string]any{"transaction_id": id, "restore": true},
		})
		return err
	})
}

// SetCategory assigns a category (source = user) — used by review, inline
// editor and the review queue; audit transaction.edited.
func (s *Service) SetCategory(id string, categoryID *string) error {
	t, err := s.Repos.Get(id)
	if err != nil {
		return err
	}
	if t.TransferID != nil {
		return &ValidationError{Message: "Transfers are always the Internal transfer category"}
	}
	txType := t.Type
	if err := s.eligibleCategory(txType, categoryID); err != nil {
		return err
	}
	before := s.snapshot(t)
	t.CategoryID = categoryID
	t.CategorySource = core.CategorySourceUser
	t.CategoryConfidence = nil
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Repos.Update(tx, t); err != nil {
			return err
		}
		after := s.snapshot(t)
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "transaction.edited",
			EntityType: "transaction", EntityID: id,
			Title: "Categorised transaction", Sub: t.Description,
			Diff: fieldDiffs(before, after), Before: before, After: after,
			Revert: map[string]any{"transaction_id": id, "fields": before},
		})
		return err
	})
}

// Snapshot helpers for the audit payloads / engine re-application.

// TxSnapshot is the engine-facing transaction snapshot (doc 11 §4.5).
type TxSnapshot struct {
	ID              string  `json:"id"`
	AccountID       string  `json:"account_id"`
	Date            string  `json:"date"`
	Description     string  `json:"description"`
	Merchant        *string `json:"merchant"`
	AmountMinor     int64   `json:"amount_minor"`
	Currency        string  `json:"currency"`
	Type            string  `json:"type"`
	CategoryID      *string `json:"category_id"`
	CategorySource  string  `json:"category_source"`
	SourceText      string  `json:"source_text"`
	DestinationText string  `json:"destination_text"`
	RecurringKey    *string `json:"recurring_key"`
	TransferID      *string `json:"transfer_id"`
	ImportBatchID   *string `json:"import_batch_id"`
}

// Snapshot projects a transaction to its audit payload.
func (s *Service) Snapshot(t *core.Transaction) TxSnapshot { return s.snapshot(t) }

func (s *Service) snapshot(t *core.Transaction) TxSnapshot {
	return TxSnapshot{
		ID: t.ID, AccountID: t.AccountID, Date: t.Date, Description: t.Description,
		Merchant: t.Merchant, AmountMinor: t.AmountMinor, Currency: t.Currency,
		Type: t.Type, CategoryID: t.CategoryID, CategorySource: t.CategorySource,
		SourceText: t.SourceText, DestinationText: t.DestinationText,
		RecurringKey: t.RecurringKey, TransferID: t.TransferID, ImportBatchID: t.ImportBatchID,
	}
}

func (s *Service) mustReload(id string) *core.Transaction {
	t, err := s.Repos.GetTx(id)
	if err != nil {
		return nil
	}
	return t
}

// fieldDiffs renders a compact before/after diff for the audit entry.
func fieldDiffs(before, after TxSnapshot) []audit.DiffLine {
	var out []audit.DiffLine
	pair := func(label string, b, a any) {
		if fmt.Sprint(b) != fmt.Sprint(a) {
			out = append(out,
				audit.DiffLine{Line: "− " + label + ": " + fmt.Sprint(b), Cls: "diff-del"},
				audit.DiffLine{Line: "+ " + label + ": " + fmt.Sprint(a), Cls: "diff-add"})
		}
	}
	pair("date", before.Date, after.Date)
	pair("description", before.Description, after.Description)
	pair("amount", before.AmountMinor, after.AmountMinor)
	pair("type", before.Type, after.Type)
	pair("category", before.CategoryID, after.CategoryID)
	return out
}

// IsNotFound maps driver errors.
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
// Get returns one enriched transaction.
func (s *Service) Get(id string) (*core.Transaction, error) {
	return s.Repos.Get(id)
}
