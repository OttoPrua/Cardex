//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
)

func localCompletionShell() (name string, prefix []string) {
	root := strings.TrimSpace(os.Getenv("SystemRoot"))
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "cmd.exe"), []string{"/C"}
}
