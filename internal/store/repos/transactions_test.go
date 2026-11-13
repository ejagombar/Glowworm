package repos

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"glowworm/internal/store"
)

func TestSearchMatchesGroupsIgnoringSpaces(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/t.db")
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

// A transfer row names the account at each end, also when the other leg is
// outside the dates listed or its account has been removed, and two transfers
// on the same day keep their order from one listing to the next.
func TestDisplayRowsNameTransferEnds(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/t.db")
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
	const at = "2026-01-01T00:00:00Z"
	for _, a := range []struct{ id, name, deleted string }{{"a1", "Main", ""}, {"a2", "Savings", ""}, {"a3", "Old ISA", at}} {
		must(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at, deleted_at)
			VALUES (?,?,'debit','#000','GBP',?,?,NULLIF(?, ''))`, a.id, a.name, at, at, a.deleted)
	}
	leg := func(id, account, date string, amount int) {
		must(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES (?,?,?,'TRANSFER',?,'GBP','transfer',?,?,?)`, id, account, date, amount, id, at, at)
	}
	link := func(id, src, dst string) {
		must(`INSERT INTO transfers (id, source_transaction_id, destination_transaction_id, detected, created_at) VALUES (?,?,?,'manual',?)`, id, src, dst, at)
		must(`UPDATE transactions SET transfer_id = ? WHERE id IN (?,?)`, id, src, dst)
	}
	// Six transfers from Main to Savings on one day.
	for i := 0; i < 6; i++ {
		out, in := fmt.Sprintf("out%d", i), fmt.Sprintf("in%d", i)
		leg(out, "a1", "2026-03-01", -100-i)
		leg(in, "a2", "2026-03-01", 100+i)
		link(fmt.Sprintf("x%d", i), out, in)
	}
	// One that left in February and arrived in March.
	leg("late-out", "a1", "2026-02-27", -500)
	leg("late-in", "a2", "2026-03-02", 500)
	link("late", "late-out", "late-in")
	// One into an account that has since been removed.
	leg("isa-out", "a1", "2026-03-05", -900)
	leg("isa-in", "a3", "2026-03-05", 900)
	link("isa", "isa-out", "isa-in")
	// And one that was never matched to another account.
	must(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, destination_text, dedupe_hash, created_at, updated_at)
		VALUES ('lone','a1','2026-03-06','TO MUM',-50,'GBP','transfer','Mum','lone',?,?)`, at, at)

	r := &Transactions{DB: db}
	list := func() []string {
		t.Helper()
		rows, err := r.DisplayRows(ListFilter{From: "2026-03-01", To: "2026-03-31"}, "")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, row := range rows {
			out = append(out, fmt.Sprintf("%s %s %s: %s -> %s (%s)", row.Kind, row.Rep.ID, row.Rep.Date,
				row.Rep.SourceName, row.Rep.DestinationName, row.Rep.Counterparty))
		}
		return out
	}
	first := list()
	want := []string{
		"single lone 2026-03-06: Main -> Mum (To Mum)",
		"linked isa-out 2026-03-05: Main -> Old ISA (To Old ISA)",
		"linked late-in 2026-03-02: Main -> Savings (From Main)",
	}
	if len(first) != len(want)+6 {
		t.Fatalf("got %d rows: %q", len(first), first)
	}
	for i, w := range want {
		if first[i] != w {
			t.Errorf("row %d = %q, want %q", i, first[i], w)
		}
	}
	for _, row := range first[len(want):] {
		if !strings.HasSuffix(row, "2026-03-01: Main -> Savings (To Savings)") {
			t.Errorf("same-day transfer row = %q", row)
		}
	}
	for i := 0; i < 20; i++ {
		if again := list(); strings.Join(again, "\n") != strings.Join(first, "\n") {
			t.Fatalf("order changed between listings:\n%q\n%q", first, again)
		}
	}
}
