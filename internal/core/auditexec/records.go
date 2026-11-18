package auditexec

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/store/repos"
)

// expect refuses to delete or bring back a record that isn't as the entry
// left it: gone altogether, or already deleted or brought back by something
// else. Marking the entry undone or redone all the same would let a later
// redo or undo reverse that other change.
func expect(tx *sql.Tx, table, what, id string, live bool) error {
	var deleted sql.NullString
	err := tx.QueryRow(`SELECT deleted_at FROM `+table+` WHERE id = ?`, id).Scan(&deleted)
	switch {
	case err == sql.ErrNoRows:
		return conflict("that " + what + " no longer exists")
	case err != nil:
		return err
	case live && deleted.Valid:
		return conflict("that " + what + " has already been deleted")
	case !live && !deleted.Valid:
		return conflict("that " + what + " has already been brought back")
	}
	return nil
}

// field is one part of a record that an update can change.
type field struct{ key, label string }

var accountFields = []field{
	{"name", "name"}, {"type", "type"}, {"color", "colour"}, {"currency", "currency"},
	{"opening_balance_minor", "opening balance"}, {"opening_balance_date", "opening balance date"},
	{"notes", "notes"}, {"account_number", "account number"}, {"sort_code", "sort code"},
	{"iban", "IBAN"}, {"bic", "BIC"},
}

var categoryFields = []field{{"name", "name"}, {"color", "colour"}, {"type", "type"}, {"description", "description"}}

// changed works out what undoing or redoing an update writes. The update
// took the record from one snapshot (from) to the other (to); each field it
// changed goes to its to value if the record (cur) still holds the from
// value. One changed again since is left alone and named in kept.
func changed(fields []field, cur, from, to map[string]any) (set map[string]any, kept []string) {
	set = map[string]any{}
	for _, f := range fields {
		if snapVal(from[f.key]) == snapVal(to[f.key]) {
			continue
		}
		if snapVal(cur[f.key]) != snapVal(from[f.key]) {
			kept = append(kept, f.label)
			continue
		}
		set[f.key] = to[f.key]
	}
	return set, kept
}

// keptNote says which fields an undo or redo left as a later change had them.
func keptNote(name string, kept []string) error {
	if len(kept) == 0 {
		return nil
	}
	return &audit.Partial{Notes: []string{fmt.Sprintf("%s: kept the %s from a later change", name, joinAnd(kept))}}
}

