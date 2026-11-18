package server

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A device turns notifications on, gets a test, is listed, and is removed;
// reminder settings are checked and saved.
func TestNotificationsAPI(t *testing.T) {
	a, c := newLockTestApp(t)
	var got atomic.Int32
	push := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer push.Close()
	a.Notify.Client = push.Client()

	var info struct {
		PublicKey string `json:"public_key"`
		Devices   []struct {
			ID         string `json:"id"`
			Label      string `json:"label"`
			ThisDevice bool   `json:"this_device"`
			Endpoint   string `json:"endpoint"`
		} `json:"devices"`
		Settings struct {
			Enabled     bool `json:"enabled"`
			DefaultDays int  `json:"default_days"`
			Hour        int  `json:"hour"`
			RepeatDays  int  `json:"repeat_days"`
		} `json:"settings"`
	}
	get := func(q string) {
		t.Helper()
		rec := c.do("GET", "/api/v1/notifications"+q, "", "")
		if rec.Code != 200 {
			t.Fatalf("GET: %d %s", rec.Code, rec.Body)
		}
		info.Devices = nil
		if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
	}
	get("")
	if info.PublicKey == "" || len(info.Devices) != 0 {
		t.Fatalf("%+v", info)
	}
	if !info.Settings.Enabled || info.Settings.DefaultDays != 30 || info.Settings.Hour != 9 || info.Settings.RepeatDays != 7 {
		t.Errorf("defaults %+v", info.Settings)
	}

	if rec := c.do("POST", "/api/v1/notifications/test", "application/json", `{}`); rec.Code != 400 {
		t.Errorf("test with no devices: %d", rec.Code)
	}

	key, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	endpoint := push.URL + "/device/1"
	body, _ := json.Marshal(map[string]any{
		"endpoint": endpoint,
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(auth),
		},
	})
	if rec := c.do("POST", "/api/v1/notifications/subscribe", "application/json", string(body)); rec.Code != 200 {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	get("?endpoint=" + endpoint)
	if len(info.Devices) != 1 || !info.Devices[0].ThisDevice || info.Devices[0].Label == "" {
		t.Fatalf("devices %+v", info.Devices)
	}
	if info.Devices[0].Endpoint != "" {
		t.Error("the endpoint is sent back to the page")
	}

	rec := c.do("POST", "/api/v1/notifications/test", "application/json", `{"endpoint":"`+endpoint+`"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"sent":1`) || got.Load() != 1 {
		t.Fatalf("test: %d %s (push service got %d)", rec.Code, rec.Body, got.Load())
	}
	// Nothing out of date, so no reminder to try.
	if rec := c.do("POST", "/api/v1/notifications/test", "application/json", `{"reminder":true}`); rec.Code != 400 {
		t.Errorf("reminder with nothing due: %d %s", rec.Code, rec.Body)
	}

	if rec := c.do("PUT", "/api/v1/notifications/settings", "application/json", `{"hour":25}`); rec.Code != 400 {
		t.Errorf("hour 25: %d", rec.Code)
	}
	rec = c.do("PUT", "/api/v1/notifications/settings", "application/json", `{"enabled":false,"default_days":60,"hour":18,"repeat_days":0}`)
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	get("")
	if s := info.Settings; s.Enabled || s.DefaultDays != 60 || s.Hour != 18 || s.RepeatDays != 0 {
		t.Errorf("saved %+v", s)
	}
	// The savings page and the ledger follow the default interval.
	b, err := a.book(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b.ReminderDays != 60 {
		t.Errorf("ledger's reminder days %d", b.ReminderDays)
	}

	if rec := c.do("DELETE", "/api/v1/notifications/devices/"+info.Devices[0].ID, "", ""); rec.Code != 200 {
		t.Fatalf("remove: %d", rec.Code)
	}
	get("")
	if len(info.Devices) != 0 {
		t.Fatal("device still listed")
	}
	if settings := c.do("GET", "/settings", "", "").Body.String(); !strings.Contains(settings, `id="notifySettings"`) {
		t.Error("Settings has no Reminders panel")
	}
}

func TestDeviceLabel(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Mobile Safari/537.36":                                "Chrome on Android",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0":                                                        "Firefox on Windows",
		"": "A device",
	} {
		if got := deviceLabel(ua); got != want {
			t.Errorf("%q: %q, want %q", ua, got, want)
		}
	}
}
