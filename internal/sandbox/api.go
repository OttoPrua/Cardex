// Package sandbox wraps an executor so it cannot write outside its worktree. Design §13.
package sandbox

import (
	"errors"
	"os/exec"
)

// ErrNotImplemented is returned when a platform cannot build a sandbox wrapper.
var ErrNotImplemented = errors.New("not implemented")

// Event is one sandbox fact. Host persists it; the wrapper does not open the store.
type Event struct {
	Type string
	Data string
}

// Wrap returns cmd restricted to the writable paths.
// On non-macOS the command is not wrapped and one sandbox_skipped event is recorded.
func Wrap(cmd *exec.Cmd, writable []string) (*exec.Cmd, error) {
	if cmd == nil {
		return nil, errors.New("sandbox: nil command")
	}
	return wrapOS(cmd, writable)
}
