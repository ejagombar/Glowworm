package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"glowworm/internal/config"
	"glowworm/internal/crypto"
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
