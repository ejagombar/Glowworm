package audit

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"glowworm/internal/store"
)

// history is a log over a scratch table: each written entry adds a row, undo
// hides it and redo shows it again, so live() is the state the log describes.
type history struct {
	t  *testing.T
	db *store.DB
	w  *Writer
	e  *Engine
	// failing names refuse to be undone or redone, after touching their row.
	failing map[string]bool
}

func newHistory(t *testing.T) *history {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE scratch (id TEXT PRIMARY KEY, deleted INTEGER DEFAULT 0, touched INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	w := &Writer{DB: db}
	return &history{t: t, db: db, w: w, e: NewEngine(db, w), failing: map[string]bool{}}
}

func (h *history) apply(tx *sql.Tx, ent *AuditRow, reapply bool) error {
	var revert map[string]any
	if err := jsonUnmarshal(ent.Revert, &revert); err != nil {
		return err
	}
	name, _ := revert["name"].(string)
	if name == "" {
		return fmt.Errorf("no handler for action %q", ent.Action)
	}
	if h.failing[name] {
		if _, err := tx.Exec(`UPDATE scratch SET touched = touched + 1 WHERE id = ?`, name); err != nil {
			return err
		}
		return fmt.Errorf("%s is stuck", name)
	}
	deleted := 1
	if reapply {
		deleted = 0
	}
	_, err := tx.Exec(`UPDATE scratch SET deleted = ? WHERE id = ?`, deleted, name)
	return err
}

// add writes a user change that can be undone.
func (h *history) add(name string) int64 {
	return h.write("user", "transaction.created", name)
}

func (h *history) write(actor, action, name string) int64 {
	h.t.Helper()
	var id int64
	err := h.db.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO scratch (id) VALUES (?)`, name); err != nil {
			return err
		}
		var err error
		id, err = h.w.Write(tx, Input{Actor: actor, Action: action, EntityType: "scratch", EntityID: name,
			Title: "Added " + name, Revert: map[string]any{"name": name}})
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *history) live() []string {
	h.t.Helper()
	rows, err := h.db.Query(`SELECT id FROM scratch WHERE deleted = 0 ORDER BY id`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// active lists the names of the entries the log says are in effect.
func (h *history) active() []string {
	h.t.Helper()
	rows, err := h.db.Query(`SELECT entity_id FROM audit_entries WHERE state = 'active' ORDER BY entity_id`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := []string{}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// want checks both the data and the log's own view of it.
func (h *history) want(step string, names ...string) {
	h.t.Helper()
	if names == nil {
		names = []string{}
	}
	if got := h.live(); !reflect.DeepEqual(got, names) {
		h.t.Fatalf("%s: live rows = %v, want %v", step, got, names)
	}
	if got := h.active(); !reflect.DeepEqual(got, names) {
		h.t.Fatalf("%s: active entries = %v, want %v", step, got, names)
	}
}

func (h *history) rewind(id int64) JumpResult {
	h.t.Helper()
	res, err := h.e.JumpBack(id, h.apply)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *history) redo(ids ...int64) []string {
	h.t.Helper()
	notes, err := h.e.Redo(ids, h.apply)
	if err != nil {
		h.t.Fatal(err)
	}
	return notes
}

func (h *history) undo(id int64) {
	h.t.Helper()
	if _, err := h.e.Undo(id, h.apply); err != nil {
		h.t.Fatal(err)
	}
}

func (h *history) paths() int {
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM audit_paths`).Scan(&n)
	return n
}

func TestRedoOfARewindUndoesWhatCameAfterIt(t *testing.T) {
	h := newHistory(t)
	a := h.add("a")
	b := h.add("b")
	c := h.add("c")

	h.rewind(a)
	h.want("rewind", "a")
	d := h.add("d")
	h.want("new work", "a", "d")

	h.redo(c, b)
	h.want("redo the rewound block", "a", "b", "c")

	// And back again: the work done after the rewind is its own block now.
	h.redo(d)
	h.want("redo the newer work", "a", "d")
}

func TestRedoOfASingleUndoAfterARewindKeepsNewerWork(t *testing.T) {
	h := newHistory(t)
	a := h.add("a")
	h.add("b")
	c := h.add("c")
	h.add("d")

	h.rewind(c)
	h.add("e")
	h.undo(a)
	h.want("undo a", "b", "c", "e")

	// a was undone on its own, not by the rewind: redoing it is no reason to
	// throw away e and bring back d.
	h.redo(a)
	h.want("redo a", "a", "b", "c", "e")
}

func TestRedoOnlyAppliesTheChosenChanges(t *testing.T) {
	h := newHistory(t)
	a := h.add("a")
	b := h.add("b")
	h.write("user", "settings.updated", "s") // can't be undone, so it splits the undone blocks
	c := h.add("c")

	h.undo(b)
	h.rewind(a)
	h.want("rewind", "a", "s")

	h.redo(c)
	h.want("redo c alone", "a", "c", "s")
}

