package core

import (
	"regexp"
)

const (
	ActorUser   = "user"
	ActorAI     = "ai"
	ActorSystem = "system"
)

const (
	TypeExpense  = "expense"
	TypeIncome   = "income"
	TypeTransfer = "transfer"
)

const (
	AccountDebit   = "debit"
	AccountCredit  = "credit"
	AccountSavings = "savings"
)

const (
	CategorySpend    = "spend"
	CategoryIncome   = "income"
	CategoryInternal = "internal"
)

// InternalTransferCategoryID is the system category every transfer leg is
// in. The schema seeds it, and system categories can't be renamed or deleted.
const InternalTransferCategoryID = "c-system-internal"

const (
	CategorySourceUser  = "user"
	CategorySourceLocal = "local"
	CategorySourceAI    = "ai"
	CategorySourceNone  = "none"
)

var colorRE = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// ValidColor reports whether c is a hex colour like #2e9d8f. Colours end up
// in style attributes, so nothing else is stored.
func ValidColor(c string) bool { return colorRE.MatchString(c) }

var Palette = []string{
	"#0e6f51", "#4f5bc7", "#c9822e", "#c0483f",
	"#2f7fb0", "#7a58b8", "#2e9d8f", "#6b7280",
}

type Account struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Type                string `json:"type"`
	Color               string `json:"color"`
	Currency            string `json:"currency"`
	OpeningBalanceMinor int64  `json:"opening_balance_minor"`
	OpeningBalanceDate  string `json:"opening_balance_date"`
	AccountNumber       string `json:"-"`
	SortCode            string `json:"-"`
	IBAN                string `json:"-"`
	BIC                 string `json:"-"`
	Notes               string `json:"notes"`
	// InterestRate is a yearly ratio (0.045 for 4.5%), nil when not known.
	InterestRate *float64 `json:"interest_rate"`
	// TaxWrapper is "" (none), "isa", "lisa", "pension" or "other".
	TaxWrapper string `json:"tax_wrapper"`
	// BalanceReminderDays: nil follows the reminder settings, 0 never
	// reminds, N reminds once the latest recorded balance is N days old.
	BalanceReminderDays *int64  `json:"balance_reminder_days"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
	DeletedAt           *string `json:"-"`
	CurrentBalanceMinor int64   `json:"current_balance_minor"`
	RefLast4            string  `json:"ref_last4"`
	HasNumber           bool    `json:"has_account_number"`
	HasIBAN             bool    `json:"has_iban"`
	HasBIC              bool    `json:"has_bic"`
	TxCount             int     `json:"tx_count"`
	CorrectionsCount    int     `json:"corrections_count"`
}

type Valuation struct {
	ID         string  `json:"id"`
	AccountID  string  `json:"account_id"`
	ValueMinor int64   `json:"value_minor"`
	Date       string  `json:"date"`
	Note       string  `json:"note"`
	Source     string  `json:"source"`
	DriftMinor *int64  `json:"drift_minor"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	DeletedAt  *string `json:"-"`
}

type Category struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Color       string  `json:"color"`
	Type        string  `json:"type"`
	Description string  `json:"description"`
	IsSystem    bool    `json:"is_system"`
	SortOrder   int     `json:"sort_order"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	DeletedAt   *string `json:"-"`
	SpentMinor  *int64  `json:"spent_minor"`
}

type Transaction struct {
	ID                 string   `json:"id"`
	AccountID          string   `json:"account_id"`
	Date               string   `json:"date"`
	Description        string   `json:"description"`
	Merchant           *string  `json:"merchant"`
	AmountMinor        int64    `json:"amount_minor"`
	Currency           string   `json:"currency"`
	Type               string   `json:"type"`
	CategoryID         *string  `json:"category_id"`
	CategorySource     string   `json:"category_source"`
	CategoryConfidence *float64 `json:"category_confidence"`
	SourceText         string   `json:"source_text"`
	DestinationText    string   `json:"destination_text"`
	RecurringKey       *string  `json:"recurring_key"`
	TransferID         *string  `json:"transfer_id"`
	ImportBatchID      *string  `json:"import_batch_id"`
	// OriginalAmountMinor/OriginalCurrency: the amount actually paid when it
	// wasn't in the account's currency (signed like AmountMinor).
	OriginalAmountMinor *int64  `json:"original_amount_minor,omitempty"`
	OriginalCurrency    string  `json:"original_currency,omitempty"`
	DedupeHash          string  `json:"-"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
	DeletedAt           *string `json:"-"`

	Category        *Category `json:"category,omitempty"`
	Account         *Account  `json:"account,omitempty"`
	Counterparty    string    `json:"counterparty,omitempty"`
	SourceName      string    `json:"source,omitempty"`
	DestinationName string    `json:"destination,omitempty"`
	Linked          bool      `json:"linked"`
	Recurring       *string   `json:"recurring,omitempty"`
}

