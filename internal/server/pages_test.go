package server

import (
	"fmt"
	"strings"
	"testing"

	"glowworm/internal/clock"
	"glowworm/internal/store"
)

func referenceBalanceAt(t *testing.T, db *store.DB, accountID, date string) int64 {
	t.Helper()
	var opening int64
	var openingDate any
	if err := db.QueryRow(`SELECT opening_balance_minor, opening_balance_date FROM accounts WHERE id=?`, accountID).
		Scan(&opening, &openingDate); err != nil {
		t.Fatal(err)
	}
	since := ""
	if s, ok := openingDate.(string); ok && s != "" {
		since = s
	}
	balance := opening
	var valDate any
	var val int64
	err := db.QueryRow(`SELECT value_minor, date FROM valuations WHERE account_id=? AND date<=? AND deleted_at IS NULL
		ORDER BY date DESC, created_at DESC LIMIT 1`, accountID, date).Scan(&val, &valDate)
	if err == nil {
		balance = val
		if s, ok := valDate.(string); ok {
			since = s
		}
	}
	q := `SELECT COALESCE(SUM(amount_minor),0) FROM transactions WHERE account_id=? AND deleted_at IS NULL AND date <= ?`
	args := []any{accountID, date}
	if since != "" {
		q += ` AND date > ?`
		args = append(args, since)
	}
	var sum any
	if err := db.QueryRow(q, args...).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if v, ok := sum.(int64); ok {
		balance += v
	}
	return balance
}

func newSeriesTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func insertAccount(t *testing.T, db *store.DB, id string, opening int64, openingDate any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO accounts (id, name, type, color, currency, opening_balance_minor, opening_balance_date, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?, '2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		id, id, "debit", "#0e6f51", "GBP", opening, openingDate); err != nil {
		t.Fatal(err)
	}
}

func insertTx(t *testing.T, db *store.DB, id, accountID, date string, amount int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
		VALUES (?,?,?,'tx',?,?,'expense','h','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
		id, accountID, date, amount, "GBP"); err != nil {
		t.Fatal(err)
	}
}

func insertValuation(t *testing.T, db *store.DB, id, accountID, date string, value int64, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO valuations (id, account_id, date, value_minor, source, created_at, updated_at)
		VALUES (?,?,?,?, 'manual', ?,?)`, id, accountID, date, value, createdAt, createdAt); err != nil {
		t.Fatal(err)
	}
}

func compareSeries(t *testing.T, a *App, from, to string) {
	t.Helper()
	labels, values, err := balancePointSeries(a, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) == 0 {
		t.Fatalf("empty series for %s..%s", from, to)
	}
	var accIDs []string
	rows, err := a.DB.Query(`SELECT id FROM accounts WHERE deleted_at IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		accIDs = append(accIDs, id)
	}
	for i, d := range labels {
		want := int64(0)
		for _, id := range accIDs {
			want += referenceBalanceAt(t, a.DB, id, d)
		}
		if values[i] != want {
			t.Errorf("balance at %s: swept %d, reference %d", d, values[i], want)
		}
	}
}

