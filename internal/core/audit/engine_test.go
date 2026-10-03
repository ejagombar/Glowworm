package audit

import (
	"database/sql"
	"testing"

	"glowworm/internal/store"
)

// doc 11 §8: audit engine round-trips — jump back, restore path, forget,
// delete — over fixture mutations. The apply hook mirrors the production
// executor contract (soft-delete/re-restore on revert/re-apply).
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

	// apply mirrors auditexec: revert = soft-delete the created row;
	// reapply = restore it.
	apply := func(tx *sql.Tx, ent *AuditRow, reapply bool) error {
		// Record-only markers (audit.*) have nothing to apply — same contract
		// as the production executor.
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

	// Three mutations.
	e1, e2, e3 := write("alpha"), write("beta"), write("gamma")
	if len(live()) != 3 {
		t.Fatalf("live = %v, want 3 rows", live())
	}

	// 1. Jump back to e1: beta+gamma shelved (reverted + inactive).
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

	// 2. New change on the new path, then restore the original path.
	write("delta")
	if len(live()) != 2 {
		t.Fatalf("live = %v", live())
	}
	// Entry 2 (beta) belongs to the shelved main path — restore it.
	res2, err := e.RestorePath("main", apply)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Reactivated) != 2 { // beta + gamma
		t.Fatalf("reactivated = %v", res2.Reactivated)
	}
	if len(res2.Shelved) != 2 { // jumpback marker + delta
		t.Fatalf("shelved = %v", res2.Shelved)
	}
	got = live()
	if len(got) != 3 || got[0] != "alpha" || got[1] != "beta" || got[2] != "gamma" {
		t.Fatalf("after restore live = %v", got)
	}

	// 3. Delete-with-undo one entry: effect undone + record scrubbed.
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

	// 4. Forget: remove the record, keep the effect.
	if err := e.Forget(e2); err != nil {
		t.Fatal(err)
	}
	got = live()
	if len(got) != 2 { // effect retained
		t.Fatalf("after forget live = %v", got)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_entries WHERE id = ?`, e2).Scan(&n)
	if n != 0 {
		t.Fatal("forgotten entry should be scrubbed")
	}

	// 5. Policy: import.confirmed can't be forgotten/deleted.
	pol := policy.Evaluate("import.confirmed", "active")
	if pol.Revert != true || pol.Forget != false || pol.Delete != false {
		t.Fatalf("import.confirmed policy = %+v", pol)
	}
}

func decodeJSON(s string, v any) error {
	return jsonUnmarshal(s, v)
}
