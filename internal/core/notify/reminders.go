package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"glowworm/internal/core/ledger"
	"glowworm/internal/store"
	"glowworm/internal/util"
)

// Settings keys for reminders. How old a balance may get before it's due is
// ledger.SettingReminderDays.
const (
	KeyRemindersEnabled = "reminders_enabled"
	KeyReminderHour     = "reminder_hour"
	KeyReminderRepeat   = "reminder_repeat_days"
	keyReminderLastRun  = "reminder_last_run"
)

// ReminderSettings is how reminders are set up.
type ReminderSettings struct {
	// Enabled sends reminders to the devices with notifications on.
	Enabled bool `json:"enabled"`
	// DefaultDays is how old a savings account's balance may get before
	// it's due (0: never). It's the same for every savings account.
	DefaultDays int `json:"default_days"`
	// Hour is the local hour of the day reminders go out, 0–23.
	Hour int `json:"hour"`
	// RepeatDays is how often a reminder for a balance still out of date is
	// sent again (0: only once until it's updated).
	RepeatDays int `json:"repeat_days"`
	// LastRun is the last day reminders were looked at.
	LastRun string `json:"last_run"`
}

// Defaults when nothing is set.
const (
	DefaultReminderHour   = 9
	DefaultReminderRepeat = 7
)

// Reminders sends a notification, at most once a day, listing the balances
// that are out of date.
type Reminders struct {
	DB     *store.DB
	Notify *Service
	// Book is the ledger as of today.
	Book func(ctx context.Context) (*ledger.Book, error)
	// Now is the local time.
	Now func() time.Time
	Log *slog.Logger
}

func (r *Reminders) setting(key string) (json.RawMessage, bool) {
	var v string
	if err := r.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil || v == "" || v == "null" {
		return nil, false
	}
	return json.RawMessage(v), true
}

func (r *Reminders) intSetting(key string, def, lo, hi int) int {
	raw, ok := r.setting(key)
	if !ok {
		return def
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return def
		}
		if _, err := fmt.Sscan(s, &f); err != nil {
			return def
		}
	}
	n := int(f)
	if n < lo || n > hi {
		return def
	}
	return n
}

// Settings reads the reminder settings.
func (r *Reminders) Settings() ReminderSettings {
	s := ReminderSettings{
		Enabled:     true,
		DefaultDays: r.intSetting(ledger.SettingReminderDays, ledger.DefaultReminderDays, 0, 3650),
		Hour:        r.intSetting(KeyReminderHour, DefaultReminderHour, 0, 23),
		RepeatDays:  r.intSetting(KeyReminderRepeat, DefaultReminderRepeat, 0, 365),
	}
	if raw, ok := r.setting(KeyRemindersEnabled); ok {
		_ = json.Unmarshal(raw, &s.Enabled)
	}
	if raw, ok := r.setting(keyReminderLastRun); ok {
		_ = json.Unmarshal(raw, &s.LastRun)
	}
	return s
}

// Due is the out-of-date balances a reminder should go out for today: due
// ones not reminded about since they went out of date, or not within
// RepeatDays.
func (r *Reminders) Due(ctx context.Context, set ReminderSettings) ([]ledger.Status, error) {
	b, err := r.Book(ctx)
	if err != nil {
		return nil, err
	}
	sent := map[string]string{}
	rows, err := r.DB.QueryContext(ctx, `SELECT account_id, last_sent_at FROM reminder_state`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return nil, err
		}
		sent[id] = at
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []ledger.Status
	for _, st := range b.Statuses() {
		if !st.Due {
			continue
		}
		// Reminded about this time it went out of date yet? It went out of
		// date ReminderDays after the latest balance's date (so a balance
		// entered with an earlier date still starts afresh).
		last, ok := sent[st.Account.ID]
		switch {
		case !ok:
		case st.LastValued != "" && last < addDays(st.LastValued, st.ReminderDays):
		case set.RepeatDays > 0 && last <= addDays(b.Today, -set.RepeatDays):
		default:
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

// Tick sends today's reminder if it's time and hasn't gone yet. It says
// whether one was sent.
func (r *Reminders) Tick(ctx context.Context) (bool, error) {
	set := r.Settings()
	now := r.Now()
	today := now.Format("2006-01-02")
	if !set.Enabled || now.Hour() < set.Hour || set.LastRun >= today {
		return false, nil
	}
	// Recorded first: a push service that's down shouldn't mean a retry
	// every few minutes all day.
	if err := r.setLastRun(today); err != nil {
		return false, err
	}
	due, err := r.Due(ctx, set)
	if err != nil || len(due) == 0 {
		return false, err
	}
	m := ReminderMessage(due)
	// The app icon's badge counts every out-of-date balance, as the
	// Savings tab does, not only those reminded about today.
	if b, err := r.Book(ctx); err == nil {
		for _, st := range b.Statuses() {
			if st.Due {
				m.Badge++
			}
		}
	}
	results, err := r.Notify.SendAll(ctx, m)
	if errors.Is(err, ErrNoDevices) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	delivered := false
	for _, res := range results {
		if res.Err == nil {
			delivered = true
		} else if r.Log != nil {
			r.Log.Warn("reminder not delivered", "device", res.Subscription.Label, "service", res.Subscription.Service, "err", res.Err)
		}
	}
	if !delivered {
		return false, nil
	}
	return true, r.DB.WithTx(func(tx *sql.Tx) error {
		for _, st := range due {
			if _, err := tx.Exec(`INSERT INTO reminder_state (account_id, last_sent_at) VALUES (?, ?)
				ON CONFLICT(account_id) DO UPDATE SET last_sent_at = excluded.last_sent_at`, st.Account.ID, today); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Reminders) setLastRun(day string) error {
	b, _ := json.Marshal(day)
	_, err := r.DB.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		keyReminderLastRun, string(b), util.NowStr())
	return err
}

// Run checks every few minutes until ctx ends.
func (r *Reminders) Run(ctx context.Context, every time.Duration) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := r.Tick(ctx); err != nil && r.Log != nil {
			r.Log.Error("balance reminders", "err", err)
		}
		t.Reset(every)
	}
}

// ReminderMessage is the notification for these out-of-date balances.
func ReminderMessage(due []ledger.Status) Message {
	m := Message{Title: "Balances to update", URL: "/savings", Tag: "balance-reminder"}
	if len(due) == 1 {
		m.Title = "Time to update " + due[0].Account.Name
		m.Body = "Its balance " + age(due[0]) + ". Tap to update it."
		return m
	}
	parts := make([]string, 0, len(due))
	for _, st := range due {
		parts = append(parts, st.Account.Name+" ("+shortAge(st)+")")
	}
	m.Body = fmt.Sprintf("%d balances are out of date: %s", len(due), strings.Join(parts, ", "))
	if len(m.Body) > 240 {
		m.Body = m.Body[:237] + "…"
	}
	return m
}

func age(st ledger.Status) string {
	switch {
	case st.DaysSince < 0:
		return "has never been recorded"
	case st.DaysSince == 0:
		return "was last updated today"
	case st.DaysSince == 1:
		return "was last updated yesterday"
	default:
		return fmt.Sprintf("was last updated %d days ago", st.DaysSince)
	}
}

func shortAge(st ledger.Status) string {
	switch {
	case st.DaysSince < 0:
		return "never"
	case st.DaysSince == 1:
		return "1 day"
	default:
		return fmt.Sprintf("%d days", st.DaysSince)
	}
}

func addDays(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}
