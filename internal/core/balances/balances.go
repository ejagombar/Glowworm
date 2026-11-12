package balances

import (
	"database/sql"
	"fmt"
	"time"
)

func Account(db *sql.DB, accountID, date string) (int64, error) {
	return Expected(db, accountID, date, "")
}

// Expected is the account's balance at the end of date as if the valuation
// excludeID had never been recorded: what its opening balance, earlier values
// and transactions say. A recorded value is checked against it.
func Expected(db *sql.DB, accountID, date, excludeID string) (int64, error) {
	if date == "" {
		date = "9999-12-31"
	}
	var opening int64
	var openingDate sql.NullString
	err := db.QueryRow(`SELECT opening_balance_minor, COALESCE(opening_balance_date,'')
		FROM accounts WHERE id = ? AND deleted_at IS NULL`, accountID).
		Scan(&opening, &openingDate)
	if err != nil {
		return 0, err
	}
	balance := opening
	since := ""
	if openingDate.Valid && openingDate.String != "" {
		since = openingDate.String
	}
	var val int64
	var valDate string
	err = db.QueryRow(`SELECT value_minor, date FROM valuations
		WHERE account_id = ? AND date <= ? AND id <> ? AND deleted_at IS NULL ORDER BY date DESC, created_at DESC LIMIT 1`,
		accountID, date, excludeID).Scan(&val, &valDate)
	switch err {
	case nil:
		balance = val
		since = valDate
	case sql.ErrNoRows:
	default:
		return 0, err
	}
	sum, err := sumTransactions(db, accountID, since, date)
	if err != nil {
		return 0, err
	}
	return balance + sum, nil
}

func All(db *sql.DB, date string) (map[string]int64, error) {
	ids := map[string]int64{}
	rows, err := db.Query(`SELECT id FROM accounts WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids[id] = 0
	}
	rows.Close()
	out := map[string]int64{}
	for id := range ids {
		b, err := Account(db, id, date)
		if err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, nil
}

func sumTransactions(db *sql.DB, accountID, fromExclusive, toInclusive string) (int64, error) {
	q := `SELECT COALESCE(SUM(amount_minor),0) FROM transactions
		WHERE account_id = ? AND deleted_at IS NULL AND date <= ?`
	args := []any{accountID, toInclusive}
	if fromExclusive != "" && fromExclusive != "0000-01-01" {
		q += ` AND date > ?`
		args = append(args, fromExclusive)
	}
	var sum int64
	err := db.QueryRow(q, args...).Scan(&sum)
	return sum, err
}

func Series(db *sql.DB, accountID, from, to string) (labels []string, values []int64, err error) {
	months := MonthKeys(from, to)
	labels = make([]string, 0, len(months))
	values = make([]int64, 0, len(months))
	for _, m := range months {
		end := MonthEnd(m)
		var total int64
		if accountID == "" {
			ids := map[string]int64{}
			rows, e := db.Query(`SELECT id FROM accounts WHERE deleted_at IS NULL`)
			if e != nil {
				return nil, nil, e
			}
			for rows.Next() {
				var id string
				if e := rows.Scan(&id); e != nil {
					rows.Close()
					return nil, nil, e
				}
				ids[id] = 0
			}
			rows.Close()
			for id := range ids {
				b, e := Account(db, id, end)
				if e != nil {
					return nil, nil, e
				}
				total += b
			}
		} else {
			total, err = Account(db, accountID, end)
			if err != nil {
				return nil, nil, err
			}
		}
		labels = append(labels, m)
		values = append(values, total)
	}
	return labels, values, nil
}

func MonthKeys(from, to string) []string {
	if len(from) < 7 {
		from = "1970-01"
	}
	if len(to) < 7 {
		to = "2999-12"
	}
	fy, fm := atoi(from[:4]), atoi(from[5:7])
	ty, tm := atoi(to[:4]), atoi(to[5:7])
	var out []string
	for y, m := fy, fm; y < ty || (y == ty && m <= tm); {
		out = append(out, pad(y)+"-"+pad(m))
		m++
		if m > 12 {
			m = 1
			y++
		}
		if len(out) > 1200 {
			break
		}
	}
	return out
}

func MonthEnd(month string) string {
	y, m := atoi(month[:4]), atoi(month[5:7])
	nextY, nextM := y, m+1
	if nextM > 12 {
		nextM = 1
		nextY++
	}
	end := pad(nextY) + "-" + pad(nextM) + "-01"
	t, _ := parseISO(end)
	t = t.AddDate(0, 0, -1)
	return t.Format("2006-01-02")
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return n
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func pad(n int) string {
	return fmt.Sprintf("%04d", n)
}

func parseISO(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.UTC)
}
