//go:build !unix

package store

import "errors"

var ErrLocked = errors.New("data directory is in use by another Glowworm process")

func LockDataDir(dir string) (release func(), err error) { return func() {}, nil }
