// Glowworm — command entrypoint (doc 01 §2): config, store, secret, server.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"glowworm/internal/config"
	"glowworm/internal/crypto"
	"glowworm/internal/server"
	"glowworm/internal/store"
)

var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	var log *slog.Logger
	if cfg.LogFormat == "json" {
		log = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	} else {
		log = slog.New(slog.NewTextHandler(os.Stdout, nil))
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Error("data dir", "err", err)
		os.Exit(1)
	}
	secret, err := crypto.LoadOrCreate(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		log.Error("secret key", "err", err)
		os.Exit(1)
	}
	if cfg.SecretKey == "" {
		log.Info("generated secret.key in data dir", "dir", cfg.DataDir)
	}

	dsn := cfg.DSNSQLite()
	db, err := store.Open(cfg.DBDialect, dsn)
	if err != nil {
		log.Error("store", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	app, err := server.New(cfg, db, secret, log)
	if err != nil {
		log.Error("server", "err", err)
		os.Exit(1)
	}
	if cfg.SetupToken != "" {
		log.Info("first-run setup gated by GLOWWORM_SETUP_TOKEN")
	}
	log.Info("glowworm listening", "addr", cfg.Addr, "version", version, "db", dsn, "auth", cfg.AuthMode)
	if err := http.ListenAndServe(cfg.Addr, app.Router()); err != nil {
		log.Error("listen", "err", err)
		os.Exit(1)
	}
	_ = filepath.Join
}