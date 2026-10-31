//go:build !darwin && !linux

package runner

func stdoutIsTTY() bool { return false }
