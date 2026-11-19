package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGzipHTML(t *testing.T) {
	page := "<!doctype html><title>x</title>" + strings.Repeat("<p>row</p>", 200)
	h := gzipHTML(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", "99999")
			_, _ = io.WriteString(w, page)
		case "/api":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}))
	get := func(path, enc string) *http.Response {
		req := httptest.NewRequest("GET", path, nil)
		if enc != "" {
			req.Header.Set("Accept-Encoding", enc)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}

	res := get("/page", "gzip, deflate, br")
	if res.Header.Get("Content-Encoding") != "gzip" || res.Header.Get("Content-Length") != "" {
		t.Fatalf("page headers = %v, want gzip and no Content-Length", res.Header)
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != page {
		t.Fatalf("page came back as %q", b)
	}

	res = get("/page", "")
	if b, _ := io.ReadAll(res.Body); res.Header.Get("Content-Encoding") != "" || string(b) != page {
		t.Fatalf("page without Accept-Encoding: encoding %q, body %q", res.Header.Get("Content-Encoding"), b)
	}

	res = get("/api", "gzip")
	if b, _ := io.ReadAll(res.Body); res.Header.Get("Content-Encoding") != "" || string(b) != `{"ok":true}` {
		t.Fatalf("API reply was changed: encoding %q, body %q", res.Header.Get("Content-Encoding"), b)
	}
}
