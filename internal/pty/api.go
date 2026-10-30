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
	Cmd    *exec.Cmd          // 已启动的命令。
}
