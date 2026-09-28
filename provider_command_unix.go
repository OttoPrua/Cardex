//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
)

func providerCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

func createPrivateProviderFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return f, nil
}

func lifecycleProbeModeValid(info os.FileInfo) bool { return info.Mode().Perm() == 0600 }

func providerCodexWritableRootsArg(dir string) string { return codexWritableRootsArg(dir) }
