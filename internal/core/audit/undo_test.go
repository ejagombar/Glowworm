package audit

import (
	"database/sql"
	"testing"

	"glowworm/internal/store"
)

func TestUndoRedoDiscardTakeDependantsAlong(t *testing.T) {
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	w := &Writer{DB: db}
	e := NewEngine(db, w)
	var applied []string
	apply := func(tx *sql.Tx, ent *AuditRow, reapply bool) error {
		dir := "undo "
		if reapply {
			dir = "redo "
		}
		applied = append(applied, dir+ent.Action)
		return nil
	}
	write := func(actor, action string) int64 {
		var id int64
		if err := db.WithTx(func(tx *sql.Tx) error {
			var err error
			id, err = w.Write(tx, Input{Actor: actor, Action: action, EntityType: "x", EntityID: "x", Title: action})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	state := func(id int64) string {
		var s string
		if err := db.QueryRow(`SELECT state FROM audit_entries WHERE id = ?`, id).Scan(&s); err != nil {
			return "gone"
		}
		return s
	}

	imp := write("user", "import.confirmed")
	retrain := write("system", "classifier.retrained")
	if err := w.LinkCause(imp); err != nil {
		t.Fatal(err)
	}
	later := write("user", "category.created")

	if err := e.Undo(retrain, apply); err == nil {
		t.Fatal("a derived entry must not be undoable on its own")
	}
	if err := e.Undo(imp, apply); err != nil {
		t.Fatal(err)
	}
	if state(imp) != "inactive" || state(retrain) != "inactive" || state(later) != "active" {
		t.Fatalf("undo: import=%s retrain=%s later=%s", state(imp), state(retrain), state(later))
	}
	if len(applied) != 2 || applied[0] != "undo classifier.retrained" || applied[1] != "undo import.confirmed" {
		t.Fatalf("dependants come off first: %v", applied)
	}

	applied = nil
	if err := e.Redo([]int64{imp}, apply); err != nil {
		t.Fatal(err)
	}
	if state(imp) != "active" || state(retrain) != "active" {
		t.Fatalf("redo: import=%s retrain=%s", state(imp), state(retrain))
	}
	if len(applied) != 2 || applied[0] != "redo import.confirmed" {
		t.Fatalf("redo order: %v", applied)
	}

	if err := e.Undo(imp, apply); err != nil {
		t.Fatal(err)
	}
	if n, err := e.Discard([]int64{imp}); err != nil || n != 2 {
		t.Fatalf("discard removed %d (%v), want the import and its retrain", n, err)
	}
	if state(imp) != "gone" || state(retrain) != "gone" || state(later) != "active" {
		t.Fatalf("discard: import=%s retrain=%s later=%s", state(imp), state(retrain), state(later))
	}
	if n, _ := e.Discard([]int64{later}); n != 0 {
		t.Fatal("discard must leave active entries alone")
	}
}

func TestRewindDeactivatesDerivedEntries(t *testing.T) {
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	w := &Writer{DB: db}
	e := NewEngine(db, w)
	write := func(actor, action string) int64 {
		var id int64
		_ = db.WithTx(func(tx *sql.Tx) error {
			var err error
			id, err = w.Write(tx, Input{Actor: actor, Action: action, EntityType: "x", EntityID: "x", Title: action})
			return err
		})
		return id
	}
	first := write("user", "category.created")
	write("user", "import.confirmed")
	write("system", "recurring.detected")
	res, err := e.JumpBack(first, func(*sql.Tx, *AuditRow, bool) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deactivated) != 2 || len(res.Failed) != 0 {
		t.Fatalf("rewind should undo the import and its derived entry: %+v", res)
	}
}
