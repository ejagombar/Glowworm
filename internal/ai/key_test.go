package ai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Gemini key must travel in a header: a URL is quoted in transport
// errors, which reach the server log and the page.
func TestGeminiKeyStaysOutOfTheURL(t *testing.T) {
	const key = "AIza-very-secret"
	var gotURL, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL, gotHeader = r.URL.String(), r.Header.Get("x-goog-api-key")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"ok\":true}"}]}}]}`))
	}))
	p := &Gemini{BaseURL: srv.URL, APIKey: key, Model: "m"}
	if _, _, err := p.CompleteStructured(transcriptRequest()); err != nil {
		t.Fatal(err)
	}
	if gotHeader != key {
		t.Errorf("x-goog-api-key = %q", gotHeader)
	}
	if strings.Contains(gotURL, key) {
		t.Errorf("key sent in the URL: %s", gotURL)
	}

	// With the server gone the request fails in transport, and the error
	// names the URL.
	srv.Close()
	_, _, err := p.CompleteStructured(transcriptRequest())
	if err == nil {
		t.Fatal("no error from a closed server")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("the error gives the key away: %v", err)
	}
}
