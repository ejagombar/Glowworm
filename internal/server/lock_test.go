package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"glowworm/internal/config"
	"glowworm/internal/crypto"
	"glowworm/internal/store/repos"
)

func newLockTestApp(t *testing.T) (*App, *testClient) {
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
	c := &testClient{t: t, h: a.Router(), cookies: map[string]*http.Cookie{}}
	if loc := c.login("longpassword1"); loc != "/" {
		t.Fatalf("login went to %q", loc)
	}
	return a, c
}

// turnOnLock does what setting up Face ID leaves behind: a passkey and the
// device's lock cookie.
func turnOnLock(t *testing.T, a *App, c *testClient) {
	t.Helper()
	u, err := a.Repos.Users.ByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Repos.Passkeys.Add(&repos.Passkey{UserID: u.ID, CredentialID: "abc", Credential: "{}", Name: "iPhone"}); err != nil {
		t.Fatal(err)
	}
	c.cookies[deviceLockCookie] = &http.Cookie{Name: deviceLockCookie, Value: encodeToken(a.Secret, "lock|"+u.ID)}
}

// expireUnlock stands in for the lock time passing with no requests.
func expireUnlock(t *testing.T, a *App, c *testClient) {
	t.Helper()
	sess, ok := readSession(requestWith(c), a.Secret)
	if !ok {
		t.Fatal("no session")
	}
	rec := httptest.NewRecorder()
	writeUnlock(rec, requestWith(c), a.Secret, sess.Nonce, time.Now().Add(-time.Second))
	for _, ck := range rec.Result().Cookies() {
		c.cookies[ck.Name] = ck
	}
}

func requestWith(c *testClient) *http.Request {
	req := httptest.NewRequest("GET", "/", nil)
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	return req
}

func TestLockOnlyOnDevicesThatTurnItOn(t *testing.T) {
	a, c := newLockTestApp(t)
	expireUnlock(t, a, c)
	if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != 200 {
		t.Fatalf("no lock on this device, but got %d", code)
	}
	// A lock cookie with no passkeys left doesn't lock either.
	u, _ := a.Repos.Users.ByUsername("owner")
	c.cookies[deviceLockCookie] = &http.Cookie{Name: deviceLockCookie, Value: encodeToken(a.Secret, "lock|"+u.ID)}
	if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != 200 {
		t.Fatalf("no passkeys, but got %d", code)
	}
}

func TestLockedUntilUnlocked(t *testing.T) {
	a, c := newLockTestApp(t)
	turnOnLock(t, a, c)
	// Signing in just now counts as unlocking.
	if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != 200 {
		t.Fatalf("just signed in: %d", code)
	}

	expireUnlock(t, a, c)
	if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != http.StatusLocked {
		t.Fatalf("API while locked: %d", code)
	}
	rec := c.do("GET", "/transactions?q=x", "", "")
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/unlock?next=%2Ftransactions%3Fq%3Dx" {
		t.Fatalf("page while locked: %d %q", rec.Code, loc)
	}
	if code := c.do("GET", "/unlock?next=/transactions", "", "").Code; code != 200 {
		t.Fatalf("unlock page: %d", code)
	}

	form := url.Values{"password": {"wrongpassword"}, "next": {"/transactions"}, "csrf_token": {c.cookies[csrfCookie].Value}}
	rec = c.do("POST", "/unlock/password", "application/x-www-form-urlencoded", form.Encode())
	if loc := rec.Header().Get("Location"); loc != "/unlock?next=%2Ftransactions&e=1" {
		t.Fatalf("wrong password went to %q", loc)
	}
	if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != http.StatusLocked {
		t.Fatalf("after a wrong password: %d", code)
	}

	form.Set("password", "longpassword1")
	rec = c.do("POST", "/unlock/password", "application/x-www-form-urlencoded", form.Encode())
	if loc := rec.Header().Get("Location"); loc != "/transactions" {
		t.Fatalf("password unlock went to %q", loc)
	}
	if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != 200 {
		t.Fatalf("after unlocking: %d", code)
	}
}

func TestUnlockBelongsToItsSession(t *testing.T) {
	a, c := newLockTestApp(t)
	turnOnLock(t, a, c)
	unlock := c.cookies[unlockCookie]
	// Signing in again (as after the session runs out) is a new session; the
	// old session's unlock doesn't carry over to it, but the new sign-in
	// unlocks by itself.
	c.login("longpassword1")
	if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != 200 {
		t.Fatalf("after signing in again: %d", code)
	}
	c.cookies[unlockCookie] = unlock
	if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != http.StatusLocked {
		t.Fatalf("another session's unlock was accepted: %d", code)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/transactions?q=1": "/transactions?q=1", "": "/", "https://evil.example": "/",
		"//evil.example": "/", "/\\evil.example": "/", "/unlock?next=/": "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPasskeyHost(t *testing.T) {
	for _, tc := range []struct {
		host  string
		https bool
		ok    bool
	}{
		{"glowworm.example.com", true, true},
		{"glowworm.example.com", false, false},
		{"10.0.0.5:9101", true, false},
		{"localhost:8080", false, true},
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = tc.host
		if tc.https {
			req.Header.Set("X-Forwarded-Proto", "https")
		}
		if _, ok := passkeyHost(req); ok != tc.ok {
			t.Errorf("passkeyHost(%q, https=%v) = %v", tc.host, tc.https, ok)
		}
	}
}
