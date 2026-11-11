//go:build !windows

package host

import (
	"syscall"
)

func osSignal(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

// ownGroup is this process's group id.
func ownGroup() int { return syscall.Getpgrp() }

func inOurGroup(id int) bool {
	pgid, err := syscall.Getpgid(id)
	if err != nil {
		return false
	}
	return pgid == syscall.Getpgrp()
}