func TestNestedRewindsRestoreEachTimeline(t *testing.T) {
	h := newHistory(t)
	a := h.add("a")
	b := h.add("b")
	c := h.add("c")
	h.rewind(b) // c undone
	d := h.add("d")
	h.rewind(a) // b and d undone
	e := h.add("e")
	h.want("two rewinds", "a", "e")

	// b and c are one grey block in the log. Redoing it returns to the first
	// timeline, so the work done on the others goes.
	h.redo(c, b)
	h.want("back to the first timeline", "a", "b", "c")

	// d was added after the first rewind, when c was gone.
	h.redo(d)
	h.want("back to the second timeline", "a", "b", "d")

	h.redo(e)
	h.want("back to the third timeline", "a", "e")
}

func TestRewindKeepsWhatTheTargetCaused(t *testing.T) {
	h := newHistory(t)
	h.add("a")
	imp := h.write("user", "import.confirmed", "import")
	h.write("system", "transfer.linked", "link")
	if err := h.w.LinkCause(imp); err != nil {
		t.Fatal(err)
	}
	if got := h.e.LatestRewindableID(); got != imp {
		t.Fatalf("latest rewindable = %d, want the import (%d): what it caused is part of it", got, imp)
	}
	later := h.add("later")

	res := h.rewind(imp)
	if len(res.Deactivated) != 1 || res.Deactivated[0] != later {
		t.Fatalf("rewind undid %v, want just the later change", res.Deactivated)
	}
	h.want("rewind to the import", "a", "import", "link")

	newer := h.add("newer")
	h.redo(later)
	h.want("redo", "a", "import", "later", "link")
	h.redo(newer)
	h.want("redo the newer work", "a", "import", "link", "newer")
}

func TestRewindWithNothingToUndoChangesNothing(t *testing.T) {
	h := newHistory(t)
	a := h.add("a")
	res := h.rewind(a)
	if len(res.Deactivated) != 0 || h.paths() != 1 {
		t.Fatalf("rewind to the newest change: %+v, %d timelines", res, h.paths())
	}
	h.want("nothing to rewind", "a")
}

func TestRewindSaysWhatItCouldNotUndo(t *testing.T) {
	h := newHistory(t)
	a := h.add("a")
	h.add("b")
	h.write("user", "settings.updated", "s")
	h.add("c")
	h.failing["b"] = true

	res := h.rewind(a)
	h.want("rewind", "a", "b", "s")
	notes := strings.Join(res.Notes, "\n")
	if !strings.Contains(notes, "Added b") || !strings.Contains(notes, "b is stuck") {
		t.Fatalf("notes should name the change that failed and why: %q", notes)
	}
	if !strings.Contains(notes, "Added s") {
		t.Fatalf("notes should name the change that can't be rewound: %q", notes)
	}
	var touched int
	_ = h.db.QueryRow(`SELECT touched FROM scratch WHERE id = 'b'`).Scan(&touched)
	if touched != 0 {
		t.Fatal("a change that failed to undo must leave nothing behind")
	}
}

func TestRedoSkipsWhatItCannotApply(t *testing.T) {
	h := newHistory(t)
	a := h.add("a")
	b := h.add("b")
	c := h.add("c")
	h.rewind(a)
	h.failing["b"] = true

	notes := h.redo(c, b)
	h.want("redo", "a", "c")
	if got := strings.Join(notes, "\n"); !strings.Contains(got, "Added b") || !strings.Contains(got, "b is stuck") {
		t.Fatalf("notes should name the change that failed and why: %q", got)
	}

	// Nothing at all could be redone: that's an error, and nothing changes.
	h.undo(c)
	h.failing["c"] = true
	d := h.add("d")
	if _, err := h.e.Redo([]int64{b}, h.apply); err == nil {
		t.Fatal("redo of a change that can't be applied should fail")
	}
	h.want("failed redo", "a", "d")
	_ = d
}

func TestDiscardLeavesNoDanglingCause(t *testing.T) {
	h := newHistory(t)
	imp := h.write("user", "import.confirmed", "import")
	link := h.write("system", "transfer.linked", "link")
	if err := h.w.LinkCause(imp); err != nil {
		t.Fatal(err)
	}
	// The import goes but what it caused refused to: it must not stay tied to
	// an entry that no longer exists (ids can be reused).
	h.failing["link"] = true
	if _, err := h.db.Exec(`UPDATE audit_entries SET state = 'inactive' WHERE id = ?`, imp); err != nil {
		t.Fatal(err)
	}
	if n, err := h.e.Discard([]int64{imp}); err != nil || n != 1 {
		t.Fatalf("discard removed %d (%v)", n, err)
	}
	var cause sql.NullInt64
	if err := h.db.QueryRow(`SELECT caused_by FROM audit_entries WHERE id = ?`, link).Scan(&cause); err != nil {
		t.Fatal(err)
	}
	if cause.Valid {
		t.Fatalf("caused_by = %d, want it cleared", cause.Int64)
	}
}
