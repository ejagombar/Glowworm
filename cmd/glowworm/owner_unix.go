//go:build unix

package main

import (
	"os"
	"syscall"
)

// sameOwner gives paths to the owner of dir, so a backup made with sudo
// stays readable by the user Glowworm runs as.
func sameOwner(dir string, paths ...string) {
	st, err := os.Stat(dir)
	if err != nil {
		return
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 {
		return
	}
	for _, p := range paths {
		_ = os.Chown(p, int(sys.Uid), int(sys.Gid))
	}
}
