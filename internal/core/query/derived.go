package query

import (
	"context"
	gosql "database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"modernc.org/sqlite/vtab"

	"glowworm/internal/core/ledger"
	"glowworm/internal/store"
)

// Derived tables are tables a query can read that the app works out from
// the stored data rather than keeps: an account's balance over time takes
// valuations, opening balances and transactions together, which is hard to
// get right in one SELECT. Each is an SQLite virtual table: when a query
// names one, the query's connection reads a ledger.Book (a compact summary
// of the accounts' history) and the table computes the rows SQLite asks
// for, taking the query's filters on grain, account and dates into account,
// so nothing is written and nothing the query doesn't need is computed.
//
// The tables live in the connection's temp schema for one query only.

// derivedTable describes one derived table: its columns, and the rows for
// a book given the filters the query pushed down.
type derivedTable struct {
	columns []string // "name TYPE"
	// filterable are the columns rows() can narrow by, by index.
	filterable map[int]bool
	rows       func(b *ledger.Book, f filters) [][]vtab.Value
}

var derivedTables = map[string]*derivedTable{
	"account_periods": accountPeriods,
	"account_status":  accountStatus,
	"goal_progress":   goalProgress,
}

// filters are the constraints SQLite passed down: col -> op -> value. SQLite
// still checks every row against the query itself, so a table may return
// more rows than match, never fewer.
type filters map[int]map[vtab.ConstraintOp]string

func (f filters) eq(col int) (string, bool) {
	v, ok := f[col][vtab.OpEQ]
	return v, ok
}

// lowest is the smallest value col may take (from >, >= and =), or "".
func (f filters) lowest(col int) string {
	lo := ""
	for op, v := range f[col] {
		if (op == vtab.OpGE || op == vtab.OpGT || op == vtab.OpEQ) && v > lo {
			lo = v
		}
	}
	return lo
}

// highest is the largest value col may take (from <, <= and =), or "".
func (f filters) highest(col int) string {
	hi := ""
	for op, v := range f[col] {
		if (op == vtab.OpLE || op == vtab.OpLT || op == vtab.OpEQ) && (hi == "" || v < hi) {
			hi = v
		}
	}
	return hi
}

// derivedNames are the derived tables sql names, sorted.
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

// books holds the book each query's tables read, by the token the tables
// are created with.
var (
	books     sync.Map
	bookToken atomic.Int64
)

func init() {
	for name, t := range derivedTables {
		if err := vtab.RegisterModule(nil, moduleName(name), &derivedModule{table: t}); err != nil {
			panic(err)
		}
	}
}

func moduleName(table string) string { return "glowworm_" + table }

// buildDerived reads the book and creates the named tables on conn,
// returning what drops them again.
func buildDerived(ctx context.Context, db *store.DB, conn *gosql.Conn, names []string, today string) (func(context.Context) error, error) {
	book, err := ledger.Cached(ctx, db.DB, conn, today)
	if err != nil {
		return nil, err
	}
	token := fmt.Sprint(bookToken.Add(1))
	books.Store(token, book)
	drop := func(ctx context.Context) error {
		defer books.Delete(token)
		for _, name := range names {
			if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS temp.`+name); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range names {
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS temp.`+name); err != nil {
			_ = drop(context.Background())
			return nil, err
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`CREATE VIRTUAL TABLE temp.%s USING %s(%s)`, name, moduleName(name), token)); err != nil {
			_ = drop(context.Background())
			return nil, err
		}
	}
	return drop, nil
}

// ---- The vtab plumbing, shared by every derived table.

type derivedModule struct{ table *derivedTable }

func (m *derivedModule) Create(ctx vtab.Context, args []string) (vtab.Table, error) {
	return m.Connect(ctx, args)
}

func (m *derivedModule) Connect(ctx vtab.Context, args []string) (vtab.Table, error) {
	if len(args) < 4 {
		return nil, fmt.Errorf("derived table: no book")
	}
	b, ok := books.Load(strings.TrimSpace(args[3]))
	if !ok {
		return nil, fmt.Errorf("derived table: book %s is gone", args[3])
	}
	if err := ctx.Declare("CREATE TABLE x(" + strings.Join(m.table.columns, ", ") + ")"); err != nil {
		return nil, err
	}
	return &derivedVTable{t: m.table, book: b.(*ledger.Book)}, nil
}

type derivedVTable struct {
	t    *derivedTable
	book *ledger.Book
}

// BestIndex takes every usable comparison on a filterable column, and
// encodes which they are in IdxStr ("col:op,col:op"), in argument order.
func (v *derivedVTable) BestIndex(info *vtab.IndexInfo) error {
	var parts []string
	n := 0
	cost := 1e6
	for i := range info.Constraints {
		c := &info.Constraints[i]
		if !c.Usable || !v.t.filterable[c.Column] {
			continue
		}
		switch c.Op {
		case vtab.OpEQ, vtab.OpGE, vtab.OpGT, vtab.OpLE, vtab.OpLT:
		default:
			continue
		}
		c.ArgIndex = n
		n++
		parts = append(parts, fmt.Sprintf("%d:%d", c.Column, c.Op))
		if c.Op == vtab.OpEQ {
			cost /= 20
		} else {
			cost /= 3
		}
	}
	info.IdxStr = strings.Join(parts, ",")
	info.EstimatedCost = cost
	info.EstimatedRows = int64(cost)
	return nil
}

func (v *derivedVTable) Open() (vtab.Cursor, error) { return &derivedCursor{v: v}, nil }
func (v *derivedVTable) Disconnect() error          { return nil }
func (v *derivedVTable) Destroy() error             { return nil }

