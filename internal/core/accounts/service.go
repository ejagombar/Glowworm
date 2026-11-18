package accounts

import (
	"database/sql"
	"encoding/json"
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

// Input is an account as a form sends it. The bank details and notes are
// optional: an edit that leaves one out keeps what the account has, so a
// form with no field for it, or one that never loaded it, can't blank it.
// Sending it empty clears it.
type Input struct {
	Name                string  `json:"name"`
	Type                string  `json:"type"`
	Currency            string  `json:"currency"`
	OpeningBalanceMinor int64   `json:"opening_balance_minor"`
	OpeningBalanceDate  string  `json:"opening_balance_date"`
	AccountNumber       *string `json:"account_number"`
	SortCode            *string `json:"sort_code"`
	IBAN                *string `json:"iban"`
	BIC                 *string `json:"bic"`
	Notes               *string `json:"notes"`
	Color               string  `json:"color"`
	// The savings details follow the same rule: left out keeps, sent sets.
	// InterestRate and BalanceReminderDays clear with null inside a
	// present field, so they are read through Opt.
	InterestRate        OptFloat `json:"interest_rate"`
	TaxWrapper          *string  `json:"tax_wrapper"`
	BalanceReminderDays OptInt   `json:"balance_reminder_days"`
}

// OptFloat is a JSON field that may be missing (Set false), null (Set,
// Value nil) or a number.
type OptFloat struct {
	Set   bool
	Value *float64
}

func (o *OptFloat) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	o.Value = &f
	return nil
}

// OptInt is OptFloat for whole numbers.
type OptInt struct {
	Set   bool
	Value *int64
}

func (o *OptInt) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	o.Value = &n
	return nil
}

// TaxWrappers are the tax wrappers an account can be in.
var TaxWrappers = map[string]string{"": "None", "isa": "ISA", "lisa": "Lifetime ISA", "pension": "Pension", "other": "Other"}

// sent sets dst to a value the input carries, and leaves it alone otherwise.
func sent(dst *string, v *string) {
	if v != nil {
		*dst = strings.TrimSpace(*v)
	}
}

// details puts the bank details and notes the input carries on an account.
func (in *Input) details(a *core.Account) {
	sent(&a.AccountNumber, in.AccountNumber)
	sent(&a.SortCode, in.SortCode)
	sent(&a.IBAN, in.IBAN)
	sent(&a.BIC, in.BIC)
	sent(&a.Notes, in.Notes)
	sent(&a.TaxWrapper, in.TaxWrapper)
	if in.InterestRate.Set {
		a.InterestRate = in.InterestRate.Value
	}
	if in.BalanceReminderDays.Set {
		a.BalanceReminderDays = in.BalanceReminderDays.Value
	}
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
	if in.TaxWrapper != nil {
		if _, ok := TaxWrappers[strings.TrimSpace(*in.TaxWrapper)]; !ok {
			return &ValidationError{Message: "Tax wrapper must be none, isa, lisa, pension or other"}
		}
	}
	if r := in.InterestRate.Value; r != nil && (*r < -1 || *r > 1) {
		return &ValidationError{Message: "Interest rate is a yearly ratio: 0.045 for 4.5%"}
	}
	if d := in.BalanceReminderDays.Value; d != nil && (*d < 0 || *d > 3660) {
		return &ValidationError{Message: "Reminder interval must be between 0 (never) and 3660 days"}
	}
	return nil
}

func accountSnapshot(a *core.Account) map[string]any {
	return map[string]any{
		"id": a.ID, "name": a.Name, "type": a.Type, "color": a.Color,
		"currency": a.Currency, "opening_balance_minor": a.OpeningBalanceMinor,
		"opening_balance_date": a.OpeningBalanceDate, "notes": a.Notes,
		// The bank details too, or undoing an edit couldn't put them back.
		// Snapshots stay in the database and are never shown.
		"account_number": a.AccountNumber, "sort_code": a.SortCode, "iban": a.IBAN, "bic": a.BIC,
		"interest_rate": floatOrNil(a.InterestRate), "tax_wrapper": a.TaxWrapper,
		"balance_reminder_days": intOrNil(a.BalanceReminderDays),
	}
}

func floatOrNil(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func intOrNil(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
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
		Color: color,
	}
	in.details(a)
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
	if in.Color != "" {
		a.Color = in.Color
	}
	in.details(a)
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Repos.Update(tx, a); err != nil {
			return err
		}
		after := accountSnapshot(a)
		diff := []audit.DiffLine{}
		for _, k := range []string{"name", "type", "currency", "opening_balance_minor", "opening_balance_date",
			"interest_rate", "tax_wrapper", "balance_reminder_days"} {
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
		v := &core.Valuation{
			AccountID: accountID, ValueMinor: in.ValueMinor, Date: in.Date, Note: in.Note,
			Source: source, DriftMinor: &drift,
		}
		if err := s.Repos.CreateValuation(tx, v); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "valuation.created",
			EntityType: "valuation", EntityID: v.ID,
			Title: "Updated account value",
			Sub:   fmt.Sprintf("%s · %s", a.Name, in.Date),
			Diff: []audit.DiffLine{
				{Line: "− " + money.Format(computed, a.Currency), Cls: "diff-del"},
				{Line: "+ " + money.Format(in.ValueMinor, a.Currency), Cls: "diff-add"},
			},
			After:  map[string]any{"account_id": accountID, "value_minor": in.ValueMinor, "date": in.Date, "note": in.Note, "source": source},
			Revert: map[string]any{"valuation_id": v.ID},
		})
		if err != nil {
			return err
		}
		res = &ValuationResult{Valuation: v, ComputedMinor: computed, DriftMinor: drift}
		return nil
	})
	return res, err
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
