package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
)

// staticFile is one embedded asset, read and hashed once at startup.
type staticFile struct {
	body  []byte
	gz    []byte // gzip of body, nil when compressing doesn't pay
	hash  string // the ?v= value pages link with (see static.Version)
	ctype string
}

// staticSet holds every embedded asset in memory. The files never change
// while the process runs, so nothing is read, hashed or compressed per
// request, and nothing is written after loadStatic returns: handlers can
// share it without a lock.
type staticSet struct {
	files map[string]*staticFile
}

func loadStatic(fsys fs.FS) *staticSet {
	set := &staticSet{files: map[string]*staticFile{}}
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil
		}
		set.files[path] = newStaticFile(path, data)
		return nil
	})
	return set
}

func newStaticFile(path string, data []byte) *staticFile {
	sum := sha256.Sum256(data)
	f := &staticFile{body: data, hash: hex.EncodeToString(sum[:8]), ctype: staticContentType(path)}
	if compressible(path) {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		_, _ = zw.Write(data)
		if zw.Close() == nil && buf.Len() < len(data)*9/10 {
			f.gz = buf.Bytes()
		}
	}
	return f
}

func staticContentType(path string) string {
	switch {
	case strings.HasSuffix(path, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(path, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(path, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(path, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(path, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(path, ".png"):
		return "image/png"
	case strings.HasSuffix(path, ".webmanifest"):
		return "application/manifest+json"
	}
	return ""
}

// compressible is text; fonts and PNGs are compressed already.
func compressible(path string) bool {
	for _, ext := range []string{".css", ".js", ".html", ".svg", ".webmanifest"} {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// serve writes f, compressed when the client takes gzip, or a 304 when it
// already has these bytes. Cache-Control is the caller's to set.
func (f *staticFile) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	etag := `"` + f.hash + `"`
	h.Set("ETag", etag)
	if f.gz != nil {
		h.Set("Vary", "Accept-Encoding")
	}
	if etagMatches(r.Header.Get("If-None-Match"), f.hash) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if f.ctype != "" {
		h.Set("Content-Type", f.ctype)
	}
	body := f.body
	if f.gz != nil && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		body = f.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// etagMatches reads an If-None-Match header: a list of tags, each possibly
// weak (a proxy that compresses weakens them) and, from pages loaded before
// tags were quoted, possibly bare.
func etagMatches(header, hash string) bool {
	if header == "" {
		return false
	}
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" {
			return true
		}
		tag = strings.TrimPrefix(tag, "W/")
		if strings.Trim(tag, `"`) == hash {
			return true
		}
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, enc := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(enc), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		q := strings.TrimSpace(params)
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}

// handler serves the set under the strip prefix (e.g. "/static/").
func (s *staticSet) handler(strip string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, strip)
		f, ok := s.files[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch {
		case r.URL.Query().Get("v") == f.hash:
			// A versioned URL (the asset template func) names these exact bytes;
			// a new build links a new URL, so it can be kept for good.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case strings.HasPrefix(path, "fonts/") || strings.HasPrefix(path, "icons/"):
			// Linked from CSS and the manifest without a version; they don't change.
			w.Header().Set("Cache-Control", "public, max-age=2592000")
		default:
			// Unversioned: the binary is updated in place and must never serve
			// a stale app.js, so check every time.
			w.Header().Set("Cache-Control", "no-cache")
		}
		f.serve(w, r)
	})
}

// serviceWorker is js/sw.js, served from the site root, where a worker must
// live to cover every page, versioned by a hash of what it caches so the
// browser picks up a new offline page or stylesheet.
func (s *staticSet) serviceWorker() http.Handler {
	var src []byte
	if f, ok := s.files["js/sw.js"]; ok {
		src = f.body
	}
	h := sha256.New()
	h.Write(src)
	for _, name := range []string{"offline.html", "css/styles.css"} {
		if f, ok := s.files[name]; ok {
			h.Write(f.body)
		}
	}
	body := bytes.ReplaceAll(src, []byte("__VERSION__"), []byte(hex.EncodeToString(h.Sum(nil))[:12]))
	f := newStaticFile("js/sw.js", body)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		f.serve(w, r)
	})
}
