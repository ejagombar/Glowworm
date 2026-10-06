package server

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Without Unwrap, http.ResponseController can't reach the real writer and
// SSE handlers can't Flush.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

var reqCounter struct {
	mu sync.Mutex
	n  uint64
}

func nextReqID() string {
	reqCounter.mu.Lock()
	reqCounter.n++
	n := reqCounter.n
	reqCounter.mu.Unlock()
	return time.Now().Format("150405") + "-" + hexEncode([]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := nextReqID()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		sw.Header().Set("X-Request-Id", id)
		start := time.Now()
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic", "req_id", id, "path", r.URL.Path, "panic", p,
					"stack", string(debug.Stack()))
				if sw.status == 200 {
					sw.Header().Set("Content-Type", "application/json")
					sw.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(sw).Encode(map[string]any{
						"error": map[string]any{"code": "internal",
							"message": "Something went wrong on the server (the log has the details)."},
					})
				}
			}
			log.Info("request", "req_id", id, "method", r.Method, "path", r.URL.Path,
				"status", sw.status, "dur_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(sw, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

type limiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

func (l *limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	hits := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			hits = append(hits, t)
		}
	}
	l.hits[key] = hits
	if len(hits) >= l.limit {
		return false
	}
	l.hits[key] = append(l.hits[key], now)
	return true
}

func clientIP(r *http.Request, trustedProxies string) string {
	if trustedProxies != "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil && isTrustedProxy(host, trustedProxies) {
			if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
				parts := strings.Split(xf, ",")
				return strings.TrimSpace(parts[len(parts)-1])
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isTrustedProxy(host, cidrs string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, c := range strings.Split(cidrs, ",") {
		if _, network, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			if network.Contains(ip) {
				return true
			}
		}
	}
	return false
}