// removeAccount undoes the adding of an account. Only an empty one goes:
// its transactions and values would be left belonging to nothing.
func (x *Executor) removeAccount(tx *sql.Tx, id string) error {
	if err := expect(tx, "accounts", "account", id, true); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM transactions WHERE account_id = ? AND deleted_at IS NULL)
		+ (SELECT COUNT(*) FROM valuations WHERE account_id = ? AND deleted_at IS NULL)`, id, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return conflict("that account has transactions or values in it now; delete it from its settings instead")
	}
	return x.Accounts.SoftDelete(tx, id)
}

// applyAccountUpdate moves an account from one snapshot of an update to the
// other, touching only the details the update changed.
func (x *Executor) applyAccountUpdate(tx *sql.Tx, id string, from, to map[string]any) error {
	a, err := scanAccountIn(x.Accounts, tx, id)
	if err == sql.ErrNoRows {
		return conflict("that account no longer exists")
	}
	if err != nil {
		return err
	}
	set, kept := changed(accountFields, map[string]any{
		"name": a.Name, "type": a.Type, "color": a.Color, "currency": a.Currency,
		"opening_balance_minor": a.OpeningBalanceMinor, "opening_balance_date": a.OpeningBalanceDate,
		"notes": a.Notes, "account_number": a.AccountNumber, "sort_code": a.SortCode, "iban": a.IBAN, "bic": a.BIC,
	}, from, to)
	for k, v := range set {
		switch k {
		case "name":
			a.Name = str(v)
		case "type":
			a.Type = str(v)
		case "color":
			a.Color = str(v)
		case "currency":
			a.Currency = str(v)
		case "opening_balance_minor":
			a.OpeningBalanceMinor = int64F(v)
		case "opening_balance_date":
			a.OpeningBalanceDate = str(v)
		case "notes":
			a.Notes = str(v)
		case "account_number":
			a.AccountNumber = str(v)
		case "sort_code":
			a.SortCode = str(v)
		case "iban":
			a.IBAN = str(v)
		case "bic":
			a.BIC = str(v)
		}
	}
	if len(set) > 0 {
		if err := x.Accounts.Update(tx, a); err != nil {
			return err
		}
	}
	return keptNote(a.Name, kept)
}

// applyCategoryUpdate does the same for a category. A name another category
// has taken since can't come back.
func (x *Executor) applyCategoryUpdate(tx *sql.Tx, id string, from, to map[string]any) error {
	c, err := scanCategoryIn(x.Categories, tx, id)
	if err == sql.ErrNoRows {
		return conflict("that category no longer exists")
	}
	if err != nil {
		return err
	}
	set, kept := changed(categoryFields, map[string]any{
		"name": c.Name, "color": c.Color, "type": c.Type, "description": c.Description,
	}, from, to)
	if name, ok := set["name"]; ok {
		var taken int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM categories WHERE name = ? COLLATE NOCASE AND id != ? AND deleted_at IS NULL`,
			str(name), id).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 || str(name) == "" {
			delete(set, "name")
			kept = append([]string{"name"}, kept...)
		}
	}
	for k, v := range set {
		switch k {
		case "name":
			c.Name = str(v)
		case "color":
			c.Color = str(v)
		case "type":
			c.Type = str(v)
		case "description":
			c.Description = str(v)
		}
	}
	if len(set) > 0 {
		if err := x.Categories.Update(tx, c); err != nil {
			return err
		}
	}
	return keptNote(c.Name, kept)
}

// removeCategory deletes a category that an entry added, or deletes it again.
func (x *Executor) removeCategory(tx *sql.Tx, id string) error {
	if err := expect(tx, "categories", "category", id, true); err != nil {
		return err
	}
	return x.Categories.Delete(tx, id)
}

// bringBackCategory restores a deleted category, from the entry's snapshot
// if the row itself is gone.
func (x *Executor) bringBackCategory(tx *sql.Tx, id string, snapshot map[string]any) error {
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM categories WHERE id = ?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 && str(snapshot["name"]) != "" {
		var taken int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM categories WHERE name = ? COLLATE NOCASE AND deleted_at IS NULL`,
			str(snapshot["name"])).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return conflict("another category has its name now")
		}
		return x.restoreCategory(tx, id, snapshot)
	}
	if err := expect(tx, "categories", "category", id, false); err != nil {
		return err
	}
	err := x.Categories.Restore(tx, id)
	if errors.Is(err, repos.ErrNameTaken) {
		return conflict("another category has its name now")
	}
	return err
}

func (x *Executor) restoreCategory(tx *sql.Tx, id string, before map[string]any) error {
	c := &core.Category{ID: id}
	c.Name, _ = before["name"].(string)
	c.Color, _ = before["color"].(string)
	c.Type, _ = before["type"].(string)
	c.Description, _ = before["description"].(string)
	if c.Color == "" {
		c.Color = "#6b7280"
	}
	if c.Type == "" {
		c.Type = core.CategorySpend
	}
	return x.Categories.CreateWithID(tx, c)
}

// setValuation removes or brings back a dated account value.
func (x *Executor) setValuation(tx *sql.Tx, id string, live bool) error {
	if err := expect(tx, "valuations", "account value", id, !live); err != nil {
		return err
	}
	if live {
		return x.Accounts.RestoreValuation(tx, id)
	}
	return x.Accounts.DeleteValuation(tx, id)
}
