// Package core holds the shared domain entity types for Glowworm's services
// (docs/design/02-data-model.md). Services live in core/<concern> subpackages.
package core

import "time"

// Actor kinds for audit entries.
const (
	ActorUser   = "user"
	ActorAI     = "ai"
	ActorSystem = "system"
)

// Transaction types.
const (
	TypeExpense  = "expense"
	TypeIncome   = "income"
	TypeTransfer = "transfer"
)

// Account types.
const (
	AccountDebit   = "debit"
	AccountCredit  = "credit"
	AccountSavings = "savings"
)

// Category types.
const (
	CategorySpend    = "spend"
	CategoryIncome   = "income"
	CategoryInternal = "internal"
)

// Category sources (who assigned the category).
const (
	CategorySourceUser = "user"
	CategorySourceLocal = "local"
	CategorySourceAI    = "ai"
	CategorySourceNone  = "none"
)

// Palette is the prototype colour palette (accounts, categories, widgets).
var Palette = []string{
	"#0e6f51", "#4f5bc7", "#c9822e", "#c0483f",
	"#2f7fb0", "#7a58b8", "#2e9d8f", "#6b7280",
}

// Account is a user-owned financial account (doc 02 §3.1).
type Account struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Type               string     `json:"type"`
	Color              string     `json:"color"`
	Currency           string     `json:"currency"`
	OpeningBalanceMinor int64     `json:"opening_balance_minor"`
	OpeningBalanceDate string     `json:"opening_balance_date"` // "" = not set
	AccountNumber      string     `json:"-"`
	SortCode           string     `json:"-"`
	IBAN               string     `json:"-"`
	BIC                string     `json:"-"`
	Notes              string     `json:"notes"`
	CreatedAt          string     `json:"created_at"`
	UpdatedAt          string     `json:"updated_at"`
	DeletedAt          *string    `json:"-"`
	// Computed (list/detail views):
	CurrentBalanceMinor int64      `json:"current_balance_minor"`
	RefLast4            string     `json:"ref_last4"`
	HasNumber           bool       `json:"has_account_number"`
	HasIBAN             bool       `json:"has_iban"`
	HasBIC              bool       `json:"has_bic"`
	TxCount             int        `json:"tx_count"`
	CorrectionsCount    int        `json:"corrections_count"`
}

// Valuation is a point-in-time correction (doc 02 §3.2).
type Valuation struct {
	ID          string  `json:"id"`
	AccountID   string  `json:"account_id"`
	ValueMinor  int64   `json:"value_minor"`
	Date        string  `json:"date"`
	Note        string  `json:"note"`
	Source      string  `json:"source"`
	DriftMinor  *int64  `json:"drift_minor"`
	AuditLogID  *int64  `json:"audit_log_id"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	DeletedAt   *string `json:"-"`
}

// Category (doc 02 §4).
type Category struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Color     string  `json:"color"`
	Type      string  `json:"type"`
	IsSystem  bool    `json:"is_system"`
	SortOrder int     `json:"sort_order"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	DeletedAt *string `json:"-"`
	// Computed:
	SpentMinor *int64 `json:"spent_minor"` // nil → card shows "—"
}

// Transaction — one row from one statement (doc 02 §5.1).
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
	DedupeHash         string   `json:"-"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
	DeletedAt          *string  `json:"-"`

	// Enriched (API/display rows, doc 03 §5.1):
	Category        *Category `json:"category,omitempty"`
	Account         *Account  `json:"account,omitempty"`
	Counterparty    string    `json:"counterparty,omitempty"`
	SourceName      string    `json:"source,omitempty"`
	DestinationName string    `json:"destination,omitempty"`
	Linked          bool      `json:"linked"`
	Recurring       *string   `json:"recurring,omitempty"` // recurring series label
}

// Transfer links two legs (doc 02 §5.2).
type Transfer struct {
	ID             string   `json:"id"`
	SourceTxID     string   `json:"source_transaction_id"`      // outbound (amount < 0)
	DestinationTxID string  `json:"destination_transaction_id"` // inbound (amount > 0)
	Detected       string   `json:"detected"` // auto | manual
	Method         string   `json:"method"`   // identifier | description | amount-date | manual
	Confidence     *float64 `json:"confidence"`
	CreatedAt      string   `json:"created_at"`
	DeletedAt      *string  `json:"-"`
}

// RecurringKey metadata (doc 02 §6.1).
type RecurringKey struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	Cadence      string   `json:"cadence"`
	IntervalDays *int     `json:"interval_days"`
	AmountMinor  *int64   `json:"amount_minor"`
	Direction    string   `json:"direction"`
	FirstSeen    string   `json:"first_seen"`
	LastSeen     string   `json:"last_seen"`
	NextExpected *string  `json:"next_expected"`
	Confidence   float64  `json:"confidence"`
	Active       bool     `json:"active"`
	UpdatedAt    string   `json:"updated_at"`
}

// ImportBatch (doc 02 §7.1).
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

// ImportSchema (doc 02 §7.3).
type ImportSchema struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	BankHint    string   `json:"bank_hint"`
	Kind        string   `json:"kind"`
	Mapping     string   `json:"mapping"`  // JSON column mapping (doc 06 §5.2)
	Columns     []string `json:"columns"`  // ordered source column names
	SampleHash  string   `json:"-"`
	Confidence  float64  `json:"confidence"`
	UseCount    int      `json:"use_count"`
	LastUsedAt  *string  `json:"last_used_at"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// PinnedWidget (doc 02 §8.1).
type PinnedWidget struct {
	ID        string `json:"id"`
	Position  int    `json:"position"`
	Col       int    `json:"col"`
	Rows      int    `json:"rows"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	Dataset   string `json:"dataset"` // JSON canonical dataset
	Plan      *string `json:"plan"`   // JSON query plan (re-executed on dashboard render)
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// User (doc 02 §2.2).
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	PasswordHash string `json:"-"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// AuditEntry (doc 02 §8.3).
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
	After       string  `json:"after"`
	Revert      string `json:"revert"`
	PathID      string `json:"path_id"`
	State       string `json:"state"`

	// Enriched:
	DiffLines []DiffLine `json:"diff_lines,omitempty"`
}

// DiffLine is one renderable diff row (doc 11 §2).
type DiffLine struct {
	Line string `json:"line"`
	Cls  string `json:"cls"` // diff-add | diff-del
}

// AuditPath (doc 02 §8.3 / doc 11 §4.1).
type AuditPath struct {
	ID          string `json:"id"`
	ParentPathID *string `json:"parent_path_id"`
	ForkEntryID  *int64  `json:"fork_entry_id"`
	Label        string  `json:"label"`
	IsActive     bool    `json:"is_active"`
	CreatedAt    string  `json:"created_at"`
}

// Conversation / Message (doc 02 §8.2).
type Conversation struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Message struct {
	ID             string  `json:"id"`
	ConversationID string  `json:"conversation_id"`
	Role           string  `json:"role"`
	Text           string  `json:"text"`
	Steps          []string `json:"steps"`
	Widget         *string `json:"widget"` // JSON widget (with dataset)
	Dataset        *string `json:"dataset"`
	Plan           *string `json:"plan"`
	CreatedAt      string  `json:"created_at"`
}

// Date parsing helpers used by services.
var _ = time.Now
