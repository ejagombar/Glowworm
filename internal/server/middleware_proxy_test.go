package server

import (
	"net/http/httptest"
	"testing"
)

// A trusted proxy can be one address (as the systemd unit sets it) or a
// range; only then is X-Forwarded-For believed.
func TestClientIPBehindProxy(t *testing.T) {
	for _, c := range []struct {
		trusted, remote, want string
	}{
		{"127.0.0.1", "127.0.0.1:5000", "203.0.113.9"},
		{"127.0.0.0/8", "127.0.0.1:5000", "203.0.113.9"},
		{"10.0.0.1, 127.0.0.1", "127.0.0.1:5000", "203.0.113.9"},
		{"::1", "[::1]:5000", "203.0.113.9"},
		{"127.0.0.1", "198.51.100.7:5000", "198.51.100.7"}, // not the proxy: header ignored
		{"", "127.0.0.1:5000", "127.0.0.1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		r.Header.Set("X-Forwarded-For", "192.0.2.1, 203.0.113.9")
		if got := clientIP(r, c.trusted); got != c.want {
			t.Errorf("trusted %q from %s: %s, want %s", c.trusted, c.remote, got, c.want)
		}
	}
}
