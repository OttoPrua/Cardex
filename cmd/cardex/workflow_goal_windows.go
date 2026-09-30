//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

func setupManualGoalProcGroup(cmd *exec.Cmd) (func() error, error) {
	setupProcGroup(cmd)
	return func() error { return nil }, nil
}

func fileIsTerminal(f *os.File) bool { return false }

func stdinIsTTY() bool { return false }

func openGoalPTY() (*os.File, *os.File, error) {
	return nil, nil, fmt.Errorf("%s: windows", goalFailPTYMissing)
}
