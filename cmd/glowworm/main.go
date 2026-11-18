package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // zone names work on Windows and in minimal containers

	"github.com/ejagombar/Glowworm/internal/backup"
	"github.com/ejagombar/Glowworm/internal/config"
	"github.com/ejagombar/Glowworm/internal/crypto"
	"github.com/ejagombar/Glowworm/internal/server"
	"github.com/ejagombar/Glowworm/internal/store"
)

// version is set at build time (-X main.version=v1.2.3); a build by
// `go install …@v1.2.3` gets it from the module version instead.
var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = bi.Main.Version
	}
}

const usage = `Usage: glowworm <command>

Commands:
  serve                     run the web server (default)
  backup [-o FILE]          write a full backup (safe while the server runs)
  restore [--force] FILE    replace this instance's data with a backup;
                            stop the server first
  user create [-u NAME]     create the account from the terminal (prompts
                            for the password); otherwise the browser asks
  setup-url [--qr]          print the link that creates the account, and a
                            QR code of it with --qr
  version                   print the version
  healthcheck [--url URL]   exit 0 if the server answers /healthz

Configuration comes from GLOWWORM_* environment variables or glowworm.toml.
`

func main() {
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		serve()
		os.Exit(exitCode)
	case "backup":
		runBackup(args)
	case "restore":
		runRestore(args)
	case "user":
		runUser(args)
	case "setup-url":
		runSetupURL(args)
	case "version":
		fmt.Println(version)
	case "healthcheck":
		healthcheck(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "glowworm: "+format+"\n", a...)
	os.Exit(1)
}

func loadConfig() config.Config {
	cfg, err := config.Load()
	if err != nil {
		fail("%v", err)
	}
	return cfg
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// exitCode is what main exits with once serve has returned and its deferred
// cleanup has run.
var exitCode int

func serve() {
	cfg := loadConfig()
	log := newLogger(cfg)

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Error("data dir", "err", err)
		os.Exit(1)
	}
	release, err := store.LockDataDir(cfg.DataDir)
	if err != nil {
		log.Error("data dir", "dir", cfg.DataDir, "err", err)
		os.Exit(1)
	}
	defer release()
	secret, err := crypto.LoadOrCreate(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		log.Error("secret key", "err", err)
		os.Exit(1)
	}

	dsn := cfg.DSNSQLite()
	db, err := store.Open(dsn)
	if err != nil {
		log.Error("store", "err", err)
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	app, err := server.New(cfg, db, secret, log)
	if err != nil {
		log.Error("server", "err", err)
		os.Exit(1)
	}
	app.Version = version
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Error("listen", "err", err)
		exitCode = 1
		return
	}
	log.Info("glowworm listening", "addr", cfg.Addr, "version", version, "db", dsn, "auth", cfg.AuthMode, "log_level", cfg.LogLevel)
	srv := newHTTPServer(cfg.Addr, app.Router())
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	app.StartReminders(stop)
	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(ln) }()
	if cfg.AuthMode == "password" && !app.Audit.HasUser() {
		go announceSetup(stop, cfg, log, db.DB, app.Instance)
	}
	select {
	case err := <-failed:
		// Not os.Exit: the deferred calls release the data directory and
		// close the database.
		log.Error("listen", "err", err)
		exitCode = 1
	case <-stop.Done():
		cancel() // a second signal stops the process the default way
		log.Info("shutting down")
		ctx, done := context.WithTimeout(context.Background(), shutdownGrace)
		defer done()
		// Requests in flight finish first, so a write isn't cut off halfway;
		// open streams that outlast the grace period are dropped.
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
	}
}

const shutdownGrace = 10 * time.Second

// newHTTPServer bounds how long a client may take over its request headers
// and how long an idle connection is kept, so slow or silent clients can't
// tie up connections for good. There is deliberately no read or write
// timeout: statement uploads over a slow link and the streamed import and
// chat replies both run for as long as they need.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
}

func runBackup(args []string) {
	out := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--output":
			if i+1 >= len(args) {
				fail("%s needs a file name", args[i])
			}
			out = args[i+1]
			i++
		default:
			fail("backup: unexpected argument %q\n\n%s", args[i], usage)
		}
	}
	cfg := loadConfig()
	dsn := cfg.DSNSQLite()
	if _, err := os.Stat(dsn); err != nil {
		fail("no database at %s — nothing to back up", dsn)
	}
	secret, err := crypto.LoadOrCreate(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		fail("secret key: %v", err)
	}
	db, err := store.Open(dsn)
	if err != nil {
		fail("store: %v", err)
	}
	defer func() { _ = db.Close() }()
	if out == "" {
		out = backup.FileName(time.Now())
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		fail("%v", err)
	}
	if err := backup.Write(f, db, secret, version); err != nil {
		_ = f.Close()
		_ = os.Remove(out)
		fail("backup: %v", err)
	}
	if err := f.Close(); err != nil {
		fail("%v", err)
	}
	fmt.Printf("Backup written to %s\nIt contains all your data and the key that decrypts your AI key — keep it private.\n", out)
}

func runRestore(args []string) {
	force, file := false, ""
	for _, a := range args {
		switch {
		case a == "--force" || a == "-f":
			force = true
		case strings.HasPrefix(a, "-"):
			fail("restore: unknown flag %q\n\n%s", a, usage)
		case file != "":
			fail("restore: only one backup file can be given")
		default:
			file = a
		}
	}
	if file == "" {
		fail("restore: which backup file?\n\n%s", usage)
	}
	cfg := loadConfig()
	sum, err := backup.Restore(file, cfg.DataDir, cfg.DSNSQLite(), force)
	if err != nil {
		fail("restore: %v", err)
	}
	m := sum.Manifest
	fmt.Printf("Restored the backup made %s (Glowworm %s).\n", m.CreatedAt, m.AppVersion)
	if sum.Owner != "" {
		fmt.Printf("Owner: %s\n", sum.Owner)
	}
	fmt.Printf("%s, %s.\n", plural(sum.Accounts, "account"), plural(sum.Transactions, "transaction"))
	if sum.ReplacedDir != "" {
		fmt.Printf("The previous data was moved to %s — delete it once you're happy.\n", sum.ReplacedDir)
	}
	if cfg.SecretKey != "" {
		fmt.Println("Note: GLOWWORM_SECRET_KEY is set and overrides the restored secret.key; set it to the backup's key or the stored AI key can't be decrypted.")
	}
	fmt.Println("Start Glowworm again to use it.")
}

func healthcheck(args []string) {
	url := "http://127.0.0.1:8080/healthz"
	if len(args) > 1 && args[0] == "--url" {
		url = args[1]
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil || resp.StatusCode != 200 {
		fmt.Println("unhealthy")
		os.Exit(1)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	fmt.Println("ok")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
