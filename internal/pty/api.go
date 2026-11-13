// Package pty opens a pseudo-terminal and starts a command on it. Design §2.
package pty

import (
	"errors"
	"io"
	"os/exec"
)

// ErrNotImplemented is returned on platforms without a PTY implementation.
var ErrNotImplemented = errors.New("not implemented")

// PTY is the master side of a started command.
type PTY struct {
	Master io.ReadWriteCloser // 供 Drive 读写的主端。
	Cmd    *exec.Cmd          // 已启动的命令；Cmd.Process 可用，等待请用 Wait。
	wait   func() error
}

// Wait waits for the command to exit. On Windows it then closes the pseudo
// console so reads from Master drain and end; call it instead of Cmd.Wait.
func (p *PTY) Wait() error {
	if p.wait != nil {
		return p.wait()
	}
	return p.Cmd.Wait()
}

// Rows and Cols are the terminal size every PTY starts with, on ConPTY and
// Unix alike. A TUI on a 0x0 terminal wraps every character onto its own line.
const (
	Rows = 50
	Cols = 200
)
