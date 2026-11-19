package server

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// gzipHTML compresses pages (text/html responses) for clients that take
// gzip. A page's markup shrinks to about a fifth, which is most of the wait
// for the next page on a slow phone connection. Everything else goes out as
// the handler wrote it: static files compress themselves (static.go), and
// API replies and event streams are left alone.
func gzipHTML(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipHTMLWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

type gzipHTMLWriter struct {
	http.ResponseWriter
	decided bool
	zw      *gzip.Writer // nil when passing through
}

func (w *gzipHTMLWriter) decide(status int) {
	if w.decided {
		return
	}
	w.decided = true
	h := w.Header()
	if !strings.HasPrefix(h.Get("Content-Type"), "text/html") || h.Get("Content-Encoding") != "" ||
		status == http.StatusNoContent || status == http.StatusNotModified || status < 200 {
		return
	}
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	h.Del("Content-Length")
	w.zw, _ = gzip.NewWriterLevel(w.ResponseWriter, gzip.DefaultCompression)
}

func (w *gzipHTMLWriter) WriteHeader(code int) {
	w.decide(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipHTMLWriter) Write(b []byte) (int, error) {
	if !w.decided {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(b))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.zw != nil {
		return w.zw.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush keeps streaming responses streaming, compressed or not.
func (w *gzipHTMLWriter) Flush() {
	if w.zw != nil {
		_ = w.zw.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipHTMLWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipHTMLWriter) close() {
	if w.zw != nil {
		_ = w.zw.Close()
	}
}
