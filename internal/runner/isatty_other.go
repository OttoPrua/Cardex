//go:build !darwin && !linux && !windows

package runner

func stdoutIsTTY() bool { return false }
