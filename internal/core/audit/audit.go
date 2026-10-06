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

type DiffLine struct {
	Line string `json:"line"`
	Cls  string `json:"cls"`
}

type Input struct {
	Actor       string
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

func (w *Writer) WriteRevertLink(tx *sql.Tx, revertEntryID, revertedEntryID int64) error {
	_, err := tx.Exec(`INSERT OR REPLACE INTO audit_reverts (entry_id, reverted_entry) VALUES (?,?)`,
		revertEntryID, revertedEntryID)
	return err
}

func (w *Writer) WasReverted(tx *sql.Tx, entryID int64) (bool, error) {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM audit_reverts WHERE reverted_entry = ?`, entryID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func Field(label, from, to string) []DiffLine {
	return []DiffLine{
		{Line: "− " + label + ": " + from, Cls: "diff-del"},
		{Line: "+ " + label + ": " + to, Cls: "diff-add"},
	}
}

func Add(line string) DiffLine { return DiffLine{Line: "+ " + line, Cls: "diff-add"} }

func Del(line string) DiffLine { return DiffLine{Line: "− " + line, Cls: "diff-del"} }

type Engine struct {
	DB     *store.DB
	Writer *Writer
	Policy *Policy
}

func NewEngine(db *store.DB, w *Writer) *Engine {
	return &Engine{DB: db, Writer: w, Policy: &Policy{}}
}

type Capabilities struct {
	Revert   bool   `json:"revert"`
	JumpBack bool   `json:"jump_back"`
	Forget   bool   `json:"forget"`
	Delete   bool   `json:"delete"`
	Derived  bool   `json:"derived,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func (e *Engine) Capabilities(id int64) (Capabilities, error) {
	var ent AuditRow
	err := e.DB.QueryRow(`SELECT action, state FROM audit_entries WHERE id = ?`, id).
		Scan(&ent.Action, &ent.State)
	if err != nil {
		return Capabilities{}, fmt.Errorf("audit: entry %d: %w", id, err)
	}
	return e.Policy.Evaluate(ent.Action, ent.State), nil
}

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

type ApplyFunc func(tx *sql.Tx, ent *AuditRow, reapply bool) error

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
			if !cap.Revert && !cap.Derived {
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
		// No record of the jump itself: the undone entries stay in the log,
		// greyed out and collapsed, which says the same thing.
		res = JumpResult{Deactivated: deactivated, Failed: failed}
		return nil
	})
	return res, err
}

type JumpResult struct {
	Deactivated []int64          `json:"deactivated"`
	Failed      []map[string]any `json:"failed,omitempty"`
}

func (e *Engine) RestorePath(pathID string, apply ApplyFunc) (RestoreResult, error) {
	var res RestoreResult
	err := e.DB.WithTx(func(tx *sql.Tx) error {
		var isActive bool
		if err := tx.QueryRow(`SELECT is_active FROM audit_paths WHERE id = ?`, pathID).Scan(&isActive); err != nil {
			return fmt.Errorf("audit: path %s: %w", pathID, err)
		}

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
				continue
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

		if _, err := tx.Exec(`UPDATE audit_paths SET is_active = CASE WHEN id = ? THEN 1 ELSE 0 END`, pathID); err != nil {
			return err
		}
		res = RestoreResult{Reactivated: reapplied, Shelved: shelved, Skipped: skipped}
		return nil
	})
	return res, err
}

type RestoreResult struct {
	Reactivated []int64 `json:"reactivated"`
	Shelved     []int64 `json:"shelved"`
	Skipped     []int64 `json:"skipped,omitempty"`
}

func (e *Engine) activeOutsideChain(tx *sql.Tx, pathID string) ([]AuditRow, error) {
	var chain []struct {
		id   string
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
				return false
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
			if err := apply(tx, ent, false); err != nil {
				return err
			}
		}
		return e.removeRecord(tx, entryID, "undo", ent)
	})
}

func (e *Engine) removeRecord(tx *sql.Tx, entryID int64, mode string, ent *AuditRow) error {
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

type ListOptions struct {
	Actor      string
	Actors     []string
	Action     string
	EntityType string
	EntityID   string
	Limit      int
	Cursor     int64
}

func (e *Engine) List(opt ListOptions) ([]core.AuditEntry, string, error) {
	if opt.Limit <= 0 || opt.Limit > 200 {
		opt.Limit = 50
	}
	q := `SELECT id, timestamp, actor, actor_detail, action, entity_type, entity_id, title, sub, diff, path_id, state,
		COALESCE(caused_by, 0) FROM audit_entries WHERE 1=1`
	args := []any{}
	if opt.Actor != "" {
		q += ` AND actor = ?`
		args = append(args, opt.Actor)
	}
	if len(opt.Actors) > 0 {
		q += ` AND actor IN (?` + strings.Repeat(",?", len(opt.Actors)-1) + `)`
		for _, a := range opt.Actors {
			args = append(args, a)
		}
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
			&ent.EntityType, &ent.EntityID, &ent.Title, &ent.Sub, &ent.Diff, &ent.PathID, &ent.State, &ent.CausedBy); err != nil {
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

func (e *Engine) HasUser() bool {
	var n int
	_ = e.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n > 0
}

func (e *Engine) PathLabel(pathID string) string {
	var label string
	_ = e.DB.QueryRow(`SELECT label FROM audit_paths WHERE id = ?`, pathID).Scan(&label)
	if strings.TrimSpace(label) == "" {
		return pathID
	}
	return label
}
