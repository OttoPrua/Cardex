//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func localCompletionShell() (name string, prefix []string) {
	root := strings.TrimSpace(os.Getenv("SystemRoot"))
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "cmd.exe"), []string{"/D", "/S", "/V:OFF", "/C"}
}

// cmd.exe parses its command expression differently from CommandLineToArgvW.
// Match the existing batch adapter: preserve the expression inside cmd's outer
// quotes instead of letting exec.Command escape its internal quotes with slashes.
func localCompletionCommand(ctx context.Context, command string) *exec.Cmd {
	name, prefix := localCompletionShell()
	cmd := exec.CommandContext(ctx, name)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: syscall.EscapeArg(name) + " " + strings.Join(prefix, " ") + " \"" + command + "\"",
	}
	return cmd
}
