package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"glowworm/internal/core/ledger"
	"glowworm/internal/core/notify"
)

// reminderEvery is how often the reminder job looks whether it's time.
const reminderEvery = 10 * time.Minute

// StartReminders runs the balance reminders until ctx ends.
func (a *App) StartReminders(ctx context.Context) {
	go a.Reminders.Run(ctx, reminderEvery)
}

// apiNotifications is what the Settings panel shows: the key to subscribe
// with, the devices, and the reminder settings.
func (a *App) apiNotifications(w http.ResponseWriter, r *http.Request) error {
	key, err := a.Notify.PublicKey()
	if err != nil {
		return err
	}
	devices, err := a.Notify.List()
	if err != nil {
		return err
	}
	if devices == nil {
		devices = []*notify.Subscription{}
	}
	// The device asking is marked, so the page can say "this device".
	mine := r.URL.Query().Get("endpoint")
	type device struct {
		*notify.Subscription
		ThisDevice bool `json:"this_device"`
	}
	list := make([]device, 0, len(devices))
	for _, d := range devices {
		list = append(list, device{d, mine != "" && d.Endpoint == mine})
	}
	writeJSON(w, map[string]any{
		"public_key": key,
		"devices":    list,
		"settings":   a.Reminders.Settings(),
		"timezone":   a.Clock.Location().String(),
	})
	return nil
}

func requestOrigin(r *http.Request) string {
	if o := r.Header.Get("Origin"); o != "" {
		return o
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (a *App) apiNotifySubscribe(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		Label string `json:"label"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.Label) == "" {
		in.Label = deviceLabel(r.UserAgent())
	}
	sub, err := a.Notify.Subscribe(in.Endpoint, in.Keys.P256dh, in.Keys.Auth, in.Label, requestOrigin(r))
	if err != nil {
		return err
	}
	writeJSON(w, sub)
	return nil
}

func (a *App) apiNotifyUnsubscribe(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if err := a.Notify.Unsubscribe(in.Endpoint); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) apiNotifyRemove(w http.ResponseWriter, r *http.Request) error {
	if err := a.Notify.Remove(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

// apiNotifyTest sends a test notification to one device (by endpoint) or to
// all of them. With "reminder": true it sends the reminder today's
// out-of-date balances would get, without counting it as sent.
func (a *App) apiNotifyTest(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Endpoint string `json:"endpoint"`
		Reminder bool   `json:"reminder"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	m := notify.Message{
		Title: "Glowworm notifications are on",
		Body:  "This is a test. Reminders to update your balances will look like this.",
		URL:   "/settings#notifications", Tag: "test",
	}
	if in.Reminder {
		b, err := a.book(r.Context())
		if err != nil {
			return err
		}
		var due []ledger.Status
		for _, st := range b.Statuses() {
			if st.Due {
				due = append(due, st)
			}
		}
		if len(due) == 0 {
			return &notify.ValidationError{Message: "No balances are out of date right now, so there's nothing to remind you about."}
		}
		m = notify.ReminderMessage(due)
	}
	var results []notify.Result
	if in.Endpoint != "" {
		res, err := a.Notify.SendTo(r.Context(), in.Endpoint, m)
		if err != nil {
			return err
		}
		results = []notify.Result{res}
	} else {
		var err error
		if results, err = a.Notify.SendAll(r.Context(), m); err != nil {
			if errors.Is(err, notify.ErrNoDevices) {
				return &notify.ValidationError{Message: "No devices have notifications turned on yet."}
			}
			return err
		}
	}
	type outcome struct {
		ID      string `json:"id"`
		Label   string `json:"label"`
		Service string `json:"service"`
		OK      bool   `json:"ok"`
		Gone    bool   `json:"gone"`
		Error   string `json:"error,omitempty"`
	}
	out := make([]outcome, 0, len(results))
	sent := 0
	for _, res := range results {
		o := outcome{ID: res.Subscription.ID, Label: res.Subscription.Label, Service: res.Subscription.Service,
			OK: res.Err == nil, Gone: res.Gone}
		if res.Err != nil {
			o.Error = res.Err.Error()
		} else {
			sent++
		}
		out = append(out, o)
	}
	writeJSON(w, map[string]any{"sent": sent, "results": out})
	return nil
}

// apiNotifySettings saves the reminder settings.
func (a *App) apiNotifySettings(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Enabled     *bool `json:"enabled"`
		DefaultDays *int  `json:"default_days"`
		Hour        *int  `json:"hour"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	set := map[string]any{}
	if in.Enabled != nil {
		set[notify.KeyRemindersEnabled] = *in.Enabled
	}
	if in.DefaultDays != nil {
		if *in.DefaultDays < 0 || *in.DefaultDays > 3650 {
			return &notify.ValidationError{Message: "The reminder interval must be between 0 and 3650 days"}
		}
		set[ledger.SettingReminderDays] = *in.DefaultDays
	}
	if in.Hour != nil {
		if *in.Hour < 0 || *in.Hour > 23 {
			return &notify.ValidationError{Message: "The time must be an hour from 0 to 23"}
		}
		set[notify.KeyReminderHour] = *in.Hour
	}
	err := a.DB.WithTx(func(tx *sql.Tx) error {
		for k, v := range set {
			if err := a.Repos.Settings.Set(tx, k, v); err != nil {
				return err
			}
		}
		// A new time today counts again: moving the hour later shouldn't
		// skip a day.
		if in.Hour != nil {
			if _, err := tx.Exec(`DELETE FROM settings WHERE key = 'reminder_last_run'`); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	writeJSON(w, a.Reminders.Settings())
	return nil
}

// deviceLabel names a device from its user agent, for the device list.
func deviceLabel(ua string) string {
	os := ""
	switch {
	case strings.Contains(ua, "iPhone"):
		os = "iPhone"
	case strings.Contains(ua, "iPad"):
		os = "iPad"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Mac OS X"):
		os = "Mac"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	browser := ""
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	switch {
	case os != "" && browser != "":
		return browser + " on " + os
	case os != "":
		return os
	case browser != "":
		return browser
	}
	return "A device"
}
