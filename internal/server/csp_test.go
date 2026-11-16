package server

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	reScriptTag   = regexp.MustCompile(`<script\b[^>]*>`)
	reInlineEvent = regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`)
	reNonce       = regexp.MustCompile(`'nonce-([0-9a-f]{32})'`)
)

// Every page carries a policy, and every inline script on it carries that
// page's nonce, or the browser would refuse to run it.
func TestPagesCarryAContentSecurityPolicy(t *testing.T) {
	c := &testClient{t: t, h: newTestApp(t), cookies: map[string]*http.Cookie{}}
	seen := map[string]bool{}
	check := func(path string) {
		t.Helper()
		rec := c.do("GET", path, "", "")
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		csp := rec.Header().Get("Content-Security-Policy")
		m := reNonce.FindStringSubmatch(csp)
		if m == nil {
			t.Fatalf("%s: no nonce in the policy %q", path, csp)
		}
		if seen[m[1]] {
			t.Errorf("%s: nonce %s was used before", path, m[1])
		}
		seen[m[1]] = true
		for _, bad := range []string{"'unsafe-eval'", "script-src 'self' 'unsafe-inline'", "*"} {
			if strings.Contains(csp, bad) {
				t.Errorf("%s: policy allows %s: %s", path, bad, csp)
			}
		}
		body := rec.Body.String()
		tags := reScriptTag.FindAllString(body, -1)
		if len(tags) == 0 {
			t.Fatalf("%s: no scripts found", path)
		}
		for _, tag := range tags {
			if strings.Contains(tag, " src=") {
				continue
			}
			if !strings.Contains(tag, `nonce="`+m[1]+`"`) {
				t.Errorf("%s: inline script without the page's nonce: %s", path, tag)
			}
		}
		if ev := reInlineEvent.FindString(body); ev != "" {
			t.Errorf("%s: inline event handler, which the policy blocks: %s", path, ev)
		}
	}
	check("/login")
	c.login("longpassword1")
	for _, p := range []string{"/", "/transactions", "/savings", "/queries", "/import", "/history", "/settings"} {
		check(p)
	}
}

// Templates are checked at the source as well, for markup a test page
// doesn't happen to draw (an empty state, a modal).
func TestTemplatesHaveNoInlineHandlers(t *testing.T) {
	files, err := filepath.Glob("../render/templates/*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("no templates found: %v", err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if ev := reInlineEvent.FindString(string(src)); ev != "" {
			t.Errorf("%s: inline event handler: %s", filepath.Base(f), ev)
		}
		for _, tag := range reScriptTag.FindAllString(string(src), -1) {
			if !strings.Contains(tag, " src=") && !strings.Contains(tag, `nonce="{{$.Nonce}}"`) {
				t.Errorf("%s: inline script without a nonce: %s", filepath.Base(f), tag)
			}
		}
	}
}
