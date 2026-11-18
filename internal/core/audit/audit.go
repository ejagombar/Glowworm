package audit

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/store"
	"github.com/ejagombar/Glowworm/internal/util"
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

// Capabilities says whether an entry can be undone, and why not.
type Capabilities struct {
	Revert  bool
	Derived bool
	Reason  string
}

// AuditRow is a log entry as the history engine and its ApplyFunc see it.
type AuditRow struct {
	ID       int64
	Action   string
	State    string
	Before   string
	After    string
	Revert   string
	Title    string
	PathID   string
	CausedBy int64

	// Where the entry sits in history: its own place, or that of the change
	// that caused it, which it never leaves.
	linePath string
	lineID   int64
}

// ApplyFunc undoes an entry's change, or with reapply does it again.
type ApplyFunc func(tx *sql.Tx, ent *AuditRow, reapply bool) error

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

func (e *Engine) HasUser() bool {
	var n int
	_ = e.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n > 0
}
