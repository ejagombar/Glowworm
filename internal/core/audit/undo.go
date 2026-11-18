package audit

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ejagombar/Glowworm/internal/util"
)

// How history works
//
// Every change writes an entry, active while the change is in effect and
// inactive (greyed out in the log) once undone. Undo and Redo flip one entry
// and what the system did because of it, in place.
//
// A rewind undoes everything after an entry and starts a new timeline (an
// audit_paths row) forking from it; what is done next is written there. The
// rewound entries stay on the timeline they were written on. A timeline's
// history is its own entries plus, for each ancestor, the entries up to the
// one its child forked from. Redoing entries that aren't part of the current
// timeline's history goes back to theirs: whatever was done on the timelines
// that branched away is undone, and those entries wait to be redone in turn.

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

// Reason is an error from undoing or redoing as a person should read it.
func Reason(err error) string {
	s := err.Error()
	for _, prefix := range []string{"audit: ", "auditexec: "} {
		s = strings.TrimPrefix(s, prefix)
	}
	return s
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

// loadMany reads the entries matching a condition on audit_entries a.
func (e *Engine) loadMany(tx *sql.Tx, where string, args ...any) ([]AuditRow, error) {
	rows, err := tx.Query(`SELECT a.id, a.action, a.state, a.before, a.after, a.revert, a.title, a.path_id,
		COALESCE(a.caused_by, 0), COALESCE(c.path_id, a.path_id), COALESCE(c.id, a.id)
		FROM audit_entries a LEFT JOIN audit_entries c ON c.id = a.caused_by WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.Action, &r.State, &r.Before, &r.After, &r.Revert, &r.Title, &r.PathID,
			&r.CausedBy, &r.linePath, &r.lineID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (e *Engine) load(tx *sql.Tx, id int64) (*AuditRow, error) {
	rows, err := e.loadMany(tx, `a.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("audit: that change is no longer in the log")
	}
	return &rows[0], nil
}

func idList(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return "(?" + strings.Repeat(",?", len(ids)-1) + ")", args
}

func setState(tx *sql.Tx, id int64, state string) error {
	_, err := tx.Exec(`UPDATE audit_entries SET state = ? WHERE id = ?`, state, id)
	return err
}

// try applies one entry so that a failure leaves nothing half done: the
// caller may carry on with the rest. Notes from a Partial are collected and
// don't count as failure.
func try(tx *sql.Tx, apply ApplyFunc, r *AuditRow, reapply bool, notes *[]string) error {
	if apply == nil {
		return nil
	}
	if _, err := tx.Exec(`SAVEPOINT audit_step`); err != nil {
		return err
	}
	err := apply(tx, r, reapply)
	if err != nil && partialNotes(err, notes) {
		err = nil
	}
	if err != nil {
		if _, rerr := tx.Exec(`ROLLBACK TO audit_step`); rerr != nil {
			return rerr
		}
	}
	if _, rerr := tx.Exec(`RELEASE audit_step`); rerr != nil {
		return rerr
	}
	return err
}

// Undo reverses one entry, plus the system changes it caused, and leaves
// them in the log greyed out (inactive) so they can be redone. The notes say
// what was left alone because a later change touched it.
func (e *Engine) Undo(entryID int64, apply ApplyFunc) ([]string, error) {
	var notes []string
	err := e.DB.WithTx(func(tx *sql.Tx) error {
		notes = nil
		ent, err := e.load(tx, entryID)
		if err != nil {
			return err
		}
		if cap := e.Policy.Evaluate(ent.Action, ent.State); !cap.Revert {
			return fmt.Errorf("audit: %s", cap.Reason)
		}
		deps, err := e.loadMany(tx, `a.caused_by = ? AND a.state = 'active' ORDER BY a.id DESC`, entryID)
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
			if err := setState(tx, r.ID, "inactive"); err != nil {
				return err
			}
		}
		return nil
	})
	return notes, err
}

// timeline reports whether a place in the log belongs to a path's history:
// everything written on the path itself, and on each ancestor everything up
// to the entry its child forked from.
func (e *Engine) timeline(tx *sql.Tx, pathID string) (func(r *AuditRow) bool, error) {
	type bound struct {
		all  bool
		upTo int64
	}
	chain := map[string]bound{}
	b := bound{all: true}
	for p := pathID; p != ""; {
		if _, seen := chain[p]; seen {
			break
		}
		chain[p] = b
		var parent sql.NullString
		var fork sql.NullInt64
		if err := tx.QueryRow(`SELECT parent_path_id, fork_entry_id FROM audit_paths WHERE id = ?`, p).Scan(&parent, &fork); err != nil {
			return nil, fmt.Errorf("audit: timeline %s: %w", p, err)
		}
		b = bound{upTo: fork.Int64}
		p = parent.String
	}
	return func(r *AuditRow) bool {
		b, ok := chain[r.linePath]
		return ok && (b.all || r.lineID <= b.upTo)
	}, nil
}