type derivedCursor struct {
	v    *derivedVTable
	rows [][]vtab.Value
	i    int
}

func (c *derivedCursor) Filter(_ int, idxStr string, vals []vtab.Value) error {
	f := filters{}
	if idxStr != "" {
		for i, part := range strings.Split(idxStr, ",") {
			var col, op int
			if _, err := fmt.Sscanf(part, "%d:%d", &col, &op); err != nil || i >= len(vals) {
				continue
			}
			if f[col] == nil {
				f[col] = map[vtab.ConstraintOp]string{}
			}
			f[col][vtab.ConstraintOp(op)] = fmt.Sprint(vals[i])
		}
	}
	c.rows = c.v.t.rows(c.v.book, f)
	c.i = 0
	return nil
}

func (c *derivedCursor) Next() error { c.i++; return nil }
func (c *derivedCursor) Eof() bool   { return c.i >= len(c.rows) }
func (c *derivedCursor) Column(col int) (vtab.Value, error) {
	return c.rows[c.i][col], nil
}
func (c *derivedCursor) Rowid() (int64, error) { return int64(c.i + 1), nil }
func (c *derivedCursor) Close() error          { return nil }

// ---- The tables.

// account_periods: one row per account per period at each grain, from the
// account's start to today, every period present so balances add up.
// closing = opening + deposits - withdrawals + income - spend + growth.
var accountPeriods = &derivedTable{
	columns: []string{"grain TEXT", "period TEXT", "start_date TEXT", "end_date TEXT", "account_id TEXT",
		"account TEXT", "account_type TEXT", "currency TEXT", "opening_minor INTEGER", "closing_minor INTEGER",
		"deposits_minor INTEGER", "withdrawals_minor INTEGER", "income_minor INTEGER", "spend_minor INTEGER",
		"growth_minor INTEGER"},
	// grain, start_date, end_date, account_id, account, account_type
	filterable: map[int]bool{0: true, 2: true, 3: true, 4: true, 5: true, 6: true},
	rows: func(b *ledger.Book, f filters) [][]vtab.Value {
		grains := ledger.Grains
		if g, ok := f.eq(0); ok {
			grains = []string{g}
		}
		// A period overlaps from..to when it ends on or after from and
		// starts on or before to.
		from, to := max(f.lowest(3), f.lowest(2)), f.highest(2)
		if hi := f.highest(3); hi != "" && (to == "" || hi < to) {
			to = hi
		}
		var out [][]vtab.Value
		for _, a := range b.Accounts {
			if id, ok := f.eq(4); ok && id != a.ID {
				continue
			}
			if n, ok := f.eq(5); ok && n != a.Name {
				continue
			}
			if t, ok := f.eq(6); ok && t != a.Type {
				continue
			}
			for _, g := range grains {
				for _, p := range a.Periods(g, from, to, b.Today) {
					out = append(out, []vtab.Value{p.Grain, p.Label, p.Start, p.End, a.ID, a.Name, a.Type, a.Currency,
						p.Opening, p.Closing, p.Flows.Deposits, p.Flows.Withdrawals, p.Flows.Income, p.Flows.Spend, p.Growth})
				}
			}
		}
		return out
	},
}

// account_status: each account's balance now and how up to date it is.
var accountStatus = &derivedTable{
	columns: []string{"account_id TEXT", "account TEXT", "account_type TEXT", "currency TEXT", "balance_minor INTEGER",
		"last_valued TEXT", "days_since INTEGER", "reminder_days INTEGER", "due INTEGER",
		"tax_wrapper TEXT"},
	rows: func(b *ledger.Book, _ filters) [][]vtab.Value {
		var out [][]vtab.Value
		for _, s := range b.Statuses() {
			a := s.Account
			var last, since vtab.Value
			if s.LastValued != "" {
				last, since = s.LastValued, int64(s.DaysSince)
			}
			out = append(out, []vtab.Value{a.ID, a.Name, a.Type, a.Currency, s.Balance, last, since,
				int64(s.ReminderDays), boolInt(s.Due), a.TaxWrapper})
		}
		return out
	},
}

// goal_progress: each goal's progress and pace.
var goalProgress = &derivedTable{
	columns: []string{"goal_id TEXT", "goal TEXT", "color TEXT", "target_minor INTEGER", "target_date TEXT",
		"current_minor INTEGER", "remaining_minor INTEGER", "progress REAL", "pace_minor INTEGER",
		"months_left INTEGER", "monthly_needed_minor INTEGER", "projected_month TEXT", "on_track INTEGER", "accounts TEXT"},
	rows: func(b *ledger.Book, _ filters) [][]vtab.Value {
		var out [][]vtab.Value
		for _, p := range b.Progress() {
			g := p.Goal
			var date, months, needed, projected, onTrack vtab.Value
			if g.TargetDate != "" {
				date, months, needed = g.TargetDate, int64(p.MonthsLeft), p.MonthlyNeeded
			}
			if p.Projected != "" {
				projected = p.Projected
			}
			if p.OnTrack != nil {
				onTrack = boolInt(*p.OnTrack)
			}
			names := make([]string, len(p.Accounts))
			for i, a := range p.Accounts {
				names[i] = a.Name
			}
			out = append(out, []vtab.Value{g.ID, g.Name, g.Color, g.TargetMinor, date, p.Current, p.Remaining,
				p.Progress, int64(p.Pace), months, needed, projected, onTrack, strings.Join(names, ", ")})
		}
		return out
	},
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