func TestBalancePointSeriesParity(t *testing.T) {
	db := newSeriesTestDB(t)
	a := &App{DB: db, Clock: clock.New("UTC")}

	t.Run("opening plus transactions", func(t *testing.T) {
		insertAccount(t, db, "a1", 10000, "2024-01-01")
		insertTx(t, db, "t1", "a1", "2024-01-05", -4000)
		insertTx(t, db, "t2", "a1", "2024-02-01", 3000)
		insertTx(t, db, "t3", "a1", "2024-02-10", -1000)
		compareSeries(t, a, "2024-01-01", "2024-03-01")
	})

	t.Run("opening without date", func(t *testing.T) {
		insertAccount(t, db, "a2", 5000, nil)
		insertTx(t, db, "t4", "a2", "2024-01-05", 2500)
		insertTx(t, db, "t5", "a2", "2024-02-05", -1000)
		compareSeries(t, a, "2024-01-01", "2024-03-01")
	})

	t.Run("opening date in the future", func(t *testing.T) {
		insertAccount(t, db, "a3", 9000, "2024-06-01")
		insertTx(t, db, "t6", "a3", "2024-01-05", 1000)
		insertTx(t, db, "t7", "a3", "2024-07-05", 2000)
		compareSeries(t, a, "2024-01-01", "2024-08-01")
	})

	t.Run("valuations replace checkpoints", func(t *testing.T) {
		insertAccount(t, db, "a4", 1000, "2024-01-01")
		insertTx(t, db, "t8", "a4", "2024-01-10", 500)
		insertValuation(t, db, "v1", "a4", "2024-01-02", 7000, "2024-01-02T00:00:00Z")
		insertValuation(t, db, "v2", "a4", "2024-01-10", 8000, "2024-01-10T00:00:00Z")
		insertValuation(t, db, "v3", "a4", "2024-02-01", 9000, "2024-02-01T00:00:00Z")
		insertValuation(t, db, "v4", "a4", "2024-02-01", 9500, "2024-02-01T12:00:00Z")
		insertValuation(t, db, "v5", "a4", "2024-02-20", 9100, "2024-02-20T00:00:00Z")
		insertTx(t, db, "t9", "a4", "2024-03-01", 250)
		compareSeries(t, a, "2024-01-01", "2024-03-15")
	})

	t.Run("range starts after earliest data", func(t *testing.T) {
		compareSeries(t, a, "2024-02-01", "2024-03-15")
	})

	t.Run("multiple accounts", func(t *testing.T) {
		insertAccount(t, db, "a5", 20000, "2024-01-15")
		insertTx(t, db, "t10", "a5", "2024-01-20", -6000)
		insertValuation(t, db, "v6", "a5", "2024-02-10", 15000, "2024-02-10T00:00:00Z")
		compareSeries(t, a, "2024-01-01", "2024-03-15")
	})

	t.Run("capped series beyond 370 points", func(t *testing.T) {
		insertAccount(t, db, "a6", 0, nil)
		for i := 0; i < 400; i++ {
			insertTx(t, db, fmt.Sprintf("c%d", i), "a6", fmt.Sprintf("2023-%02d-%02d", 1+i/28, 1+i%28), int64(10+i))
		}
		compareSeries(t, a, "2023-01-01", "2024-12-31")
	})
}

func TestParseStoredWidgets(t *testing.T) {
	arr := `[{"id":"w-1","type":"number","title":"T","data":{"value":"£1"}},
	         {"id":"w-2","type":"bar","title":"B","data":{"labels":[],"series":[]}}]`
	if ws := parseStoredWidgets(arr); len(ws) != 2 {
		t.Errorf("array: got %d widgets, want 2", len(ws))
	}
	if ws := parseStoredWidgets(""); len(ws) != 0 {
		t.Errorf("empty: got %d", len(ws))
	}
}

func TestFormatAssistantText(t *testing.T) {
	out := formatAssistantText("You spent **£12.50** <script>alert(1)</script>\nnext line")
	if !strings.Contains(out, "<strong>£12.50</strong>") {
		t.Errorf("bold not rendered: %q", out)
	}
	if strings.Contains(out, "<script>") {
		t.Errorf("HTML not escaped: %q", out)
	}
}

func TestBalanceScopeSeriesSplitsSavings(t *testing.T) {
	db := newSeriesTestDB(t)
	a := &App{DB: db, Clock: clock.New("UTC")}
	insertAccount(t, db, "cur", 1000, "2024-01-01")
	insertAccount(t, db, "sav", 5000, "2024-01-01")
	if _, err := db.Exec(`UPDATE accounts SET type = 'savings' WHERE id = 'sav'`); err != nil {
		t.Fatal(err)
	}
	insertTx(t, db, "t1", "cur", "2024-02-01", -200)
	insertTx(t, db, "t2", "sav", "2024-03-01", 700)
	last := func(scope string) (int64, int) {
		labels, values, err := balanceScopeSeries(a, "2024-01-01", "2024-12-31", scope)
		if err != nil {
			t.Fatal(err)
		}
		return values[len(values)-1], len(labels)
	}
	if v, n := last("available"); v != 800 || n != 2 {
		t.Fatalf("available = %d over %d points, want 800 over 2 (opening + its own tx day)", v, n)
	}
	if v, _ := last("savings"); v != 5700 {
		t.Fatalf("savings = %d, want 5700", v)
	}
	if v, _ := last("all"); v != 6500 {
		t.Fatalf("all = %d, want 6500", v)
	}
}
