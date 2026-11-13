//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
)

func shellArgv(line string) (string, []string) { return "/bin/sh", []string{"-c", line} }

// NewGroup starts cmd as the leader of its own process group, so a group
// signal reaches the whole tree.
func NewGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// Detach is NewGroup on Unix.
func Detach(cmd *exec.Cmd) { NewGroup(cmd) }

func scriptCmd(cmd *exec.Cmd) {}

// Tool finds a POSIX tool such as false or sleep on PATH.
func Tool(name string) (string, error) { return exec.LookPath(name) }

// LookPath is exec.LookPath on Unix; env is used only on Windows.
func LookPath(name string, env []string) (string, error) { return exec.LookPath(name) }
