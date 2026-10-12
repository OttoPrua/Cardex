//go:build windows

package main

import (
	"errors"
	"syscall"
)

const (
	windowsERROR_INVALID_PARAMETER = syscall.Errno(87)
	windowsERROR_ACCESS_DENIED     = syscall.Errno(5)
)

// classifyProcessObservation reports Windows liveness for pid.
// Access denied and other failed observations are permission-unknown, not dead.
func classifyProcessObservation(pid int) processObservation {
	if pid <= 0 {
		return processObservationDead
	}
	handle, err := syscall.OpenProcess(0x100000, false, uint32(pid)) // SYNCHRONIZE
	if err != nil {
		return classifyWindowsProcessOpenError(err)
	}
	defer syscall.CloseHandle(handle)
	status, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		return processObservationUnknown
	}
	if status == syscall.WAIT_OBJECT_0 {
		return processObservationDead
	}
	return processObservationLive
}

func classifyWindowsProcessOpenError(err error) processObservation {
	if err == nil {
		return processObservationLive
	}
	if errors.Is(err, windowsERROR_INVALID_PARAMETER) {
		return processObservationDead
	}
	return processObservationUnknown
}
