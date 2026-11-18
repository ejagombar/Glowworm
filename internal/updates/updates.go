// Package updates finds out whether a newer Glowworm has been released, and
// fetches and installs it for `glowworm update`.
package updates

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// LatestURL is GitHub's latest-release API for Glowworm. Only the address
// of the server and a "Glowworm/<version>" user agent are sent.
var LatestURL = "https://api.github.com/repos/ejagombar/Glowworm/releases/latest"

// Release is a published version and its downloads.
type Release struct {
	Tag    string  `json:"tag_name"`
	URL    string  `json:"html_url"`
	Assets []Asset `json:"assets"`
}

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Latest asks GitHub for the newest release.
func Latest(ctx context.Context, client *http.Client, current string) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, LatestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Glowworm/"+current)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("GitHub has no release to offer (none published yet, or the repository isn't public)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var r Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, err
	}
	if _, ok := parse(r.Tag); !ok {
		return nil, fmt.Errorf("the latest release has an unexpected tag %q", r.Tag)
	}
	return &r, nil
}

// Newer reports whether latest is a later release than current. A current
// version that isn't a release (dev, or a build between tags) never is
// behind, so it isn't nagged.
func Newer(latest, current string) bool {
	l, ok1 := parse(latest)
	c, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// IsRelease reports whether v is a tagged release version, like v1.4.0.
func IsRelease(v string) bool {
	_, ok := parse(v)
	return ok
}

// parse reads vX.Y or vX.Y.Z; anything else (pre-releases, pseudo-versions)
// isn't a release.
func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if !strings.HasPrefix(v, "v") || len(parts) < 2 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Checker looks for a new release once a day, for Settings to show.
type Checker struct {
	Current string
	Client  *http.Client
	Log     *slog.Logger
	latest  atomic.Pointer[Release]
}

// Available is the newer release found by the last check, or nil.
func (c *Checker) Available() *Release {
	if c == nil {
		return nil
	}
	r := c.latest.Load()
	if r == nil || !Newer(r.Tag, c.Current) {
		return nil
	}
	return r
}

// Checked reports whether a check has had an answer yet.
func (c *Checker) Checked() bool {
	return c != nil && c.latest.Load() != nil
}

// Check asks once and remembers the answer.
func (c *Checker) Check(ctx context.Context) error {
	r, err := Latest(ctx, c.Client, c.Current)
	if err != nil {
		return err
	}
	c.latest.Store(r)
	if Newer(r.Tag, c.Current) {
		c.Log.Info("a new version of Glowworm is out", "version", r.Tag, "running", c.Current, "notes", r.URL)
	}
	return nil
}

// Run checks a minute after start, then daily, until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err := c.Check(ctx); err != nil && ctx.Err() == nil {
			c.Log.Debug("update check", "err", err)
		}
		wait = 24 * time.Hour
	}
}

// AssetName is the release download for a platform.
func AssetName(goos, goarch string) string {
	name := "glowworm-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// Download fetches the binary for a platform from r and checks it against
// the release's checksums.txt.
func Download(ctx context.Context, client *http.Client, r *Release, goos, goarch string) ([]byte, error) {
	name := AssetName(goos, goarch)
	var bin, sums *Asset
	for i := range r.Assets {
		switch r.Assets[i].Name {
		case name:
			bin = &r.Assets[i]
		case "checksums.txt":
			sums = &r.Assets[i]
		}
	}
	if bin == nil {
		return nil, fmt.Errorf("%s has no download for %s/%s", r.Tag, goos, goarch)
	}
	if sums == nil {
		return nil, fmt.Errorf("%s has no checksums.txt, so the download can't be checked", r.Tag)
	}
	list, err := fetch(ctx, client, sums.URL, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("checksums: %w", err)
	}
	want, err := checksumFor(list, name)
	if err != nil {
		return nil, err
	}
	data, err := fetch(ctx, client, bin.URL, 512<<20)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("%s doesn't match its checksum: not installing it", name)
	}
	return data, nil
}

func checksumFor(list []byte, name string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(list))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt doesn't list %s", name)
}

func fetch(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("download is larger than expected")
	}
	return data, nil
}

// Install puts data in place of the binary at path, keeping its mode. The
// old one is moved aside first, which also works for a running program on
// Windows, and put back if the new one can't be moved in.
func Install(path string, data []byte) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".glowworm-new-*")
	if err != nil {
		return fmt.Errorf("can't write next to %s (run it as the user that owns it, e.g. with sudo): %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), st.Mode().Perm()); err != nil {
		return err
	}
	old := path + ".old"
	_ = os.Remove(old)
	if err := os.Rename(path, old); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Rename(old, path)
		return err
	}
	_ = os.Remove(old) // stays on Windows while the old one runs; harmless
	return nil
}
