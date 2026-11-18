// Package ledger works out accounts' balances over time, and what follows
// from them (goals' progress, balances due an update), from the stored
// opening balances, valuations and transactions. It reads a compact
// summary (one row per account per day that had transactions, plus the
// valuations) and computes everything else on demand, so the cost follows
// the number of days with activity, not the length of history.
//
// The rules are those of balances.Expected: a valuation sets the balance at
// the end of its day; transactions count after the latest valuation before
// them or, with none, after the opening balance date.
package ledger

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Querier is what Load reads through: a *sql.DB, *sql.Conn or *sql.Tx.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// DefaultReminderDays is how old a savings account's latest balance may get
// before it's due an update, when the settings don't say.
const DefaultReminderDays = 30

// SettingReminderDays is the settings key for that default (0 turns the
// default off).
const SettingReminderDays = "reminder_default_days"

// Book is everything Load read, as of Today.
type Book struct {
	Today        string
	ReminderDays int
	Accounts     []*Account
	Goals        []*Goal
	byID         map[string]*Account
}

// Account is one account and its history.
type Account struct {
	ID, Name, Type, Currency, TaxWrapper string
	InterestRate                         *float64
	ReminderDays                         *int64
	Opening                              int64
	OpeningDate, Created                 string

	flows                 map[string]Flows
	values                map[string]int64
	firstFlow, lastFlow   string
	firstValue, lastValue string

	days []Day // computed once, on first use
	done bool
}

// Flows is what an account's transactions did over a span, each figure as
// the account sees it: deposits and withdrawals are transfers in and out
// (both positive); income and spend are net of refunds.
type Flows struct {
	Deposits, Withdrawals, Income, Spend int64
}

// Net is the flows' effect on the balance.
func (f Flows) Net() int64 { return f.Deposits - f.Withdrawals + f.Income - f.Spend }

func (f *Flows) add(o Flows) {
	f.Deposits += o.Deposits
	f.Withdrawals += o.Withdrawals
	f.Income += o.Income
	f.Spend += o.Spend
}

// Day is a day on which something happened to an account: the balance
// before and after, and why. Growth is what a recorded balance added over
// what the transactions say (interest, returns).
type Day struct {
	Date             string
	Opening, Closing int64
	Flows            Flows
	Growth           int64
}

// Period is an account over one period of a grain. Closing = Opening +
// Flows.Net() + Growth.
type Period struct {
	Grain, Label, Start, End string
	Opening, Closing         int64
	Flows                    Flows
	Growth                   int64
}

// Goal is a target over some accounts (all savings accounts when it names
// none).
type Goal struct {
	ID, Name, Color, TargetDate string
	TargetMinor                 int64
	AccountIDs                  []string
}

// Load reads the book as of today.
func Load(ctx context.Context, q Querier, today string) (*Book, error) {
	b := &Book{Today: today, ReminderDays: DefaultReminderDays, byID: map[string]*Account{}}
	if err := b.loadAccounts(ctx, q); err != nil {
		return nil, err
	}
	if err := b.loadFlows(ctx, q); err != nil {
		return nil, err
	}
	if err := b.loadValues(ctx, q); err != nil {
		return nil, err
	}
	if err := b.loadGoals(ctx, q); err != nil {
		return nil, err
	}
	if err := b.loadSettings(ctx, q); err != nil {
		return nil, err
	}
	// Worked out now, so the book is read-only from here and can be shared.
	for _, a := range b.Accounts {
		a.Days()
	}
	return b, nil
}

func (b *Book) loadSettings(ctx context.Context, q Querier) error {
	rows, err := q.QueryContext(ctx, `SELECT value FROM settings WHERE key = ?`, SettingReminderDays)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			if n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(v), `"`)); err == nil && n >= 0 {
				b.ReminderDays = n
			}
		}
	}
	return rows.Err()
}

