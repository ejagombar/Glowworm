package updates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"v1.4.0", "v1.3.0", true},
		{"v1.3.1", "v1.3.0", true},
		{"v1.10.0", "v1.9.3", true},
		{"v2.0", "v1.9.9", true},
		{"v1.4", "v1.4.0", false},
		{"v1.3.0", "v1.3.0", false},
		{"v1.2.0", "v1.3.0", false},
		{"v1.4.0", "dev", false},
		{"v1.4.0", "v1.3.1-0.20261118173517-901b5d4ed77c+dirty", false}, // between tags
		{"v1.4.0-rc1", "v1.3.0", false},
		{"1.4.0", "v1.3.0", false},
	} {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.latest, c.current, got)
		}
	}
}

func TestAssetName(t *testing.T) {
	if AssetName("linux", "arm64") != "glowworm-linux-arm64" || AssetName("windows", "amd64") != "glowworm-windows-amd64.exe" {
		t.Error(AssetName("linux", "arm64"), AssetName("windows", "amd64"))
	}
}

// fakeGitHub serves a release whose linux/amd64 download is body, with a
// checksums.txt that says sum.
func fakeGitHub(t *testing.T, tag string, body []byte, sum string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			if r.Header.Get("User-Agent") == "" {
				t.Error("no user agent")
			}
			_ = json.NewEncoder(w).Encode(Release{Tag: tag, URL: "https://example.com/" + tag, Assets: []Asset{
				{Name: "glowworm-linux-amd64", URL: srv.URL + "/bin"},
				{Name: "checksums.txt", URL: srv.URL + "/sums"},
			}})
		case "/bin":
			_, _ = w.Write(body)
		case "/sums":
			_, _ = io.WriteString(w, "0000000000000000000000000000000000000000000000000000000000000000  glowworm-darwin-arm64\n"+
				sum+"  glowworm-linux-amd64\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := LatestURL
	LatestURL = srv.URL + "/latest"
	t.Cleanup(func() { LatestURL = old })
	return srv
}

func TestDownloadChecksSum(t *testing.T) {
	body := []byte("new glowworm")
	h := sha256.Sum256(body)
	srv := fakeGitHub(t, "v9.0.0", body, hex.EncodeToString(h[:]))
	rel, err := Latest(t.Context(), srv.Client(), "v1.0.0")
	if err != nil || rel.Tag != "v9.0.0" {
		t.Fatalf("%+v %v", rel, err)
	}
	got, err := Download(t.Context(), srv.Client(), rel, "linux", "amd64")
	if err != nil || string(got) != "new glowworm" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Download(t.Context(), srv.Client(), rel, "freebsd", "riscv64"); err == nil {
		t.Error("no download for the platform, but no error")
	}

	bad := fakeGitHub(t, "v9.0.0", body, "1111111111111111111111111111111111111111111111111111111111111111")
	rel, _ = Latest(t.Context(), bad.Client(), "v1.0.0")
	if _, err := Download(t.Context(), bad.Client(), rel, "linux", "amd64"); err == nil {
		t.Error("a download that doesn't match its checksum was accepted")
	}
}

func TestChecker(t *testing.T) {
	srv := fakeGitHub(t, "v1.5.0", nil, "")
	c := &Checker{Current: "v1.4.2", Client: srv.Client(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if c.Checked() || c.Available() != nil {
		t.Fatal("something available before any check")
	}
	if err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r := c.Available(); !c.Checked() || r == nil || r.Tag != "v1.5.0" {
		t.Errorf("after the check: %+v", r)
	}
	c.Current = "v1.5.0"
	if c.Available() != nil {
		t.Error("the running version offered as an update")
	}
	var none *Checker
	if none.Available() != nil || none.Checked() {
		t.Error("a nil checker")
	}
}

func TestInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "glowworm")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	st, _ := os.Stat(path)
	if string(got) != "new" || st.Mode().Perm() != 0o755 {
		t.Errorf("%q %v", got, st.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*"))
	if len(left) != 1 {
		t.Errorf("left behind: %v", left)
	}
}
