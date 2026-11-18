package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ejagombar/Glowworm/internal/config"
	"github.com/ejagombar/Glowworm/internal/crypto"
)

// The account settings form loads one account, shows its bank details, and
// sends back only what was changed.
func TestTheAccountFormSeesBankDetailsAndKeepsThoseItDoesNotSend(t *testing.T) {
	a, c := newLockTestApp(t)
	rec := c.do("POST", "/api/v1/accounts", "application/json",
		`{"name":"Main","type":"debit","currency":"GBP","account_number":"12345678","sort_code":"11-22-33","iban":"GB00TEST","bic":"TESTGB","notes":"bills"}`)
	if rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil || made.ID == "" {
		t.Fatalf("create returned %s", rec.Body)
	}

	rec = c.do("GET", "/api/v1/accounts/"+made.ID, "", "")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	for key, want := range map[string]string{"name": "Main", "account_number": "12345678", "sort_code": "11-22-33", "iban": "GB00TEST", "bic": "TESTGB", "notes": "bills"} {
		if got[key] != want {
			t.Errorf("get: %s = %v, want %s", key, got[key], want)
		}
	}
	// Only there: the list of accounts still leaves them out.
	if list := c.do("GET", "/api/v1/accounts", "", "").Body.String(); !strings.Contains(list, made.ID) || strings.Contains(list, "GB00TEST") || strings.Contains(list, "12345678") {
		t.Errorf("the account list gives bank details away, or is missing the account: %s", list)
	}

	stored := func() string {
		t.Helper()
		var d [5]string
		if err := a.DB.QueryRow(`SELECT account_number, sort_code, iban, bic, notes FROM accounts WHERE id = ?`, made.ID).
			Scan(&d[0], &d[1], &d[2], &d[3], &d[4]); err != nil {
			t.Fatal(err)
		}
		return strings.Join(d[:], "|")
	}
	// A rename from the form: no bank details changed, so none are sent.
	if rec := c.do("PATCH", "/api/v1/accounts/"+made.ID, "application/json", `{"name":"Joint","type":"debit","opening_balance_minor":0,"opening_balance_date":""}`); rec.Code != 200 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	if got, want := stored(), "12345678|11-22-33|GB00TEST|TESTGB|bills"; got != want {
		t.Fatalf("after a rename: %s, want %s", got, want)
	}
	// One changed and one cleared.
	if rec := c.do("PATCH", "/api/v1/accounts/"+made.ID, "application/json", `{"name":"Joint","type":"debit","iban":"GB99NEW","bic":""}`); rec.Code != 200 {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	if got, want := stored(), "12345678|11-22-33|GB99NEW||bills"; got != want {
		t.Fatalf("after changing the IBAN and clearing the BIC: %s, want %s", got, want)
	}
}

// Deleting the account sends the owner straight to making a new one, the
// setup token filled in, and that page takes it.
func TestInstanceDeleteLeadsToSetup(t *testing.T) {
	a, c := newLockTestApp(t)
	a.Cfg.SetupToken = "tok en/1"
	rec := c.do("POST", "/api/v1/instance/delete", "application/json", `{"password":"longpassword1","confirm":"DELETE"}`)
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Next string `json:"next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Next != "/setup?token=tok+en%2F1" {
		t.Fatalf("next %q", out.Next)
	}
	if rec := c.do("GET", out.Next, "", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `action="/setup?token=`) {
		t.Errorf("setup page with the token: %d", rec.Code)
	}
	if rec := c.do("GET", "/setup", "", ""); rec.Code != 403 {
		t.Errorf("setup without the token: %d", rec.Code)
	}
	// With the token switched off, plain /setup.
	a.Cfg.SetupToken = "off"
	if got := a.setupURL(); got != "/setup" {
		t.Errorf("no token: %q", got)
	}
}

// With no token configured, a new instance makes one: the setup page needs
// it, the link carries it, and it's gone once the account exists. Deleting
// the account makes a fresh one.
func TestSetupTokenMadeOnFirstRun(t *testing.T) {
	secret, err := crypto.LoadOrCreate("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AuthMode: "password", Timezone: "UTC"}
	a, err := New(cfg, newSeriesTestDB(t), secret, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := &testClient{t: t, h: a.Router(), cookies: map[string]*http.Cookie{}}

	link := a.setupURL()
	token := strings.TrimPrefix(link, "/setup?token=")
	if token == link || len(token) < 20 {
		t.Fatalf("setup link %q", link)
	}
	if again := a.setupURL(); again != link {
		t.Errorf("the token changed: %q then %q", link, again)
	}
	if rec := c.do("GET", "/setup", "", ""); rec.Code != 403 {
		t.Errorf("setup without the token: %d", rec.Code)
	}
	if rec := c.do("GET", "/setup?token=wrong", "", ""); rec.Code != 403 {
		t.Errorf("setup with the wrong token: %d", rec.Code)
	}
	if rec := c.do("GET", link, "", ""); rec.Code != 200 {
		t.Fatalf("setup with the token: %d", rec.Code)
	}
	form := url.Values{"username": {"owner"}, "password": {"longpassword1"}, "password_confirm": {"longpassword1"},
		"csrf_token": {c.cookies[csrfCookie].Value}}.Encode()
	if rec := c.do("POST", link, "application/x-www-form-urlencoded", form); rec.Code != 302 || rec.Header().Get("Location") != "/" {
		t.Fatalf("create account: %d %s", rec.Code, rec.Body)
	}
	var n int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'setup_token'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("token still stored after setup (%d, %v)", n, err)
	}

	rec := c.do("POST", "/api/v1/instance/delete", "application/json", `{"password":"longpassword1","confirm":"DELETE"}`)
	var out struct {
		Next string `json:"next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if out.Next == link || !strings.HasPrefix(out.Next, "/setup?token=") {
		t.Errorf("after deleting, setup link %q (was %q)", out.Next, link)
	}
	if rec := c.do("GET", out.Next, "", ""); rec.Code != 200 {
		t.Errorf("new setup link: %d", rec.Code)
	}
}

// /healthz names this run of the server, for the public address check.
func TestHealthzInstance(t *testing.T) {
	a, c := newLockTestApp(t)
	body := c.do("GET", "/healthz", "", "").Body.String()
	if a.Instance == "" || !strings.Contains(body, `"instance":"`+a.Instance+`"`) {
		t.Errorf("healthz %s, instance %q", body, a.Instance)
	}
}
