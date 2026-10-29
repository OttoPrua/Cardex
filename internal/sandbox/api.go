// Package sandbox wraps an executor so it cannot write outside its worktree. Design §13.
package sandbox

import (
	"errors"
	"os/exec"
)

// ErrNotImplemented is returned until R2-A fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Wrap returns cmd restricted to the writable paths.
// On non-macOS the implementation records that the sandbox was skipped.
func Wrap(cmd *exec.Cmd, writable []string) (*exec.Cmd, error) {
	return nil, ErrNotImplemented
}
