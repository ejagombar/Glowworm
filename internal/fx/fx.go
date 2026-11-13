// Package fx supplies daily exchange rates for converting imported
// transactions into the account's currency. Rates come from the European
// Central Bank via the free Frankfurter API (only currency codes and dates
// are sent) and are cached in the database.
package fx

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"glowworm/internal/store"
)

// Source gives the rate for 1 unit of from in to, for each requested date.
type Source interface {
	Rates(from, to string, dates []string) (map[string]float64, error)
}

type Frankfurter struct {
	DB      *store.DB
	BaseURL string // default https://api.frankfurter.dev/v1
	Client  *http.Client
}

// Rates answers from the cache where it can and fetches the rest with one
// range request. Weekends and holidays have no ECB rate, so a date takes the
// latest earlier working day's rate.
func (f *Frankfurter) Rates(from, to string, dates []string) (map[string]float64, error) {
	from, to = strings.ToUpper(from), strings.ToUpper(to)
	out := map[string]float64{}
	if len(dates) == 0 {
		return out, nil
	}
	sorted := append([]string(nil), dates...)
	sort.Strings(sorted)
	// Look back a week so the first date can borrow an earlier working day.
	start, _ := time.Parse("2006-01-02", sorted[0])
	lo := start.AddDate(0, 0, -7).Format("2006-01-02")
	hi := sorted[len(sorted)-1]

	known, err := f.cached(from, to, lo, hi)
	if err != nil {
		return nil, err
	}
	if missing(known, sorted) {
		fetched, err := f.fetch(from, to, lo, hi)
		if err != nil {
			return nil, err
		}
		for d, r := range fetched {
			known[d] = r
		}
		f.store(from, to, fetched)
	}
	days := make([]string, 0, len(known))
	for d := range known {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range dates {
		i := sort.SearchStrings(days, d)
		switch {
		case i < len(days) && days[i] == d:
			out[d] = known[d]
		case i > 0:
			out[d] = known[days[i-1]]
		}
	}
	return out, nil
}

// missing reports whether a date has neither its own rate nor one within the
// previous few days.
func missing(known map[string]float64, dates []string) bool {
	for _, d := range dates {
		t, _ := time.Parse("2006-01-02", d)
		found := false
		for back := 0; back < 5 && !found; back++ {
			_, found = known[t.AddDate(0, 0, -back).Format("2006-01-02")]
		}
		if !found {
			return true
		}
	}
	return false
}

func (f *Frankfurter) cached(from, to, lo, hi string) (map[string]float64, error) {
	out := map[string]float64{}
	if f.DB == nil {
		return out, nil
	}
	rows, err := f.DB.Query(`SELECT date, rate FROM fx_rates WHERE base = ? AND quote = ? AND date BETWEEN ? AND ?`, from, to, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		var r float64
		if err := rows.Scan(&d, &r); err != nil {
			return nil, err
		}
		out[d] = r
	}
	return out, rows.Err()
}

func (f *Frankfurter) store(from, to string, rates map[string]float64) {
	if f.DB == nil || len(rates) == 0 {
		return
	}
	_ = f.DB.WithTx(func(tx *sql.Tx) error {
		for d, r := range rates {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO fx_rates (date, base, quote, rate) VALUES (?,?,?,?)`, d, from, to, r); err != nil {
				return err
			}
		}
		return nil
	})
}

const maxResponse = 8 << 20

func (f *Frankfurter) fetch(from, to, lo, hi string) (map[string]float64, error) {
	base := f.BaseURL
	if base == "" {
		base = "https://api.frankfurter.dev/v1"
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	u := fmt.Sprintf("%s/%s..%s?base=%s&symbols=%s", strings.TrimRight(base, "/"),
		url.PathEscape(lo), url.PathEscape(hi), url.QueryEscape(from), url.QueryEscape(to))
	resp, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("fx: couldn't reach the exchange-rate service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fx: exchange-rate service answered %s", resp.Status)
	}
	var body struct {
		Rates map[string]map[string]float64 `json:"rates"`
	}
	// Decades of daily rates for one pair are well under a megabyte.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&body); err != nil {
		return nil, fmt.Errorf("fx: unreadable exchange-rate response: %w", err)
	}
	out := map[string]float64{}
	for d, m := range body.Rates {
		if r, ok := m[to]; ok && r > 0 {
			out[d] = r
		}
	}
	return out, nil
}