func activePath(tx *sql.Tx) (string, error) {
	var id string
	if err := tx.QueryRow(`SELECT id FROM audit_paths WHERE is_active = 1 LIMIT 1`).Scan(&id); err != nil {
		return "", fmt.Errorf("audit: no active path: %w", err)
	}
	return id, nil
}

// shelve undoes the given active entries, newest first, and returns the ones
// it undid. One that can't be undone stays in effect and is named in the
// notes; it never stops the rest.
func (e *Engine) shelve(tx *sql.Tx, rows []AuditRow, apply ApplyFunc, notes *[]string) ([]int64, error) {
	var done []int64
	var left []string
	for i := range rows {
		r := &rows[i]
		cap := e.Policy.Evaluate(r.Action, r.State)
		if !cap.Revert && !cap.Derived {
			if !logOnly(r.Action) {
				left = append(left, "“"+r.Title+"”")
			}
			continue
		}
		if err := try(tx, apply, r, false, notes); err != nil && !cap.Derived {
			*notes = append(*notes, fmt.Sprintf("Couldn't undo “%s” (%s), so it's still in effect", r.Title, Reason(err)))
			continue
		}
		if err := setState(tx, r.ID, "inactive"); err != nil {
			return nil, err
		}
		done = append(done, r.ID)
	}
	if len(left) > 0 {
		if more := len(left) - 3; more > 0 {
			left = append(left[:3], fmt.Sprintf("%d more", more))
		}
		*notes = append(*notes, "History can't undo "+joinAnd(left)+", so that stays as it is")
	}
	return done, nil
}

func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// JumpResult says what a rewind undid, and in Notes what it couldn't.
type JumpResult struct {
	Deactivated []int64  `json:"deactivated"`
	Notes       []string `json:"notes,omitempty"`
}

// JumpBack rewinds to just after an entry: every later change is undone,
// newest first, and stays in the log to be redone. The entry keeps what the
// system did because of it. Work done from here on starts a new timeline.
func (e *Engine) JumpBack(entryID int64, apply ApplyFunc) (JumpResult, error) {
	var res JumpResult
	err := e.DB.WithTx(func(tx *sql.Tx) error {
		res = JumpResult{}
		ent, err := e.load(tx, entryID)
		if err != nil {
			return err
		}
		if ent.State != "active" {
			return fmt.Errorf("audit: can't rewind to a change that has been undone")
		}
		targets, err := e.loadMany(tx, `a.state = 'active' AND a.id > ? AND (a.caused_by IS NULL OR a.caused_by > ?)
			ORDER BY a.id DESC`, entryID, entryID)
		if err != nil {
			return err
		}
		done, err := e.shelve(tx, targets, apply, &res.Notes)
		if err != nil {
			return err
		}
		res.Deactivated = done
		if len(done) == 0 {
			return nil // nothing changed, so there is no new timeline to start
		}

		current, err := activePath(tx)
		if err != nil {
			return err
		}
		onCurrent, err := e.timeline(tx, current)
		if err != nil {
			return err
		}
		// The new timeline keeps the current one up to this entry. An entry
		// left over from another timeline only marks the point in time.
		parent := current
		if own := (&AuditRow{linePath: ent.PathID, lineID: ent.ID}); onCurrent(own) {
			parent = ent.PathID
		}
		pathID := util.NewID()
		if _, err := tx.Exec(`INSERT INTO audit_paths (id, parent_path_id, fork_entry_id, label, is_active, created_at)
			VALUES (?,?,?,?,0,?)`, pathID, parent, entryID,
			"Jump back · "+time.Now().Format("2 Jan"), util.NowStr()); err != nil {
			return err
		}
		// No record of the jump itself: the undone entries stay in the log,
		// greyed out and collapsed, which says the same thing.
		return activate(tx, pathID)
	})
	return res, err
}

func activate(tx *sql.Tx, pathID string) error {
	_, err := tx.Exec(`UPDATE audit_paths SET is_active = CASE WHEN id = ? THEN 1 ELSE 0 END`, pathID)
	return err
}

