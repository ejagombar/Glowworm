package repos

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"glowworm/internal/core"
	"glowworm/internal/core/normalize"
	"glowworm/internal/store"
	"glowworm/internal/util"
)

type Transactions struct{ DB *store.DB }

func DedupeHash(accountID, date string, amountMinor int64, description string) string {
	h := sha256.Sum256([]byte(accountID + "|" + date + "|" + fmt.Sprint(amountMinor) + "|" + normalize.Text(description)))
	return hex.EncodeToString(h[:8])
}

const txCols = `t.id, t.account_id, t.date, t.description, t.merchant, t.amount_minor, t.currency, t.type,
	t.category_id, t.category_source, t.category_confidence, t.source_text, t.destination_text,
	t.recurring_key, t.transfer_id, t.import_batch_id, t.created_at, t.updated_at, t.deleted_at,
	t.original_amount_minor, COALESCE(t.original_currency, '')`

func scanTx(sc Scanner) (*core.Transaction, error) {
	var t core.Transaction
	var merchant, catID, recKey, transferID, batchID, deletedAt sql.NullString
	var conf sql.NullFloat64
	var origAmount sql.NullInt64
	err := sc.Scan(&t.ID, &t.AccountID, &t.Date, &t.Description, &merchant, &t.AmountMinor, &t.Currency, &t.Type,
		&catID, &t.CategorySource, &conf, &t.SourceText, &t.DestinationText,
		&recKey, &transferID, &batchID, &t.CreatedAt, &t.UpdatedAt, &deletedAt,
		&origAmount, &t.OriginalCurrency)
	if err != nil {
		return nil, err
	}
	if origAmount.Valid {
		n := origAmount.Int64
		t.OriginalAmountMinor = &n
	}
	if merchant.Valid {
		s := merchant.String
		t.Merchant = &s
	}
	if catID.Valid {
		s := catID.String
		t.CategoryID = &s
	}
	if conf.Valid {
		f := conf.Float64
		t.CategoryConfidence = &f
	}
	if recKey.Valid {
		s := recKey.String
		t.RecurringKey = &s
	}
	if transferID.Valid {
		s := transferID.String
		t.TransferID = &s
		t.Linked = true
	}
	if batchID.Valid {
		s := batchID.String
		t.ImportBatchID = &s
	}
	if deletedAt.Valid {
		s := deletedAt.String
		t.DeletedAt = &s
	}
	return &t, nil
}

type ListFilter struct {
	AccountID   string
	Q           string
	CategoryID  string
	Type        string
	From, To    string
	Sort        string
	Limit       int
	NeedsReview bool
}

func (f ListFilter) where() (string, []any) {
	w := ` WHERE t.deleted_at IS NULL`
	args := []any{}
	if f.AccountID != "" {
		w += ` AND t.account_id = ?`
		args = append(args, f.AccountID)
	}
	if f.CategoryID != "" {
		w += ` AND t.category_id = ?`
		args = append(args, f.CategoryID)
	}
	switch f.Type {
	case "recurring":
		w += ` AND t.recurring_key IS NOT NULL`
	case core.TypeExpense, core.TypeIncome, core.TypeTransfer:
		w += ` AND t.type = ?`
		args = append(args, f.Type)
	}
	if f.From != "" {
		w += ` AND t.date >= ?`
		args = append(args, f.From)
	}
	if f.To != "" {
		w += ` AND t.date <= ?`
		args = append(args, f.To)
	}
	if f.NeedsReview {
		w += ` AND t.category_source = 'none'`
	}
	return w, args
}

func txOrder(sort string) string {
	switch sort {
	case "date_asc":
		return ` ORDER BY t.date ASC, t.created_at ASC`
	case "amount_desc":
		return ` ORDER BY t.amount_minor ASC, t.date DESC`
	case "amount_asc":
		return ` ORDER BY t.amount_minor DESC, t.date DESC`
	default:
		return ` ORDER BY t.date DESC, t.created_at DESC`
	}
}

func (r *Transactions) List(f ListFilter) ([]*core.Transaction, error) {
	w, args := f.where()
	q := `SELECT ` + txCols + ` FROM transactions t` + w + txOrder(f.Sort)
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := r.query(q, args...)
	if err != nil {
		return nil, err
	}
	return r.enrich(rows)
}

