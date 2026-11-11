// Package proc holds the small OS-specific bits of starting child processes:
// running a shell command line and giving a child its own process group.
package proc

import (
	"context"
	"os/exec"
)

// Shell returns a command that runs line through the platform shell:
// /bin/sh -c on Unix; on Windows, Git for Windows' sh.exe when found
// (so acceptance commands written for sh keep working), else cmd /C.
func Shell(ctx context.Context, line string) *exec.Cmd {
	name, args := shellArgv(line)
	return exec.CommandContext(ctx, name, args...)
}
