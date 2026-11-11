//go:build !windows

package host_test

import (
	"os"
	"syscall"
	"testing"
)

// makeBlockingPrompt replaces path with a FIFO, so opening it for reading
// blocks until release opens the write side.
func makeBlockingPrompt(t *testing.T, path string) func() {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	return func() {
		f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = f.Close()
		}
	}
}
