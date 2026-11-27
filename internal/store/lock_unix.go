//go:build !windows

package store

import (
	"os"
	"syscall"
)

func lockExclusive(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// lockSnapshotRead uses the same lock as Open, but never creates it or waits
// behind a writer. It is not a substitute for SQLite's journal locks.
func lockSnapshotRead(f *os.File) (func(), error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}

func openSnapshotFile(path string, strict bool) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if strict {
		flags |= snapshotReadFlags
	}
	return os.OpenFile(path, flags, 0)
}
