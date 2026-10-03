// Package recurring implements automatic recurring/bill detection (doc 08 §6).
package recurring

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"time"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/normalize"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

// Service detects recurring series.
type Service struct {
	DB    *store.DB
	Txs   *repos.Transactions
	Audit *audit.Writer
}

// ValidationError is user-facing.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// cadence windows (doc 08 §6.1).
var cadenceWindows = []struct {
	name  string
	lo, hi float64
}{
	{"weekly", 6, 8}, {"fortnightly", 12, 16}, {"monthly", 27, 33},
	{"quarterly", 88, 95}, {"annually", 360, 370},
}

// Detect scans all live transactions and upserts recurring_keys (doc 08 §6.1).
// Idempotent; member edits drop out on the next sweep.
func (s *Service) Detect() (int, error) {
	txs, err := s.Txs.List(repos.ListFilter{Limit: 0})
	if err != nil {
		return 0, err
	}
	// Exclude linked-transfer legs from series detection (doc 08 §6.1).
	pool := []*core.Transaction{}
	for _, t := range txs {
		if t.TransferID != nil && t.Type == core.TypeTransfer {
			continue
		}
		pool = append(pool, t)
	}
	// Group by normalised description + amount bucket.
	type group struct {
		key   string
		label string
		dir   string
		txs   []*core.Transaction
	}
	groups := map[string]*group{}
	for _, t := range pool {
		label := normalize.Text(t.Description)
		if label == "" {
			continue
		}
		dir := "out"
		if t.AmountMinor > 0 {
			dir = "in"
		}
		// Amount bucket: ±1% tolerance via rounding to 1% of magnitude.
		amount := math.Abs(float64(t.AmountMinor))
		bucket := int64(math.Round(amount / math.Max(amount*0.01, 100)))
		key := label + "|" + fmt.Sprint(bucket) + "|" + dir
		g := groups[key]
		if g == nil {
			g = &group{key: key, label: label, dir: dir}
			groups[key] = g
		}
		g.txs = append(g.txs, t)
	}

	now := time.Now().UTC()
	var keysDetected []string
	err = s.DB.WithTx(func(tx *sql.Tx) error {
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
				continue // irregular (doc 08 §6.1)
			}
			// Regularity: stdev/mean ≤ 0.2.
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
			// Stable key (doc 02 §6.1).
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
			// Stamp members.
			for _, t := range g.txs {
				if _, err := tx.Exec(`UPDATE transactions SET recurring_key = ? WHERE id = ?`, key, t.ID); err != nil {
					return err
				}
			}
			keysDetected = append(keysDetected, key)
		}
		// One audit entry per sweep (doc 08 §6.1).
		if len(keysDetected) > 0 {
			if _, err := s.Audit.Write(tx, audit.Input{
				Actor: core.ActorSystem, Action: "recurring.detected",
				EntityType: "recurring", EntityID: "sweep",
				Title: fmt.Sprintf("Detected %d recurring series", len(keysDetected)),
				Sub:   "Bills, subscriptions and salaries are labelled automatically",
				After: map[string]any{"keys": keysDetected},
				Revert: map[string]any{"keys": keysDetected},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return len(keysDetected), err
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

func mostCommonLabel(txs []*core.Transaction) string {
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

// Series returns the series metadata + member transactions (grouped for
// transfer series), powering the recurring modal (doc 08 §6.2).
func (s *Service) Series(key string) (*core.RecurringKey, []*repos.DisplayRow, error) {
	k, err := s.Txs.RecurringKey(key)
	if err != nil {
		return nil, nil, err
	}
	txs, err := s.Txs.RecurringSeries(key)
	if err != nil {
		return nil, nil, err
	}
	// Group transfer legs (prototype recurringRowEl parity).
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