// Redo re-applies undone entries, oldest first, with the system changes they
// caused. Entries undone one at a time are re-applied where they are. Entries
// undone by a rewind belong to a timeline that was left: redoing them goes
// back to it, undoing what was done since on the timelines that branched off.
//
// An entry that can't be re-applied stays undone and is named in the notes;
// if none can, that is the error and nothing changes.
func (e *Engine) Redo(ids []int64, apply ApplyFunc) ([]string, error) {
	var notes []string
	err := e.DB.WithTx(func(tx *sql.Tx) error {
		notes = nil
		plan, err := e.planRedo(tx, ids)
		if err != nil {
			return err
		}
		targets := plan.targets
		if plan.back != "" {
			if _, err := e.shelve(tx, plan.outside, apply, &notes); err != nil {
				return err
			}
			if err := activate(tx, plan.back); err != nil {
				return err
			}
		}

		applied := 0
		skipped := map[int64]bool{}
		var firstErr error
		for i := range targets {
			r := &targets[i]
			if skipped[r.CausedBy] {
				skipped[r.ID] = true
				continue
			}
			if err := try(tx, apply, r, true, &notes); err != nil && !e.Policy.Evaluate(r.Action, "active").Derived {
				skipped[r.ID] = true
				if firstErr == nil {
					firstErr = fmt.Errorf("audit: couldn't redo “%s”: %s", r.Title, Reason(err))
				}
				notes = append(notes, fmt.Sprintf("Couldn't redo “%s” (%s), so it's still undone", r.Title, Reason(err)))
				continue
			}
			if err := setState(tx, r.ID, "active"); err != nil {
				return err
			}
			applied++
		}
		if applied == 0 {
			return firstErr
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return notes, nil
}

// redoPlan is what a redo would do: the entries it re-applies and, when
// they belong to a timeline that was left, that timeline and the entries in
// effect outside it, which are undone first.
type redoPlan struct {
	targets []AuditRow
	back    string
	outside []AuditRow
}

func (e *Engine) planRedo(tx *sql.Tx, ids []int64) (redoPlan, error) {
	var plan redoPlan
	if len(ids) == 0 {
		return plan, fmt.Errorf("audit: nothing to redo")
	}
	in, args := idList(ids)
	targets, err := e.loadMany(tx, `a.state = 'inactive' AND (a.id IN `+in+` OR a.caused_by IN `+in+`) ORDER BY a.id ASC`,
		append(args, args...)...)
	if err != nil {
		return plan, err
	}
	if len(targets) == 0 {
		return plan, fmt.Errorf("audit: nothing to redo")
	}
	plan.targets = targets

	current, err := activePath(tx)
	if err != nil {
		return plan, err
	}
	onCurrent, err := e.timeline(tx, current)
	if err != nil {
		return plan, err
	}
	for i := range targets {
		if !onCurrent(&targets[i]) {
			plan.back = targets[i].linePath
		}
	}
	if plan.back == "" {
		return plan, nil
	}
	onTheirs, err := e.timeline(tx, plan.back)
	if err != nil {
		return plan, err
	}
	for i := range targets {
		if !onTheirs(&targets[i]) {
			return plan, fmt.Errorf("audit: these changes were undone at different times and can't be redone together — redo them one block at a time")
		}
	}
	active, err := e.loadMany(tx, `a.state = 'active' ORDER BY a.id DESC`)
	if err != nil {
		return plan, err
	}
	for i := range active {
		if !onTheirs(&active[i]) {
			plan.outside = append(plan.outside, active[i])
		}
	}
	return plan, nil
}

// RedoSetsAside lists the entries in effect that redoing ids would undo,
// without changing anything: those on timelines that branched away from the
// one the entries belong to. Entries history can't undo aren't listed.
func (e *Engine) RedoSetsAside(ids []int64) ([]int64, error) {
	var out []int64
	err := e.DB.WithTx(func(tx *sql.Tx) error {
		plan, err := e.planRedo(tx, ids)
		if err != nil {
			return err
		}
		for _, r := range plan.outside {
			if cap := e.Policy.Evaluate(r.Action, r.State); cap.Revert || cap.Derived {
				out = append(out, r.ID)
			}
		}
		return nil
	})
	return out, err
}

// Discard permanently removes undone entries (and the undone system changes
// they caused) from the log. Their effects are already gone.
func (e *Engine) Discard(ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	n := 0
	err := e.DB.WithTx(func(tx *sql.Tx) error {
		n = 0
		in, args := idList(ids)
		targets, err := e.loadMany(tx, `a.state = 'inactive' AND (a.id IN `+in+` OR a.caused_by IN `+in+`)`,
			append(args, args...)...)
		if err != nil {
			return err
		}
		for _, r := range targets {
			if _, err := tx.Exec(`DELETE FROM audit_entries WHERE id = ?`, r.ID); err != nil {
				return err
			}
			// Anything it caused that is still in effect stands on its own
			// now, rather than pointing at an id a later entry may reuse.
			if _, err := tx.Exec(`UPDATE audit_entries SET caused_by = NULL WHERE caused_by = ?`, r.ID); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// LatestRewindableID is the newest active entry a rewind would undo. What an
// entry caused is part of it, and rewinding to the entry keeps it, so only
// changes made in their own right count. Rewinding to this entry or anything
// newer would change nothing.
func (e *Engine) LatestRewindableID() int64 {
	rows, err := e.DB.Query(`SELECT id, action FROM audit_entries
		WHERE state = 'active' AND caused_by IS NULL ORDER BY id DESC`)
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
		if cap := e.Policy.Evaluate(action, "active"); cap.Revert || cap.Derived {
			return id
		}
	}
	return 0
}