type Transfer struct {
	ID              string   `json:"id"`
	SourceTxID      string   `json:"source_transaction_id"`
	DestinationTxID string   `json:"destination_transaction_id"`
	Detected        string   `json:"detected"`
	Method          string   `json:"method"`
	Confidence      *float64 `json:"confidence"`
	CreatedAt       string   `json:"created_at"`
	DeletedAt       *string  `json:"-"`
}

type RecurringKey struct {
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	Cadence      string  `json:"cadence"`
	IntervalDays *int    `json:"interval_days"`
	AmountMinor  *int64  `json:"amount_minor"`
	Direction    string  `json:"direction"`
	FirstSeen    string  `json:"first_seen"`
	LastSeen     string  `json:"last_seen"`
	NextExpected *string `json:"next_expected"`
	Confidence   float64 `json:"confidence"`
	Active       bool    `json:"active"`
	UpdatedAt    string  `json:"updated_at"`
}

type ImportBatch struct {
	ID          string  `json:"id"`
	Filename    string  `json:"filename"`
	SizeBytes   int64   `json:"size_bytes"`
	SHA256      string  `json:"-"`
	Mime        string  `json:"-"`
	AccountID   *string `json:"account_id"`
	SchemaID    *string `json:"schema_id"`
	Status      string  `json:"status"`
	Detection   *string `json:"-"`
	ParseError  *string `json:"-"`
	CreatedAt   string  `json:"created_at"`
	ConfirmedAt *string `json:"confirmed_at"`
}

type ImportSchema struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	BankHint    string   `json:"bank_hint"`
	Kind        string   `json:"kind"`
	Mapping     string   `json:"mapping"`
	Columns     []string `json:"columns"`
	SampleHash  string   `json:"-"`
	Confidence  float64  `json:"confidence"`
	UseCount    int      `json:"use_count"`
	LastUsedAt  *string  `json:"last_used_at"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	AccountID   *string  `json:"account_id"`
	AccountName string   `json:"account_name,omitempty"`
}

type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	PasswordHash string `json:"-"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type AuditEntry struct {
	ID          int64  `json:"id"`
	Timestamp   string `json:"timestamp"`
	Actor       string `json:"actor"`
	ActorDetail string `json:"actor_detail"`
	Action      string `json:"action"`
	EntityType  string `json:"entity_type"`
	EntityID    string `json:"entity_id"`
	Title       string `json:"title"`
	Sub         string `json:"sub"`
	Diff        string `json:"diff"`
	Before      string `json:"before"`
	After       string `json:"after"`
	Revert      string `json:"revert"`
	PathID      string `json:"path_id"`
	State       string `json:"state"`
	CausedBy    int64  `json:"caused_by,omitempty"`

	DiffLines []DiffLine `json:"diff_lines,omitempty"`
}

type DiffLine struct {
	Line string `json:"line"`
	Cls  string `json:"cls"`
}

type Conversation struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Message struct {
	ID             string   `json:"id"`
	ConversationID string   `json:"conversation_id"`
	Role           string   `json:"role"`
	Text           string   `json:"text"`
	Steps          []string `json:"steps"`
	Widget         *string  `json:"widget"`
	CreatedAt      string   `json:"created_at"`
}

func (t *Transaction) AccountName() string {
	if t.Account != nil {
		return t.Account.Name
	}
	return t.AccountID
}

func (t *Transaction) RecurringLabel() string {
	if t.Recurring != nil {
		return *t.Recurring
	}
	if t.RecurringKey != nil {
		return *t.RecurringKey
	}
	return ""
}