func (b *Book) loadAccounts(ctx context.Context, q Querier) error {
	rows, err := q.QueryContext(ctx, `SELECT id, name, type, currency, opening_balance_minor,
		COALESCE(opening_balance_date, ''), created_at, interest_rate, tax_wrapper, balance_reminder_days
		FROM accounts WHERE deleted_at IS NULL ORDER BY name, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		a := &Account{flows: map[string]Flows{}, values: map[string]int64{}}
		var rate sql.NullFloat64
		var remind sql.NullInt64
		if err := rows.Scan(&a.ID, &a.Name, &a.Type, &a.Currency, &a.Opening, &a.OpeningDate, &a.Created,
			&rate, &a.TaxWrapper, &remind); err != nil {
			return err
		}
		if rate.Valid {
			a.InterestRate = &rate.Float64
		}
		if remind.Valid {
			a.ReminderDays = &remind.Int64
		}
		b.Accounts = append(b.Accounts, a)
		b.byID[a.ID] = a
	}
	return rows.Err()
}

func (b *Book) loadFlows(ctx context.Context, q Querier) error {
	rows, err := q.QueryContext(ctx, `SELECT account_id, date,
		COALESCE(SUM(CASE WHEN type = 'transfer' AND amount_minor > 0 THEN amount_minor END), 0),
		COALESCE(SUM(CASE WHEN type = 'transfer' AND amount_minor < 0 THEN -amount_minor END), 0),
		COALESCE(SUM(CASE WHEN type = 'income' THEN amount_minor END), 0),
		COALESCE(SUM(CASE WHEN type NOT IN ('transfer', 'income') THEN -amount_minor END), 0)
		FROM transactions WHERE deleted_at IS NULL GROUP BY account_id, date`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, date string
		var f Flows
		if err := rows.Scan(&id, &date, &f.Deposits, &f.Withdrawals, &f.Income, &f.Spend); err != nil {
			return err
		}
		a := b.byID[id]
		if a == nil || !validDay(date) {
			continue
		}
		a.flows[date] = f
		if a.firstFlow == "" || date < a.firstFlow {
			a.firstFlow = date
		}
		if date > a.lastFlow {
			a.lastFlow = date
		}
	}
	return rows.Err()
}

func (b *Book) loadValues(ctx context.Context, q Querier) error {
	// Ordered so the latest recorded value of a day wins, as in balances.
	rows, err := q.QueryContext(ctx, `SELECT account_id, date, value_minor FROM valuations
		WHERE deleted_at IS NULL ORDER BY account_id, date, created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, date string
		var v int64
		if err := rows.Scan(&id, &date, &v); err != nil {
			return err
		}
		a := b.byID[id]
		if a == nil || !validDay(date) {
			continue
		}
		a.values[date] = v
		if a.firstValue == "" || date < a.firstValue {
			a.firstValue = date
		}
		if date > a.lastValue {
			a.lastValue = date
		}
	}
	return rows.Err()
}