// SearchMatch is the transactions whose description, group, category,
// recurring series or the bank's source/destination text contains the
// search, ignoring case and spaces: "PureGym" finds "Pure Gym Ltd" and
// everything in the PureGym group. Account names aren't searched, as the
// account filter does that.
func (r *Transactions) SearchMatch(f ListFilter) ([]*core.Transaction, error) {
	w, args := f.where()
	q := `SELECT ` + txCols + ` FROM transactions t` + w + txOrder(f.Sort)
	// The names matched are looked up after the query, so the limit is
	// applied once they have been.
	if f.Limit > 0 && f.Q == "" {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := r.query(q, args...)
	if err != nil {
		return nil, err
	}
	enr, err := r.enrich(rows)
	if err != nil {
		return nil, err
	}
	if f.Q == "" {
		return enr, nil
	}
	squash := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), "") }
	needle := squash(f.Q)
	out := enr[:0]
	for _, t := range enr {
		for _, field := range []string{
			t.Description, deref(t.Merchant), categoryName(t.Category), t.RecurringLabel(),
			t.SourceText, t.DestinationText,
		} {
			if strings.Contains(squash(field), needle) {
				out = append(out, t)
				break
			}
		}
		if f.Limit > 0 && len(out) == f.Limit {
			break
		}
	}
	return out, nil
}

