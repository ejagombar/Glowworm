package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	"github.com/ejagombar/Glowworm/internal/config"
	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/firstrun"
	"github.com/ejagombar/Glowworm/internal/store"
)

// announceSetup prints the link that creates the account once the server
// is listening: with a QR code when a phone could open it, after checking
// the public address really reaches this server. In JSON logs it's a field
// instead, since a QR code would be noise there.
func announceSetup(ctx context.Context, cfg config.Config, log *slog.Logger, db *sql.DB, instance string) {
	token, err := firstrun.Token(db, cfg.SetupToken)
	if err != nil {
		log.Error("setup token", "err", err)
		return
	}
	where := firstrun.Where(cfg.PublicURL, cfg.Addr)
	link := firstrun.SetupLink(where.Base, token)
	var reach error
	tried := cfg.PublicURL != ""
	if tried {
		reach = firstrun.Reachable(ctx, cfg.PublicURL, instance)
	}
	if ctx.Err() != nil {
		return
	}
	if cfg.LogFormat == "json" {
		args := []any{"url", link}
		if tried {
			args = append(args, "reachable", reach == nil)
			if reach != nil {
				args = append(args, "reach_err", reach.Error())
			}
		}
		log.Info("no account yet: open the setup link to create it", args...)
		return
	}
	firstrun.Banner(os.Stdout, link, where, tried, reach, firstrun.UseColor(os.Stdout))
}

func runSetupURL(args []string) {
	withQR := false
	for _, a := range args {
		switch a {
		case "--qr":
			withQR = true
		default:
			fail("setup-url: unexpected argument %q\n\n%s", a, usage)
		}
	}
	cfg := loadConfig()
	where := firstrun.Where(cfg.PublicURL, cfg.Addr)
	if cfg.AuthMode != "password" {
		fmt.Printf("Sign-in is off (GLOWWORM_AUTH_MODE=%s), so there's no account to create: open %s\n", cfg.AuthMode, where.Base)
		return
	}
	dsn := cfg.DSNSQLite()
	if _, err := os.Stat(dsn); err != nil {
		fail("no database at %s: start Glowworm (glowworm serve) first, with the same settings", dsn)
	}
	db, err := store.Open(dsn)
	if err != nil {
		fail("store: %v", err)
	}
	defer func() { _ = db.Close() }()
	if (&audit.Engine{DB: db}).HasUser() {
		fmt.Printf("The account already exists. Sign in at %s/login\n", where.Base)
		return
	}
	token, err := firstrun.Token(db.DB, cfg.SetupToken)
	if err != nil {
		fail("setup token: %v", err)
	}
	link := firstrun.SetupLink(where.Base, token)
	if !withQR {
		fmt.Println(link)
		return
	}
	tried := cfg.PublicURL != ""
	var reach error
	if tried {
		reach = firstrun.Reachable(context.Background(), cfg.PublicURL, "")
	}
	firstrun.Banner(os.Stdout, link, where, tried, reach, firstrun.UseColor(os.Stdout))
}
