package auditexec

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/util"
)

// conflict is why an entry can't be replayed against the data as it is now.
type conflict string

func (c conflict) Error() string { return "auditexec: " + string(c) }

// leg is what linking needs to know about a transaction.
type leg struct {
	id, account, typ string
	amount           int64
	transfer         string
	live             bool
}

// loadLeg reads inside the transaction, so it sees what earlier entries of
// the same undo or redo have already changed.
func loadLeg(tx *sql.Tx, id string) (*leg, error) {
	l := &leg{id: id}
	var transfer, deleted sql.NullString
	err := tx.QueryRow(`SELECT account_id, type, amount_minor, transfer_id, deleted_at FROM transactions WHERE id = ?`, id).
		Scan(&l.account, &l.typ, &l.amount, &transfer, &deleted)
	if err == sql.ErrNoRows {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	l.transfer = transfer.String
	l.live = !deleted.Valid
	return l, nil
}

// link puts a transfer back between its two transactions under the id it
// had, which is the id the entries about it know. It refuses rather than
// undo what has happened to either side since. With waiting, that includes
// a side that is no longer a transfer waiting for its other side.
func (x *Executor) link(tx *sql.Tx, tr *core.Transfer, waiting bool) error {
	a, err := loadLeg(tx, tr.SourceTxID)
	if err != nil {
		return err
	}
	b, err := loadLeg(tx, tr.DestinationTxID)
	if err != nil {
		return err
	}
	switch {
	case !a.live || !b.live:
		return conflict("one of its transactions has been deleted")
	case a.transfer != "" && a.transfer == b.transfer:
		return nil
	case a.transfer != "" || b.transfer != "":
		return conflict("one of its transactions is now part of another transfer")
	case a.account == b.account:
		return conflict("its transactions are now in the same account")
	case (a.amount < 0) == (b.amount < 0):
		return conflict("its transactions no longer move money in opposite directions")
	case waiting && (a.typ != core.TypeTransfer || b.typ != core.TypeTransfer):
		return conflict("one of its transactions has been changed since")
	}
	if a.amount > 0 {
		a, b = b, a
	}
	// Neither side is linked, so a transfer still naming one is left over
	// and would only stop this one being stored.
	if _, err := tx.Exec(`DELETE FROM transfers WHERE id = ? OR source_transaction_id IN (?, ?) OR destination_transaction_id IN (?, ?)`,
		tr.ID, a.id, b.id, a.id, b.id); err != nil {
		return err
	}
	tr.SourceTxID, tr.DestinationTxID = a.id, b.id
	return x.Txs.CreateTransfer(tx, tr, &core.Transaction{ID: a.id}, &core.Transaction{ID: b.id})
}

// unlink removes a transfer. With restore, each side goes back to the type
// and category in its snapshot (what it was before the link); without, both
// are left as breaking a link by hand leaves them: plain income and expense
// with no category.
func (x *Executor) unlink(tx *sql.Tx, id string, legs []any, restore bool) error {
	res, err := tx.Exec(`DELETE FROM transfers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	changed, _ := res.RowsAffected()
	for _, l := range legs {
		snap, _ := l.(map[string]any)
		typ, category, source := "", any(nil), "none"
		if restore {
			typ = str(snap["type"])
			if c := str(snap["category_id"]); c != "" {
				// A category deleted since doesn't come back with it.
				var live int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM categories WHERE id = ? AND deleted_at IS NULL`, c).Scan(&live); err != nil {
					return err
				}
				if live > 0 {
					category = c
					if s := str(snap["category_source"]); s != "" {
						source = s
					}
				}
			}
		}
		// Only a side still tied to this transfer: one deleted or relinked
		// since is no longer this entry's to change.
		res, err := tx.Exec(`UPDATE transactions SET
				type = CASE WHEN ? != '' THEN ? WHEN amount_minor > 0 THEN 'income' ELSE 'expense' END,
				transfer_id = NULL, category_id = ?, category_source = ?, category_confidence = NULL, updated_at = ?
			WHERE id = ? AND transfer_id = ?`,
			typ, typ, category, source, util.NowStr(), str(snap["id"]), id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		changed += n
	}
	if changed == 0 {
		return conflict("that link has already been broken")
	}
	return nil
}

