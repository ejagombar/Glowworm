// Package changes applies edits proposed in Queries. The AI drafts a
// Proposal (an action and the transactions it would touch); the user sees
// every transaction in a preview and nothing changes until they apply it.
// Applying goes through the same services as editing by hand, and the edits
// are tied to one History entry so they're undone together.
package changes

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/transactions"
	"glowworm/internal/core/transfers"
	"glowworm/internal/store"
	"glowworm/internal/util"
)

const (
	SetCategory    = "set_category"
	Delete         = "delete"
	LinkTransfer   = "link_transfer"
	UnlinkTransfer = "unlink_transfer"

	// MaxTransactions caps one proposal, so a preview stays readable.
	MaxTransactions = 500
)

// Proposal is what the AI drafts and the user applies. IDs are fixed when
// it's drafted, so what the preview lists is exactly what gets changed.
type Proposal struct {
	ID         string   `json:"id"`
	Action     string   `json:"action"`
	IDs        []string `json:"ids"`
	CategoryID string   `json:"category_id,omitempty"`
}

// Row is one transaction in a preview. Skip says why it would be left alone.
type Row struct {
	ID          string `json:"id"`
	Date        string `json:"date"`
	Description string `json:"description"`
	Account     string `json:"account"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Category    string `json:"category"`
	Type        string `json:"type"`
	Linked      bool   `json:"linked"`
	Deleted     bool   `json:"deleted"`
	Skip        string `json:"skip,omitempty"`
}

type Preview struct {
	Proposal Proposal `json:"proposal"`
	Summary  string   `json:"summary"`
	Rows     []Row    `json:"rows"`
	// Ready counts the rows that would change.
	Ready int `json:"ready"`
	// Applied is set once the proposal has been applied (and not undone);
	// Done then says what it did, and the rows show how things are now.
	Applied bool   `json:"applied"`
	Done    string `json:"done,omitempty"`
}

type Service struct {
	DB           *store.DB
	Transactions *transactions.Service
	Transfers    *transfers.Service
	Audit        *audit.Writer
	// applyMu makes applying a change one at a time, so a double click or
	// two devices pressing Apply together can't both apply it.
	applyMu sync.Mutex
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, a ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, a...)}
}

// CategoryByName finds a category for a proposal, ignoring case.
func (s *Service) CategoryByName(name string) (string, error) {
	var id string
	err := s.DB.QueryRow(`SELECT id FROM categories WHERE name = ? COLLATE NOCASE AND deleted_at IS NULL`,
		strings.TrimSpace(name)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", invalid("there is no category called %q", name)
	}
	return id, err
}

// Draft fills in a new proposal's ID and checks it.
func (s *Service) Draft(p Proposal) (*Preview, error) {
	p.ID = util.NewID()
	return s.Preview(p)
}

// Preview lists what applying p would do, without changing anything.
func (s *Service) Preview(p Proposal) (*Preview, error) {
	p.IDs = unique(p.IDs)
	if len(p.IDs) == 0 {
		return nil, invalid("no transactions selected")
	}
	if len(p.IDs) > MaxTransactions {
		return nil, invalid("%d transactions is too many for one change (at most %d)", len(p.IDs), MaxTransactions)
	}
	var catName, catType string
	switch p.Action {
	case SetCategory:
		if err := s.DB.QueryRow(`SELECT name, type FROM categories WHERE id = ? AND deleted_at IS NULL`,
			p.CategoryID).Scan(&catName, &catType); err != nil {
			return nil, invalid("unknown category")
		}
		if catType == core.CategoryInternal {
			return nil, invalid("transactions become internal transfers by linking two of them, not by category")
		}
	case Delete, UnlinkTransfer:
	case LinkTransfer:
		if len(p.IDs) != 2 {
			return nil, invalid("a transfer links exactly two transactions, not %d", len(p.IDs))
		}
	default:
		return nil, invalid("unknown action %q", p.Action)
	}
	rows, err := s.rows(p.IDs)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, invalid("none of the selected transactions exist")
	}
	out := &Preview{Proposal: p, Rows: rows}
	if p.ID != "" {
		var n int
		_ = s.DB.QueryRow(`SELECT COUNT(*) FROM audit_entries WHERE action = 'change.applied'
			AND entity_id = ? AND state = 'active'`, p.ID).Scan(&n)
		out.Applied = n > 0
	}
	for i := range rows {
		if rows[i].Deleted {
			rows[i].Skip = "deleted"
		}
	}
	switch p.Action {
	case SetCategory:
		for i := range rows {
			r := &rows[i]
			switch {
			case r.Skip != "":
			case r.Linked:
				r.Skip = "a linked transfer"
			case r.Category == catName:
				r.Skip = "already " + catName
			case r.Type == core.TypeTransfer:
				r.Skip = "an internal transfer"
			case (catType == core.CategoryIncome) != (r.Type == core.TypeIncome):
				r.Skip = "it's " + r.Type + ", " + catName + " is " + catTypeLabel(catType)
			}
		}
		out.Summary = "Set %s to " + catName
	case Delete:
		out.Summary = "Delete %s"
	case UnlinkTransfer:
		for i := range rows {
			if rows[i].Skip == "" && !rows[i].Linked {
				rows[i].Skip = "not a linked transfer"
			}
		}
		out.Summary = "Break the transfer link on %s"
	case LinkTransfer:
		if len(rows) == 2 {
			a, b := rows[0], rows[1]
			var why string
			switch {
			case out.Applied:
				rows[0].Skip, rows[1].Skip = "linked", "linked"
			case a.Deleted || b.Deleted:
				why = "one of them is deleted"
			case a.Linked || b.Linked:
				why = "one of them is already linked"
			case a.Account == b.Account:
				why = "both are in " + a.Account
			case (a.AmountMinor > 0) == (b.AmountMinor > 0):
				why = "both are money " + direction(a.AmountMinor)
			}
			if why != "" {
				return nil, invalid("these two can't be linked as a transfer: %s", why)
			}
		} else {
			return nil, invalid("one of the two transactions doesn't exist")
		}
		out.Summary = "Link %s as an internal transfer"
	}
	if out.Applied {
		_ = s.DB.QueryRow(`SELECT sub FROM audit_entries WHERE action = 'change.applied'
			AND entity_id = ? AND state = 'active' ORDER BY id DESC LIMIT 1`, p.ID).Scan(&out.Done)
		for i := range rows {
			rows[i].Skip = ""
		}
		out.Summary = out.Done
		return out, nil
	}
	for _, r := range rows {
		if r.Skip == "" {
			out.Ready++
		}
	}
	out.Summary = fmt.Sprintf(out.Summary, count(out.Ready))
	return out, nil
}

func direction(amount int64) string {
	if amount > 0 {
		return "in"
	}
	return "out"
}

func catTypeLabel(t string) string {
	if t == core.CategoryIncome {
		return "an income category"
	}
	return "a spend category"
}

func (s *Service) rows(ids []string) ([]Row, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := `SELECT t.id, t.date, t.description, a.name, t.amount_minor, t.currency,
			COALESCE(c.name, ''), t.type, t.transfer_id IS NOT NULL, t.deleted_at IS NOT NULL
		FROM transactions t JOIN accounts a ON a.id = t.account_id
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.id IN (?` + strings.Repeat(",?", len(ids)-1) + `)
		ORDER BY t.date DESC, t.id`
	rs, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Row
	for rs.Next() {
		var r Row
		if err := rs.Scan(&r.ID, &r.Date, &r.Description, &r.Account, &r.AmountMinor, &r.Currency,
			&r.Category, &r.Type, &r.Linked, &r.Deleted); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// Apply makes the change. Each transaction is edited through the usual
// service, then the edits are tied to one "change.applied" entry so undoing
// that entry undoes them all.
func (s *Service) Apply(p Proposal) (*Preview, error) {
	if p.ID == "" {
		return nil, invalid("this change has no id")
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	pv, err := s.Preview(p)
	if err != nil {
		return nil, err
	}
	if pv.Applied {
		return nil, invalid("this change has already been applied")
	}
	if pv.Ready == 0 {
		return nil, invalid("nothing to change: every transaction is already as asked")
	}
	var causeID int64
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		var err error
		causeID, err = s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, ActorDetail: "proposed in Queries",
			Action: "change.applied", EntityType: "change", EntityID: p.ID,
			Title: "Applied a change from Queries",
			Sub:   pv.Summary,
			After: p,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	transferIDs, applyErr := s.apply(p, pv)
	// Tie what was written to the change, even if it stopped part way, so
	// undoing it puts back whatever did get changed. Only entries about its
	// own transactions and transfers: another device may be saving too.
	ids := append([]string{}, p.IDs...)
	ids = append(ids, transferIDs...)
	args := []any{causeID, causeID}
	for _, id := range ids {
		args = append(args, id)
	}
	if _, err := s.DB.Exec(`UPDATE audit_entries SET caused_by = ? WHERE id > ? AND caused_by IS NULL
		AND entity_type IN ('transaction', 'transfer')
		AND entity_id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...); err != nil {
		return nil, err
	}
	if applyErr != nil {
		return nil, applyErr
	}
	return s.Preview(p)
}

