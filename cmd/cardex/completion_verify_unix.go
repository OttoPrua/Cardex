//go:build !windows

package main

import (
	"context"
	"os/exec"
)

func localCompletionShell() (name string, prefix []string) {
	return "/bin/sh", []string{"-c"}
}

func localCompletionCommand(ctx context.Context, command string) *exec.Cmd {
	name, prefix := localCompletionShell()
	return exec.CommandContext(ctx, name, append(prefix, command)...)
}
