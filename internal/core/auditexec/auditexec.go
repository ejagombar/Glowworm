package auditexec

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
	"glowworm/internal/util"
)

type Executor struct {
	DB          *store.DB
	Accounts    *repos.Accounts
	Categories  *repos.Categories
	Txs         *repos.Transactions
	InternalCat func(tx *sql.Tx) (string, error)
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

	case "transaction.edited", "categorisation.applied":
		id := str(revert["transaction_id"])
		if id == "" {
			return fmt.Errorf("auditexec: no transaction id in payload")
		}
		var target map[string]any
		if reapply {
			target = after
		} else {
			target = revertFields(revert)
			if target == nil {
				target = before
			}
		}
		if cat, ok := target["categories"]; ok {
			return x.applyCategories(tx, id, cat, reapply)
		}
		return x.applyTxFields(tx, id, target)

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
		if vid == "" || vid == "@valuation_id" {
			var vID string
			err := tx.QueryRow(`SELECT id FROM valuations WHERE account_id = ? AND date = ? AND value_minor = ?
				ORDER BY created_at DESC LIMIT 1`,
				str(after["account_id"]), str(after["date"]), int64F(after["value_minor"])).Scan(&vID)
			if err != nil {
				return fmt.Errorf("auditexec: valuation not found: %w", err)
			}
			vid = vID
		}
		if reapply {
			return x.Accounts.RestoreValuation(tx, vid)
		}
		return x.Accounts.DeleteValuation(tx, vid)

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

	case "transfer.linked", "transfer.unlink":
		return x.applyTransfer(tx, ent, before, after, revert, reapply)

	case "import.confirmed":
		return x.applyImportConfirmed(tx, ent, revert, reapply)

	case "import.schema_detected", "import.cancelled", "import.reverted",
		"audit.reverted", "audit.jumpback", "audit.path.activated", "audit.entry.deleted",
		"schema.saved", "schema.updated", "schema.deleted",
		"widget.pinned", "widget.updated", "widget.unpinned", "widget.reordered",
		"settings.ai.updated", "classifier.reverted",
		"setup.completed", "setup.account_created", "setup.categories_seeded", "setup.widgets_seeded",
		"account.valuation.correction":
		if ent.Action == "import.confirmed" || ent.Action == "schema.saved" ||
			ent.Action == "schema.updated" || ent.Action == "schema.deleted" ||
			ent.Action == "widget.pinned" || ent.Action == "widget.updated" ||
			ent.Action == "widget.unpinned" || ent.Action == "widget.reordered" ||
			ent.Action == "settings.ai.updated" ||
			ent.Action == "setup.completed" {
			return fmt.Errorf("auditexec: revert for %s not wired in this build", ent.Action)
		}
		return nil

	default:
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

func (x *Executor) applyTxFields(tx *sql.Tx, id string, f map[string]any) error {
	if f == nil {
		return nil
	}
	t, err := getTxIn(x.Txs, tx, id)
	if err != nil {
		return err
	}
	if v, ok := f["date"].(string); ok && v != "" {
		t.Date = v
	}
	if v, ok := f["description"].(string); ok && v != "" {
		t.Description = v
	}
	if v, ok := f["amount_minor"]; ok {
		t.AmountMinor = int64F(v)
	}
	if v, ok := f["type"].(string); ok && v != "" {
		t.Type = v
	}
	if v, ok := f["category_id"]; ok {
		switch cv := v.(type) {
		case string:
			if cv == "" {
				t.CategoryID = nil
			} else {
				t.CategoryID = &cv
			}
		case nil:
			t.CategoryID = nil
		}
	}
	if v, ok := f["category_source"].(string); ok && v != "" {
		t.CategorySource = v
	}
	if v, ok := f["merchant"]; ok {
		switch mv := v.(type) {
		case string:
			if mv != "" {
				t.Merchant = &mv
			} else {
				t.Merchant = nil
			}
		case nil:
			t.Merchant = nil
		}
	}
	_, err = tx.Exec(`UPDATE transactions SET date=?, description=?, merchant=?, amount_minor=?, type=?,
		category_id=?, category_source=?, updated_at=? WHERE id=?`,
		t.Date, t.Description, t.Merchant, t.AmountMinor, t.Type,
		t.CategoryID, t.CategorySource, util.NowStr(), t.ID)
	return err
}

func (x *Executor) applyCategories(tx *sql.Tx, id string, cat any, reapply bool) error {
	items, ok := cat.([]any)
	if !ok {
		return fmt.Errorf("auditexec: bad categories payload")
	}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		txID := str(m["transaction_id"])
		which := m["before"]
		if reapply {
			which = m["after"]
		}
		wm, _ := which.(map[string]any)
		if wm == nil || txID == "" {
			continue
		}
		var catID any
		switch c := wm["category_id"].(type) {
		case string:
			if c != "" {
				catID = c
			} else {
				catID = nil
			}
		default:
			catID = nil
		}
		src, _ := wm["source"].(string)
		if src == "" {
			src = "none"
		}
		if _, err := tx.Exec(`UPDATE transactions SET category_id=?, category_source=?, category_confidence=NULL, updated_at=? WHERE id=?`,
			catID, src, nowStr(), txID); err != nil {
			return err
		}
	}
	return nil
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

func (x *Executor) applyTransfer(tx *sql.Tx, ent *audit.AuditRow, before, after, revert map[string]any, reapply bool) error {
	transferID := str(revert["transfer_id"])
	if transferID == "" {
		transferID = str(after["transfer_id"])
	}
	internalCat, err := x.InternalCat(tx)
	if err != nil {
		return err
	}
	legsOf := func(m map[string]any) (string, string) {
		legs, _ := m["legs"].([]any)
		if len(legs) != 2 {
			return "", ""
		}
		l0, _ := legs[0].(map[string]any)
		l1, _ := legs[1].(map[string]any)
		return str(l0["id"]), str(l1["id"])
	}
	if ent.Action == "transfer.linked" {
		if reapply {
			a, b := legsOf(after)
			if a == "" || b == "" {
				return fmt.Errorf("auditexec: transfer legs missing in payload")
			}
			src, err := x.Txs.GetTx(a)
			if err != nil {
				return err
			}
			dst, err := x.Txs.GetTx(b)
			if err != nil {
				return err
			}
			if src.AmountMinor < 0 {
				_ = src
			} else {
				src, dst = dst, src
			}
			tr := &core.Transfer{SourceTxID: src.ID, DestinationTxID: dst.ID,
				Detected: "manual", Method: "restore"}
			return x.Txs.CreateTransfer(tx, tr, src, dst, internalCat)
		}
		if transferID == "" {
			return fmt.Errorf("auditexec: transfer id missing in payload")
		}
		if err := x.unstampLegs(tx, ent, before); err != nil {
			return err
		}
		return x.Txs.DeleteTransfer(tx, transferID)
	}
	if reapply {
		if transferID == "" {
			return fmt.Errorf("auditexec: transfer id missing in payload")
		}
		if err := x.unstampLegs(tx, ent, before); err != nil {
			return err
		}
		return x.Txs.DeleteTransfer(tx, transferID)
	}
	a, b := legsOf(before)
	if a == "" || b == "" {
		return fmt.Errorf("auditexec: transfer legs missing in payload")
	}
	src, err := x.Txs.GetTx(a)
	if err != nil {
		return err
	}
	dst, err := x.Txs.GetTx(b)
	if err != nil {
		return err
	}
	if src.AmountMinor < 0 {
		_ = src
	} else {
		src, dst = dst, src
	}
	tr := &core.Transfer{SourceTxID: src.ID, DestinationTxID: dst.ID,
		Detected: "manual", Method: "restore"}
	return x.Txs.CreateTransfer(tx, tr, src, dst, internalCat)
}

func (x *Executor) unstampLegs(tx *sql.Tx, ent *audit.AuditRow, snapshot map[string]any) error {
	legs, _ := snapshot["legs"].([]any)
	for _, l := range legs {
		m, _ := l.(map[string]any)
		if m == nil {
			continue
		}
		id := str(m["id"])
		if id == "" {
			continue
		}
		typ, _ := m["type"].(string)
		catID := any(nil)
		switch c := m["category_id"].(type) {
		case string:
			if c != "" {
				catID = c
			}
		}
		src, _ := m["category_source"].(string)
		if src == "" {
			src = "none"
		}
		if _, err := tx.Exec(`UPDATE transactions SET type=?, transfer_id=NULL, category_id=?, category_source=?, updated_at=? WHERE id=?`,
			typ, catID, src, util.NowStr(), id); err != nil {
			return err
		}
	}
	return nil
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
