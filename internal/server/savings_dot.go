package server

import (
	"context"
	"encoding/json"
	"slices"

	"glowworm/internal/core/ledger"
	"glowworm/internal/core/notify"
)

// keySavingsSeen is the out-of-date balances the Savings page was last
// opened with: the Savings tab has a dot while another one is out of date.
// A reminder going out clears it.
const keySavingsSeen = notify.KeySavingsSeen

// dueKeys names each out-of-date savings balance by its account and the
// date of the balance that went out of date, so updating it and having it
// go out of date again counts as new.
func dueKeys(b *ledger.Book) []string {
	keys := []string{}
	for _, st := range b.Statuses() {
		if st.Due {
			since := st.LastValued
			if since == "" {
				since = "never"
			}
			keys = append(keys, st.Account.ID+":"+since)
		}
	}
	slices.Sort(keys)
	return keys
}

func (a *App) savingsSeen() map[string]bool {
	seen := map[string]bool{}
	var raw string
	if a.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, keySavingsSeen).Scan(&raw) != nil {
		return seen
	}
	var keys []string
	_ = json.Unmarshal([]byte(raw), &keys)
	for _, k := range keys {
		seen[k] = true
	}
	return seen
}

// savingsDot says whether the Savings tab shows its dot, and on the Savings
// page itself records what's out of date now as seen, which clears it.
func (a *App) savingsDot(ctx context.Context, view string) bool {
	b, err := a.book(ctx)
	if err != nil {
		a.Log.Warn("savings dot", "err", err)
		return false
	}
	keys := dueKeys(b)
	seen := a.savingsSeen()
	if view == "savings" {
		if len(keys) != len(seen) || slices.ContainsFunc(keys, func(k string) bool { return !seen[k] }) {
			if err := a.Repos.Settings.Set(nil, keySavingsSeen, keys); err != nil {
				a.Log.Warn("savings dot", "err", err)
			}
		}
		return false
	}
	return slices.ContainsFunc(keys, func(k string) bool { return !seen[k] })
}
