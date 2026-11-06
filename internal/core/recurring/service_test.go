package recurring

import (
	"fmt"
	"testing"

	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

func TestDetect(t *testing.T) {
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at)
		VALUES ('a1','Main','debit','#000','GBP','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	n := 0
	add := func(date, desc string, amount int64) string {
		n++
		id := fmt.Sprintf("t%d", n)
		must(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES (?,?,?,?,?,'GBP','expense','h','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, id, "a1", date, desc, amount)
		return id
	}
	// A gym membership: the same amount monthly.
	var gym []string
	for _, d := range []string{"2026-01-20", "2026-02-20", "2026-03-20", "2026-04-20"} {
		gym = append(gym, add(d, "Pure Gym Ltd", -2999))
	}
	// A supermarket visited every few days, three of whose small baskets
	// fall about a month apart at similar prices: not a series.
	var shop []string
	for i, d := range []string{"2026-01-07", "2026-01-10", "2026-01-15", "2026-01-20", "2026-01-26", "2026-02-02",
		"2026-02-09", "2026-02-13", "2026-02-18", "2026-02-25", "2026-03-03", "2026-03-09"} {
		amount := int64(-500 - 137*i)
		switch d {
		case "2026-01-07":
			amount = -179
		case "2026-02-02", "2026-03-09":
			amount = -165
		}
		shop = append(shop, add(d, "Lidl", amount))
	}
	// A bill whose amount wobbles by pennies is still one series.
	var bill []string
	for i, d := range []string{"2026-01-05", "2026-02-05", "2026-03-05"} {
		bill = append(bill, add(d, "Thames Water", -3200-int64(i)*20))
	}
	// Rent to a person, with "To" in front only some months.
	var rent []string
	for i, d := range []string{"2026-01-28", "2026-02-27", "2026-03-28", "2026-04-28", "2026-05-28"} {
		desc := "Jo Bloggs - rent and water"
		if i%2 == 1 {
			desc = "To Jo Bloggs - Rent and water"
		}
		rent = append(rent, add(d, desc, -101266))
	}
	// A tag left from an older detection is cleared.
	must(`UPDATE transactions SET recurring_key = 'stale' WHERE id = ?`, shop[0])

	s := &Service{DB: db, Txs: &repos.Transactions{DB: db}, Audit: &audit.Writer{DB: db}}
	got, err := s.Detect()
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Errorf("detected %d series, want 3 (gym, water and rent)", got)
	}
	tagged := func(id string) bool {
		var k *string
		if err := db.QueryRow(`SELECT recurring_key FROM transactions WHERE id = ?`, id).Scan(&k); err != nil {
			t.Fatal(err)
		}
		return k != nil
	}
	for _, id := range append(append(gym, bill...), rent...) {
		if !tagged(id) {
			t.Errorf("%s should be in a series", id)
		}
	}
	for _, id := range shop {
		if tagged(id) {
			t.Errorf("Lidl %s should not be in a series", id)
		}
	}
	// Running again finds nothing new, so writes no history entry.
	var before, after int
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_entries`).Scan(&before)
	if _, err := s.Detect(); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_entries`).Scan(&after)
	if after != before {
		t.Errorf("a repeat detection wrote %d history entries", after-before)
	}
}
