//go:build !darwin && !linux && !windows

package pty

import "os/exec"

// Start reports that this platform has no PTY implementation.
func Start(cmd *exec.Cmd) (*PTY, error) {
	return nil, ErrNotImplemented
}