// applyTransfer replays a link being made (transfer.linked) or broken
// (transfer.unlink); undoing one is doing the other.
func (x *Executor) applyTransfer(tx *sql.Tx, ent *audit.AuditRow, before, after, revert map[string]any, reapply bool) error {
	// Both kinds of entry list the two sides in their before snapshot. The
	// transfer itself is described where it existed: after a link, before a break.
	was := after
	if ent.Action == "transfer.unlink" {
		was = before
	}
	id := str(revert["transfer_id"])
	if id == "" {
		id = str(was["transfer_id"])
	}
	legs, _ := before["legs"].([]any)
	if id == "" || len(legs) != 2 {
		return fmt.Errorf("auditexec: transfer missing in payload")
	}
	if (ent.Action == "transfer.linked") != reapply {
		return x.unlink(tx, id, legs, ent.Action == "transfer.linked")
	}
	side := func(i int) string {
		m, _ := legs[i].(map[string]any)
		return str(m["id"])
	}
	tr := &core.Transfer{ID: id, SourceTxID: side(0), DestinationTxID: side(1), Detected: "manual", Method: "restore"}
	// Entries written before these were recorded don't say how it was found.
	if d := str(was["detected"]); d != "" {
		tr.Detected, tr.Method = d, str(was["method"])
		if c, ok := was["confidence"].(float64); ok {
			tr.Confidence = &c
		}
	}
	return x.link(tx, tr, false)
}

// hideTx deletes a transaction. If it is one side of a transfer the link
// goes with it, leaving the other side a transfer waiting to be paired, and
// the entry keeps the transfer so showTx can put it back.
func (x *Executor) hideTx(tx *sql.Tx, ent *audit.AuditRow, revert map[string]any, id string) error {
	l, err := loadLeg(tx, id)
	if err != nil {
		return err
	}
	if !l.live {
		// Marking this undone as well would let a redo bring back a
		// transaction that something else deleted.
		return conflict("that transaction has already been deleted")
	}
	var broken []*core.Transfer
	tr, err := x.Txs.BreakTransferOf(tx, id)
	if err != nil {
		return err
	}
	if tr != nil {
		broken = append(broken, tr)
	}
	if err := x.Txs.SoftDelete(tx, id); err != nil {
		return err
	}
	return x.remember(tx, ent, revert, broken)
}

// showTx brings a deleted transaction back, with the transfer it was part of.
func (x *Executor) showTx(tx *sql.Tx, revert map[string]any, id string) error {
	res, err := tx.Exec(`UPDATE transactions SET deleted_at = NULL WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return conflict("that transaction no longer exists")
	}
	return x.relink(tx, revert)
}

// remember notes on an entry the transfers that undoing or redoing it broke.
// It replaces what was noted before, which described an earlier round.
func (x *Executor) remember(tx *sql.Tx, ent *audit.AuditRow, revert map[string]any, broken []*core.Transfer) error {
	if len(broken) == 0 && revert["transfers"] == nil {
		return nil
	}
	if broken == nil {
		broken = []*core.Transfer{}
	}
	b, err := json.Marshal(broken)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE audit_entries SET revert = json_set(revert, '$.transfers', json(?)) WHERE id = ?`, string(b), ent.ID)
	return err
}

// relink puts back the transfers an entry remembers, each only if both its
// sides are still transfers waiting to be paired.
func (x *Executor) relink(tx *sql.Tx, revert map[string]any) error {
	list, _ := revert["transfers"].([]any)
	lost := 0
	for _, v := range list {
		m, _ := v.(map[string]any)
		tr := &core.Transfer{ID: str(m["id"]), SourceTxID: str(m["source_transaction_id"]),
			DestinationTxID: str(m["destination_transaction_id"]), Detected: str(m["detected"]), Method: str(m["method"])}
		if c, ok := m["confidence"].(float64); ok {
			tr.Confidence = &c
		}
		var changed conflict
		if err := x.link(tx, tr, true); errors.As(err, &changed) {
			lost++
		} else if err != nil {
			return err
		}
	}
	switch {
	case lost == 1:
		return &audit.Partial{Notes: []string{"Its transfer link wasn't put back: the other side has changed since"}}
	case lost > 1:
		return &audit.Partial{Notes: []string{fmt.Sprintf("%d transfer links weren't put back: the other sides have changed since", lost)}}
	}
	return nil
}
