package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLimitKey(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"2001:db8:1:2:aaaa::1": "2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb::9": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":      "2001:db8:1:3::/64",
		"fe80::1%eth0":         "fe80::/64",
		"not an address":       "not an address",
	}
	for in, want := range cases {
		if got := limitKey(in); got != want {
			t.Errorf("limitKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// Addresses that fail once and never come back must not be kept for ever.
func TestLimiterForgetsExpiredKeys(t *testing.T) {
	l := newLimiter(5, 20*time.Millisecond)
	for i := 0; i < 500; i++ {
		l.Fail(fmt.Sprintf("login:10.0.%d.%d", i/250, i%250))
	}
	if len(l.hits) != 500 {
		t.Fatalf("%d keys recorded, want 500", len(l.hits))
	}
	time.Sleep(30 * time.Millisecond)
	l.Fail("login:192.0.2.1")
	if len(l.hits) != 1 {
		t.Errorf("%d keys kept after the window passed, want only the new one", len(l.hits))
	}
}

func TestLimiterBlocksAndResets(t *testing.T) {
	l := newLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if l.Blocked("k") {
			t.Fatalf("blocked after %d failures", i)
		}
		l.Fail("k")
	}
	if !l.Blocked("k") {
		t.Fatal("not blocked after 3 failures")
	}
	if l.Blocked("other") {
		t.Error("another key was blocked too")
	}
	l.Reset("k")
	if l.Blocked("k") {
		t.Error("still blocked after a reset")
	}
}

// /sw-log needs no session, so it must not be a way to fill the server log.
func TestSWLogIsRateLimited(t *testing.T) {
	h := newTestApp(t)
	post := func() int {
		r := httptest.NewRequest(http.MethodPost, "/sw-log", strings.NewReader(`{"ua":"t","events":[{"t":1,"e":"x"}]}`))
		r.RemoteAddr = "198.51.100.9:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 20; i++ {
		if code := post(); code != http.StatusNoContent {
			t.Fatalf("post %d: %d", i+1, code)
		}
	}
	if code := post(); code != http.StatusTooManyRequests {
		t.Errorf("post 21: %d, want 429", code)
	}
}

// A signed-in session must not be a way to guess the password without limit.
func TestPasswordConfirmationIsRateLimited(t *testing.T) {
	c := &testClient{t: t, h: newTestApp(t), cookies: map[string]*http.Cookie{}}
	c.login("longpassword1")
	guess := func() *httptest.ResponseRecorder {
		return c.do(http.MethodPost, "/api/v1/account/password", "application/json",
			`{"current_password":"wrong-guess","new_password":"anotherpassword2"}`)
	}
	for i := 0; i < 5; i++ {
		if w := guess(); !strings.Contains(w.Body.String(), "Current password is incorrect") {
			t.Fatalf("guess %d: %d %s", i+1, w.Code, w.Body.String())
		}
	}
	w := guess()
	if !strings.Contains(w.Body.String(), "Too many wrong passwords") {
		t.Fatalf("guess 6 was still checked: %d %s", w.Code, w.Body.String())
	}
	// Even the right password waits out the block.
	w = c.do(http.MethodPost, "/api/v1/account/password", "application/json",
		`{"current_password":"longpassword1","new_password":"anotherpassword2"}`)
	if !strings.Contains(w.Body.String(), "Too many wrong passwords") {
		t.Errorf("the block was lifted early: %d %s", w.Code, w.Body.String())
	}
}
