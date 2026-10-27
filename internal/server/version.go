package server

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// dataVersion changes whenever a request changes data, so a page open on
// another device (or tab) can tell its copy is out of date. It's kept in a
// file in the data directory, so restarting the server doesn't make every
// open page think something changed.
type dataVersion struct {
	n    atomic.Uint64
	path string
	mu   sync.Mutex // orders writes of the file
}

func newDataVersion(path string) *dataVersion {
	v := &dataVersion{path: path}
	if b, err := os.ReadFile(path); err == nil {
		if n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil {
			v.n.Store(n)
		}
	}
	return v
}

func (v *dataVersion) String() string { return strconv.FormatUint(v.n.Load(), 10) }

func (v *dataVersion) bump() {
	n := v.n.Add(1)
	if v.path == "" {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if n < v.n.Load() {
		return // a later bump has written, or will write, a newer number
	}
	tmp := v.path + ".tmp"
	if os.WriteFile(tmp, []byte(strconv.FormatUint(n, 10)), 0o600) == nil {
		_ = os.Rename(tmp, v.path)
	}
}

const versionHeader = "X-Glowworm-Version"

// withVersion bumps the version when a request that can change data succeeds,
// and tells every response the version, so a page's own saves don't look
// like someone else's.
func (v *dataVersion) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutating := r.Method != http.MethodGet && r.Method != http.MethodHead &&
			!strings.HasSuffix(r.URL.Path, "/preview") && r.URL.Path != "/sw-log"
		next.ServeHTTP(&versionWriter{ResponseWriter: w, v: v, mutating: mutating}, r)
	})
}

type versionWriter struct {
	http.ResponseWriter
	v        *dataVersion
	mutating bool
	wrote    bool
}

func (w *versionWriter) stamp(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	if w.mutating && status < 400 {
		w.v.bump()
	}
	w.Header().Set(versionHeader, w.v.String())
}

func (w *versionWriter) WriteHeader(code int) {
	w.stamp(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *versionWriter) Write(b []byte) (int, error) {
	w.stamp(http.StatusOK)
	return w.ResponseWriter.Write(b)
}

func (w *versionWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
