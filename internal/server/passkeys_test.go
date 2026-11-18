package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/ejagombar/Glowworm/internal/config"
	"github.com/ejagombar/Glowworm/internal/crypto"
	"github.com/ejagombar/Glowworm/internal/store/repos"
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

func requestWith(c *testClient) *http.Request {
	req := httptest.NewRequest("GET", "/", nil)
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	return req
}

// A device that turned on the old app lock (v1.0.0) carries on unlocked,
// and loses the lock's cookies.
func TestOldAppLockNoLongerLocks(t *testing.T) {
	a, c := newLockTestApp(t)
	u, err := a.Repos.Users.ByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Repos.Passkeys.Add(&repos.Passkey{UserID: u.ID, CredentialID: "abc", Credential: "{}", Name: "iPhone"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Repos.Settings.Set(nil, "lock_after_minutes", 0); err != nil {
		t.Fatal(err)
	}
	sess, _ := readSession(requestWith(c), a.Secret)
	c.cookies[deviceLockCookie] = &http.Cookie{Name: deviceLockCookie, Value: encodeToken(a.Secret, "lock|"+u.ID)}
	c.cookies[unlockCookie] = &http.Cookie{Name: unlockCookie, Value: encodeToken(a.Secret, fmt.Sprintf("unlock|%s|%d", sess.Nonce, time.Now().Add(-time.Hour).Unix()))}

	if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != 200 {
		t.Fatalf("API with the old lock's cookies: %d", code)
	}
	if c.cookies[deviceLockCookie].MaxAge >= 0 || c.cookies[unlockCookie].MaxAge >= 0 {
		t.Fatal("the old lock's cookies weren't cleared")
	}
	if code := c.do("GET", "/transactions", "", "").Code; code != 200 {
		t.Fatalf("page: %d", code)
	}
	// Bookmarks and old pages that went to /unlock carry straight on.
	rec := c.do("GET", "/unlock?next=%2Ftransactions%3Fq%3Dx", "", "")
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "/transactions?q=x" {
		t.Fatalf("/unlock: %d %q", rec.Code, loc)
	}
	if code := c.do("GET", "/settings", "", "").Code; code != 200 {
		t.Fatalf("settings: %d", code)
	}
}

// A session in use never runs out: once a day a request starts its time
// over, keeping who it is and its CSRF token. Sessions from v1.0.0 (30 days)
// are lengthened the same way.
func TestSessionRenews(t *testing.T) {
	a, c := newLockTestApp(t)
	sess, ok := readSession(requestWith(c), a.Secret)
	if !ok {
		t.Fatal("no session")
	}
	if d := time.Until(sess.Expires); d < sessionTTL-time.Minute {
		t.Fatalf("new session lasts %v", d)
	}
	// Used again straight away: nothing to renew.
	before := c.cookies[sessionCookie].Value
	c.do("GET", "/api/v1/accounts", "", "")
	if c.cookies[sessionCookie].Value != before {
		t.Fatal("renewed a session issued just now")
	}

	old := fmt.Sprintf("%s|%d|%s|%s", sess.UserID, time.Now().Add(3*24*time.Hour).Unix(), sess.Nonce, sess.Fingerprint)
	c.cookies[sessionCookie] = &http.Cookie{Name: sessionCookie, Value: encodeToken(a.Secret, old)}
	csrf := c.cookies[csrfCookie].Value
	if code := c.do("GET", "/api/v1/accounts", "", "").Code; code != 200 {
		t.Fatalf("old session: %d", code)
	}
	renewed, ok := readSession(requestWith(c), a.Secret)
	if !ok || renewed.Nonce != sess.Nonce || renewed.UserID != sess.UserID {
		t.Fatalf("renewed session %+v, was %+v", renewed, sess)
	}
	if d := time.Until(renewed.Expires); d < sessionTTL-time.Minute {
		t.Fatalf("renewed session lasts %v", d)
	}
	if ck := c.cookies[csrfCookie]; ck.Value != csrf || ck.MaxAge != int(sessionTTL.Seconds()) {
		t.Fatalf("csrf cookie %q max-age %d", ck.Value, ck.MaxAge)
	}
}

// Signing in with a passkey starts without a session, and only where
// passkeys can work. A response that doesn't check out signs nobody in.
func TestPasskeyLogin(t *testing.T) {
	_, owner := newLockTestApp(t)
	c := &testClient{t: t, h: owner.h, cookies: map[string]*http.Cookie{}}

	if code := c.do("POST", "/login/passkey/begin", "application/json", "{}").Code; code < 400 || code >= 500 {
		t.Fatalf("begin at an IP address over http: %d", code)
	}

	host := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = "localhost:8080"
		req.RemoteAddr = "192.0.2.1:1234"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://localhost:8080")
		for _, ck := range c.cookies {
			req.AddCookie(ck)
		}
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		for _, ck := range rec.Result().Cookies() {
			c.cookies[ck.Name] = ck
		}
		return rec
	}
	rec := host("POST", "/login/passkey/begin", "{}")
	if rec.Code != 200 {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body)
	}
	var opts struct {
		PublicKey struct {
			Challenge        string `json:"challenge"`
			RPID             string `json:"rpId"`
			UserVerification string `json:"userVerification"`
			AllowCredentials []any  `json:"allowCredentials"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &opts); err != nil {
		t.Fatal(err)
	}
	if opts.PublicKey.Challenge == "" || opts.PublicKey.RPID != "localhost" || opts.PublicKey.UserVerification != "required" || len(opts.PublicKey.AllowCredentials) != 0 {
		t.Fatalf("options: %+v", opts.PublicKey)
	}
	if c.cookies[ceremonyCookie] == nil || c.cookies[ceremonyCookie].Value == "" {
		t.Fatal("no ceremony cookie")
	}

	rec = host("POST", "/login/passkey/finish", `{"id":"abc","rawId":"abc","type":"public-key","response":{}}`)
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("finish with a made-up passkey: %d %s", rec.Code, rec.Body)
	}
	if ck := c.cookies[sessionCookie]; ck != nil && ck.Value != "" {
		t.Fatal("a made-up passkey got a session")
	}
}

// Usernames can be as short as two letters.
func TestTwoLetterUsername(t *testing.T) {
	secret, err := crypto.LoadOrCreate("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AuthMode: "password", Timezone: "UTC", OwnerUsername: "ed", OwnerPassword: "longpassword1"}
	a, err := New(cfg, newSeriesTestDB(t), secret, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := &testClient{t: t, h: a.Router(), cookies: map[string]*http.Cookie{}}
	form := url.Values{"username": {"ed"}, "password": {"longpassword1"}}.Encode()
	if loc := c.do("POST", "/login", "application/x-www-form-urlencoded", form).Header().Get("Location"); loc != "/" {
		t.Fatalf("login as ed went to %q", loc)
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

func TestPasskeyName(t *testing.T) {
	icloud, _ := hex.DecodeString("fbfc3007154e4ecc8c0b6e020557d7bd")
	iphone := "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)"
	for _, tc := range []struct {
		cred webauthn.Credential
		ua   string
		want string
	}{
		{webauthn.Credential{Authenticator: webauthn.Authenticator{AAGUID: icloud}}, iphone, "iCloud Keychain"},
		{webauthn.Credential{Authenticator: webauthn.Authenticator{AAGUID: make([]byte, 16)}}, iphone, "iPhone"},
		{webauthn.Credential{Authenticator: webauthn.Authenticator{Attachment: protocol.CrossPlatform}, Transport: []protocol.AuthenticatorTransport{protocol.Hybrid, protocol.Internal}}, "Windows", "Phone"},
		{webauthn.Credential{Authenticator: webauthn.Authenticator{Attachment: protocol.CrossPlatform}, Transport: []protocol.AuthenticatorTransport{protocol.USB}}, "Macintosh", "Security key"},
	} {
		if got := passkeyName(&tc.cred, tc.ua); got != tc.want {
			t.Errorf("passkeyName = %q, want %q", got, tc.want)
		}
	}
}
