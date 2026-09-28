//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Batch launchers need cmd.exe; Go's CreateProcess quoting is for executables.
// Grok receives its arbitrary prompt through a file, never through shell text.
func providerCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	ext := strings.ToLower(filepath.Ext(cmd.Path))
	if ext != ".cmd" && ext != ".bat" {
		return cmd
	}
	if cmd.Err != nil {
		return cmd
	}
	words := append([]string{cmd.Path}, args...)
	for i, word := range words {
		// Percent expansion occurs even inside quotes; reject rather than reinterpret.
		if strings.ContainsAny(word, "\"%!\r\n\x00") {
			cmd.Err = fmt.Errorf("unsafe batch launcher argument; use a native executable")
			return cmd
		}
		// The child executable uses Windows argv quoting: double trailing backslashes
		// so they cannot escape the closing quote after cmd forwards the command line.
		trailing := len(word) - len(strings.TrimRight(word, "\\"))
		words[i] = "\"" + word + strings.Repeat("\\", trailing) + "\""
	}
	shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if !filepath.IsAbs(shell) {
		cmd.Err = fmt.Errorf("absolute Windows system shell unavailable")
		return cmd
	}
	cmd = exec.CommandContext(ctx, shell)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "\"" + shell + "\" /d /s /v:off /c \"" + strings.Join(words, " ") + "\""}
	return cmd
}

// TOML literal strings preserve Windows backslashes without embedded double
// quotes, so the usual npm .cmd shim can forward this option unchanged.
func providerCodexWritableRootsArg(dir string) string {
	roots := codexWritableRoots(dir)
	quoted := make([]string, len(roots))
	for i, root := range roots {
		if strings.ContainsAny(root, "'\r\n") {
			// Keep the executable-compatible representation; a batch launcher rejects it.
			return codexWritableRootsArg(dir)
		}
		quoted[i] = "'" + root + "'"
	}
	return "sandbox_workspace_write.writable_roots=[" + strings.Join(quoted, ",") + "]"
}
