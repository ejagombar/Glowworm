package query

import (
	"context"
	gosql "database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Derived tables are tables a query can read that the app works out from the
// stored data rather than keeps: an account's balance over time takes
// valuations, opening balances and transactions together, which is hard to
// get right in one SELECT. When a query names one, it is built for that query
// alone as a TEMP table on the query's own connection, before the connection
// is made read-only, and dropped afterwards. Nothing is stored, so changing
// one needs no migration.

// derivedTable builds one derived table on conn. today is the last day it
// covers (later dated rows still count).
type derivedTable struct {
	create string
	build  func(ctx context.Context, conn *gosql.Conn, today string) error
}

var derivedTables = map[string]derivedTable{
	"account_periods": {create: accountPeriodsDDL, build: buildAccountPeriods},
}

// accountPeriodsDDL: one row per account per period (day, week, month and
// year), from the account's start to today, so a series never has gaps and
// balances add up across accounts. The identity
//
//	closing = opening + deposits - withdrawals + income - spend + growth
//
// holds for every row. growth is the change no transaction explains: what
// a recorded balance (a valuation) adds over what the transactions say, so
// interest and investment returns on a savings account.
const accountPeriodsDDL = `CREATE TEMP TABLE account_periods (
  grain TEXT NOT NULL,
  period TEXT NOT NULL,
  start_date TEXT NOT NULL,
  end_date TEXT NOT NULL,
  account_id TEXT NOT NULL,
  account TEXT NOT NULL,
  account_type TEXT NOT NULL,
  currency TEXT NOT NULL,
  opening_minor INTEGER NOT NULL,
  closing_minor INTEGER NOT NULL,
  deposits_minor INTEGER NOT NULL,
  withdrawals_minor INTEGER NOT NULL,
  income_minor INTEGER NOT NULL,
  spend_minor INTEGER NOT NULL,
  growth_minor INTEGER NOT NULL
)`

// derivedNames are the derived tables sql names, in a fixed order.
func derivedNames(sql string) []string {
	toks, _ := tokenizeSQL(sql)
	seen := map[string]bool{}
	for _, t := range toks {
		l := strings.ToLower(t)
		if _, ok := derivedTables[l]; ok {
			seen[l] = true
		}
	}
	var out []string
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// buildDerived creates the named derived tables on conn.
func buildDerived(ctx context.Context, conn *gosql.Conn, names []string, today string) error {
	for _, name := range names {
		d := derivedTables[name]
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS temp.`+name); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, d.create); err != nil {
			return err
		}
		if err := d.build(ctx, conn, today); err != nil {
			return fmt.Errorf("building %s: %w", name, err)
		}
	}
	return nil
}

func dropDerived(ctx context.Context, conn *gosql.Conn, names []string) error {
	for _, name := range names {
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS temp.`+name); err != nil {
			return err
		}
	}
	return nil
}

// dayFlows is what an account's transactions on one day did, each figure
// as the account sees it: deposits and withdrawals are transfers in and out
// (both positive), income and spend net of refunds.
type dayFlows struct {
	deposits, withdrawals, income, spend int64
}

func (f dayFlows) net() int64 { return f.deposits - f.withdrawals + f.income - f.spend }

type periodAccount struct {
	id, name, typ, currency string
	opening                 int64
	openingDate, created    string
	flows                   map[string]dayFlows
	values                  map[string]int64 // valuation per day, the latest recorded that day
	firstFlow, lastFlow     string
	firstValue, lastValue   string
}

// accountDay is one account's day: its balance before and after, and why.
type accountDay struct {
	date             string
	opening, closing int64
	flows            dayFlows
	growth           int64
}

func buildAccountPeriods(ctx context.Context, conn *gosql.Conn, today string) error {
	accts, err := loadPeriodAccounts(ctx, conn)
	if err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO temp.account_periods VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, a := range accts {
		days := a.days(today)
		for _, grain := range []string{"day", "week", "month", "year"} {
			for _, p := range rollUp(days, grain) {
				if _, err := stmt.ExecContext(ctx, grain, p.period, p.start, p.end, a.id, a.name, a.typ, a.currency,
					p.opening, p.closing, p.flows.deposits, p.flows.withdrawals, p.flows.income, p.flows.spend, p.growth); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

func loadPeriodAccounts(ctx context.Context, conn *gosql.Conn) ([]*periodAccount, error) {
	byID := map[string]*periodAccount{}
	var order []*periodAccount
	rows, err := conn.QueryContext(ctx, `SELECT id, name, type, currency, opening_balance_minor,
		COALESCE(opening_balance_date, ''), created_at FROM accounts WHERE deleted_at IS NULL ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		a := &periodAccount{flows: map[string]dayFlows{}, values: map[string]int64{}}
		if err := rows.Scan(&a.id, &a.name, &a.typ, &a.currency, &a.opening, &a.openingDate, &a.created); err != nil {
			rows.Close()
			return nil, err
		}
		byID[a.id] = a
		order = append(order, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = conn.QueryContext(ctx, `SELECT account_id, date,
		COALESCE(SUM(CASE WHEN type = 'transfer' AND amount_minor > 0 THEN amount_minor END), 0),
		COALESCE(SUM(CASE WHEN type = 'transfer' AND amount_minor < 0 THEN -amount_minor END), 0),
		COALESCE(SUM(CASE WHEN type = 'income' THEN amount_minor END), 0),
		COALESCE(SUM(CASE WHEN type NOT IN ('transfer', 'income') THEN -amount_minor END), 0)
		FROM transactions WHERE deleted_at IS NULL GROUP BY account_id, date ORDER BY account_id, date`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, date string
		var f dayFlows
		if err := rows.Scan(&id, &date, &f.deposits, &f.withdrawals, &f.income, &f.spend); err != nil {
			rows.Close()
			return nil, err
		}
		a := byID[id]
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
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Ordered so the latest recorded value of a day wins, as in balances.
	rows, err = conn.QueryContext(ctx, `SELECT account_id, date, value_minor FROM valuations
		WHERE deleted_at IS NULL ORDER BY account_id, date, created_at`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, date string
		var v int64
		if err := rows.Scan(&id, &date, &v); err != nil {
			rows.Close()
			return nil, err
		}
		a := byID[id]
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
	rows.Close()
	return order, rows.Err()
}

func validDay(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// days walks the account from its start to today (or its last dated entry,
// if later), a day at a time, by the same rules as balances.Expected: a
// valuation sets the balance at the end of its day; transactions count
// after the latest valuation before them, or with none, after the opening
// balance date.
//
// The first valuation, with no opening balance date on or before it, says
// what the account held to begin with rather than how it grew (as the
// reconcile check reads it), so the difference is moved into the opening
// balance instead of counting as growth.
func (a *periodAccount) days(today string) []accountDay {
	// Transactions before the opening balance date don't count, so they
	// don't start the account either.
	start, firsts := a.openingDate, []string{a.firstFlow, a.firstValue}
	if start != "" {
		firsts = firsts[1:]
	}
	for _, d := range firsts {
		if d != "" && (start == "" || d < start) {
			start = d
		}
	}
	if start == "" || !validDay(start) {
		if a.opening == 0 {
			return nil
		}
		start = today
		if len(a.created) >= 10 && validDay(a.created[:10]) && a.created[:10] < start {
			start = a.created[:10]
		}
	}
	end := today
	for _, d := range []string{a.lastFlow, a.lastValue} {
		if d > end {
			end = d
		}
	}
	if start > end {
		end = start
	}
	walk := func(opening int64) ([]accountDay, int64) {
		var out []accountDay
		bal := opening
		seenValue := false
		var rebase int64
		for d := start; d <= end; d = nextDay(d) {
			day := accountDay{date: d, opening: bal}
			if seenValue || a.openingDate == "" || d > a.openingDate {
				day.flows = a.flows[d]
			}
			closing := bal + day.flows.net()
			if v, ok := a.values[d]; ok {
				drift := v - closing
				if !seenValue && (a.openingDate == "" || a.openingDate > d) {
					rebase = drift
				} else {
					day.growth = drift
				}
				closing = v
				seenValue = true
			}
			day.closing = closing
			bal = closing
			out = append(out, day)
		}
		return out, rebase
	}
	out, rebase := walk(a.opening)
	if rebase != 0 {
		out, _ = walk(a.opening + rebase)
	}
	return out
}

func nextDay(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return "9999-12-31\x00" // stops the walk
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

type periodRow struct {
	period, start, end string
	opening, closing   int64
	flows              dayFlows
	growth             int64
}

// periodOf is the period a day falls in at a grain, labelled as widget
// queries label buckets: the day, the Monday a week starts on, YYYY-MM or
// YYYY. start and end are the period's first and last calendar days.
func periodOf(d, grain string) (label, start, end string) {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d, d, d
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

// rollUp groups consecutive days into periods: the first day's opening,
// the last day's closing, and everything in between added up.
func rollUp(days []accountDay, grain string) []periodRow {
	var out []periodRow
	for _, d := range days {
		label, start, end := periodOf(d.date, grain)
		if n := len(out); n == 0 || out[n-1].period != label {
			out = append(out, periodRow{period: label, start: start, end: end, opening: d.opening})
		}
		p := &out[len(out)-1]
		p.closing = d.closing
		p.flows.deposits += d.flows.deposits
		p.flows.withdrawals += d.flows.withdrawals
		p.flows.income += d.flows.income
		p.flows.spend += d.flows.spend
		p.growth += d.growth
	}
	return out
}
