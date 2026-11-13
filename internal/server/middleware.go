package server

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
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
					_ = json.NewEncoder(sw).Encode(map[string]any{
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
		// Nothing here is meant to be embedded in, opened from or read by
		// another site, and the app uses none of the device features below.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

type limiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
	swept  time.Time
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{hits: map[string][]time.Time{}, limit: limit, window: window}
}

// recent drops hits older than the window, and forgets keys with none left
// so the map doesn't grow with every address that ever connected.
func (l *limiter) recent(key string, now time.Time) []time.Time {
	l.sweep(now)
	hits := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			hits = append(hits, t)
		}
	}
	if len(hits) == 0 {
		delete(l.hits, key)
		return nil
	}
	l.hits[key] = hits
	return hits
}

// sweep forgets keys whose hits have all expired. recent only tidies the key
// it is asked about, so an address that fails once and never returns would
// otherwise stay for good: someone cycling through addresses could grow the
// map without limit. Run at most once a window, it costs next to nothing.
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < l.window {
		return
	}
	l.swept = now
	for key, hits := range l.hits {
		// Hits are in time order: the last is the newest.
		if len(hits) == 0 || now.Sub(hits[len(hits)-1]) >= l.window {
			delete(l.hits, key)
		}
	}
}

// Allow records a hit and reports whether it's within the limit.
func (l *limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.recent(key, now)) >= l.limit {
		return false
	}
	l.hits[key] = append(l.hits[key], now)
	return true
}

// Blocked reports whether key has used up its limit, without recording a hit.
func (l *limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key, time.Now())) >= l.limit
}

// Fail records a failed attempt.
func (l *limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits[key] = append(l.recent(key, time.Now()), time.Now())
}

// Reset forgets key's attempts, after one succeeds.
func (l *limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
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

// limitKey is what an address is rate-limited under. A v6 client usually
// holds a whole /64 and can pick a new address in it for every request, so
// those are counted together; a v4 address stands for itself.
func limitKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	if p, err := addr.Prefix(64); err == nil {
		return p.String()
	}
	return addr.String()
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

// contentSecurityPolicy is sent with every page. Scripts run only from this
// server, or inline when they carry the page's nonce, so text that reaches
// the page as markup (a description, a category name, an assistant reply)
// can't run as script; nothing loads from, posts to or is framed by another
// site. Styles stay open to inline use: the templates and widgets set colours
// and sizes with style attributes.
func contentSecurityPolicy(nonce string) string {
	return "default-src 'self'; " +
		"script-src 'self' 'nonce-" + nonce + "'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; " +
		"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}
