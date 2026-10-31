//go:build darwin

package sandbox

import (
	"fmt"
	"os/exec"
	"syscall"
)

var osErrNil = fmt.Errorf("sandbox: nil command")

func wrapOS(cmd *exec.Cmd, writable []string) (*exec.Cmd, error) {
	profile := Profile(writable)
	wrapped := exec.Command("/usr/bin/sandbox-exec", "-p", profile, "--", cmd.Path)
	args := append([]string{"sandbox-exec", "-p", profile, "--"}, cmd.Args...)
	wrapped.Path = "/usr/bin/sandbox-exec"
	wrapped.Args = args
	wrapped.Env = cmd.Env
	wrapped.Dir = cmd.Dir
	wrapped.Stdin = cmd.Stdin
	wrapped.Stdout = cmd.Stdout
	wrapped.Stderr = cmd.Stderr
	if cmd.SysProcAttr != nil {
		attr := *cmd.SysProcAttr
		wrapped.SysProcAttr = &attr
	} else {
		wrapped.SysProcAttr = &syscall.SysProcAttr{}
	}
	return wrapped, nil
}
