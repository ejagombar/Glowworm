package accounts

import (
	"database/sql"
	"fmt"
	"strings"

	"glowworm/internal/clock"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/balances"
	"glowworm/internal/money"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

type Service struct {
	DB          *store.DB
	Repos       *repos.Accounts
	Txs         *repos.Transactions
	Audit       *audit.Writer
	Clock       *clock.Clock
	DisplayCC   string
	AfterChange func()
}

var ErrNotFound = repos.ErrNotFound

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func validType(t string) bool {
	return t == core.AccountDebit || t == core.AccountCredit || t == core.AccountSavings
}

func (s *Service) enrich(a *core.Account, txCounts map[string]int) (*core.Account, error) {
	b, err := balances.Account(s.DB.DB, a.ID, "")
	if err != nil {
		return nil, err
	}
	a.CurrentBalanceMinor = b
	a.TxCount = txCounts[a.ID]
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM valuations WHERE account_id = ? AND deleted_at IS NULL`, a.ID).Scan(&n)
	a.CorrectionsCount = n
	return a, nil
}

func (s *Service) List(txCounts map[string]int) ([]*core.Account, error) {
	list, err := s.Repos.List()
	if err != nil {
		return nil, err
	}
	out := make([]*core.Account, 0, len(list))
	for _, a := range list {
		e, err := s.enrich(a, txCounts)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Service) Get(id string) (*core.Account, error) {
	a, err := s.Repos.Get(id)
	if err != nil {
		return nil, err
	}
	return s.enrich(a, nil)
}

type Input struct {
	Name                string `json:"name"`
	Type                string `json:"type"`
	Currency            string `json:"currency"`
	OpeningBalanceMinor int64  `json:"opening_balance_minor"`
	OpeningBalanceDate  string `json:"opening_balance_date"`
	AccountNumber       string `json:"account_number"`
	SortCode            string `json:"sort_code"`
	IBAN                string `json:"iban"`
	BIC                 string `json:"bic"`
	Notes               string `json:"notes"`
	Color               string `json:"color"`
}

func (in *Input) Validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return &ValidationError{Message: "Account name is required"}
	}
	if !validType(in.Type) {
		return &ValidationError{Message: "Account type must be debit, credit or savings"}
	}
	if len(in.Name) > 80 {
		return &ValidationError{Message: "Account name is too long"}
	}
	if in.Color != "" && !core.ValidColor(in.Color) {
		return &ValidationError{Message: "Colour must be a hex code like #2e9d8f"}
	}
	return nil
}

func accountSnapshot(a *core.Account) map[string]any {
	return map[string]any{
		"id": a.ID, "name": a.Name, "type": a.Type, "color": a.Color,
		"currency": a.Currency, "opening_balance_minor": a.OpeningBalanceMinor,
		"opening_balance_date": a.OpeningBalanceDate, "notes": a.Notes,
	}
}

func (s *Service) Create(in Input) (*core.Account, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if in.Currency == "" {
		in.Currency = s.DisplayCC
	}
	color := in.Color
	if color == "" {
		color = s.Repos.NextColor()
	}
	a := &core.Account{
		Name: strings.TrimSpace(in.Name), Type: in.Type, Currency: in.Currency,
		OpeningBalanceMinor: in.OpeningBalanceMinor, OpeningBalanceDate: in.OpeningBalanceDate,
		AccountNumber: in.AccountNumber, SortCode: in.SortCode, IBAN: in.IBAN, BIC: in.BIC, Notes: in.Notes,
		Color: color,
	}
	var created *core.Account
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Repos.Create(tx, a); err != nil {
			return err
		}
		sub := fmt.Sprintf("opening %s", money.Format(a.OpeningBalanceMinor, a.Currency))
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "account.created",
			EntityType: "account", EntityID: a.ID,
			Title: "Added account", Sub: fmt.Sprintf("%s · %s", a.Name, sub),
			After:  accountSnapshot(a),
			Revert: map[string]any{"account_id": a.ID},
		})
		if err != nil {
			return err
		}
		created = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *Service) Update(id string, in Input) (*core.Account, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	a, err := s.Repos.Get(id)
	if err != nil {
		return nil, err
	}
	before := accountSnapshot(a)
	a.Name = strings.TrimSpace(in.Name)
	a.Type = in.Type
	if in.Currency != "" {
		a.Currency = in.Currency
	}
	a.OpeningBalanceMinor = in.OpeningBalanceMinor
	a.OpeningBalanceDate = in.OpeningBalanceDate
	a.AccountNumber = in.AccountNumber
	a.SortCode = in.SortCode
	a.IBAN = in.IBAN
	a.BIC = in.BIC
	if in.Color != "" {
		a.Color = in.Color
	}
	a.Notes = in.Notes
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Repos.Update(tx, a); err != nil {
			return err
		}
		after := accountSnapshot(a)
		diff := []audit.DiffLine{}
		for _, k := range []string{"name", "type", "currency", "opening_balance_minor", "opening_balance_date"} {
			bv, av := fmt.Sprint(before[k]), fmt.Sprint(after[k])
			if bv != av {
				diff = append(diff, audit.DiffLine{Line: "− " + k + ": " + bv, Cls: "diff-del"},
					audit.DiffLine{Line: "+ " + k + ": " + av, Cls: "diff-add"})
			}
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "account.updated",
			EntityType: "account", EntityID: a.ID,
			Title: "Updated account", Sub: a.Name,
			Diff: diff, Before: before, After: after,
			Revert: map[string]any{"account_id": a.ID, "fields": before},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Repos.Get(id)
}

type ValuationInput struct {
	ValueMinor int64  `json:"value_minor"`
	Date       string `json:"date"`
	Note       string `json:"note"`
}

type ValuationResult struct {
	Valuation     *core.Valuation `json:"valuation"`
	ComputedMinor int64           `json:"computed_minor"`
	DriftMinor    int64           `json:"drift_minor"`
}

func (s *Service) CreateValuation(accountID string, in ValuationInput, source string) (*ValuationResult, error) {
	a, err := s.Repos.Get(accountID)
	if err != nil {
		return nil, err
	}
	if in.Date == "" {
		in.Date = s.Clock.Today()
	}
	if len(in.Date) != 10 {
		return nil, &ValidationError{Message: "Valuation date must be YYYY-MM-DD"}
	}
	computed, err := balances.Account(s.DB.DB, accountID, in.Date)
	if err != nil {
		return nil, err
	}
	drift := in.ValueMinor - computed
	var res *ValuationResult
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		auditID, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "valuation.created",
			EntityType: "valuation", EntityID: "",
			Title: "Updated account value",
			Sub:   fmt.Sprintf("%s · %s", a.Name, in.Date),
			Diff: []audit.DiffLine{
				{Line: "− " + money.Format(computed, a.Currency), Cls: "diff-del"},
				{Line: "+ " + money.Format(in.ValueMinor, a.Currency), Cls: "diff-add"},
			},
			After:  map[string]any{"account_id": accountID, "value_minor": in.ValueMinor, "date": in.Date, "note": in.Note, "source": source},
			Revert: map[string]any{"valuation_id": "@valuation_id"},
		})
		if err != nil {
			return err
		}
		v := &core.Valuation{
			AccountID: accountID, ValueMinor: in.ValueMinor, Date: in.Date, Note: in.Note,
			Source: source, DriftMinor: &drift, AuditLogID: &auditID,
		}
		if err := s.Repos.CreateValuation(tx, v); err != nil {
			return err
		}
		if err := fillValuationAudit(tx, auditID, v.ID); err != nil {
			return err
		}
		res = &ValuationResult{Valuation: v, ComputedMinor: computed, DriftMinor: drift}
		return nil
	})
	return res, err
}

func fillValuationAudit(tx *sql.Tx, auditID int64, valuationID string) error {
	_, err := tx.Exec(`UPDATE audit_entries SET entity_id = ?,
		revert = json_object('valuation_id', ?) WHERE id = ?`, valuationID, valuationID, auditID)
	return err
}

// DeleteValuation removes a dated value (soft delete), logged so it can be
// undone from the audit log.
func (s *Service) DeleteValuation(accountID, valuationID string) error {
	a, err := s.Repos.Get(accountID)
	if err != nil {
		return err
	}
	v, err := s.Repos.GetValuation(valuationID)
	if err != nil || v.AccountID != accountID || v.DeletedAt != nil {
		return &ValidationError{Message: "That value no longer exists"}
	}
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Repos.DeleteValuation(tx, valuationID); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "valuation.deleted",
			EntityType: "valuation", EntityID: valuationID,
			Title:  "Removed account value",
			Sub:    fmt.Sprintf("%s · %s", a.Name, v.Date),
			Diff:   []audit.DiffLine{audit.Del(money.Format(v.ValueMinor, a.Currency) + " on " + v.Date)},
			Before: map[string]any{"account_id": accountID, "value_minor": v.ValueMinor, "date": v.Date},
			Revert: map[string]any{"valuation_id": valuationID},
		})
		return err
	})
	if err == nil {
		s.afterChange()
	}
	return err
}

func (s *Service) Valuations(accountID string) ([]*core.Valuation, error) {
	return s.Repos.Valuations(accountID)
}
