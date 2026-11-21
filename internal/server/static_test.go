package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	staticFS "github.com/ejagombar/Glowworm/web/static"
)

func testStatic() *staticSet {
	return loadStatic(fstest.MapFS{
		"js/app.js":         {Data: []byte(strings.Repeat("console.log('hello');\n", 200))},
		"js/sw.js":          {Data: []byte(`const CACHE = "glowworm-offline-__VERSION__";`)},
		"css/styles.css":    {Data: []byte("body{}")},
		"offline.html":      {Data: []byte("<p>offline</p>")},
		"fonts/inter.woff2": {Data: bytes.Repeat([]byte{0}, 4096)},
		"icons/favicon.png": {Data: []byte("png")},
	})
}

func getStatic(t *testing.T, h http.Handler, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestStaticServesCompressedAndPlain(t *testing.T) {
	set := testStatic()
	h := set.handler("/static/")
	want := set.files["js/app.js"].body

	plain := getStatic(t, h, "/static/js/app.js", nil)
	if plain.Code != 200 || plain.Header().Get("Content-Encoding") != "" || !bytes.Equal(plain.Body.Bytes(), want) {
		t.Fatalf("plain: code %d, encoding %q, %d bytes", plain.Code, plain.Header().Get("Content-Encoding"), plain.Body.Len())
	}
	if plain.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("Vary = %q: a cache could hand the gzip copy to a client that can't read it", plain.Header().Get("Vary"))
	}

	gz := getStatic(t, h, "/static/js/app.js", map[string]string{"Accept-Encoding": "br, gzip"})
	if gz.Header().Get("Content-Encoding") != "gzip" || gz.Body.Len() >= len(want) {
		t.Fatalf("gzip: encoding %q, %d bytes of %d", gz.Header().Get("Content-Encoding"), gz.Body.Len(), len(want))
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, want) {
		t.Error("gzip body doesn't unpack to the file")
	}

	if w := getStatic(t, h, "/static/js/app.js", map[string]string{"Accept-Encoding": "gzip;q=0"}); w.Header().Get("Content-Encoding") != "" {
		t.Error("gzip sent to a client that refused it")
	}
	// Already-compressed formats are sent as they are.
	if w := getStatic(t, h, "/static/fonts/inter.woff2", map[string]string{"Accept-Encoding": "gzip"}); w.Header().Get("Content-Encoding") != "" {
		t.Error("font was gzipped")
	}
}

func TestStaticCachingAndRevalidation(t *testing.T) {
	set := testStatic()
	h := set.handler("/static/")
	hash := set.files["js/app.js"].hash

	cases := []struct{ target, want string }{
		{"/static/js/app.js?v=" + hash, "public, max-age=31536000, immutable"},
		{"/static/js/app.js?v=stale", "no-cache"},
		{"/static/js/app.js", "no-cache"},
		{"/static/fonts/inter.woff2", "public, max-age=2592000"},
		// Asked for by a fixed name (manifest, iOS), so a redrawn logo must get through.
		{"/static/icons/favicon.png", "no-cache"},
	}
	for _, c := range cases {
		if got := getStatic(t, h, c.target, nil).Header().Get("Cache-Control"); got != c.want {
			t.Errorf("%s: Cache-Control = %q, want %q", c.target, got, c.want)
		}
	}

	first := getStatic(t, h, "/static/js/app.js", nil)
	etag := first.Header().Get("ETag")
	if etag != `"`+hash+`"` {
		t.Fatalf("ETag = %q", etag)
	}
	// Quoted, weakened by a compressing proxy, in a list, or bare as older pages sent it.
	for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, hash} {
		if w := getStatic(t, h, "/static/js/app.js", map[string]string{"If-None-Match": inm}); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Errorf("If-None-Match %s: code %d, %d bytes", inm, w.Code, w.Body.Len())
		}
	}
	if w := getStatic(t, h, "/static/js/app.js", map[string]string{"If-None-Match": `"other"`}); w.Code != 200 {
		t.Errorf("a different tag got %d", w.Code)
	}

	for _, missing := range []string{"/static/", "/static/nope.js", "/static/../secret", "/static/js"} {
		if w := getStatic(t, h, missing, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s: code %d, want 404", missing, w.Code)
		}
	}
}

// Concurrent first requests used to race on a shared map of tags.
func TestStaticConcurrentRequests(t *testing.T) {
	h := testStatic().handler("/static/")
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for _, p := range []string{"/static/js/app.js", "/static/css/styles.css", "/static/icons/favicon.png"} {
				if w := getStatic(t, h, p, nil); w.Code != 200 {
					t.Errorf("%s: %d", p, w.Code)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestServiceWorkerVersioned(t *testing.T) {
	a := testStatic()
	w := getStatic(t, a.serviceWorker(), "/sw.js", nil)
	body := w.Body.String()
	if strings.Contains(body, "__VERSION__") || !strings.Contains(body, "glowworm-offline-") {
		t.Fatalf("version not filled in: %s", body)
	}
	if w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Errorf("headers: %v", w.Header())
	}
	// What the worker caches changing must change the worker.
	b := testStatic()
	b.files["css/styles.css"] = newStaticFile("css/styles.css", []byte("body{color:red}"))
	if other := getStatic(t, b.serviceWorker(), "/sw.js", nil).Body.String(); other == body {
		t.Error("a new stylesheet left the service worker unchanged")
	}
	if w := getStatic(t, a.serviceWorker(), "/sw.js", map[string]string{"If-None-Match": w.Header().Get("ETag")}); w.Code != http.StatusNotModified {
		t.Errorf("revalidating an unchanged worker: %d", w.Code)
	}
}

// The tag pages link with (?v=) must be the one the handler compares against.
func TestStaticHashMatchesAssetVersion(t *testing.T) {
	set := loadStatic(staticFS.FS)
	for _, p := range []string{"js/app.js", "js/widgets.js", "css/styles.css"} {
		f, ok := set.files[p]
		if !ok {
			t.Fatalf("%s not loaded", p)
		}
		if v := staticFS.Version(p); v == "" || v != f.hash {
			t.Errorf("%s: handler hash %q, asset version %q", p, f.hash, v)
		}
		if f.gz == nil {
			t.Errorf("%s has no gzip copy", p)
		}
	}
}
