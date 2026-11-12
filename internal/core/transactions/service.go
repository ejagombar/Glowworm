package transactions

import (
	"database/sql"
	"fmt"

	"glowworm/internal/clock"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

type Service struct {
	DB         *store.DB
	Repos      *repos.Transactions
	Categories *repos.Categories
	Audit      *audit.Writer
	Clock      *clock.Clock
}

var ErrNotFound = repos.ErrNotFound

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// ConflictError is an edit made to a transaction that has changed since the
// editor loaded it, on another device or in another tab.
type ConflictError struct{ Description string }

func (e *ConflictError) Error() string {
	return "“" + e.Description + "” was changed somewhere else after you opened it. Reload to see the change, then edit again."
}

func (s *Service) deriveType(categoryID *string, linked bool) string {
	if linked {
		return core.TypeTransfer
	}
	if categoryID != nil && *categoryID != "" {
		if c, err := s.Categories.Get(*categoryID); err == nil {
			if c.Type == core.CategoryInternal {
				return core.TypeTransfer
			}
			if c.Type == core.CategoryIncome {
				return core.TypeIncome
			}
		}
	}
	return core.TypeExpense
}

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
		if *categoryID != core.InternalTransferCategoryID {
			return &ValidationError{Message: "Transfers are always the Internal transfer category"}
		}
	}
	return nil
}

type CreateInput struct {
	AccountID   string  `json:"account_id"`
	Date        string  `json:"date"`
	Description string  `json:"description"`
	AmountMinor int64   `json:"amount_minor"`
	Currency    string  `json:"currency"`
	Type        string  `json:"type"`
	CategoryID  *string `json:"category_id"`
	Merchant    *string `json:"merchant"`
	// ExpectedUpdatedAt, when set, is the version of the transaction the
	// edit was made against; a newer one is a conflict, not overwritten.
	ExpectedUpdatedAt string `json:"expected_updated_at,omitempty"`
}

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
	case core.TypeExpense, core.TypeIncome, "":
	default:
		return &ValidationError{Message: "Type must be expense, income or transfer"}
	}
	if in.Type == core.TypeTransfer {
		return &ValidationError{Message: "Create the two legs first, then link them as a transfer"}
	}
	return nil
}

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
		CategoryID:     in.CategoryID,
		CategorySource: core.CategorySourceUser,
	}
	if in.CategoryID == nil {
		t.CategorySource = core.CategorySourceNone
	}
	t.Type = in.Type
	if t.Type == "" {
		t.Type = s.deriveType(in.CategoryID, false)
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

func (s *Service) Edit(id string, in CreateInput, categorySource string) (*core.Transaction, error) {
	t, err := s.Repos.Get(id)
	if err != nil {
		return nil, err
	}
	if in.ExpectedUpdatedAt != "" && in.ExpectedUpdatedAt != t.UpdatedAt {
		return nil, &ConflictError{Description: t.Description}
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
	if in.AccountID != "" && in.AccountID != t.AccountID {
		if t.TransferID != nil {
			return nil, &ValidationError{Message: "Break the transfer link before moving it to another account"}
		}
		var currency string
		err := s.DB.QueryRow(`SELECT currency FROM accounts WHERE id = ? AND deleted_at IS NULL`, in.AccountID).Scan(&currency)
		if err != nil {
			return nil, &ValidationError{Message: "That account doesn't exist"}
		}
		if currency != t.Currency {
			return nil, &ValidationError{Message: "That account is in " + currency + ", not " + t.Currency}
		}
		t.AccountID = in.AccountID
	}
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
	if in.Type != "" {
		if in.Type != t.Type && t.TransferID != nil {
			return nil, &ValidationError{Message: "Break the transfer link before changing its type"}
		}
		t.Type = in.Type
	} else if t.TransferID == nil {
		t.Type = s.deriveType(t.CategoryID, false)
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
		diff := s.fieldDiffs(before, after)
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

func (s *Service) Delete(id string) error {
	t, err := s.Repos.Get(id)
	if err != nil {
		return err
	}
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if t.TransferID != nil {
			if err := s.Repos.DeleteTransfer(tx, *t.TransferID); err != nil {
				return err
			}
		}
		if err := s.Repos.SoftDelete(tx, id); err != nil {
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
			Diff: s.fieldDiffs(before, after), Before: before, After: after,
			Revert: map[string]any{"transaction_id": id, "fields": before},
		})
		return err
	})
}

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

func (s *Service) fieldDiffs(before, after TxSnapshot) []audit.DiffLine {
	var out []audit.DiffLine
	pair := func(label string, b, a any) {
		if fmt.Sprint(b) != fmt.Sprint(a) {
			out = append(out,
				audit.DiffLine{Line: "− " + label + ": " + fmt.Sprint(b), Cls: "diff-del"},
				audit.DiffLine{Line: "+ " + label + ": " + fmt.Sprint(a), Cls: "diff-add"})
		}
	}
	if before.AccountID != after.AccountID {
		pair("account", s.accountName(before.AccountID), s.accountName(after.AccountID))
	}
	pair("date", before.Date, after.Date)
	pair("description", before.Description, after.Description)
	pair("amount", before.AmountMinor, after.AmountMinor)
	pair("type", before.Type, after.Type)
	if deref(before.CategoryID) != deref(after.CategoryID) {
		pair("category", s.categoryName(before.CategoryID), s.categoryName(after.CategoryID))
	}
	return out
}

func (s *Service) categoryName(id *string) string {
	if id == nil || *id == "" {
		return "Uncategorised"
	}
	var name string
	if s.DB.QueryRow(`SELECT name FROM categories WHERE id = ?`, *id).Scan(&name) != nil {
		return *id
	}
	return name
}

func (s *Service) accountName(id string) string {
	var name string
	if s.DB.QueryRow(`SELECT name FROM accounts WHERE id = ?`, id).Scan(&name) != nil {
		return id
	}
	return name
}

func (s *Service) Get(id string) (*core.Transaction, error) {
	return s.Repos.Get(id)
}
