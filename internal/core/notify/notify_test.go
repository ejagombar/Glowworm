package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"glowworm/internal/core/ledger"
	"glowworm/internal/crypto"
	"glowworm/internal/store"
)

// pushService stands in for a browser's push service: it records what it
// was sent and answers with the status set for the device.
type pushService struct {
	*httptest.Server
	mu     sync.Mutex
	got    map[string]int // path -> deliveries
	status map[string]int
	auth   []string
}

func newPushService(t *testing.T) *pushService {
	p := &pushService{got: map[string]int{}, status: map[string]int{}}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.got[r.URL.Path]++
		p.auth = append(p.auth, r.Header.Get("Authorization"))
		if st := p.status[r.URL.Path]; st != 0 {
			w.WriteHeader(st)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *pushService) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got[path]
}

// deviceKeys are the keys a browser makes for a subscription.
func deviceKeys(t *testing.T) (p256dh, auth string) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := make([]byte, 16)
	_, _ = rand.Read(a)
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(a)
}

func newService(t *testing.T) (*Service, *pushService) {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	secret, err := crypto.Parse(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	p := newPushService(t)
	return &Service{DB: db, Secret: secret, Client: p.Client()}, p
}

func subscribe(t *testing.T, s *Service, p *pushService, path, label string) *Subscription {
	t.Helper()
	k, a := deviceKeys(t)
	sub, err := s.Subscribe(p.URL+path, k, a, label, "https://money.example")
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func TestKeysAreMadeOnceAndKeptEncrypted(t *testing.T) {
	s, _ := newService(t)
	pub, err := s.PublicKey()
	if err != nil || pub == "" {
		t.Fatalf("public key %q, %v", pub, err)
	}
	again, _ := s.PublicKey()
	if again != pub {
		t.Fatal("the key changed between calls")
	}
	_, priv, err := s.keys()
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, keyVAPIDPrivate).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, priv) {
		t.Fatal("the private key is stored in the clear")
	}
}

func TestSubscribeValidatesAndRefreshes(t *testing.T) {
	s, p := newService(t)
	k, a := deviceKeys(t)
	for _, bad := range []string{"", "http://push.example/x", "not a url"} {
		if _, err := s.Subscribe(bad, k, a, "", ""); err == nil {
			t.Errorf("subscribed %q", bad)
		}
	}
	if _, err := s.Subscribe(p.URL+"/a", "", a, "", ""); err == nil {
		t.Error("subscribed without keys")
	}
	first := subscribe(t, s, p, "/a", "Phone")
	second := subscribe(t, s, p, "/a", "Phone again")
	if first.ID != second.ID || second.Label != "Phone again" {
		t.Fatalf("resubscribing made %+v then %+v", first, second)
	}
	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("%d devices, want 1", len(list))
	}
}

