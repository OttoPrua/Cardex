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
