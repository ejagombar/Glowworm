package query

import (
	"reflect"
	"strings"
	"testing"

	"glowworm/internal/core/balances"
)

func seedPeriodsData(t *testing.T) *Service {
	t.Helper()
	db := newAgentTestDB(t)
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	ts := "2024-01-01T00:00:00Z"
	must(`INSERT INTO accounts (id, name, type, color, currency, opening_balance_minor, opening_balance_date, created_at, updated_at) VALUES
		('cur','Current','debit','#000','GBP',50000,'2024-01-01',?,?),
		('sav','ISA','savings','#000','GBP',0,NULL,?,?)`, ts, ts, ts, ts)
	tx := func(id, acct, date, typ string, amount int64) {
		must(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES (?,?,?,?,?,'GBP',?,?,?,?)`, id, acct, date, id, amount, typ, id, ts, ts)
	}
	val := func(id, acct, date string, v int64, created string) {
		must(`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at)
			VALUES (?,?,?,?,'manual',?,?)`, id, acct, v, date, created, created)
	}
	// Before the opening balance date: not counted.
	tx("old", "cur", "2023-12-20", "expense", -999)
	tx("pay", "cur", "2024-01-25", "income", 200000)
	tx("shop", "cur", "2024-02-02", "expense", -3000)
	tx("out", "cur", "2024-02-05", "transfer", -20000)
	tx("in", "sav", "2024-02-05", "transfer", 20000)
	// The first value is where the ISA started; a later one shows interest.
	val("v1", "sav", "2024-01-10", 100000, ts)
	val("v2", "sav", "2024-03-01", 120500, ts)
	val("v3", "sav", "2024-03-01", 121000, "2024-03-02T00:00:00Z") // same day, later: wins
	return &Service{DB: db, Clock: periodsClock{}}
}

type periodsClock struct{}

func (periodsClock) Today() string { return "2024-03-15" }

func TestAccountPeriodsMonthly(t *testing.T) {
	svc := seedPeriodsData(t)
	res, err := svc.runQuery(`SELECT account, period, opening_minor, closing_minor, deposits_minor,
		withdrawals_minor, income_minor, spend_minor, growth_minor
		FROM account_periods WHERE grain = 'month' ORDER BY account, period`)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range res.Rows {
		got = append(got, strings.Join(cellsText(r), " "))
	}
	want := []string{
		"Current 2024-01 50000 250000 0 0 200000 0 0",
		"Current 2024-02 250000 227000 0 20000 0 3000 0",
		"Current 2024-03 227000 227000 0 0 0 0 0",
		"ISA 2024-01 100000 100000 0 0 0 0 0",
		"ISA 2024-02 100000 120000 20000 0 0 0 0",
		"ISA 2024-03 120000 121000 0 0 0 0 1000",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Every day's closing balance is what balances.Account says, and the rows
// add up.
func TestAccountPeriodsMatchBalances(t *testing.T) {
	svc := seedPeriodsData(t)
	res, err := svc.runQuery(`SELECT account_id, period, closing_minor,
		opening_minor + deposits_minor - withdrawals_minor + income_minor - spend_minor + growth_minor
		FROM account_periods WHERE grain = 'day' AND period >= '2024-01-10'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) == 0 {
		t.Fatal("no rows")
	}
	for _, r := range res.Rows {
		id, day := cellText(r[0]), cellText(r[1])
		closing, _ := cellNumber(r[2])
		sum, _ := cellNumber(r[3])
		if closing != sum {
			t.Errorf("%s %s: closing %v but parts add to %v", id, day, closing, sum)
		}
		want, err := balances.Account(svc.DB.DB, id, day)
		if err != nil {
			t.Fatal(err)
		}
		if int64(closing) != want {
			t.Errorf("%s %s: closing %v, balances says %d", id, day, closing, want)
		}
	}
}

