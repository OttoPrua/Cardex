package host

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// signalKill is the only process-signal syscall. Tests replace it.
var signalKill = syscall.Kill

// ErrRefused means the target was not signaled.
var ErrRefused = errors.New("signal refused")

// SignalProcess signals one process.
// pid <= 1, this process, and this process group are refused before the syscall.
func SignalProcess(pid int, sig syscall.Signal) error {
	if err := refuseTarget(pid); err != nil {
		return err
	}
	return signalKill(pid, sig)
}

// SignalGroup signals process group pgid.
// The negative kill convention is applied only after the same refusal.
func SignalGroup(pgid int, sig syscall.Signal) error {
	if err := refuseTarget(pgid); err != nil {
		return err
	}
	return signalKill(-pgid, sig)
}

func refuseTarget(id int) error {
	if id <= 1 {
		return fmt.Errorf("%w: target %d", ErrRefused, id)
	}
	if id == os.Getpid() {
		return fmt.Errorf("%w: this process %d", ErrRefused, id)
	}
	if id == syscall.Getpgrp() || inOurGroup(id) {
		return fmt.Errorf("%w: this process group %d", ErrRefused, id)
	}
	return nil
}

func inOurGroup(id int) bool {
	pgid, err := syscall.Getpgid(id)
	if err != nil {
		return false
	}
	return pgid == syscall.Getpgrp()
}