func categoryName(c *core.Category) string {
	if c == nil {
		return "Uncategorised"
	}
	return c.Name
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (r *Transactions) query(q string, args ...any) ([]*core.Transaction, error) {
	rows, err := r.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Transaction
	for rows.Next() {
		t, err := scanTx(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Transactions) Get(id string) (*core.Transaction, error) {
	rows, err := r.query(`SELECT `+txCols+` FROM transactions t WHERE t.id = ? AND t.deleted_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	enr, err := r.enrich(rows)
	if err != nil {
		return nil, err
	}
	return enr[0], nil
}

func (r *Transactions) GetTx(id string) (*core.Transaction, error) {
	rows, err := r.query(`SELECT `+txCols+` FROM transactions t WHERE t.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return rows[0], nil
}

func (r *Transactions) enrich(txs []*core.Transaction) ([]*core.Transaction, error) {
	if len(txs) == 0 {
		return txs, nil
	}
	cats := map[string]*core.Category{}
	crows, err := r.DB.Query(`SELECT ` + categoryCols + ` FROM categories WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		c, err := scanCategory(crows)
		if err != nil {
			crows.Close()
			return nil, err
		}
		cats[c.ID] = c
	}
	crows.Close()
	accs := map[string]*core.Account{}
	arows, err := r.DB.Query(`SELECT ` + accountCols + ` FROM accounts WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		a, err := scanAccount(arows)
		if err != nil {
			arows.Close()
			return nil, err
		}
		accs[a.ID] = a
	}
	arows.Close()
	recLabels := map[string]string{}
	rrows, err := r.DB.Query(`SELECT key, label FROM recurring_keys`)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var k, l string
		if err := rrows.Scan(&k, &l); err != nil {
			rrows.Close()
			return nil, err
		}
		recLabels[k] = l
	}
	rrows.Close()
	for _, t := range txs {
		if t.CategoryID != nil {
			t.Category = cats[*t.CategoryID]
		}
		t.Account = accs[t.AccountID]
		if t.RecurringKey != nil {
			label := recLabels[*t.RecurringKey]
			if label == "" {
				label = *t.RecurringKey
			}
			t.Recurring = &label
		}
	}
	return txs, nil
}

func (r *Transactions) Create(tx *sql.Tx, t *core.Transaction) error {
	t.ID = util.NewID()
	t.CreatedAt = util.NowStr()
	t.UpdatedAt = t.CreatedAt
	if t.DedupeHash == "" {
		t.DedupeHash = DedupeHash(t.AccountID, t.Date, t.AmountMinor, t.Description)
	}
	_, err := exec(r.DB, tx, `INSERT INTO transactions
		(id, account_id, date, description, merchant, amount_minor, currency, type, category_id,
		 category_source, category_confidence, source_text, destination_text, recurring_key, transfer_id,
		 import_batch_id, dedupe_hash, created_at, updated_at, original_amount_minor, original_currency)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.AccountID, t.Date, t.Description, nullStr(t.Merchant), t.AmountMinor, t.Currency, t.Type,
		nullStr(t.CategoryID), t.CategorySource, nullFloat(t.CategoryConfidence), t.SourceText, t.DestinationText,
		nullStr(t.RecurringKey), nullStr(t.TransferID), nullStr(t.ImportBatchID), t.DedupeHash, t.CreatedAt, t.UpdatedAt,
		t.OriginalAmountMinor, nullIfEmptyStr(t.OriginalCurrency))
	return err
}

func (r *Transactions) Update(tx *sql.Tx, t *core.Transaction) error {
	t.UpdatedAt = util.NowStr()
	_, err := exec(r.DB, tx, `UPDATE transactions SET account_id=?, date=?, description=?, merchant=?, amount_minor=?,
		currency=?, type=?, category_id=?, category_source=?, category_confidence=?, source_text=?,
		destination_text=?, recurring_key=?, transfer_id=?, updated_at=? WHERE id=? AND deleted_at IS NULL`,
		t.AccountID, t.Date, t.Description, nullStr(t.Merchant), t.AmountMinor, t.Currency, t.Type,
		nullStr(t.CategoryID), t.CategorySource, nullFloat(t.CategoryConfidence), t.SourceText,
		t.DestinationText, nullStr(t.RecurringKey), nullStr(t.TransferID), t.UpdatedAt, t.ID)
	return err
}

func (r *Transactions) SoftDelete(tx *sql.Tx, id string) error {
	_, err := exec(r.DB, tx, `UPDATE transactions SET deleted_at = ? WHERE id = ?`, util.NowStr(), id)
	return err
}

func (r *Transactions) Restore(tx *sql.Tx, id string) error {
	_, err := exec(r.DB, tx, `UPDATE transactions SET deleted_at = NULL WHERE id = ?`, id)
	return err
}

// CreateTransfer links two transactions as one transfer. A transfer that
// comes with an id keeps it: history puts a transfer back under the id its
// entries know it by.
func (r *Transactions) CreateTransfer(tx *sql.Tx, tr *core.Transfer, sourceTx, destTx *core.Transaction) error {
	if tr.ID == "" {
		tr.ID = util.NewID()
	}
	tr.CreatedAt = util.NowStr()
	if _, err := exec(r.DB, tx, `INSERT INTO transfers (id, source_transaction_id, destination_transaction_id, detected, method, confidence, created_at)
		VALUES (?,?,?,?,?,?,?)`, tr.ID, tr.SourceTxID, tr.DestinationTxID, tr.Detected, tr.Method, nullFloat(tr.Confidence), tr.CreatedAt); err != nil {
		return err
	}
	for _, t := range []*core.Transaction{sourceTx, destTx} {
		if _, err := exec(r.DB, tx, `UPDATE transactions SET type='transfer', transfer_id=?, category_id=?, category_source='none', category_confidence=NULL, updated_at=? WHERE id=?`,
			tr.ID, core.InternalTransferCategoryID, util.NowStr(), t.ID); err != nil {
			return err
		}
	}
	return nil
}

func (r *Transactions) DeleteTransfer(tx *sql.Tx, transferID string) error {
	_, err := exec(r.DB, tx, `DELETE FROM transfers WHERE id = ?`, transferID)
	return err
}

func (r *Transactions) TransferByID(id string) (*core.Transfer, error) {
	return r.transferByID(id, true)
}

func (r *Transactions) transferByID(id string, liveOnly bool) (*core.Transfer, error) {
	q := `SELECT id, source_transaction_id, destination_transaction_id, detected, method, confidence, created_at, deleted_at
		FROM transfers WHERE id = ?`
	if liveOnly {
		q += ` AND deleted_at IS NULL`
	}
	var tr core.Transfer
	var conf sql.NullFloat64
	var deletedAt sql.NullString
	err := r.DB.QueryRow(q, id).Scan(&tr.ID, &tr.SourceTxID, &tr.DestinationTxID, &tr.Detected, &tr.Method, &conf, &tr.CreatedAt, &deletedAt)
	if err != nil {
		return nil, err
	}
	if conf.Valid {
		f := conf.Float64
		tr.Confidence = &f
	}
	return &tr, nil
}

func (r *Transactions) UnlinkedCandidates(accountID string, from, to string) ([]*core.Transaction, error) {
	q := `SELECT ` + txCols + ` FROM transactions t
		WHERE t.deleted_at IS NULL AND t.transfer_id IS NULL
		AND t.account_id != ? AND t.date BETWEEN ? AND ?
		ORDER BY t.date DESC LIMIT 2000`
	rows, err := r.query(q, accountID, from, to)
	if err != nil {
		return nil, err
	}
	return r.enrich(rows)
}

// UnlinkedTxs includes transfers with no other leg yet (marked Internal
// transfer before the other account was imported), so they can be paired.
func (r *Transactions) UnlinkedTxs() ([]*core.Transaction, error) {
	rows, err := r.query(`SELECT ` + txCols + ` FROM transactions t
		WHERE t.deleted_at IS NULL AND t.transfer_id IS NULL
		ORDER BY t.date DESC LIMIT 5000`)
	if err != nil {
		return nil, err
	}
	return r.enrich(rows)
}

func (r *Transactions) CountByAccount(from, to string) (map[string]int, error) {
	q := `SELECT account_id, COUNT(*) FROM transactions WHERE deleted_at IS NULL`
	var args []any
	if from != "" {
		q += ` AND date >= ?`
		args = append(args, from)
	}
	if to != "" {
		q += ` AND date <= ?`
		args = append(args, to)
	}
	q += ` GROUP BY account_id`
	rows, err := r.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (r *Transactions) UpsertRecurringKey(tx *sql.Tx, k *core.RecurringKey) error {
	_, err := exec(r.DB, tx, `INSERT INTO recurring_keys (key, label, cadence, interval_days, amount_minor, direction, first_seen, last_seen, next_expected, confidence, active, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET label=excluded.label, cadence=excluded.cadence, interval_days=excluded.interval_days,
			amount_minor=excluded.amount_minor, direction=excluded.direction, last_seen=excluded.last_seen,
			next_expected=excluded.next_expected, confidence=excluded.confidence, active=excluded.active, updated_at=excluded.updated_at`,
		k.Key, k.Label, k.Cadence, nullInt(k.IntervalDays), nullInt64(k.AmountMinor), k.Direction,
		k.FirstSeen, k.LastSeen, nullStr(k.NextExpected), k.Confidence, boolInt(k.Active), util.NowStr())
	return err
}

func scanRecurringKey(sc Scanner) (*core.RecurringKey, error) {
	var k core.RecurringKey
	var interval, amount sql.NullInt64
	var next sql.NullString
	var active int
	err := sc.Scan(&k.Key, &k.Label, &k.Cadence, &interval, &amount, &k.Direction, &k.FirstSeen, &k.LastSeen, &next, &k.Confidence, &active, &k.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if interval.Valid {
		i := int(interval.Int64)
		k.IntervalDays = &i
	}
	if amount.Valid {
		a := amount.Int64
		k.AmountMinor = &a
	}
	if next.Valid {
		s := next.String
		k.NextExpected = &s
	}
	k.Active = active == 1
	return &k, nil
}

func (r *Transactions) RecurringKey(key string) (*core.RecurringKey, error) {
	return scanRecurringKey(r.DB.QueryRow(`SELECT key, label, cadence, interval_days, amount_minor, direction, first_seen, last_seen, next_expected, confidence, active, updated_at
		FROM recurring_keys WHERE key = ?`, key))
}

func (r *Transactions) RecurringSeries(key string) ([]*core.Transaction, error) {
	rows, err := r.query(`SELECT `+txCols+` FROM transactions t WHERE t.deleted_at IS NULL AND t.recurring_key = ? ORDER BY t.date ASC`, key)
	if err != nil {
		return nil, err
	}
	return r.enrich(rows)
}

type DisplayRow struct {
	Kind        string              `json:"kind"`
	Tx          *core.Transaction   `json:"-"`
	Members     []*core.Transaction `json:"-"`
	Rep         *core.Transaction   `json:"-"`
	Source      string              `json:"source_account,omitempty"`
	Destination string              `json:"destination_account,omitempty"`
}

func (r *Transactions) DisplayRows(f ListFilter, accountID string) ([]*DisplayRow, error) {
	var rows []*core.Transaction
	var err error
	if f.Q != "" {
		rows, err = r.SearchMatch(f)
	} else {
		rows, err = r.List(f)
	}
	if err != nil {
		return nil, err
	}
	r.resolveNames(rows)

	var out []*DisplayRow
	if accountID != "" {
		for _, t := range rows {
			if t.AccountID != accountID {
				continue
			}
			out = append(out, &DisplayRow{Kind: "single", Tx: t, Rep: t, Members: []*core.Transaction{t}})
		}
		return out, nil
	}
	grouped := map[string][]*core.Transaction{}
	for _, t := range rows {
		if t.TransferID != nil && t.Type == core.TypeTransfer {
			grouped[*t.TransferID] = append(grouped[*t.TransferID], t)
			continue
		}
		out = append(out, &DisplayRow{Kind: "single", Tx: t, Rep: t, Members: []*core.Transaction{t}})
	}
	for _, members := range grouped {
		rep := members[0]
		for _, m := range members {
			if m.AmountMinor < 0 {
				rep = m
				break
			}
		}
		out = append(out, &DisplayRow{Kind: "linked", Rep: rep, Tx: rep, Members: members,
			Source: rep.SourceName, Destination: rep.DestinationName})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Rep, out[j].Rep
		if a.Date != b.Date {
			return a.Date > b.Date
		}
		return a.CreatedAt > b.CreatedAt
	})
	return out, nil
}

func (r *Transactions) resolveNames(txs []*core.Transaction) {
	peers := map[string]*core.Transaction{}
	for _, t := range txs {
		if t.TransferID != nil {
			if tr, err := r.transferByID(*t.TransferID, false); err == nil {
				for _, legID := range []string{tr.SourceTxID, tr.DestinationTxID} {
					if _, ok := peers[legID]; !ok {
						if rows, err := r.query(`SELECT `+txCols+` FROM transactions t WHERE t.id = ?`, legID); err == nil && len(rows) > 0 {
							peers[legID] = rows[0]
						}
					}
				}
			}
		}
	}
	accNames := map[string]string{}
	for _, t := range txs {
		if _, ok := accNames[t.AccountID]; !ok {
			accNames[t.AccountID] = r.accountName(t.AccountID)
		}
	}
	for _, t := range txs {
		accName := accNames[t.AccountID]
		switch t.Type {
		case core.TypeTransfer:
			if t.TransferID != nil {
				if tr, err := r.transferByID(*t.TransferID, false); err == nil {
					peerID := tr.SourceTxID
					if t.ID == tr.SourceTxID {
						peerID = tr.DestinationTxID
					}
					if p, ok := peers[peerID]; ok && p != nil {
						if _, ok := accNames[p.AccountID]; !ok {
							accNames[p.AccountID] = r.accountName(p.AccountID)
						}
						peerName := accNames[p.AccountID]
						if t.AmountMinor < 0 {
							t.SourceName, t.DestinationName = accName, peerName
							t.Counterparty = "To " + peerName
						} else {
							t.SourceName, t.DestinationName = peerName, accName
							t.Counterparty = "From " + peerName
						}
					}
				}
			}
			if t.Counterparty == "" {
				if t.AmountMinor < 0 {
					t.Counterparty = "To " + firstNonEmpty(t.DestinationText, accName)
				} else {
					t.Counterparty = "From " + firstNonEmpty(t.SourceText, accName)
				}
			}
			if t.SourceName == "" {
				t.SourceName = accName
			}
			if t.DestinationName == "" {
				t.DestinationName = firstNonEmpty(t.DestinationText, accName)
			}
		case core.TypeIncome:
			t.SourceName = firstNonEmpty(t.SourceText, deref(t.Merchant), t.Description)
			t.DestinationName = accName
			t.Counterparty = "From " + t.SourceName
		default:
			t.SourceName = accName
			t.DestinationName = firstNonEmpty(t.DestinationText, deref(t.Merchant), t.Description)
			t.Counterparty = "To " + t.DestinationName
		}
	}
}

func (r *Transactions) accountName(id string) string {
	var name string
	if err := r.DB.QueryRow(`SELECT name FROM accounts WHERE id = ?`, id).Scan(&name); err != nil {
		return ""
	}
	return name
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func nullStr(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func nullFloat(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func nullInt(i *int) any {
	if i == nil {
		return nil
	}
	return *i
}

func nullInt64(i *int64) any {
	if i == nil {
		return nil
	}
	return *i
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SortDisplayRows orders rows by a table column ("date", "description",
// "category", "account", "amount"); dir is "asc" or "desc". Ties fall back to
// newest first.
func SortDisplayRows(rows []*DisplayRow, key, dir string) {
	text := func(r *DisplayRow) string {
		t := r.Rep
		switch key {
		case "description":
			return strings.ToLower(t.Description)
		case "category":
			if t.Category != nil {
				return strings.ToLower(t.Category.Name)
			}
			return "￿" // uncategorised last when ascending
		case "account":
			if r.Kind == "linked" {
				return strings.ToLower(r.Source + " " + r.Destination)
			}
			return strings.ToLower(t.AccountName())
		}
		return ""
	}
	cmp := func(a, b *DisplayRow) int {
		switch key {
		case "amount":
			switch {
			case a.Rep.AmountMinor < b.Rep.AmountMinor:
				return -1
			case a.Rep.AmountMinor > b.Rep.AmountMinor:
				return 1
			}
			return 0
		case "description", "category", "account":
			return strings.Compare(text(a), text(b))
		}
		return strings.Compare(a.Rep.Date, b.Rep.Date)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		c := cmp(rows[i], rows[j])
		if c == 0 {
			a, b := rows[i].Rep, rows[j].Rep
			if a.Date != b.Date {
				return a.Date > b.Date
			}
			return a.CreatedAt > b.CreatedAt
		}
		if dir == "asc" {
			return c < 0
		}
		return c > 0
	})
}

func nullIfEmptyStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
