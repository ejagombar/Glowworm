//go:build unix

package store

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var ErrLocked = errors.New("data directory is in use by another Glowworm process")

func LockDataDir(dir string) (release func(), err error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	// Closing f (including via its GC finalizer) releases the lock, so the
	// caller must keep this func alive.
	return func() { f.Close() }, nil
}
