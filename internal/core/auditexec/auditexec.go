package auditexec

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
	"glowworm/internal/util"
)

type Executor struct {
	DB         *store.DB
	Accounts   *repos.Accounts
	Categories *repos.Categories
	Txs        *repos.Transactions
}

func (x *Executor) Apply(tx *sql.Tx, ent *audit.AuditRow, reapply bool) error {
	var before, after, revert map[string]any
	_ = json.Unmarshal([]byte(ent.Before), &before)
	_ = json.Unmarshal([]byte(ent.After), &after)
	_ = json.Unmarshal([]byte(ent.Revert), &revert)

	switch ent.Action {
	case "transaction.created", "transaction.deleted":
		id := str(revert["transaction_id"])
		if id == "" {
			id = str(before["id"])
		}
		if id == "" {
			return fmt.Errorf("auditexec: no transaction id in payload")
		}
		if (ent.Action == "transaction.created") == reapply {
			return x.showTx(tx, revert, id)
		}
		return x.hideTx(tx, ent, revert, id)

	case "transaction.edited":
		id := str(revert["transaction_id"])
		if id == "" {
			return fmt.Errorf("auditexec: no transaction id in payload")
		}
		if reapply {
			return x.applyTxEdit(tx, id, before, after)
		}
		return x.applyTxEdit(tx, id, after, before)

	case "account.created":
		id := str(revert["account_id"])
		if reapply {
			if err := expect(tx, "accounts", "account", id, false); err != nil {
				return err
			}
			return x.Accounts.Restore(tx, id)
		}
		return x.removeAccount(tx, id)

	case "account.updated":
		if len(before) == 0 {
			before = revertFields(revert)
		}
		if reapply {
			return x.applyAccountUpdate(tx, str(revert["account_id"]), before, after)
		}
		return x.applyAccountUpdate(tx, str(revert["account_id"]), after, before)

	case "account.deleted":
		return fmt.Errorf("auditexec: account deletion can't be undone from history — restore it from Settings → Deleted accounts")

	case "account.restored", "account.purged":
		return fmt.Errorf("auditexec: %s can't be replayed from history — manage it from Settings → Accounts", ent.Action)

	case "valuation.created", "valuation.deleted":
		return x.setValuation(tx, str(revert["valuation_id"]), (ent.Action == "valuation.created") == reapply)

	case "category.created":
		id := str(revert["category_id"])
		if reapply {
			return x.bringBackCategory(tx, id, after)
		}
		return x.removeCategory(tx, id)

	case "category.updated":
		if len(before) == 0 {
			before = revertFields(revert)
		}
		if reapply {
			return x.applyCategoryUpdate(tx, str(revert["category_id"]), before, after)
		}
		return x.applyCategoryUpdate(tx, str(revert["category_id"]), after, before)

	case "category.deleted":
		// Deletion is soft, so undo brings the same row back.
		id := str(revert["category_id"])
		if reapply {
			return x.removeCategory(tx, id)
		}
		return x.bringBackCategory(tx, id, before)

	case "classifier.retrained", "recurring.detected", "merchant.enriched":
		// Derived from the data; recomputed after any undo or redo.
		return nil

	case "group_rule.created", "group_rule.deleted":
		// Rules are soft-deleted, so either way it's the same row on or off.
		// Transactions are regrouped after every undo and redo.
		on := (ent.Action == "group_rule.created") == reapply
		var deletedAt any
		if !on {
			deletedAt = nowStr()
		}
		res, err := tx.Exec(`UPDATE group_rules SET deleted_at = ? WHERE id = ?`, deletedAt, str(revert["rule_id"]))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return conflict("that rule no longer exists")
		}
		return nil

	case "change.applied":
		// The edits it made are entries caused by it, undone and redone
		// along with it; the entry itself only groups them.
		return nil

	case "transfer.linked", "transfer.unlink":
		return x.applyTransfer(tx, ent, before, after, revert, reapply)

	case "import.confirmed":
		return x.applyImportConfirmed(tx, ent, revert, reapply)

	case "import.edited":
		from, to := str(before["account_id"]), str(after["account_id"])
		if !reapply {
			from, to = to, from
		}
		return x.applyImportMove(tx, revert, from, to)

	case "import.schema_detected", "import.cancelled",
		"setup.account_created", "setup.categories_seeded", "setup.widgets_seeded":
		// Records of something that happened, with nothing to replay.
		return nil

	default:
		// Schemas, widgets and settings among them: their entries are records only.
		return fmt.Errorf("auditexec: no handler for action %q", ent.Action)
	}
}

