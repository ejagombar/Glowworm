package query

import (
	"fmt"
	"testing"
	"time"

	"github.com/ejagombar/Glowworm/internal/store"
)

// seedBig builds 20 accounts over 10 years: ~50k transactions and a
// monthly valuation on each savings account.
func seedBig(b *testing.B) *Service {
	b.Helper()
	db, err := openBenchDB(b)
	if err != nil {
		b.Fatal(err)
	}
	ts := "2015-01-01T00:00:00Z"
	tx, _ := db.Begin()
	start := time.Date(2016, 11, 18, 0, 0, 0, 0, time.UTC)
	n := 0
	for a := 0; a < 20; a++ {
		typ := "debit"
		if a%2 == 0 {
			typ = "savings"
		}
		id := fmt.Sprintf("a%02d", a)
		_, _ = tx.Exec(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at) VALUES (?,?,?,'#000','GBP',?,?)`,
			id, "Account "+id, typ, ts, ts)
		per := 2500
		for i := 0; i < per; i++ {
			d := start.AddDate(0, 0, i*3650/per).Format("2006-01-02")
			kind, amt := "expense", int64(-1000-i%500)
			if i%10 == 0 {
				kind, amt = "transfer", 50000
			}
			n++
			_, _ = tx.Exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
				VALUES (?,?,?,'x',?,'GBP',?,?,?,?)`, fmt.Sprint("t", n), id, d, amt, kind, fmt.Sprint("h", n), ts, ts)
		}
		if typ == "savings" {
			for m := 0; m < 120; m++ {
				d := start.AddDate(0, m, 0).Format("2006-01-02")
				_, _ = tx.Exec(`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at)
					VALUES (?,?,?,?,'manual',?,?)`, fmt.Sprint("v", a, "-", m), id, int64(100000+m*5000), d, ts, ts)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return &Service{DB: db, Clock: benchClock{}}
}

type benchClock struct{}

func (benchClock) Today() string { return "2026-11-18" }

func BenchmarkAccountPeriods(b *testing.B) {
	svc := seedBig(b)
	queries := map[string]string{
		"monthly-total": `SELECT period, SUM(closing_minor) FROM account_periods WHERE grain = 'month' AND account_type = 'savings' GROUP BY period ORDER BY period`,
		"yearly-flows":  `SELECT period, SUM(deposits_minor), SUM(growth_minor) FROM account_periods WHERE grain = 'year' GROUP BY period`,
		"daily-90d":     `SELECT period, SUM(closing_minor) FROM account_periods WHERE grain = 'day' AND end_date >= '2026-08-20' GROUP BY period`,
		"daily-all":     `SELECT COUNT(*) FROM account_periods WHERE grain = 'day'`,
		"goals":         `SELECT * FROM goal_progress`,
	}
	b.Run("monthly-total-cold", func(b *testing.B) {
		for b.Loop() {
			if _, err := svc.DB.Exec(`UPDATE ledger_version SET n = n + 1`); err != nil {
				b.Fatal(err)
			}
			if _, err := svc.runQuery(queries["monthly-total"]); err != nil {
				b.Fatal(err)
			}
		}
	})
	for name, q := range queries {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				if _, err := svc.runQuery(q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func openBenchDB(b *testing.B) (*store.DB, error) { return store.Open(b.TempDir() + "/b.db") }
