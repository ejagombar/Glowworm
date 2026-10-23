package static

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"sync"
)

//go:embed css js fonts icons offline.html manifest.webmanifest
var FS embed.FS

var versions sync.Map

// Version is a short hash of an embedded file's contents ("" if it doesn't
// exist). Pages link to /static/<path>?v=<Version>, which can then be cached
// for good: a new build that changes the file changes the URL.
func Version(path string) string {
	if v, ok := versions.Load(path); ok {
		return v.(string)
	}
	data, err := FS.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	v := hex.EncodeToString(sum[:8])
	versions.Store(path, v)
	return v
}
