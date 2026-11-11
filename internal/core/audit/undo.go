package audit

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Partial is returned by an ApplyFunc that applied an entry except for the
// parts changed again by a later entry, which it left alone so that change
// isn't lost. The entry still counts as applied; Notes say what was kept.
type Partial struct{ Notes []string }

func (p *Partial) Error() string { return strings.Join(p.Notes, "; ") }

// partialNotes reports whether err is only a Partial, adding its notes.
func partialNotes(err error, notes *[]string) bool {
	var p *Partial
	if errors.As(err, &p) {
		*notes = append(*notes, p.Notes...)
		return true
	}
	return false
}

// LinkCause marks the system/AI entries written after causeID (and not yet
// linked) as caused by it, so they're undone and redone together.
func (w *Writer) LinkCause(causeID int64) error {
	if causeID <= 0 {
		return nil
	}
	_, err := w.DB.Exec(`UPDATE audit_entries SET caused_by = ?
		WHERE id > ? AND actor IN ('system','ai') AND caused_by IS NULL`, causeID, causeID)
	return err
}

func (e *Engine) loadMany(tx *sql.Tx, q string, args ...any) ([]AuditRow, error) {
	rows, err := tx.Query(`SELECT id, action, state, before, after, revert FROM audit_entries WHERE `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.Action, &r.State, &r.Before, &r.After, &r.Revert); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func idList(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return "(?" + strings.Repeat(",?", len(ids)-1) + ")", args
}

// Undo reverses one entry, plus the system changes it caused, and leaves
// them in the log greyed out (inactive) so they can be redone. The notes say
// what was left alone because a later change touched it.
func (e *Engine) Undo(entryID int64, apply ApplyFunc) ([]string, error) {
	var notes []string
	err := e.DB.WithTx(func(tx *sql.Tx) error {
		ent, err := e.load(tx, entryID)
		if err != nil {
			return err
		}
		if cap := e.Policy.Evaluate(ent.Action, ent.State); !cap.Revert {
			return fmt.Errorf("audit: %s", cap.Reason)
		}
		deps, err := e.loadMany(tx, `caused_by = ? AND state = 'active' ORDER BY id DESC`, entryID)
		if err != nil {
			return err
		}
		// Newest first: dependants come off before the change they rest on.
		for _, r := range append(deps, *ent) {
			if apply != nil {
				if err := apply(tx, &r, false); err != nil && !partialNotes(err, &notes) {
					if r.ID == entryID || !e.Policy.Evaluate(r.Action, r.State).Derived {
						return err
					}
				}
			}
			if _, err := tx.Exec(`UPDATE audit_entries SET state = 'inactive' WHERE id = ?`, r.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return notes, err
}

// Redo re-applies undone entries. Entries undone by a rewind live on a path
// that is no longer active, so redoing them restores that path; entries
// undone one at a time are re-applied where they are.
func (e *Engine) Redo(ids []int64, apply ApplyFunc) ([]string, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("audit: nothing to redo")
	}
	var notes []string
	var pathID string
	var pathActive bool
	err := e.DB.QueryRow(`SELECT p.id, p.is_active FROM audit_entries a JOIN audit_paths p ON p.id = a.path_id
		WHERE a.id = ?`, ids[0]).Scan(&pathID, &pathActive)
	if err != nil {
		return nil, fmt.Errorf("audit: entry %d: %w", ids[0], err)
	}
	if !pathActive {
		_, err := e.RestorePath(pathID, apply)
		return nil, err
	}
	err = e.DB.WithTx(func(tx *sql.Tx) error {
		in, args := idList(ids)
		targets, err := e.loadMany(tx, `state = 'inactive' AND (id IN `+in+` OR caused_by IN `+in+`) ORDER BY id ASC`,
			append(args, args...)...)
		if err != nil {
			return err
		}
		for _, r := range targets {
			if apply != nil {
				if err := apply(tx, &r, true); err != nil && !partialNotes(err, &notes) && !e.Policy.Evaluate(r.Action, "active").Derived {
					return fmt.Errorf("audit: redo %s: %w", r.Action, err)
				}
			}
			if _, err := tx.Exec(`UPDATE audit_entries SET state = 'active' WHERE id = ?`, r.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return notes, err
}

// Discard permanently removes undone entries (and the undone system changes
// they caused) from the log. Their effects are already gone.
func (e *Engine) Discard(ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	n := 0
	err := e.DB.WithTx(func(tx *sql.Tx) error {
		in, args := idList(ids)
		targets, err := e.loadMany(tx, `state = 'inactive' AND (id IN `+in+` OR caused_by IN `+in+`)`,
			append(args, args...)...)
		if err != nil {
			return err
		}
		for _, r := range targets {
			if _, err := tx.Exec(`DELETE FROM audit_entries WHERE id = ?`, r.ID); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// LatestRewindableID is the newest active entry a rewind would undo (derived
// entries only follow their cause, so they don't count). Rewinding
// to it or anything newer would change nothing.
func (e *Engine) LatestRewindableID() int64 {
	rows, err := e.DB.Query(`SELECT id, action FROM audit_entries WHERE state = 'active' ORDER BY id DESC`)
	if err != nil {
		return 0
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var action string
		if rows.Scan(&id, &action) != nil {
			return 0
		}
		if e.Policy.Evaluate(action, "active").Revert {
			return id
		}
	}
	return 0
}
