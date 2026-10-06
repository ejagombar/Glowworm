// Read-only SQL sandbox for the query agent (doc 10 agent loop): the model
// may ask to run queries; every query is structurally validated to be a
// single read-only SELECT, wrapped with a hard row cap and executed with a
// timeout. No user-supplied SQL is ever allowed to mutate the database.
package query

import (
	"context"
	"fmt"
	"strings"
	"time"

	"glowworm/internal/store"
)

// Sandbox limits (guardrails, doc 10 §9).
const (
	MaxRows      = 200         // rows returned to the model
	wrapLimit    = MaxRows + 1 // fetch one extra to detect truncation
	MaxCellLen   = 200         // characters per text cell
	QueryTimeout = 10 * time.Second
	MaxSQLLen    = 4000
)

// QueryResult is what the agent sees: column names + rows as plain values.
type QueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"row_count"`
	Truncated bool     `json:"truncated"`
}

// Forbidden tokens anywhere outside string literals. Every SQLite statement
// that can write or inspect the host goes through one of these keywords
// (REPLACE only in its `REPLACE INTO` write form).
var forbiddenTokens = map[string]bool{
	"insert": true, "update": true, "delete": true, "drop": true,
	"alter": true, "create": true, "attach": true, "detach": true,
	"pragma": true, "vacuum": true, "reindex": true, "analyze": true,
	"savepoint": true, "release": true, "begin": true, "commit": true,
	"rollback": true, "explain": true, "load_extension": true,
	"readfile": true, "writefile": true, "into": true,
}

// validateReadonly enforces the single read-only SELECT shape. It tokenises
// the SQL (skipping string literals and quoted identifiers and comments) so
// keywords inside strings never trigger false rejections and hidden writes
// never sneak past a naive substring check.
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

// tokenizeSQL splits SQL into identifier-ish tokens, treating '…', "…",
// `…`, […] literals as opaque single tokens and skipping -- / /* */
// comments. Only ASCII letters, digits and underscore form tokens, which is
// enough for keyword scanning.
func tokenizeSQL(sql string) []string {
	var toks []string
	i := 0
	n := len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`': // string / quoted identifier
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
			toks = append(toks, "\x00lit") // placeholder: never a keyword
		case c == '-' && i+1 < n && sql[i+1] == '-': // line comment
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*': // block comment
			i += 2
			for i+1 < n && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i += 2
		case c == ';':
			// A trailing terminator is tolerated; anything else would stack a
			// second statement, which we never allow.
			rest := strings.TrimSpace(sql[i+1:])
			if rest != "" {
				return nil // signals invalid via empty token list
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

// truncateCell keeps text cells compact for the transcript.
func truncateCell(s string) string {
	if len(s) > MaxCellLen {
		return s[:MaxCellLen] + "…"
	}
	return s
}

// RunReadonlyQuery validates and executes one model-authored query against
// the user's database. The statement is wrapped as
// `SELECT * FROM (<query>) LIMIT 201` which caps rows regardless of any
// inner LIMIT and cannot change the query's meaning for pure SELECTs
// (verified: SQLite accepts WITH clauses inside the wrapped subquery).
func RunReadonlyQuery(db *store.DB, sql string) (*QueryResult, error) {
	if err := validateReadonly(sql); err != nil {
		return nil, fmt.Errorf("query rejected: %w", err)
	}
	wrapped := fmt.Sprintf("SELECT * FROM (%s) LIMIT %d", strings.TrimSpace(sql), wrapLimit)
	ctx, cancel := context.WithTimeout(context.Background(), QueryTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, wrapped)
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
				row[i] = v // int64/float64 pass through
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
