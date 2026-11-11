package host

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// signalKill is the only process-signal syscall. Tests replace it.
// A negative pid targets the process group (Unix) or process tree (Windows).
var signalKill = osSignal

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
	if id == ownGroup() || inOurGroup(id) {
		return fmt.Errorf("%w: this process group %d", ErrRefused, id)
	}
	return nil
}
