//go:build darwin || linux

package main

import (
	"errors"
	"strings"
	"syscall"
	"testing"
)

func ptyStartDenied(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "operation not permitted") ||
		strings.Contains(s, "operation not supported by device") ||
		errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.ENODEV)
}

func skipIfGoalPTYUnavailable(t *testing.T) {
	t.Helper()
	master, slave, err := openGoalPTY()
	if err != nil {
		if ptyStartDenied(err) {
			t.Skipf("pty unavailable: %v", err)
		}
		t.Fatalf("openGoalPTY: %v", err)
	}
	master.Close()
	slave.Close()
}