// apply makes the edits, returning the transfers it linked or unlinked.
func (s *Service) apply(p Proposal, pv *Preview) ([]string, error) {
	if p.Action == LinkTransfer {
		tr, err := s.Transfers.Link(pv.Rows[0].ID, pv.Rows[1].ID, core.ActorUser)
		if err != nil {
			return nil, err
		}
		return []string{tr.ID}, nil
	}
	var transferIDs []string
	unlinked := map[string]bool{}
	for _, r := range pv.Rows {
		if r.Skip != "" || unlinked[r.ID] {
			continue
		}
		var err error
		switch p.Action {
		case SetCategory:
			cat := p.CategoryID
			err = s.Transactions.SetCategory(r.ID, &cat)
		case Delete:
			err = s.Transactions.Delete(r.ID)
		case UnlinkTransfer:
			// Unlinking one leg frees the other too.
			if t, e := s.Transactions.Get(r.ID); e == nil && t.TransferID != nil {
				transferIDs = append(transferIDs, *t.TransferID)
			}
			if other, e := s.Transfers.CounterpartOf(r.ID); e == nil && other != nil {
				unlinked[other.ID] = true
			}
			err = s.Transfers.Unlink(r.ID, core.ActorUser)
		}
		if err != nil {
			return transferIDs, fmt.Errorf("%s (%s): %w", r.Description, r.Date, err)
		}
	}
	return transferIDs, nil
}

func unique(ids []string) []string {
	seen := map[string]bool{}
	out := ids[:0:0]
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func count(n int) string {
	if n == 1 {
		return "1 transaction"
	}
	return fmt.Sprintf("%d transactions", n)
}
