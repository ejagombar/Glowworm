package server

import (
	"context"
	"net/http"
	"time"

	"github.com/ejagombar/Glowworm/internal/firstrun"
	"github.com/ejagombar/Glowworm/internal/updates"
)

// StartUpdateCheck looks for a new release daily, for Settings to mention,
// unless it's switched off or this isn't a release build.
func (a *App) StartUpdateCheck(ctx context.Context) {
	if a.Cfg.UpdateCheck == "off" || !updates.IsRelease(a.Version) {
		return
	}
	a.Updates = &updates.Checker{Current: a.Version, Client: &http.Client{Timeout: time.Minute}, Log: a.Log}
	go a.Updates.Run(ctx)
}

// aboutView is the Glowworm panel in Settings: the version, and a newer one
// with how to get it.
type aboutView struct {
	Version string
	Checked bool             // the update check has had an answer
	Latest  *updates.Release // newer than Version, or nil
	HowCmd  string           // what to run to update
	HowNote string
}

func (a *App) about() aboutView {
	v := aboutView{Version: a.Version, Checked: a.Updates.Checked(), Latest: a.Updates.Available()}
	if v.Latest == nil {
		return v
	}
	if firstrun.InContainer() {
		v.HowCmd = "docker compose pull && docker compose up -d"
		v.HowNote = "Pull the new image and start Glowworm on it. With Podman's auto-update this happens by itself."
	} else {
		v.HowCmd = "glowworm update"
		v.HowNote = "Run it on the server, then restart Glowworm. It backs up the data first."
	}
	return v
}
