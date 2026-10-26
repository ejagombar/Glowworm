package query

import (
	"context"
	"fmt"
	"strings"
	"time"

	"glowworm/internal/store"
)

const (
	MaxRows      = 200
	wrapLimit    = MaxRows + 1
	MaxCellLen   = 200
	QueryTimeout = 10 * time.Second
	MaxSQLLen    = 4000
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

func validateReadonly(sql string) error {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return fmt.Errorf("empty query")
	}
	if len(sql) > MaxSQLLen {
		return fmt.Errorf("query too long (max %d chars)", MaxSQLLen)
	}
	toks := tokenizeSQL(sql)
	if len(toks) == 0 {
		return fmt.Errorf("no SQL tokens found")
	}
	first := strings.ToLower(toks[0])
	if first != "select" && first != "with" {
		return fmt.Errorf("only SELECT queries are allowed")
	}
	for i, t := range toks {
		l := strings.ToLower(t)
		if forbiddenTokens[l] {
			return fmt.Errorf("forbidden keyword %q in query", strings.ToUpper(l))
		}
		_ = i
	}
	return nil
}

func tokenizeSQL(sql string) []string {
	var toks []string
	i := 0
	n := len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			q := c
			i++
			for i < n {
				if sql[i] == q {
					if i+1 < n && sql[i+1] == q { // escaped quote ('')
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			toks = append(toks, "\x00lit")
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			i += 2
			for i+1 < n && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i += 2
		case c == ';':
			rest := strings.TrimSpace(sql[i+1:])
			if rest != "" {
				return nil
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
	return toks
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

// Wrapping as SELECT * FROM (<query>) LIMIT 201 caps rows regardless of any
// inner LIMIT; SQLite accepts WITH inside the subquery. args bind the query's
// parameters, such as a widget's :from and :to.
func RunReadonlyQuery(db *store.DB, sql string, args ...any) (*QueryResult, error) {
	if err := validateReadonly(sql); err != nil {
		return nil, fmt.Errorf("query rejected: %w", err)
	}
	wrapped := fmt.Sprintf("SELECT * FROM (%s) LIMIT %d", strings.TrimSpace(sql), wrapLimit)
	ctx, cancel := context.WithTimeout(context.Background(), QueryTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, wrapped, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	res := &QueryResult{Columns: make([]string, len(cols))}
	for i, c := range cols {
		res.Columns[i] = truncateCell(c)
	}
	for rows.Next() {
		if len(res.Rows) >= wrapLimit {
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
	if len(res.Rows) > MaxRows {
		res.Rows = res.Rows[:MaxRows]
		res.Truncated = true
	}
	res.RowCount = len(res.Rows)
	return res, nil
}
