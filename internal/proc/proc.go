// Package proc holds the small OS-specific bits of starting child processes:
// running a shell command line and giving a child its own process group.
package proc

import (
	"context"
	"os"
	"os/exec"
)

// Shell returns a command that runs line through the platform shell:
// /bin/sh -c on Unix; on Windows, Git for Windows' sh.exe when found
// (so acceptance commands written for sh keep working), else cmd /C.
func Shell(ctx context.Context, line string) *exec.Cmd {
	name, args := shellArgv(line)
	return exec.CommandContext(ctx, name, args...)
}

// UserHome is $HOME when set, else os.UserHomeDir. On Unix the two agree; on
// Windows os.UserHomeDir reads only USERPROFILE, so an explicit HOME (Git Bash,
// or a test) would otherwise be ignored.
func UserHome() (string, error) {
	if h := os.Getenv("HOME"); h != "" {
		return h, nil
	}
	return os.UserHomeDir()
}

// Script rewrites cmd to run a "#!" script through sh when the OS cannot exec
// it directly (Windows). It is a no-op on Unix and for real executables.
func Script(cmd *exec.Cmd) {
	if cmd == nil || (cmd.Path == "" && len(cmd.Args) == 0) {
		return
	}
	scriptCmd(cmd)
}

func hasShebang(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var b [2]byte
	n, _ := f.Read(b[:])
	return n == 2 && b[0] == '#' && b[1] == '!'
}
