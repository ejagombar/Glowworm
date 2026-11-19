package query

import (
	"context"
	gosql "database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ejagombar/Glowworm/internal/store"
)

const (
	// MaxRows caps what a query run for the AI returns: its rows go back
	// into the model's context. A widget's own query is drawn, not read,
	// so it may return up to WidgetMaxRows (a year of daily balances for
	// several accounts, say) before being cut short.
	MaxRows       = 200
	WidgetMaxRows = 10000
	MaxCellLen    = 200
	QueryTimeout  = 10 * time.Second
	MaxSQLLen     = 4000
)

type QueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"row_count"`
	Truncated bool     `json:"truncated"`
}

var forbiddenTokens = map[string]bool{
	"insert": true, "update": true, "delete": true, "drop": true,
	"alter": true, "create": true, "attach": true, "detach": true,
	"pragma": true, "vacuum": true, "reindex": true, "analyze": true,
	"savepoint": true, "release": true, "begin": true, "commit": true,
	"rollback": true, "explain": true, "load_extension": true,
	"readfile": true, "writefile": true, "into": true,
}

// privateTables hold credentials and AI logs. Query results go to the AI
// provider, and a description in an imported statement could try to talk
// the model into reading them, so queries can't name them at all.
var privateTables = map[string]bool{
	"users": true, "ai_provider_config": true, "ai_provider_keys": true, "ai_calls": true,
	// A push endpoint lets whoever holds it send notifications to that device.
	"push_subscriptions": true,
}

func validateReadonly(sql string) error {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return fmt.Errorf("empty query")
	}
	if len(sql) > MaxSQLLen {
		return fmt.Errorf("query too long (max %d chars)", MaxSQLLen)
	}
	toks, ok := tokenizeSQL(sql)
	if !ok {
		return fmt.Errorf("only one statement is allowed (remove everything after the ';')")
	}
	if len(toks) == 0 {
		return fmt.Errorf("no SQL found (only comments or strings)")
	}
	first := strings.ToLower(toks[0])
	if first != "select" && first != "with" {
		return fmt.Errorf("only SELECT queries are allowed")
	}
	for _, t := range toks {
		l := strings.ToLower(t)
		if forbiddenTokens[l] {
			return fmt.Errorf("forbidden keyword %q in query", strings.ToUpper(l))
		}
		if privateTables[l] {
			return fmt.Errorf("the %s table isn't available to queries", l)
		}
	}
	return nil
}

// tokenizeSQL returns the words of sql: keywords and names, including names
// written in double quotes or backticks, with each string literal as one
// placeholder. ok is false when something follows a ';'.
func tokenizeSQL(sql string) (toks []string, ok bool) {
	i := 0
	n := len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			q := c
			i++
			start := i
			for i < n {
				if sql[i] == q {
					if i+1 < n && sql[i+1] == q { // escaped quote ('')
						i += 2
						continue
					}
					break
				}
				i++
			}
			if q == '\'' {
				toks = append(toks, "\x00lit")
			} else {
				// A quoted name: "users" is still the users table.
				toks = append(toks, strings.ReplaceAll(sql[start:min(i, n)], string(q)+string(q), string(q)))
			}
			i++
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			i += 2
			for i+1 < n && (sql[i] != '*' || sql[i+1] != '/') {
				i++
			}
			i += 2
		case c == ';':
			rest := strings.TrimSpace(sql[i+1:])
			if rest != "" {
				return nil, false
			}
			i = n
		case isIdentChar(c):
			j := i
			for j < n && isIdentChar(sql[j]) {
				j++
			}
			toks = append(toks, sql[i:j])
			i = j
		default:
			i++
		}
	}
	return toks, true
}

func isIdentChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func truncateCell(s string) string {
	if len(s) > MaxCellLen {
		return s[:MaxCellLen] + "…"
	}
	return s
}

// Wrapping as SELECT * FROM (<query>) LIMIT n caps rows regardless of any
// inner LIMIT; SQLite accepts WITH inside the subquery. args bind the query's
// parameters, such as a widget's :from and :to.
func RunReadonlyQuery(db *store.DB, sql string, args ...any) (res *QueryResult, err error) {
	return runReadonlyQuery(db, time.Now().Format("2006-01-02"), sql, args...)
}

// runReadonlyQuery is RunReadonlyQuery with derived tables (see derived.go)
// built up to today.
func runReadonlyQuery(db *store.DB, today, sql string, args ...any) (res *QueryResult, err error) {
	return runReadonlyQueryMax(db, today, sql, MaxRows, args...)
}

// runReadonlyQueryMax is runReadonlyQuery returning up to max rows.
func runReadonlyQueryMax(db *store.DB, today, sql string, max int, args ...any) (res *QueryResult, err error) {
	err = withReadonlyRows(db, today, sql, max+1, args, func(rows *gosql.Rows) error {
		res, err = scanResult(rows, max)
		return err
	})
	return res, err
}

// WithReadonlyRows checks sql is a single SELECT, then runs it, capped at
// limit rows, on a connection that SQLite itself keeps read-only for the
// duration, so a query that slipped past the check still can't write.
func WithReadonlyRows(db *store.DB, sql string, limit int, args []any, fn func(*gosql.Rows) error) error {
	return withReadonlyRows(db, time.Now().Format("2006-01-02"), sql, limit, args, fn)
}

func withReadonlyRows(db *store.DB, today, sql string, limit int, args []any, fn func(*gosql.Rows) error) error {
	if err := validateReadonly(sql); err != nil {
		return fmt.Errorf("query rejected: %w", err)
	}
	wrapped := fmt.Sprintf("SELECT * FROM (%s) LIMIT %d", strings.TrimSpace(sql), limit)
	ctx, cancel := context.WithTimeout(context.Background(), QueryTimeout)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Derived tables are created before the connection turns read-only,
	// and dropped once it can write again.
	drop := func(context.Context) error { return nil }
	if derived := derivedNames(sql); len(derived) > 0 {
		if drop, err = buildDerived(ctx, db, conn, derived, today); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA query_only = ON`); err != nil {
		_ = drop(context.Background())
		return err
	}
	defer func() {
		// Back in the pool it must write again and hold no derived tables;
		// if it can't be put back that way, throw the connection away.
		_, err := conn.ExecContext(context.Background(), `PRAGMA query_only = OFF`)
		if err == nil {
			err = drop(context.Background())
		}
		if err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	rows, err := conn.QueryContext(ctx, wrapped, args...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return fmt.Errorf("query took longer than %s; simplify it or narrow the dates", QueryTimeout)
		}
		return err
	}
	defer rows.Close()
	if err := fn(rows); err != nil {
		return err
	}
	return rows.Err()
}

func scanResult(rows *gosql.Rows, max int) (*QueryResult, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	res := &QueryResult{Columns: make([]string, len(cols))}
	for i, c := range cols {
		res.Columns[i] = truncateCell(c)
	}
	for rows.Next() {
		if len(res.Rows) > max {
			res.Truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			switch t := v.(type) {
			case string:
				row[i] = truncateCell(t)
			case []byte:
				row[i] = truncateCell(string(t))
			case nil:
				row[i] = nil
			default:
				row[i] = v
			}
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(res.Rows) > max {
		res.Rows = res.Rows[:max]
		res.Truncated = true
	}
	res.RowCount = len(res.Rows)
	return res, nil
}