func TestAccountPeriodsGrains(t *testing.T) {
	svc := seedPeriodsData(t)
	res, err := svc.runQuery(`SELECT grain, MIN(period), MAX(period), COUNT(*) FROM account_periods
		WHERE account_id = 'sav' GROUP BY grain ORDER BY grain`)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range res.Rows {
		got = append(got, strings.Join(cellsText(r), " "))
	}
	want := "day 2024-01-10 2024-03-15 66\nmonth 2024-01 2024-03 3\nweek 2024-01-08 2024-03-11 10\nyear 2024 2024 1"
	if strings.Join(got, "\n") != want {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

// The derived table lives only for its query: the pooled connection is
// left without it, and the query itself still can't write.
func TestAccountPeriodsIsTemporary(t *testing.T) {
	svc := seedPeriodsData(t)
	svc.DB.SetMaxOpenConns(1)
	if _, err := svc.runQuery(`SELECT COUNT(*) FROM account_periods`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := svc.DB.QueryRow(`SELECT COUNT(*) FROM temp.sqlite_master WHERE name = 'account_periods'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("account_periods outlived its query")
	}
	if _, err := svc.DB.Exec(`CREATE TABLE scratch (x)`); err != nil {
		t.Fatalf("connection left read-only: %v", err)
	}
}

func cellsText(r []any) []string {
	out := make([]string, len(r))
	for i, c := range r {
		out[i] = cellText(c)
	}
	return out
}

func (s *Service) runQuery(sql string) (*QueryResult, error) {
	return runReadonlyQuery(s.DB, s.today(), sql)
}

// Filters SQLite pushes into account_periods narrow what it computes, never
// what the query gets: a filtered query matches the same rows picked out of
// the whole table.
func TestAccountPeriodsPushdownMatches(t *testing.T) {
	svc := seedPeriodsData(t)
	where := []string{
		"grain = 'month'",
		"grain = 'week' AND account_id = 'sav'",
		"grain = 'day' AND end_date >= '2024-02-01' AND start_date <= '2024-02-10'",
		"grain = 'month' AND start_date > '2024-01-15' AND end_date < '2024-03-31'",
		"account_type = 'savings' AND grain = 'year'",
		"account = 'Current' AND grain = 'day' AND start_date BETWEEN '2024-01-20' AND '2024-01-30'",
	}
	for _, w := range where {
		direct, err := svc.runQuery(`SELECT * FROM account_periods WHERE ` + w + ` ORDER BY 1,2,5`)
		if err != nil {
			t.Fatal(err)
		}
		// Wrapping in a subquery with OFFSET 0 stops SQLite pushing the
		// filter down.
		whole, err := svc.runQuery(`SELECT * FROM (SELECT * FROM account_periods LIMIT -1 OFFSET 0) WHERE ` + w + ` ORDER BY 1,2,5`)
		if err != nil {
			t.Fatal(err)
		}
		if len(direct.Rows) == 0 || !reflect.DeepEqual(direct.Rows, whole.Rows) {
			t.Errorf("%s: %d rows filtered, %d picked from the whole table", w, len(direct.Rows), len(whole.Rows))
		}
	}
}

func TestAccountStatusAndGoals(t *testing.T) {
	svc := seedPeriodsData(t)
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := svc.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	ts := "2024-01-01T00:00:00Z"
	must(`UPDATE accounts SET tax_wrapper = 'isa' WHERE id = 'sav'`)
	must(`INSERT INTO goals (id, name, target_minor, target_date, created_at, updated_at) VALUES
		('g1', 'House', 200000, '2025-12-31', ?, ?), ('g2', 'Rainy day', 100000, NULL, ?, ?)`, ts, ts, ts, ts)
	must(`INSERT INTO goal_accounts (goal_id, account_id) VALUES ('g1', 'sav'), ('g1', 'cur')`)

	res, err := svc.runQuery(`SELECT account, balance_minor, last_valued, days_since, reminder_days, due, tax_wrapper
		FROM account_status ORDER BY account`)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range res.Rows {
		got = append(got, strings.Join(cellsText(r), " "))
	}
	// Today is 2024-03-15: the ISA was valued 14 days ago, under the
	// 30-day default; the current account has no reminder by default.
	want := "Current 227000   0 0 \nISA 121000 2024-03-01 14 30 0 isa"
	if strings.Join(got, "\n") != want {
		t.Fatalf("status got\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
	must(`INSERT INTO settings (key, value, updated_at) VALUES ('reminder_default_days', '7', ?)`, ts)
	res, _ = svc.runQuery(`SELECT due FROM account_status WHERE account = 'ISA'`)
	if cellText(res.Rows[0][0]) != "1" {
		t.Error("a 7-day default doesn't make a 14-day-old balance due")
	}

	res, err = svc.runQuery(`SELECT goal, current_minor, remaining_minor, round(progress, 4), months_left, monthly_needed_minor,
		projected_month, on_track, accounts FROM goal_progress ORDER BY goal`)
	if err != nil {
		t.Fatal(err)
	}
	got = got[:0]
	for _, r := range res.Rows {
		got = append(got, strings.Join(cellsText(r), " "))
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "House 348000 0 1.74 22 0 2024-03 1 Current, ISA") ||
		!strings.HasPrefix(got[1], "Rainy day 121000 0 1.21") {
		t.Fatalf("goals:\n%s", strings.Join(got, "\n"))
	}
}

// The book behind the derived tables is kept between queries, and any
// change to what it's worked out from shows in the next one.
func TestDerivedTablesSeeChanges(t *testing.T) {
	svc := seedPeriodsData(t)
	balance := func() string {
		t.Helper()
		res, err := svc.runQuery(`SELECT balance_minor FROM account_status WHERE account_id = 'sav'`)
		if err != nil {
			t.Fatal(err)
		}
		return cellText(res.Rows[0][0])
	}
	if got := balance(); got != "121000" {
		t.Fatalf("got %s", got)
	}
	steps := []struct{ sql, want string }{
		{`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at)
			VALUES ('v9', 'sav', 130000, '2024-03-10', 'manual', '2024-03-10T00:00:00Z', '2024-03-10T00:00:00Z')`, "130000"},
		{`UPDATE valuations SET deleted_at = '2024-03-11' WHERE id = 'v9'`, "121000"},
		{`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
			VALUES ('in2', 'sav', '2024-03-12', 'in2', 500, 'GBP', 'transfer', 'in2', 'x', 'x')`, "121500"},
		{`DELETE FROM transactions WHERE id = 'in2'`, "121000"},
		{`UPDATE accounts SET opening_balance_minor = 7 WHERE id = 'sav'`, "121000"},
	}
	for _, s := range steps {
		if _, err := svc.DB.Exec(s.sql); err != nil {
			t.Fatal(err)
		}
		if got := balance(); got != s.want {
			t.Errorf("after %.40s…: %s, want %s", s.sql, got, s.want)
		}
	}
}
