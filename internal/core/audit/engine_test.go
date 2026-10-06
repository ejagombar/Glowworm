package audit

import (
	"database/sql"
	"testing"

	"glowworm/internal/store"
)

func TestHistoryEngineRoundTrip(t *testing.T) {
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE scratch (id TEXT PRIMARY KEY, deleted INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	w := &Writer{DB: db}
	e := NewEngine(db, w)
	policy := &Policy{}

	apply := func(tx *sql.Tx, ent *AuditRow, reapply bool) error {
		if len(ent.Action) > 6 && ent.Action[:6] == "audit." {
			return nil
		}
		var revert map[string]any
		if err := decodeJSON(ent.Revert, &revert); err != nil {
			return err
		}
		id := revert["transaction_id"].(string)
		var stmt string
		if reapply {
			stmt = `UPDATE scratch SET deleted = 0 WHERE id = ?`
		} else {
			stmt = `UPDATE scratch SET deleted = 1 WHERE id = ?`
		}
		_, err := tx.Exec(stmt, id)
		return err
	}

	write := func(name string) int64 {
		var entryID int64
		err := db.WithTx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO scratch (id) VALUES (?)`, name); err != nil {
				return err
			}
			var e2 error
			entryID, e2 = w.Write(tx, Input{
				Actor: "user", Action: "transaction.created",
				EntityType: "transaction", EntityID: name,
				Title:  "Added " + name,
				Revert: map[string]any{"transaction_id": name},
			})
			return e2
		})
		if err != nil {
			t.Fatal(err)
		}
		return entryID
	}
	live := func() []string {
		rows, err := db.Query(`SELECT id FROM scratch WHERE deleted = 0 ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}

	e1, e2, e3 := write("alpha"), write("beta"), write("gamma")
	if len(live()) != 3 {
		t.Fatalf("live = %v, want 3 rows", live())
	}

	res, err := e.JumpBack(e1, apply)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deactivated) != 2 {
		t.Fatalf("deactivated = %v", res.Deactivated)
	}
	got := live()
	if len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("after jump-back live = %v", got)
	}

	write("delta")
	if len(live()) != 2 {
		t.Fatalf("live = %v", live())
	}
	res2, err := e.RestorePath("main", apply)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Reactivated) != 2 {
		t.Fatalf("reactivated = %v", res2.Reactivated)
	}
	if len(res2.Shelved) != 1 { // just "delta": a jump-back writes no entry of its own
		t.Fatalf("shelved = %v", res2.Shelved)
	}
	got = live()
	if len(got) != 3 || got[0] != "alpha" || got[1] != "beta" || got[2] != "gamma" {
		t.Fatalf("after restore live = %v", got)
	}

	caps, err := e.Capabilities(e3)
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Delete {
		t.Fatalf("delete should be allowed: %+v", caps)
	}
	if err := e.Delete(e3, apply); err != nil {
		t.Fatal(err)
	}
	got = live()
	if len(got) != 2 || got[1] != "beta" {
		t.Fatalf("after delete live = %v", got)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_entries WHERE id = ?`, e3).Scan(&n)
	if n != 0 {
		t.Fatal("deleted entry should be scrubbed")
	}

	if err := e.Forget(e2); err != nil {
		t.Fatal(err)
	}
	got = live()
	if len(got) != 2 {
		t.Fatalf("after forget live = %v", got)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_entries WHERE id = ?`, e2).Scan(&n)
	if n != 0 {
		t.Fatal("forgotten entry should be scrubbed")
	}

	pol := policy.Evaluate("import.confirmed", "active")
	if pol.Revert != true || pol.Forget != false || pol.Delete != false {
		t.Fatalf("import.confirmed policy = %+v", pol)
	}
}

func decodeJSON(s string, v any) error {
	return jsonUnmarshal(s, v)
}