func TestSendRecordsAndForgetsGoneDevices(t *testing.T) {
	s, p := newService(t)
	subscribe(t, s, p, "/ok", "Laptop")
	subscribe(t, s, p, "/gone", "Old phone")
	subscribe(t, s, p, "/broken", "Tablet")
	p.status["/gone"] = http.StatusGone
	p.status["/broken"] = http.StatusTooManyRequests

	res, err := s.SendAll(context.Background(), Message{Title: "Hi", Body: "Test", Tag: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("%d results", len(res))
	}
	for _, r := range res {
		switch r.Subscription.Label {
		case "Laptop":
			if r.Err != nil {
				t.Errorf("laptop: %v", r.Err)
			}
		case "Old phone":
			if !r.Gone {
				t.Error("410 wasn't treated as gone")
			}
		case "Tablet":
			if r.Err == nil || r.Gone {
				t.Errorf("429: err %v gone %v", r.Err, r.Gone)
			}
		}
	}
	if !strings.HasPrefix(p.auth[0], "vapid t=") {
		t.Errorf("authorization %q", p.auth[0])
	}
	list, _ := s.List()
	if len(list) != 2 {
		t.Fatalf("%d devices left, want 2 (the gone one forgotten)", len(list))
	}
	for _, d := range list {
		switch d.Label {
		case "Laptop":
			if d.LastSentAt == "" || d.LastError != "" {
				t.Errorf("laptop recorded %+v", d)
			}
		case "Tablet":
			if !strings.Contains(d.LastError, "429") {
				t.Errorf("tablet's error %q", d.LastError)
			}
		}
	}
	if _, err := s.SendTo(context.Background(), p.URL+"/nobody", Message{}); err == nil {
		t.Error("sent to a device that isn't subscribed")
	}
	_ = s.Unsubscribe(p.URL + "/ok")
	_ = s.Unsubscribe(p.URL + "/broken")
	if _, err := s.SendAll(context.Background(), Message{}); err != ErrNoDevices {
		t.Errorf("with no devices: %v", err)
	}
}

func TestTopic(t *testing.T) {
	if got := topic("balance-reminder"); got != "balance-reminder" {
		t.Error(got)
	}
	if got := topic("a b/c" + strings.Repeat("x", 40)); got != "abc"+strings.Repeat("x", 29) {
		t.Error(got)
	}
}

// remindersAt is a reminder job on s's database at a settable time.
func remindersAt(s *Service, now *time.Time) *Reminders {
	return &Reminders{
		DB: s.DB, Notify: s,
		Book: func(ctx context.Context) (*ledger.Book, error) {
			return ledger.Load(ctx, s.DB.DB, now.Format("2006-01-02"))
		},
		Now: func() time.Time { return *now },
	}
}

func TestRemindersOncePerDayAndRepeat(t *testing.T) {
	s, p := newService(t)
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	ts := "2026-01-01T00:00:00Z"
	must(`INSERT INTO accounts (id, name, type, color, currency, opening_balance_minor, created_at, updated_at) VALUES
		('isa','ISA','savings','#000','GBP',0,?,?),
		('cur','Current','debit','#000','GBP',0,?,?),
		('bond','Bond','savings','#000','GBP',0,?,?)`, ts, ts, ts, ts, ts, ts)
	must(`UPDATE accounts SET balance_reminder_days = 0 WHERE id = 'bond'`)
	must(`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at)
		VALUES ('v1','isa',100000,'2026-03-01','manual',?,?)`, ts, ts)
	subscribe(t, s, p, "/phone", "Phone")

	now := time.Date(2026, 3, 20, 8, 0, 0, 0, time.UTC)
	r := remindersAt(s, &now)
	tick := func(want bool) {
		t.Helper()
		sent, err := r.Tick(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if sent != want {
			t.Fatalf("%s: sent %v, want %v", now.Format(time.DateTime), sent, want)
		}
	}
	// 19 days old: not due yet (default 30), and before 9am anyway.
	tick(false)
	now = time.Date(2026, 3, 31, 8, 59, 0, 0, time.UTC)
	tick(false) // 30 days old, but not 9 yet
	now = now.Add(2 * time.Minute)
	tick(true)
	if p.count("/phone") != 1 {
		t.Fatalf("%d sent", p.count("/phone"))
	}
	tick(false) // once a day
	now = now.AddDate(0, 0, 1)
	tick(false) // reminded yesterday; repeats weekly
	now = time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	tick(true) // a week on
	// Updated: nothing due until it's old again.
	must(`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at)
		VALUES ('v2','isa',101000,'2026-04-07','manual',?,?)`, ts, ts)
	now = now.AddDate(0, 0, 1)
	tick(false)
	now = time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
	tick(true) // old again: reminded at once, though the last was under a week before the update
	// A balance entered later but dated earlier still starts afresh.
	must(`INSERT INTO valuations (id, account_id, value_minor, date, source, created_at, updated_at)
		VALUES ('v3','isa',102000,'2026-05-06','manual',?,?)`, ts, ts)
	must(`INSERT INTO settings (key, value, updated_at) VALUES ('reminder_repeat_days', '0', ?)`, ts)
	now = time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	tick(false) // 29 days
	now = now.AddDate(0, 0, 1)
	tick(true) // 30 days; the last reminder (7 May) was before the balance went out of date
	now = now.AddDate(0, 0, 1)
	tick(false) // no repeat
	// The current account and the bond are never included.
	r.Now = func() time.Time { return now }
	due, err := r.Due(context.Background(), ReminderSettings{RepeatDays: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("due after reminding with no repeat: %d", len(due))
	}
	// Turned off: nothing.
	must(`INSERT INTO settings (key, value, updated_at) VALUES ('reminders_enabled', 'false', ?)`, ts)
	now = now.AddDate(0, 1, 0)
	tick(false)
}

func TestReminderMessage(t *testing.T) {
	one := ReminderMessage([]ledger.Status{{Account: &ledger.Account{Name: "ISA"}, DaysSince: 45}})
	if one.Title != "Time to update ISA" || !strings.Contains(one.Body, "45 days ago") || one.URL != "/savings" {
		t.Errorf("%+v", one)
	}
	two := ReminderMessage([]ledger.Status{
		{Account: &ledger.Account{Name: "ISA"}, DaysSince: 45},
		{Account: &ledger.Account{Name: "Pension"}, DaysSince: -1},
	})
	if !strings.Contains(two.Body, "ISA (45 days), Pension (never)") {
		t.Errorf("%+v", two)
	}
}
