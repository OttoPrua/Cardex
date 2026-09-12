//go:build windows

package main

import "os/exec"

func setupManualGoalProcGroup(cmd *exec.Cmd) (func() error, error) {
	setupProcGroup(cmd)
	return func() error { return nil }, nil
}
