package recurring

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/core/normalize"
	"github.com/ejagombar/Glowworm/internal/store"
	"github.com/ejagombar/Glowworm/internal/store/repos"
)

type Service struct {
	DB    *store.DB
	Txs   *repos.Transactions
	Audit *audit.Writer
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

var cadenceWindows = []struct {
	name   string
	lo, hi float64
}{
	{"weekly", 6, 8}, {"fortnightly", 12, 16}, {"monthly", 27, 33},
	{"quarterly", 88, 95}, {"annually", 360, 370},
}

func (s *Service) Detect() (int, error) {
	txs, err := s.Txs.List(repos.ListFilter{Limit: 0})
	if err != nil {
		return 0, err
	}
	pool := []*core.Transaction{}
	for _, t := range txs {
		if t.TransferID != nil && t.Type == core.TypeTransfer {
			continue
		}
		pool = append(pool, t)
	}
	type group struct {
		key   string
		label string
		dir   string
		txs   []*core.Transaction
	}
	// Payments to one payee in one direction, then split into runs of the
	// same amount: a series repeats an amount, give or take amountTolerance.
	payees := map[string]*group{}
	for _, t := range pool {
		label := seriesLabel(t)
		if label == "" {
			continue
		}
		dir := "out"
		if t.AmountMinor > 0 {
			dir = "in"
		}
		key := label + "|" + dir
		g := payees[key]
		if g == nil {
			g = &group{key: key, label: label, dir: dir}
			payees[key] = g
		}
		g.txs = append(g.txs, t)
	}
	var groups []*group
	for _, p := range payees {
		for _, run := range sameAmountRuns(p.txs) {
			if len(run) < 3 || !dominates(run, p.txs) {
				continue
			}
			groups = append(groups, &group{key: p.key, label: p.label, dir: p.dir, txs: run})
		}
	}
	var previous []string
	if rows, err := s.DB.Query(`SELECT key FROM recurring_keys`); err == nil {
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil {
				previous = append(previous, k)
			}
		}
		rows.Close()
	}

	now := time.Now().UTC()
	var keysDetected []string
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		// Every series is worked out afresh, so one that no longer holds
		// loses its tags.
		if _, err := tx.Exec(`UPDATE transactions SET recurring_key = NULL WHERE recurring_key IS NOT NULL`); err != nil {
			return err
		}
		for _, g := range groups {
			if len(g.txs) < 3 {
				continue
			}
			sort.Slice(g.txs, func(i, j int) bool { return g.txs[i].Date < g.txs[j].Date })
			gaps := []float64{}
			for i := 1; i < len(g.txs); i++ {
				a, e1 := time.Parse("2006-01-02", g.txs[i-1].Date)
				b, e2 := time.Parse("2006-01-02", g.txs[i].Date)
				if e1 != nil || e2 != nil {
					continue
				}
				gaps = append(gaps, b.Sub(a).Hours()/24)
			}
			if len(gaps) < 2 {
				continue
			}
			median := medianOf(gaps)
			cadence := ""
			interval := int(median)
			for _, w := range cadenceWindows {
				if median >= w.lo && median <= w.hi {
					cadence = w.name
					break
				}
			}
			if cadence == "" {
				continue
			}
			mean := 0.0
			for _, g2 := range gaps {
				mean += g2
			}
			mean /= float64(len(gaps))
			variance := 0.0
			for _, g2 := range gaps {
				variance += (g2 - mean) * (g2 - mean)
			}
			variance /= float64(len(gaps))
			stdev := math.Sqrt(variance)
			if mean > 0 && stdev/mean > 0.2 {
				continue
			}
			amountBucket := medianAmount(g.txs)
			h := sha256.Sum256([]byte(g.label + "|" + fmt.Sprint(amountBucket) + "|" + cadence))
			key := hex.EncodeToString(h[:8])
			nextExpected := nextOf(g.txs[len(g.txs)-1].Date, interval)
			active := 1
			if last, e := time.Parse("2006-01-02", g.txs[len(g.txs)-1].Date); e == nil {
				if now.Sub(last).Hours()/24 > float64(interval)*2 {
					active = 0
				}
			}
			confidence := math.Max(0, 1-stdev/math.Max(mean, 1))
			amount := amountBucket
			dir := g.dir
			if dir == "out" {
				amount = -amount
			}
			k := &core.RecurringKey{
				Key: key, Label: mostCommonLabel(g.txs), Cadence: cadence,
				IntervalDays: &interval, AmountMinor: &amount, Direction: dir,
				FirstSeen: g.txs[0].Date, LastSeen: g.txs[len(g.txs)-1].Date,
				NextExpected: &nextExpected, Confidence: confidence, Active: active == 1,
			}
			if err := s.Txs.UpsertRecurringKey(tx, k); err != nil {
				return err
			}
			for _, t := range g.txs {
				if _, err := tx.Exec(`UPDATE transactions SET recurring_key = ? WHERE id = ?`, key, t.ID); err != nil {
					return err
				}
			}
			keysDetected = append(keysDetected, key)
		}
		in, args := keyList(keysDetected)
		if _, err := tx.Exec(`DELETE FROM recurring_keys WHERE key NOT IN `+in, args...); err != nil {
			return err
		}
		added := newKeys(previous, keysDetected)
		if len(added) > 0 {
			if _, err := s.Audit.Write(tx, audit.Input{
				Actor: core.ActorSystem, Action: "recurring.detected",
				EntityType: "recurring", EntityID: "sweep",
				Title:  fmt.Sprintf("Detected %d recurring series", len(added)),
				Sub:    "Bills, subscriptions and salaries are labelled automatically",
				After:  map[string]any{"keys": added},
				Revert: map[string]any{"keys": added},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return len(keysDetected), err
}

// seriesLabel is what a payment's payee is compared by: the group the user's
// grouping rules put it in ("Rent and water" for both "To Jaeren Rodrigues -
// rent and water" and "Jaeren Rodrigues - Rent and water"), else its
// description.
func seriesLabel(t *core.Transaction) string {
	if t.Merchant != nil && *t.Merchant != "" {
		return normalize.Text(*t.Merchant)
	}
	return normalize.Text(t.Description)
}

// amountTolerance is how far a payment may stray from the run's smallest and
// still be the same amount: 3%, and at least 5p for small sums.
func amountTolerance(minor int64) int64 {
	return max(minor*3/100, 5)
}

// sameAmountRuns splits one payee's payments into runs of (nearly) the same
// amount, oldest first within each run.
func sameAmountRuns(txs []*core.Transaction) [][]*core.Transaction {
	abs := func(t *core.Transaction) int64 {
		if t.AmountMinor < 0 {
			return -t.AmountMinor
		}
		return t.AmountMinor
	}
	sorted := append([]*core.Transaction{}, txs...)
	sort.SliceStable(sorted, func(i, j int) bool { return abs(sorted[i]) < abs(sorted[j]) })
	var runs [][]*core.Transaction
	var run []*core.Transaction
	for _, t := range sorted {
		if len(run) > 0 && abs(t)-abs(run[0]) > amountTolerance(abs(run[0])) {
			runs = append(runs, run)
			run = nil
		}
		run = append(run, t)
	}
	if len(run) > 0 {
		runs = append(runs, run)
	}
	return runs
}

// dominates reports whether the run makes up at least half the payee's
// payments over the run's dates. At a shop visited every few days, a few
// purchases that happen to cost the same about a month apart aren't a bill.
func dominates(run, payee []*core.Transaction) bool {
	first, last := run[0].Date, run[0].Date
	for _, t := range run {
		first, last = min(first, t.Date), max(last, t.Date)
	}
	n := 0
	for _, t := range payee {
		if t.Date >= first && t.Date <= last {
			n++
		}
	}
	return len(run)*2 >= n
}

func keyList(keys []string) (string, []any) {
	if len(keys) == 0 {
		return "('')", nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	return "(?" + strings.Repeat(",?", len(keys)-1) + ")", args
}

func newKeys(previous, current []string) []string {
	seen := map[string]bool{}
	for _, k := range previous {
		seen[k] = true
	}
	var out []string
	for _, k := range current {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out
}

func medianOf(v []float64) float64 {
	cp := append([]float64{}, v...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

func medianAmount(txs []*core.Transaction) int64 {
	vals := []int64{}
	for _, t := range txs {
		vals = append(vals, t.AmountMinor)
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	return vals[len(vals)/2]
}

// mostCommonLabel names a series: its group, else its most common description.
func mostCommonLabel(txs []*core.Transaction) string {
	if m := txs[0].Merchant; m != nil && *m != "" {
		return *m
	}
	counts := map[string]int{}
	best, bestN := "", 0
	for _, t := range txs {
		counts[t.Description]++
		if counts[t.Description] > bestN {
			best, bestN = t.Description, counts[t.Description]
		}
	}
	return best
}

func nextOf(last string, intervalDays int) string {
	t, err := time.Parse("2006-01-02", last)
	if err != nil {
		return last
	}
	return t.AddDate(0, 0, intervalDays).Format("2006-01-02")
}

func (s *Service) Series(key string) (*core.RecurringKey, []*repos.DisplayRow, error) {
	k, err := s.Txs.RecurringKey(key)
	if err != nil {
		return nil, nil, err
	}
	txs, err := s.Txs.RecurringSeries(key)
	if err != nil {
		return nil, nil, err
	}
	rowsMap := map[string][]*core.Transaction{}
	var order []string
	for _, t := range txs {
		id := t.ID
		if t.TransferID != nil {
			id = *t.TransferID
		}
		if _, ok := rowsMap[id]; !ok {
			order = append(order, id)
		}
		rowsMap[id] = append(rowsMap[id], t)
	}
	rows := []*repos.DisplayRow{}
	for _, id := range order {
		members := rowsMap[id]
		rep := members[0]
		for _, m := range members {
			if m.AmountMinor < 0 {
				rep = m
				break
			}
		}
		kind := "single"
		if len(members) > 1 {
			kind = "linked"
		}
		rows = append(rows, &repos.DisplayRow{Kind: kind, Rep: rep, Tx: rep, Members: members})
	}
	return k, rows, nil
}

var _ = sql.ErrNoRows
