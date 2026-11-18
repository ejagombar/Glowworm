package firstrun

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ejagombar/Glowworm/internal/store"
)

func TestToken(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if tok, _ := Token(db.DB, "off"); tok != "" {
		t.Errorf("off: %q", tok)
	}
	if tok, _ := Token(db.DB, "mine"); tok != "mine" {
		t.Errorf("configured: %q", tok)
	}
	first, err := Token(db.DB, "")
	if err != nil || len(first) < 20 {
		t.Fatalf("made: %q %v", first, err)
	}
	if again, _ := Token(db.DB, ""); again != first {
		t.Errorf("kept: %q then %q", first, again)
	}
	if err := Clear(db.DB); err != nil {
		t.Fatal(err)
	}
	if next, _ := Token(db.DB, ""); next == first || next == "" {
		t.Errorf("after Clear: %q (was %q)", next, first)
	}
}

func TestWhere(t *testing.T) {
	for _, c := range []struct {
		public, listen, base string
		qr, note             bool
	}{
		{"https://money.example.com", "127.0.0.1:8080", "https://money.example.com", true, false},
		{"http://money.example.com", "127.0.0.1:8080", "http://money.example.com", true, true}, // not HTTPS
		{"http://localhost:8080", "127.0.0.1:8080", "http://localhost:8080", true, false},
		{"", "127.0.0.1:8094", "http://localhost:8094", false, true},
		{"", "[::1]:8094", "http://localhost:8094", false, true},
		{"", "192.168.1.5:8080", "http://192.168.1.5:8080", true, false},
	} {
		a := Where(c.public, c.listen)
		if a.Base != c.base || a.QR != c.qr || (a.Note != "") != c.note {
			t.Errorf("Where(%q, %q) = %+v", c.public, c.listen, a)
		}
	}
	// Listening everywhere: a LAN address with a QR code, or in a container
	// localhost and a note, since its own addresses mean nothing outside.
	a := Where("", "0.0.0.0:8080")
	if InContainer() && (a.QR || a.Base != "http://localhost:8080") {
		t.Errorf("in a container: %+v", a)
	}
	if !strings.HasSuffix(a.Base, ":8080") {
		t.Errorf("0.0.0.0: %+v", a)
	}
}

func TestSetupLink(t *testing.T) {
	if got := SetupLink("https://x.example", "a b/c"); got != "https://x.example/setup?token=a+b%2Fc" {
		t.Error(got)
	}
	if got := SetupLink("", ""); got != "/setup" {
		t.Error(got)
	}
}

// The code fills a square: (size + quiet zone) columns, half as many lines.
// The decoding itself was checked with zbarimg by hand.
func TestQR(t *testing.T) {
	for _, color := range []bool{false, true} {
		var b bytes.Buffer
		if err := QR(&b, "https://money.example.com/setup?token=ABCDEFGHIJKLMNOPQRSTUVWXYZ", color); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
		width := len([]rune(strings.TrimPrefix(lines[0], "  ")))
		if color {
			width = len([]rune(strings.TrimPrefix(strings.TrimSuffix(lines[0], "\x1b[0m"), "  \x1b[30;107m")))
		}
		if width < 25 || len(lines) != (width+1)/2 {
			t.Errorf("color=%v: %d lines of %d", color, len(lines), width)
		}
	}
}

func TestReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","instance":"abc"}`))
	}))
	defer srv.Close()
	if err := Reachable(t.Context(), srv.URL, "abc"); err != nil {
		t.Errorf("this server: %v", err)
	}
	if err := Reachable(t.Context(), srv.URL, ""); err != nil {
		t.Errorf("any server: %v", err)
	}
	if err := Reachable(t.Context(), srv.URL, "other"); err == nil {
		t.Error("another server passed")
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>parked domain</html>`))
	}))
	defer other.Close()
	if err := Reachable(t.Context(), other.URL, ""); err == nil {
		t.Error("a page that isn't Glowworm passed")
	}
}

func TestBannerSaysWhyNotReached(t *testing.T) {
	var b bytes.Buffer
	Banner(&b, "https://x.example/setup?token=t", Address{Base: "https://x.example", QR: true}, true, http.ErrHandlerTimeout, false)
	out := b.String()
	if !strings.Contains(out, "https://x.example/setup?token=t") || !strings.Contains(out, "Couldn't reach") || !strings.Contains(out, "█") {
		t.Error(out)
	}
}
