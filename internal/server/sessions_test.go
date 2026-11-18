package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ejagombar/Glowworm/internal/config"
	"github.com/ejagombar/Glowworm/internal/crypto"
)

type testClient struct {
	t       *testing.T
	h       http.Handler
	cookies map[string]*http.Cookie
}

func (c *testClient) do(method, path, contentType, body string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.1:1234"
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
		if ck.Name == csrfCookie {
			req.Header.Set("X-CSRF-Token", ck.Value)
		}
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		c.cookies[ck.Name] = ck
	}
	return rec
}

func (c *testClient) login(password string) string {
	c.t.Helper()
	form := url.Values{"username": {"owner"}, "password": {password}}.Encode()
	return c.do("POST", "/login", "application/x-www-form-urlencoded", form).Header().Get("Location")
}

func newTestApp(t *testing.T) http.Handler {
	t.Helper()
	secret, err := crypto.LoadOrCreate("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AuthMode: "password", Timezone: "UTC", OwnerUsername: "owner", OwnerPassword: "longpassword1"}
	a, err := New(cfg, newSeriesTestDB(t), secret, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return a.Router()
}

// Changing the password signs out every other session; the one that changed
// it carries on.
func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	h := newTestApp(t)
	laptop := &testClient{t: t, h: h, cookies: map[string]*http.Cookie{}}
	phone := &testClient{t: t, h: h, cookies: map[string]*http.Cookie{}}
	for _, c := range []*testClient{laptop, phone} {
		if loc := c.login("longpassword1"); loc != "/" {
			t.Fatalf("login went to %q", loc)
		}
		if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != 200 {
			t.Fatalf("signed-in request: %d", code)
		}
	}
	rec := laptop.do("POST", "/api/v1/account/password", "application/json",
		`{"current_password":"longpassword1","new_password":"anotherpassword2"}`)
	if rec.Code != 200 {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body)
	}
	if code := laptop.do("GET", "/api/v1/accounts", "", "").Code; code != 200 {
		t.Errorf("the session that changed the password was signed out: %d", code)
	}
	if code := phone.do("GET", "/api/v1/accounts", "", "").Code; code != 401 {
		t.Errorf("another session survived the password change: %d", code)
	}
}

// Only failed sign-ins count towards the limit.
func TestLoginLimitCountsFailuresOnly(t *testing.T) {
	h := newTestApp(t)
	c := &testClient{t: t, h: h, cookies: map[string]*http.Cookie{}}
	for i := 0; i < 8; i++ {
		if loc := c.login("longpassword1"); loc != "/" {
			t.Fatalf("sign-in %d went to %q", i+1, loc)
		}
	}
	for i := 0; i < 5; i++ {
		if loc := c.login("wrong"); loc != "/login?e=1" {
			t.Fatalf("failure %d went to %q", i+1, loc)
		}
	}
	if loc := c.login("longpassword1"); loc != "/login?e=2" {
		t.Errorf("after 5 failures, went to %q", loc)
	}
}

// Saves bump the data version other devices compare against; reads and
// failed saves don't. An edit made against an old copy of a transaction is
// refused rather than overwriting the newer change.
func TestDataVersionAndEditConflicts(t *testing.T) {
	h := newTestApp(t)
	c := &testClient{t: t, h: h, cookies: map[string]*http.Cookie{}}
	c.login("longpassword1")
	version := func() string {
		rec := c.do("GET", "/api/v1/version", "", "")
		var b struct{ Version string }
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || b.Version == "" {
			t.Fatalf("version: %s %v", rec.Body, err)
		}
		return b.Version
	}
	v0 := version()
	rec := c.do("POST", "/api/v1/accounts", "application/json", `{"name":"Main","type":"debit"}`)
	if rec.Code >= 300 {
		t.Fatalf("create account: %d %s", rec.Code, rec.Body)
	}
	v1 := version()
	if v1 == v0 {
		t.Error("a save didn't change the version")
	}
	if got := rec.Header().Get(versionHeader); got != v1 {
		t.Errorf("the save's response carried version %q, want %q", got, v1)
	}
	var acct struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &acct)
	c.do("GET", "/api/v1/accounts", "", "")
	if c.do("POST", "/api/v1/accounts", "application/json", `{"name":""}`).Code != 400 || version() != v1 {
		t.Error("a read or a refused save changed the version")
	}
	// POSTs that only read (listing AI models, checking a date format)
	// mustn't make other pages refresh.
	c.do("POST", "/api/v1/settings/ai/models", "application/json", `{}`)
	if rec := c.do("POST", "/api/v1/imports/preview-date", "application/json", `{"sample":"02/06/2026"}`); rec.Code != 200 {
		t.Fatalf("preview date: %d %s", rec.Code, rec.Body)
	}
	if version() != v1 {
		t.Error("a read-only POST changed the version")
	}

	rec = c.do("POST", "/api/v1/transactions", "application/json",
		`{"account_id":"`+acct.ID+`","date":"2026-05-01","description":"Lidl","amount_minor":-500}`)
	if rec.Code >= 300 {
		t.Fatalf("create transaction: %d %s", rec.Code, rec.Body)
	}
	var tx struct {
		ID        string
		UpdatedAt string `json:"updated_at"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tx)
	if code := c.do("PATCH", "/api/v1/transactions/"+tx.ID, "application/json",
		`{"description":"Lidl GB","expected_updated_at":"`+tx.UpdatedAt+`"}`).Code; code != 200 {
		t.Fatalf("edit against the current copy: %d", code)
	}
	rec = c.do("PATCH", "/api/v1/transactions/"+tx.ID, "application/json",
		`{"description":"Lidl UK","expected_updated_at":"2000-01-01T00:00:00Z"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "changed somewhere else") {
		t.Errorf("edit against an old copy: %d %s", rec.Code, rec.Body)
	}

	if rec := c.do("PATCH", "/api/v1/transactions/"+tx.ID, "application/json", `{"description":`); rec.Code != 400 {
		t.Errorf("malformed body: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("GET", "/api/v1/transactions/missing", "", ""); rec.Code != 404 || !strings.Contains(rec.Body.String(), "another device") {
		t.Errorf("missing transaction: %d %s", rec.Code, rec.Body)
	}
}