func revertFields(revert map[string]any) map[string]any {
	if revert == nil {
		return nil
	}
	f, _ := revert["fields"].(map[string]any)
	return f
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func int64F(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

// txEditFields are the parts of a transaction an edit can change. Each is
// undone or redone on its own, and only if it still holds the value the
// entry left: one changed again since keeps that later value. category_source
// follows category_id and isn't checked.
var txEditFields = []struct {
	label string
	keys  []string
}{
	{"account", []string{"account_id"}},
	{"date", []string{"date"}},
	{"description", []string{"description"}},
	{"merchant", []string{"merchant"}},
	{"amount", []string{"amount_minor"}},
	{"category", []string{"category_id", "type", "category_source"}},
}

// applyTxEdit moves a transaction from one snapshot of an edit (from) to the
// other (to), touching only the fields the edit changed.
func (x *Executor) applyTxEdit(tx *sql.Tx, id string, from, to map[string]any) error {
	t, err := getTxIn(x.Txs, tx, id)
	if err == sql.ErrNoRows {
		return conflict("that transaction no longer exists")
	}
	if err != nil {
		return err
	}
	cur := map[string]any{
		"account_id": t.AccountID, "date": t.Date, "description": t.Description,
		"merchant": t.Merchant, "amount_minor": t.AmountMinor, "type": t.Type,
		"category_id": t.CategoryID, "category_source": t.CategorySource,
	}
	var kept []string
	for _, f := range txEditFields {
		changed, conflict := false, false
		for _, k := range f.keys {
			if snapVal(from[k]) == snapVal(to[k]) {
				continue
			}
			changed = true
			if k != "category_source" && snapVal(cur[k]) != snapVal(from[k]) {
				conflict = true
			}
		}
		if !changed {
			continue
		}
		if !conflict && f.label == "account" {
			ok, err := x.canMove(tx, t, snapVal(to["account_id"]))
			if err != nil {
				return err
			}
			conflict = !ok
		}
		if conflict {
			kept = append(kept, f.label)
			continue
		}
		for _, k := range f.keys {
			if snapVal(from[k]) == snapVal(to[k]) {
				continue
			}
			v := snapVal(to[k])
			switch k {
			case "account_id":
				t.AccountID = v
			case "date":
				t.Date = v
			case "description":
				t.Description = v
			case "merchant":
				t.Merchant = optStr(v)
			case "amount_minor":
				t.AmountMinor = int64F(to[k])
			case "type":
				t.Type = v
			case "category_id":
				t.CategoryID = optStr(v)
			case "category_source":
				if v != "" {
					t.CategorySource = v
				}
			}
		}
	}
	if _, err := tx.Exec(`UPDATE transactions SET account_id=?, date=?, description=?, merchant=?, amount_minor=?, type=?,
		category_id=?, category_source=?, updated_at=? WHERE id=?`,
		t.AccountID, t.Date, t.Description, t.Merchant, t.AmountMinor, t.Type,
		t.CategoryID, t.CategorySource, util.NowStr(), t.ID); err != nil {
		return err
	}
	if len(kept) > 0 {
		return &audit.Partial{Notes: []string{fmt.Sprintf("%s: kept the %s from a later change", t.Description, joinAnd(kept))}}
	}
	return nil
}

// canMove reports whether a transaction can go to another account, by the
// rules an edit follows: not while it is one side of a transfer, and only to
// an account that is still there and in its currency.
func (x *Executor) canMove(tx *sql.Tx, t *core.Transaction, account string) (bool, error) {
	if t.TransferID != nil {
		return false, nil
	}
	var currency string
	err := tx.QueryRow(`SELECT currency FROM accounts WHERE id = ? AND deleted_at IS NULL`, account).Scan(&currency)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return currency == t.Currency, err
}

// snapVal is a snapshot value as text, so values read back from JSON
// (float64, nil) compare equal to the live ones (int64, *string, "").
func snapVal(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case string:
		return n
	case *string:
		if n == nil {
			return ""
		}
		return *n
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprint(v)
}

func optStr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func (x *Executor) applyImportConfirmed(tx *sql.Tx, ent *audit.AuditRow, revert map[string]any, reapply bool) error {
	batchID := str(revert["batch_id"])
	if batchID == "" {
		return fmt.Errorf("auditexec: batch id missing in payload")
	}
	if reapply {
		// Bring back the rows the undo removed (it stamped them all at once).
		var stamp sql.NullString
		if err := tx.QueryRow(`SELECT MAX(deleted_at) FROM transactions WHERE import_batch_id = ?`, batchID).Scan(&stamp); err != nil {
			return err
		}
		if !stamp.Valid {
			return nil
		}
		if _, err := tx.Exec(`UPDATE transactions SET deleted_at = NULL WHERE import_batch_id = ? AND deleted_at = ?`, batchID, stamp.String); err != nil {
			return err
		}
		return x.relink(tx, revert)
	}
	// Rows that are one side of a transfer take the link with them; the
	// entry keeps those transfers for the redo.
	rows, err := tx.Query(`SELECT id FROM transactions WHERE import_batch_id = ? AND deleted_at IS NULL AND (transfer_id IS NOT NULL
		OR id IN (SELECT source_transaction_id FROM transfers UNION SELECT destination_transaction_id FROM transfers))`, batchID)
	if err != nil {
		return err
	}
	var linked []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		linked = append(linked, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var broken []*core.Transfer
	for _, id := range linked {
		tr, err := x.Txs.BreakTransferOf(tx, id)
		if err != nil {
			return err
		}
		if tr != nil {
			broken = append(broken, tr)
		}
	}
	if _, err := tx.Exec(`UPDATE transactions SET deleted_at = ? WHERE import_batch_id = ? AND deleted_at IS NULL`, util.NowStr(), batchID); err != nil {
		return err
	}
	return x.remember(tx, ent, revert, broken)
}

// applyImportMove moves an import's transactions from one account to the
// other, leaving any moved again since where they are.
func (x *Executor) applyImportMove(tx *sql.Tx, revert map[string]any, from, to string) error {
	batchID := str(revert["batch_id"])
	if batchID == "" || from == "" || to == "" {
		return fmt.Errorf("auditexec: batch or account missing in payload")
	}
	var live int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id = ? AND deleted_at IS NULL`, to).Scan(&live); err != nil {
		return err
	}
	if live == 0 {
		return conflict("the account they would go to has been deleted")
	}
	// Like the move itself, this leaves alone a transaction that is one side
	// of a transfer, as well as any that has left the account or been deleted.
	ids, _ := revert["transaction_ids"].([]any)
	kept := 0
	for _, v := range ids {
		var date, desc string
		var amount int64
		err := tx.QueryRow(`SELECT date, description, amount_minor FROM transactions
			WHERE id = ? AND account_id = ? AND deleted_at IS NULL AND transfer_id IS NULL`, str(v), from).Scan(&date, &desc, &amount)
		if err == sql.ErrNoRows {
			kept++
			continue
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE transactions SET account_id = ?, dedupe_hash = ?, updated_at = ? WHERE id = ?`,
			to, repos.DedupeHash(to, date, amount, desc), util.NowStr(), str(v)); err != nil {
			return err
		}
	}
	if kept == len(ids) && kept > 0 {
		return conflict("none of its transactions are where it left them")
	}
	if _, err := tx.Exec(`UPDATE import_batches SET account_id = ? WHERE id = ?`, to, batchID); err != nil {
		return err
	}
	if kept == 1 {
		return &audit.Partial{Notes: []string{"1 transaction was changed, linked or removed since, so it was left where it is"}}
	}
	if kept > 1 {
		return &audit.Partial{Notes: []string{fmt.Sprintf("%d transactions were changed, linked or removed since, so they were left where they are", kept)}}
	}
	return nil
}

func nowStr() string { return util.NowStr() }

func getTxIn(r *repos.Transactions, tx *sql.Tx, id string) (*core.Transaction, error) {
	var t core.Transaction
	var merchant, catID, recKey, transferID, batchID, deletedAt sql.NullString
	var conf sql.NullFloat64
	err := repos.Row(r.DB, tx, `SELECT t.id, t.account_id, t.date, t.description, t.merchant, t.amount_minor, t.currency, t.type,
		t.category_id, t.category_source, t.category_confidence, t.source_text, t.destination_text,
		t.recurring_key, t.transfer_id, t.import_batch_id, t.created_at, t.updated_at, t.deleted_at
		FROM transactions t WHERE t.id = ?`, id).
		Scan(&t.ID, &t.AccountID, &t.Date, &t.Description, &merchant, &t.AmountMinor, &t.Currency, &t.Type,
			&catID, &t.CategorySource, &conf, &t.SourceText, &t.DestinationText,
			&recKey, &transferID, &batchID, &t.CreatedAt, &t.UpdatedAt, &deletedAt)
	if err != nil {
		return nil, err
	}
	if merchant.Valid {
		s := merchant.String
		t.Merchant = &s
	}
	if catID.Valid {
		s := catID.String
		t.CategoryID = &s
	}
	if recKey.Valid {
		s := recKey.String
		t.RecurringKey = &s
	}
	if transferID.Valid {
		s := transferID.String
		t.TransferID = &s
	}
	return &t, nil
}

func scanAccountIn(r *repos.Accounts, tx *sql.Tx, id string) (*core.Account, error) {
	return repos.ScanAccount(repos.Row(r.DB, tx, `SELECT `+repos.AccountCols+`
		FROM accounts WHERE id = ? AND deleted_at IS NULL`, id))
}

func scanCategoryIn(r *repos.Categories, tx *sql.Tx, id string) (*core.Category, error) {
	var c core.Category
	var isSys, deletedAt sql.NullString
	err := repos.Row(r.DB, tx, `SELECT id, name, color, type, description, is_system, sort_order, created_at, updated_at, deleted_at
		FROM categories WHERE id = ? AND deleted_at IS NULL`, id).
		Scan(&c.ID, &c.Name, &c.Color, &c.Type, &c.Description, &isSys, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt, &deletedAt)
	if err != nil {
		return nil, err
	}
	c.IsSystem = isSys.Valid && isSys.String == "1"
	return &c, nil
}