func (b *Book) loadGoals(ctx context.Context, q Querier) error {
	rows, err := q.QueryContext(ctx, `SELECT g.id, g.name, g.color, COALESCE(g.target_date, ''), g.target_minor,
		COALESCE((SELECT group_concat(ga.account_id) FROM goal_accounts ga
			JOIN accounts a ON a.id = ga.account_id AND a.deleted_at IS NULL WHERE ga.goal_id = g.id), '')
		FROM goals g WHERE g.deleted_at IS NULL ORDER BY g.created_at, g.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		g := &Goal{}
		var ids string
		if err := rows.Scan(&g.ID, &g.Name, &g.Color, &g.TargetDate, &g.TargetMinor, &ids); err != nil {
			return err
		}
		if ids != "" {
			g.AccountIDs = strings.Split(ids, ",")
			sort.Strings(g.AccountIDs)
		}
		b.Goals = append(b.Goals, g)
	}
	return rows.Err()
}

// Account returns the account with id, or nil.
func (b *Book) Account(id string) *Account { return b.byID[id] }

func validDay(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// Start is the account's first day: its opening balance date, or its first
// counted transaction or valuation; "" for an account with nothing at all.
func (a *Account) Start() string {
	// Transactions before the opening balance date don't count, so they
	// don't start the account either.
	start, firsts := a.OpeningDate, []string{a.firstFlow, a.firstValue}
	if start != "" {
		firsts = firsts[1:]
	}
	for _, d := range firsts {
		if d != "" && (start == "" || d < start) {
			start = d
		}
	}
	if start != "" && !validDay(start) {
		start = ""
	}
	if start == "" && a.Opening != 0 && len(a.Created) >= 10 && validDay(a.Created[:10]) {
		start = a.Created[:10]
	}
	return start
}

// LastValued is the date of the account's latest recorded balance, or "".
func (a *Account) LastValued() string { return a.lastValue }

// Days are the account's days with activity, oldest first. The first
// valuation, with no opening balance date on or before it, says what the
// account held to begin with rather than how it grew (as the reconcile
// check reads it), so the difference moves into the opening balance.
func (a *Account) Days() []Day {
	if a.done {
		return a.days
	}
	a.done = true
	if a.Start() == "" {
		return nil
	}
	dates := map[string]bool{}
	for d := range a.flows {
		dates[d] = true
	}
	for d := range a.values {
		dates[d] = true
	}
	var order []string
	for d := range dates {
		order = append(order, d)
	}
	sort.Strings(order)
	walk := func(opening int64) ([]Day, int64) {
		var out []Day
		bal := opening
		seenValue := false
		var rebase int64
		for _, d := range order {
			day := Day{Date: d, Opening: bal}
			if seenValue || a.OpeningDate == "" || d > a.OpeningDate {
				day.Flows = a.flows[d]
			}
			closing := bal + day.Flows.Net()
			if v, ok := a.values[d]; ok {
				drift := v - closing
				if !seenValue && (a.OpeningDate == "" || a.OpeningDate > d) {
					rebase = drift
				} else {
					day.Growth = drift
				}
				closing = v
				seenValue = true
			}
			day.Closing = closing
			bal = closing
			out = append(out, day)
		}
		return out, rebase
	}
	days, rebase := walk(a.Opening)
	if rebase != 0 {
		days, _ = walk(a.Opening + rebase)
	}
	// From here on Opening is the effective starting balance.
	a.Opening += rebase
	a.days = days
	return days
}

// BalanceOn is the account's balance at the end of date.
func (a *Account) BalanceOn(date string) int64 {
	days := a.Days()
	i := sort.Search(len(days), func(i int) bool { return days[i].Date > date })
	if i == 0 {
		return a.Opening
	}
	return days[i-1].Closing
}

// End is the last day a series of the account runs to: today, or its last
// dated entry when that's later.
func (a *Account) End(today string) string {
	end := today
	for _, d := range []string{a.lastFlow, a.lastValue} {
		if d > end {
			end = d
		}
	}
	return end
}

// Periods are the account's periods at grain (day, week, month or year)
// from its start to its end (see End), keeping only those that overlap
// from..to ("" for open ends). Every period is there, active or not, so
// balances add up across accounts.
func (a *Account) Periods(grain, from, to, today string) []Period {
	start := a.Start()
	if start == "" {
		return nil
	}
	end := a.End(today)
	if start > end {
		end = start
	}
	days := a.Days()
	var out []Period
	label, pStart, pEnd := PeriodOf(start, grain)
	bal := a.Opening
	di := 0
	if from != "" && from > pEnd {
		// Jump to the period from falls in, carrying the balance over.
		label, pStart, pEnd = PeriodOf(from, grain)
		di = sort.Search(len(days), func(i int) bool { return days[i].Date >= pStart })
		if di > 0 {
			bal = days[di-1].Closing
		}
	}
	for pStart <= end {
		p := Period{Grain: grain, Label: label, Start: pStart, End: pEnd, Opening: bal}
		for di < len(days) && days[di].Date <= pEnd {
			d := days[di]
			p.Flows.add(d.Flows)
			p.Growth += d.Growth
			bal = d.Closing
			di++
		}
		p.Closing = bal
		if (from == "" || p.End >= from) && (to == "" || p.Start <= to) {
			out = append(out, p)
		}
		if to != "" && p.Start > to {
			break
		}
		next := nextDay(pEnd)
		label, pStart, pEnd = PeriodOf(next, grain)
	}
	return out
}

// Grains are the period sizes Periods takes.
var Grains = []string{"day", "week", "month", "year"}

// PeriodOf is the period a day falls in at a grain, labelled as widget
// queries label buckets: the day, the Monday a week starts on, YYYY-MM or
// YYYY. start and end are its first and last calendar days.
func PeriodOf(d, grain string) (label, start, end string) {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d, d, "9999-12-31"
	}
	switch grain {
	case "week":
		s := t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
		return s.Format("2006-01-02"), s.Format("2006-01-02"), s.AddDate(0, 0, 6).Format("2006-01-02")
	case "month":
		s := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		return s.Format("2006-01"), s.Format("2006-01-02"), s.AddDate(0, 1, -1).Format("2006-01-02")
	case "year":
		return t.Format("2006"), t.Format("2006") + "-01-01", t.Format("2006") + "-12-31"
	}
	return d, d, d
}

func nextDay(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return "9999-12-31\x00"
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

func addMonths(d string, n int) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.AddDate(0, n, 0).Format("2006-01-02")
}

// daysBetween is whole days from a to b.
func daysBetween(a, b string) int {
	ta, err1 := time.Parse("2006-01-02", a)
	tb, err2 := time.Parse("2006-01-02", b)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(math.Round(tb.Sub(ta).Hours() / 24))
}
