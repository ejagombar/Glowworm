package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ejagombar/Glowworm/internal/updates"
)

// Settings shows the version, says so when it's the latest, and otherwise
// names the new release and how to get it.
func TestSettingsShowsUpdate(t *testing.T) {
	a, c := newLockTestApp(t)
	a.Version = "v1.3.0"
	if body := c.do("GET", "/settings", "", "").Body.String(); !strings.Contains(body, "Version v1.3.0") || strings.Contains(body, "up to date") {
		t.Fatal("version missing, or claimed up to date before any check")
	}

	tag := "v1.3.0"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(updates.Release{Tag: tag, URL: "https://example.com/notes"})
	}))
	defer gh.Close()
	old := updates.LatestURL
	updates.LatestURL = gh.URL
	defer func() { updates.LatestURL = old }()

	a.Updates = &updates.Checker{Current: a.Version, Client: gh.Client(), Log: a.Log}
	if err := a.Updates.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if body := c.do("GET", "/settings", "", "").Body.String(); !strings.Contains(body, "up to date") {
		t.Error("latest release, but not shown as up to date")
	}

	tag = "v1.4.0"
	if err := a.Updates.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	body := c.do("GET", "/settings", "", "").Body.String()
	if !strings.Contains(body, "v1.4.0 is out") || !strings.Contains(body, "https://example.com/notes") || strings.Contains(body, "up to date") {
		t.Error("new release not shown")
	}
	if !strings.Contains(body, "glowworm update") && !strings.Contains(body, "docker compose pull") {
		t.Error("no way to update shown")
	}
}

// Development builds and GLOWWORM_UPDATE_CHECK=off don't check.
func TestUpdateCheckOnlyForReleases(t *testing.T) {
	a, _ := newLockTestApp(t)
	a.Version = "dev"
	a.StartUpdateCheck(t.Context())
	if a.Updates != nil {
		t.Error("checking from a dev build")
	}
	a.Version, a.Cfg.UpdateCheck = "v1.3.0", "off"
	a.StartUpdateCheck(t.Context())
	if a.Updates != nil {
		t.Error("checking while switched off")
	}
}
