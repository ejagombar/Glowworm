package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ejagombar/Glowworm/internal/firstrun"
	"github.com/ejagombar/Glowworm/internal/updates"
)

// runUpdate replaces this binary with the latest release: checked against
// the release's checksums, and only after a backup, since a new version may
// migrate the database and there's no going back without one.
func runUpdate(args []string) {
	check, unit := false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			check = true
		case "--restart":
			if i+1 >= len(args) {
				fail("--restart needs a systemd unit, e.g. glowworm")
			}
			unit = args[i+1]
			i++
		default:
			fail("update: unexpected argument %q\n\n%s", args[i], usage)
		}
	}
	ctx := context.Background()
	client := &http.Client{Timeout: 10 * time.Minute}
	rel, err := updates.Latest(ctx, client, version)
	if err != nil {
		fail("couldn't find the latest release: %v", err)
	}
	if !updates.IsRelease(version) {
		fmt.Printf("This is a development build (%s); the latest release is %s.\n", version, rel.Tag)
		if check {
			return
		}
		fail("update only replaces release builds")
	}
	if !updates.Newer(rel.Tag, version) {
		fmt.Printf("Glowworm %s is the latest release.\n", version)
		return
	}
	fmt.Printf("Glowworm %s is out (this is %s): %s\n", rel.Tag, version, rel.URL)
	if check {
		return
	}
	if firstrun.InContainer() {
		fail("in a container, pull the new image instead: docker compose pull && docker compose up -d")
	}
	exe, err := os.Executable()
	if err != nil {
		fail("%v", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		fail("%v", err)
	}
	// deploy.sh's layout, /opt/glowworm/releases/<tag>/glowworm: each
	// release has its own directory, and the release workflow deploys.
	if filepath.Base(filepath.Dir(filepath.Dir(exe))) == "releases" {
		fail("%s is installed by deploy.sh; tag a release to deploy it instead", exe)
	}

	fmt.Printf("Downloading %s…\n", updates.AssetName(runtime.GOOS, runtime.GOARCH))
	data, err := updates.Download(ctx, client, rel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		fail("%v", err)
	}

	cfg := loadConfig()
	if _, err := os.Stat(cfg.DSNSQLite()); err == nil {
		dir := filepath.Join(cfg.DataDir, "backups")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fail("backup: %v", err)
		}
		out := filepath.Join(dir, fmt.Sprintf("before-%s-%s.zip", rel.Tag, time.Now().Format("20060102-150405")))
		if err := writeBackup(cfg, out); err != nil {
			fail("backing up first: %v (not updating)", err)
		}
		sameOwner(cfg.DataDir, dir, out)
		fmt.Printf("Backed up to %s\n", out)
	}

	if err := updates.Install(exe, data); err != nil {
		fail("installing: %v", err)
	}
	out, err := exec.Command(exe, "version").Output()
	if err != nil {
		fail("the new binary doesn't run here (%v); the backup is above", err)
	}
	fmt.Printf("Installed %s at %s.\n", strings.TrimSpace(string(out)), exe)
	if unit == "" {
		fmt.Println("Restart Glowworm to run it, e.g. sudo systemctl restart glowworm")
		return
	}
	if err := exec.Command("systemctl", "restart", unit).Run(); err != nil {
		fail("restarting %s: %v", unit, err)
	}
	fmt.Printf("Restarted %s.\n", unit)
}
