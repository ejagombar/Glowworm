package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/core/setup"
	"github.com/ejagombar/Glowworm/internal/store"
	"github.com/ejagombar/Glowworm/internal/store/repos"
)

func runUser(args []string) {
	if len(args) == 0 || args[0] != "create" {
		fail("user: expected \"create\"\n\n%s", usage)
	}
	username := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-u", "--username":
			if i+1 >= len(args) {
				fail("%s needs a name", args[i])
			}
			username = args[i+1]
			i++
		default:
			fail("user create: unexpected argument %q\n\n%s", args[i], usage)
		}
	}
	cfg := loadConfig()
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		fail("data dir: %v", err)
	}
	db, err := store.Open(cfg.DSNSQLite())
	if err != nil {
		fail("store: %v", err)
	}
	defer func() { _ = db.Close() }()
	svc := &setup.Service{DB: db, Users: &repos.Users{DB: db}, Settings: &repos.Settings{DB: db}, Audit: &audit.Writer{DB: db}}
	if n, _ := svc.Users.Count(); n > 0 {
		fail("an account already exists for this Glowworm")
	}

	in := bufio.NewReader(os.Stdin)
	if username == "" {
		fmt.Print("Username: ")
		line, _ := in.ReadString('\n')
		username = strings.TrimSpace(line)
	}
	password := readSecret(in, "Password (at least 10 characters): ")
	if confirm := readSecret(in, "Confirm password: "); confirm != password {
		fail("passwords do not match")
	}
	if _, err := svc.CreateOwner(username, password, "", core.ActorSystem, "from terminal"); err != nil {
		fail("%v", err)
	}
	fmt.Printf("Created account %q. Sign in with it in the browser.\n", username)
}

// readSecret turns terminal echo off while the password is typed; piped input
// (no terminal) is read as-is.
func readSecret(in *bufio.Reader, prompt string) string {
	fmt.Print(prompt)
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	echoOff := off.Run() == nil
	line, _ := in.ReadString('\n')
	if echoOff {
		on := exec.Command("stty", "echo")
		on.Stdin = os.Stdin
		_ = on.Run()
		fmt.Println()
	}
	return strings.TrimRight(line, "\r\n")
}
