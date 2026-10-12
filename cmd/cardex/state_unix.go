//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// classifyProcessObservation reports POSIX liveness for pid.
// Signal 0: nil is live, ESRCH/ErrProcessDone is dead, EPERM and every other
// observation failure are permission-unknown (not stealable).
func classifyProcessObservation(pid int) processObservation {
	if pid <= 0 {
		return processObservationDead
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return classifySignalZeroError(err)
	}
	return classifySignalZeroError(proc.Signal(syscall.Signal(0)))
}

// classifySignalZeroError maps a kill(pid,0) / FindProcess result onto the
// three-way observation class. Tests drive this helper with fixture errnos so
// they do not depend on PID 1 returning EPERM.
func classifySignalZeroError(err error) processObservation {
	if err == nil {
		return processObservationLive
	}
	if errors.Is(err, os.ErrProcessDone) {
		return processObservationDead
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if errno == syscall.ESRCH {
			return processObservationDead
		}
		return processObservationUnknown
	}
	return processObservationUnknown
}
