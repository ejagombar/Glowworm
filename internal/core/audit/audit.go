// Package audit owns the audit writer and the history engine
// (docs/design/11-audit-history-timeline.md §4).
//
// The Writer records rich, reversible entries inside the same SQL transaction
// as the mutation they describe. The Engine implements the branchable-timeline
// operations: Revert, JumpBack, RestorePath, Forget and Delete, driven by the
// capability matrix in policy.go.
package audit

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"glowworm/internal/core"
	"glowworm/internal/store"
	"glowworm/internal/util"
)

// DiffLine is one renderable diff row (doc 11 §2).
type DiffLine struct {
	Line string `json:"line"`
	Cls  string `json:"cls"` // diff-add | diff-del
}

// Input describes one audit entry to write.
type Input struct {
	Actor       string // user | ai | system
	ActorDetail string
	Action      string
	EntityType  string
	EntityID    string
	Title       string
	Sub         string
	Diff        []DiffLine
	Before      any
	After       any
	Revert      any
}

// Writer writes audit entries.
type Writer struct {
	DB *store.DB
}

func marshal(v any) string {
	if v == nil {
		return "{}"
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func marshalDiff(d []DiffLine) string {
	if len(d) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(d)
	return string(b)
}

// Write inserts an audit entry on the active path within tx and returns its id.
func (w *Writer) Write(tx *sql.Tx, in Input) (int64, error) {
	var pathID string
	if err := tx.QueryRow(`SELECT id FROM audit_paths WHERE is_active = 1 LIMIT 1`).Scan(&pathID); err != nil {
		return 0, fmt.Errorf("audit: no active path: %w", err)
	}
	res, err := tx.Exec(`INSERT INTO audit_entries
		(timestamp, actor, actor_detail, action, entity_type, entity_id, title, sub, diff, before, after, revert, path_id, state)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?, 'active')`,
		util.NowStr(), in.Actor, in.ActorDetail, in.Action, in.EntityType, in.EntityID,
		in.Title, in.Sub, marshalDiff(in.Diff), marshal(in.Before), marshal(in.After), marshal(in.Revert), pathID)
	if err != nil {
		return 0, fmt.Errorf("audit: write: %w", err)
	}
	return res.LastInsertId()
}

// WriteRevert records a revert link after a revert entry is created.
func (w *Writer) WriteRevertLink(tx *sql.Tx, revertEntryID, revertedEntryID int64) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO audit_reverts (entry_id, reverted_entry) VALUES (?,?)`,
		revertEntryID, revertedEntryID)
	return err
}

// WasReverted reports whether an entry has been reverted (excluded from
// path re-application, doc 11 §4.2).
func (w *Writer) WasReverted(tx *sql.Tx, entryID int64) (bool, error) {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM audit_reverts WHERE reverted_entry = ?`, entryID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// Diff helpers -----------------------------------------------------------

// Field renders a before→after field diff line pair.
func Field(label, from, to string) []DiffLine {
	return []DiffLine{
		{Line: "− " + label + ": " + from, Cls: "diff-del"},
		{Line: "+ " + label + ": " + to, Cls: "diff-add"},
	}
}

// Add renders a "+ value → Category" line (prototype History parity).
func Add(line string) DiffLine { return DiffLine{Line: "+ " + line, Cls: "diff-add"} }

// Del renders a struck-through "− value" line.
func Del(line string) DiffLine { return DiffLine{Line: "− " + line, Cls: "diff-del"} }

// Engine -----------------------------------------------------------------

// Engine implements the branchable-timeline operations (doc 11 §4).
type Engine struct {
	DB     *store.DB
	Writer *Writer
	Policy *Policy
}

// NewEngine wires the engine with the default policy.
func NewEngine(db *store.DB, w *Writer) *Engine {
	return &Engine{DB: db, Writer: w, Policy: &Policy{}}
}

// Capabilities is the engine's verdict for one entry (doc 11 §4.3).
type Capabilities struct {
	Revert   bool   `json:"revert"`
	JumpBack bool   `json:"jump_back"`
	Forget   bool   `json:"forget"`
	Delete   bool   `json:"delete"`
	Reason   string `json:"reason,omitempty"`
}

// Capabilities computes the verdict for an entry.
func (e *Engine) Capabilities(id int64) (Capabilities, error) {
	var ent AuditRow
	err := e.DB.QueryRow(`SELECT action, state FROM audit_entries WHERE id = ?`, id).
		Scan(&ent.Action, &ent.State)
	if err != nil {
		return Capabilities{}, fmt.Errorf("audit: entry %d: %w", id, err)
	}
	return e.Policy.Evaluate(ent.Action, ent.State), nil
}

// AuditRow is the minimal entry projection the engine needs.
type AuditRow struct {
	ID     int64
	Action string
	State  string
	Before string
	After  string
	Revert string
}

func (e *Engine) load(tx *sql.Tx, id int64) (*AuditRow, error) {
	var ent AuditRow
	err := tx.QueryRow(`SELECT id, action, state, before, after, revert FROM audit_entries WHERE id = ?`, id).
		Scan(&ent.ID, &ent.Action, &ent.State, &ent.Before, &ent.After, &ent.Revert)
	if err != nil {
		return nil, err
	}
	return &ent, nil
}

// ApplyFunc executes (or undoes) an entry's effect. reapply=true re-applies
// the recorded "after" state (path restore); false undoes it (revert/shelve).
type ApplyFunc func(tx *sql.Tx, ent *AuditRow, reapply bool) error

// Revert undoes one entry's effect through the service-layer hook and appends
// an audit.reverted entry (doc 11 §4.2).
func (e *Engine) Revert(entryID int64, apply ApplyFunc) error {
	return e.DB.WithTx(func(tx *sql.Tx) error {
		ent, err := e.load(tx, entryID)
		if err != nil {
			return err
		}
		cap := e.Policy.Evaluate(ent.Action, ent.State)
		if !cap.Revert {
			return fmt.Errorf("audit: %s", cap.Reason)
		}
		if reverted, err := e.Writer.WasReverted(tx, entryID); err == nil && reverted {
			return fmt.Errorf("audit: entry %d already reverted", entryID)
		}
		if apply != nil {
			if err := apply(tx, ent, false); err != nil {
				return err
			}
		}
		revID, err := e.Writer.Write(tx, Input{
			Actor: "user", Action: "audit.reverted",
			EntityType: "audit_entry", EntityID: fmt.Sprint(entryID),
			Title: "Reverted: " + ent.Action,
		})
		if err != nil {
			return err
		}
		return e.Writer.WriteRevertLink(tx, revID, entryID)
	})
}

// JumpBack shelves every active entry after entryID (reverting their effects
// in reverse order), forks a new path and appends audit.jumpback (doc 11 §4.2).
func (e *Engine) JumpBack(entryID int64, apply ApplyFunc) (JumpResult, error) {
	var res JumpResult
	err := e.DB.WithTx(func(tx *sql.Tx) error {
		ent, err := e.load(tx, entryID)
		if err != nil {
			return err
		}
		if ent.State != "active" {
			return fmt.Errorf("audit: cannot jump back to an inactive entry")
		}
		// Collect active entries after entryID, newest first.
		rows, err := tx.Query(`SELECT id, action, state, before, after, revert FROM audit_entries
			WHERE id > ? AND state = 'active' ORDER BY id DESC`, entryID)
		if err != nil {
			return err
		}
		var targets []AuditRow
		for rows.Next() {
			var r AuditRow
			if err := rows.Scan(&r.ID, &r.Action, &r.State, &r.Before, &r.After, &r.Revert); err != nil {
				rows.Close()
				return err
			}
			targets = append(targets, r)
		}
		rows.Close()

		var deactivated []int64
		var failed []map[string]any
		for i := range targets {
			t := targets[i]
			cap := e.Policy.Evaluate(t.Action, t.State)
			if !cap.Revert {
				failed = append(failed, map[string]any{"id": t.ID, "reason": cap.Reason})
				continue
			}
			if apply != nil {
				if err := apply(tx, &t, false); err != nil {
					failed = append(failed, map[string]any{"id": t.ID, "reason": err.Error()})
					continue
				}
			}
			if _, err := tx.Exec(`UPDATE audit_entries SET state = 'inactive' WHERE id = ?`, t.ID); err != nil {
				return err
			}
			deactivated = append(deactivated, t.ID)
		}

		// Fork a new path at the jump point.
		var parentPath string
		if err := tx.QueryRow(`SELECT path_id FROM audit_entries WHERE id = ?`, entryID).Scan(&parentPath); err != nil {
			return err
		}
		pathID := util.NewID()
		if _, err := tx.Exec(`INSERT INTO audit_paths (id, parent_path_id, fork_entry_id, label, is_active, created_at)
			VALUES (?,?,?,?,1,?)`, pathID, parentPath, entryID,
			"Jump back · "+time.Now().Format("2 Jan"), util.NowStr()); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE audit_paths SET is_active = 0 WHERE id != ? AND is_active = 1`, pathID); err != nil {
			return err
		}
		_, err = e.Writer.Write(tx, Input{
			Actor: "user", Action: "audit.jumpback",
			EntityType: "audit_path", EntityID: pathID,
			Title: fmt.Sprintf("Jumped back to entry %d", entryID),
			Sub:   fmt.Sprintf("%d later actions hidden", len(deactivated)),
			After: map[string]any{"to_entry": entryID, "deactivated": deactivated, "failed": failed},
		})
		if err != nil {
			return err
		}
		res = JumpResult{Deactivated: deactivated, Failed: failed}
		return nil
	})
	return res, err
}

// JumpResult summarises a jump-back.
type JumpResult struct {
	Deactivated []int64         `json:"deactivated"`
	Failed      []map[string]any `json:"failed,omitempty"`
}

// RestorePath makes path the active path: reverts the currently active
// future, re-applies the path's inactive entries (skipping previously
// reverted ones), and appends audit.path.activated (doc 11 §4.2).
func (e *Engine) RestorePath(pathID string, apply ApplyFunc) (RestoreResult, error) {
	var res RestoreResult
	err := e.DB.WithTx(func(tx *sql.Tx) error {
		var isActive bool
		if err := tx.QueryRow(`SELECT is_active FROM audit_paths WHERE id = ?`, pathID).Scan(&isActive); err != nil {
			return fmt.Errorf("audit: path %s: %w", pathID, err)
		}

		// 1. Deactivate (revert) every active entry that is not part of the
		// active-chain union for path.
		toShelve, err := e.activeOutsideChain(tx, pathID)
		if err != nil {
			return err
		}
		var shelved []int64
		for i := len(toShelve) - 1; i >= 0; i-- {
			t := toShelve[i]
			if apply != nil {
				if err := apply(tx, &t, false); err != nil {
					continue // never block a restore on a failed shelve
				}
			}
			if _, err := tx.Exec(`UPDATE audit_entries SET state = 'inactive' WHERE id = ?`, t.ID); err != nil {
				return err
			}
			shelved = append(shelved, t.ID)
		}

		// 2. Re-apply path's inactive entries in seq order (skip reverted).
		rows, err := tx.Query(`SELECT id, action, state, before, after, revert FROM audit_entries
			WHERE path_id = ? ORDER BY id ASC`, pathID)
		if err != nil {
			return err
		}
		var reapplied, skipped []int64
		for rows.Next() {
			var r AuditRow
			if err := rows.Scan(&r.ID, &r.Action, &r.State, &r.Before, &r.After, &r.Revert); err != nil {
				rows.Close()
				return err
			}
			if r.State != "inactive" {
				continue // already active (ancestor prefix)
			}
			if rev, _ := e.Writer.WasReverted(tx, r.ID); rev {
				skipped = append(skipped, r.ID)
				continue
			}
			if apply != nil {
				if err := apply(tx, &r, true); err != nil {
					skipped = append(skipped, r.ID)
					continue
				}
			}
			if _, err := tx.Exec(`UPDATE audit_entries SET state = 'active' WHERE id = ?`, r.ID); err != nil {
				return err
			}
			reapplied = append(reapplied, r.ID)
		}
		rows.Close()

		// 3. Path flags.
		if _, err := tx.Exec(`UPDATE audit_paths SET is_active = CASE WHEN id = ? THEN 1 ELSE 0 END`, pathID); err != nil {
			return err
		}
		_, err = e.Writer.Write(tx, Input{
			Actor: "user", Action: "audit.path.activated",
			EntityType: "audit_path", EntityID: pathID,
			Title: "Restored original path",
			Sub:   fmt.Sprintf("%d actions re-applied, %d shelved", len(reapplied), len(shelved)),
			After: map[string]any{"path": pathID, "reactivated": reapplied, "shelved": shelved, "skipped": skipped},
		})
		if err != nil {
			return err
		}
		res = RestoreResult{Reactivated: reapplied, Shelved: shelved, Skipped: skipped}
		return nil
	})
	return res, err
}

// RestoreResult summarises a path restore.
type RestoreResult struct {
	Reactivated []int64 `json:"reactivated"`
	Shelved     []int64 `json:"shelved"`
	Skipped     []int64 `json:"skipped,omitempty"`
}

// activeOutsideChain returns the currently active entries that would not be
// part of path's chain-union (ancestors up to each fork + path itself).
func (e *Engine) activeOutsideChain(tx *sql.Tx, pathID string) ([]AuditRow, error) {
	// Chain: path + all ancestors. Ancestor entries ≤ fork boundary belong to
	// the chain-union; entries beyond the fork (or on non-ancestor branches)
	// are outside.
	var chain []struct {
		id  string
		fork *int64
	}
	p := pathID
	for p != "" {
		var parent *string
		var fork *int64
		if err := tx.QueryRow(`SELECT COALESCE(parent_path_id,''), fork_entry_id FROM audit_paths WHERE id = ?`, p).
			Scan(&parent, &fork); err != nil {
			return nil, err
		}
		chain = append(chain, struct {
			id   string
			fork *int64
		}{p, fork})
		if parent == nil || *parent == "" {
			break
		}
		p = *parent
	}
	inChain := func(path string, id int64) bool {
		for _, c := range chain {
			if c.id != path {
				continue
			}
			if c.fork != nil && id > *c.fork {
				return false // beyond the fork boundary
			}
			return true
		}
		return false
	}
	rows, err := tx.Query(`SELECT id, action, path_id FROM audit_entries WHERE state = 'active' ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var id int64
		var action, path string
		if err := rows.Scan(&id, &action, &path); err != nil {
			return nil, err
		}
		if inChain(path, id) {
			continue
		}
		var ent AuditRow
		if err := tx.QueryRow(`SELECT before, after, revert FROM audit_entries WHERE id = ?`, id).
			Scan(&ent.Before, &ent.After, &ent.Revert); err != nil {
			return nil, err
		}
		ent.ID, ent.Action, ent.State = id, action, "active"
		out = append(out, ent)
	}
	return out, rows.Err()
}

// Forget removes the entry record while keeping its effect (doc 11 §4.2).
func (e *Engine) Forget(entryID int64) error {
	return e.DB.WithTx(func(tx *sql.Tx) error {
		ent, err := e.load(tx, entryID)
		if err != nil {
			return err
		}
		cap := e.Policy.Evaluate(ent.Action, ent.State)
		if !cap.Forget {
			return fmt.Errorf("audit: %s", cap.Reason)
		}
		return e.removeRecord(tx, entryID, "forget", ent)
	})
}

// Delete undoes the entry's effect (where it has one) and scrubs the record.
func (e *Engine) Delete(entryID int64, apply ApplyFunc) error {
	return e.DB.WithTx(func(tx *sql.Tx) error {
		ent, err := e.load(tx, entryID)
		if err != nil {
			return err
		}
		cap := e.Policy.Evaluate(ent.Action, ent.State)
		if !cap.Delete {
			return fmt.Errorf("audit: %s", cap.Reason)
		}
		if ent.State == "active" && apply != nil && cap.Revert {
			// Undo the effect first (subject to blocked rules), then scrub.
			if err := apply(tx, ent, false); err != nil {
				return err
			}
		}
		return e.removeRecord(tx, entryID, "undo", ent)
	})
}

func (e *Engine) removeRecord(tx *sql.Tx, entryID int64, mode string, ent *AuditRow) error {
	// Scrub derived revert links both directions.
	_, _ = tx.Exec(`DELETE FROM audit_reverts WHERE entry_id = ? OR reverted_entry = ?`, entryID, entryID)
	if _, err := tx.Exec(`DELETE FROM audit_entries WHERE id = ?`, entryID); err != nil {
		return err
	}
	action := ent.Action
	_, err := e.Writer.Write(tx, Input{
		Actor: "user", Action: "audit.entry.deleted",
		EntityType: "audit_entry", EntityID: fmt.Sprint(entryID),
		Title: fmt.Sprintf("Removed %s record from history", action),
		Sub:   "Change removed from the audit history (" + mode + ")",
		After: map[string]any{"mode": mode, "removed": []int64{entryID}, "action": action},
	})
	return err
}

// ListOptions filters the audit list (doc 03 §10).
type ListOptions struct {
	Actor     string
	Action    string
	EntityType string
	EntityID  string
	Limit     int
	Cursor    int64 // exclusive
}

// List returns entries (newest first) with parsed diff lines.
func (e *Engine) List(opt ListOptions) ([]core.AuditEntry, string, error) {
	if opt.Limit <= 0 || opt.Limit > 200 {
		opt.Limit = 50
	}
	q := `SELECT id, timestamp, actor, actor_detail, action, entity_type, entity_id, title, sub, diff, path_id, state
		FROM audit_entries WHERE 1=1`
	args := []any{}
	if opt.Actor != "" {
		q += ` AND actor = ?`
		args = append(args, opt.Actor)
	}
	if opt.Action != "" {
		if opt.Action == "import" {
			q += ` AND action LIKE 'import%'`
		} else {
			q += ` AND action = ?`
			args = append(args, opt.Action)
		}
	}
	if opt.EntityType != "" {
		q += ` AND entity_type = ?`
		args = append(args, opt.EntityType)
	}
	if opt.EntityID != "" {
		q += ` AND entity_id = ?`
		args = append(args, opt.EntityID)
	}
	if opt.Cursor > 0 {
		q += ` AND id < ?`
		args = append(args, opt.Cursor)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, opt.Limit+1)
	rows, err := e.DB.Query(q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []core.AuditEntry
	for rows.Next() {
		var ent core.AuditEntry
		if err := rows.Scan(&ent.ID, &ent.Timestamp, &ent.Actor, &ent.ActorDetail, &ent.Action,
			&ent.EntityType, &ent.EntityID, &ent.Title, &ent.Sub, &ent.Diff, &ent.PathID, &ent.State); err != nil {
			return nil, "", err
		}
		if ent.Diff != "" && ent.Diff != "[]" {
			_ = json.Unmarshal([]byte(ent.Diff), &ent.DiffLines)
		}
		out = append(out, ent)
	}
	next := ""
	if len(out) > opt.Limit {
		out = out[:opt.Limit]
		next = fmt.Sprint(out[len(out)-1].ID)
	}
	return out, next, nil
}

// Get returns one entry with parsed diff.
func (e *Engine) Get(id int64) (*core.AuditEntry, error) {
	var ent core.AuditEntry
	err := e.DB.QueryRow(`SELECT id, timestamp, actor, actor_detail, action, entity_type, entity_id,
		title, sub, diff, path_id, state FROM audit_entries WHERE id = ?`, id).
		Scan(&ent.ID, &ent.Timestamp, &ent.Actor, &ent.ActorDetail, &ent.Action,
			&ent.EntityType, &ent.EntityID, &ent.Title, &ent.Sub, &ent.Diff, &ent.PathID, &ent.State)
	if err != nil {
		return nil, err
	}
	if ent.Diff != "" && ent.Diff != "[]" {
		_ = json.Unmarshal([]byte(ent.Diff), &ent.DiffLines)
	}
	return &ent, nil
}

// HasUser reports whether any owner account exists (setup claim, doc 14 §2).
func (e *Engine) HasUser() bool {
	var n int
	_ = e.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n > 0
}

// PathLabel renders a path label for the collapsed-group header.
func (e *Engine) PathLabel(pathID string) string {
	var label string
	_ = e.DB.QueryRow(`SELECT label FROM audit_paths WHERE id = ?`, pathID).Scan(&label)
	if strings.TrimSpace(label) == "" {
		return pathID
	}
	return label
}
