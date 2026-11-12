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
		if ent.Action == "transaction.created" {
			if reapply {
				return x.Txs.Restore(tx, id)
			}
			return x.Txs.SoftDelete(tx, id)
		}
		if reapply {
			return x.Txs.SoftDelete(tx, id)
		}
		return x.Txs.Restore(tx, id)

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
			return x.Accounts.Restore(tx, id)
		}
		return x.Accounts.SoftDelete(tx, id)

	case "account.updated":
		id := str(revert["account_id"])
		fields := revertFields(revert)
		if fields == nil {
			fields = before
		}
		if reapply {
			fields = after
		}
		return x.applyAccountFields(tx, id, fields)

	case "account.deleted":
		return fmt.Errorf("auditexec: account deletion can't be undone from history — restore it from Settings → Deleted accounts")

	case "account.restored", "account.purged":
		return fmt.Errorf("auditexec: %s can't be replayed from history — manage it from Settings → Accounts", ent.Action)

	case "valuation.created":
		vid := str(revert["valuation_id"])
		if reapply {
			return x.Accounts.RestoreValuation(tx, vid)
		}
		return x.Accounts.DeleteValuation(tx, vid)

	case "valuation.deleted":
		vid := str(revert["valuation_id"])
		if reapply {
			return x.Accounts.DeleteValuation(tx, vid)
		}
		return x.Accounts.RestoreValuation(tx, vid)

	case "category.created":
		id := str(revert["category_id"])
		if reapply {
			return x.Categories.Restore(tx, id)
		}
		return x.Categories.Delete(tx, id)

	case "category.updated":
		id := str(revert["category_id"])
		fields := revertFields(revert)
		if fields == nil {
			fields = before
		}
		if reapply {
			fields = after
		}
		return x.applyCategoryFields(tx, id, fields)

	case "category.deleted":
		// Deletion is soft, so undo brings the same row back.
		id := str(revert["category_id"])
		if reapply {
			return x.Categories.Delete(tx, id)
		}
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM categories WHERE id = ?`, id).Scan(&exists); err == nil && exists == 0 {
			return x.restoreCategory(tx, id, before)
		}
		return x.Categories.Restore(tx, id)

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
		_, err := tx.Exec(`UPDATE group_rules SET deleted_at = ? WHERE id = ?`, deletedAt, str(revert["rule_id"]))
		return err

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

func (x *Executor) applyAccountFields(tx *sql.Tx, id string, f map[string]any) error {
	if f == nil {
		return nil
	}
	a, err := scanAccountIn(x.Accounts, tx, id)
	if err != nil {
		return err
	}
	if v, ok := f["name"].(string); ok && v != "" {
		a.Name = v
	}
	if v, ok := f["type"].(string); ok && v != "" {
		a.Type = v
	}
	if v, ok := f["currency"].(string); ok && v != "" {
		a.Currency = v
	}
	if v, ok := f["opening_balance_minor"]; ok {
		a.OpeningBalanceMinor = int64F(v)
	}
	if v, ok := f["opening_balance_date"].(string); ok {
		a.OpeningBalanceDate = v
	}
	if v, ok := f["notes"].(string); ok {
		a.Notes = v
	}
	return x.Accounts.Update(tx, a)
}

func (x *Executor) applyCategoryFields(tx *sql.Tx, id string, f map[string]any) error {
	if f == nil {
		return nil
	}
	c, err := scanCategoryIn(x.Categories, tx, id)
	if err != nil {
		return err
	}
	if v, ok := f["name"].(string); ok && v != "" && !c.IsSystem {
		c.Name = v
	}
	if v, ok := f["color"].(string); ok && v != "" {
		c.Color = v
	}
	if v, ok := f["type"].(string); ok && v != "" && !c.IsSystem {
		c.Type = v
	}
	// May be "" on purpose: the description was blank before.
	if v, ok := f["description"].(string); ok && !c.IsSystem {
		c.Description = v
	}
	return x.Categories.Update(tx, c)
}

func (x *Executor) restoreCategory(tx *sql.Tx, id string, before map[string]any) error {
	c := &core.Category{
		ID: id,
	}
	if before != nil {
		c.Name, _ = before["name"].(string)
		c.Color, _ = before["color"].(string)
		c.Type, _ = before["type"].(string)
		c.Description, _ = before["description"].(string)
	}
	if c.Name == "" {
		return fmt.Errorf("auditexec: cannot restore category %s (no snapshot)", id)
	}
	if c.Color == "" {
		c.Color = "#6b7280"
	}
	if c.Type == "" {
		c.Type = core.CategorySpend
	}
	return x.Categories.CreateWithID(tx, c)
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
		_, err := tx.Exec(`UPDATE transactions SET deleted_at = NULL WHERE import_batch_id = ? AND deleted_at = ?`, batchID, stamp.String)
		return err
	}
	rows, err := tx.Query(`SELECT id FROM transactions WHERE import_batch_id = ? AND deleted_at IS NULL`, batchID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	stamp := util.NowStr()
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE transactions SET deleted_at = ? WHERE id = ?`, stamp, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM transfers WHERE source_transaction_id = ? OR destination_transaction_id = ?`, id, id); err != nil {
			return err
		}
	}
	return nil
}

// applyImportMove moves an import's transactions from one account to the
// other, leaving any moved again since where they are.
func (x *Executor) applyImportMove(tx *sql.Tx, revert map[string]any, from, to string) error {
	batchID := str(revert["batch_id"])
	if batchID == "" || from == "" || to == "" {
		return fmt.Errorf("auditexec: batch or account missing in payload")
	}
	ids, _ := revert["transaction_ids"].([]any)
	kept := 0
	for _, v := range ids {
		var date, desc string
		var amount int64
		err := tx.QueryRow(`SELECT date, description, amount_minor FROM transactions
			WHERE id = ? AND account_id = ? AND deleted_at IS NULL`, str(v), from).Scan(&date, &desc, &amount)
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
	if _, err := tx.Exec(`UPDATE import_batches SET account_id = ? WHERE id = ?`, to, batchID); err != nil {
		return err
	}
	if kept > 0 {
		return &audit.Partial{Notes: []string{fmt.Sprintf("%d transactions were changed or removed since, so they were left where they are", kept)}}
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
	var a core.Account
	var openingDate, deletedAt sql.NullString
	err := repos.Row(r.DB, tx, `SELECT id, name, type, color, currency, opening_balance_minor, COALESCE(opening_balance_date,''),
		account_number, sort_code, iban, bic, notes, created_at, updated_at, deleted_at
		FROM accounts WHERE id = ? AND deleted_at IS NULL`, id).
		Scan(&a.ID, &a.Name, &a.Type, &a.Color, &a.Currency, &a.OpeningBalanceMinor, &openingDate,
			&a.AccountNumber, &a.SortCode, &a.IBAN, &a.BIC, &a.Notes, &a.CreatedAt, &a.UpdatedAt, &deletedAt)
	if err != nil {
		return nil, err
	}
	a.OpeningBalanceDate = openingDate.String
	return &a, nil
}

func scanCategoryIn(r *repos.Categories, tx *sql.Tx, id string) (*core.Category, error) {
	var c core.Category
	var isSys, deletedAt sql.NullString
	err := repos.Row(r.DB, tx, `SELECT id, name, color, type, is_system, sort_order, created_at, updated_at, deleted_at
		FROM categories WHERE id = ? AND deleted_at IS NULL`, id).
		Scan(&c.ID, &c.Name, &c.Color, &c.Type, &isSys, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt, &deletedAt)
	if err != nil {
		return nil, err
	}
	c.IsSystem = isSys.Valid && isSys.String == "1"
	return &c, nil
}
