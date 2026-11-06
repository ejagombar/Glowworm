package repos

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"glowworm/internal/store"
)

func TestSearchMatchesGroupsIgnoringSpaces(t *testing.T) {
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
	for i, tx := range []struct{ desc, group string }{
		{"PureGym", "PureGym"},
		{"Gym membership DD", "PureGym"}, // in the group, description doesn't say so
		{"Pure Gym Ltd - 0241", ""},      // not in the group, spaced differently
		{"Tesco", ""},
	} {
		must(`INSERT INTO transactions (id, account_id, date, description, merchant, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES (?,'a1','2026-03-01',?,NULLIF(?, ''),-100,'GBP','expense','h','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
			fmt.Sprintf("t%d", i), tx.desc, tx.group)
	}
	r := &Transactions{DB: db}
	search := func(q string, limit int) string {
		t.Helper()
		got, err := r.SearchMatch(ListFilter{Q: q, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		var d []string
		for _, tx := range got {
			d = append(d, tx.Description)
		}
		sort.Strings(d)
		return strings.Join(d, ", ")
	}
	want := "Gym membership DD, Pure Gym Ltd - 0241, PureGym"
	for _, q := range []string{"PureGym", "pure gym", "PUREGYM"} {
		if got := search(q, 0); got != want {
			t.Errorf("search %q = %q, want %q", q, got, want)
		}
	}
	// Categories are searched too: every one here is uncategorised.
	if got := search("uncategorised", 0); strings.Count(got, ",") != 3 {
		t.Errorf("category search = %q", got)
	}
	// Account names aren't searched: the account filter does that.
	if got := search("main", 0); got != "" {
		t.Errorf("account name search = %q, want nothing", got)
	}
	// The limit counts matches, not rows read.
	if got := search("gym", 2); strings.Count(got, ",") != 1 {
		t.Errorf("limited search = %q, want 2 results", got)
	}
}
