//go:build windows

package main

import "syscall"

// processAlive 报告 pid 对应的进程是否存活。
// Wait on the process object: OpenProcess alone can still succeed for an exited
// process while another handle retains it. Windows does not implement signal 0.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(0x100000, false, uint32(pid)) // SYNCHRONIZE
	if err != nil {
		// Access denied or failed observation is not evidence of a dead lock owner.
		return err != syscall.Errno(87) // ERROR_INVALID_PARAMETER: PID no longer exists
	}
	defer syscall.CloseHandle(handle)
	status, err := syscall.WaitForSingleObject(handle, 0)
	return err != nil || status != syscall.WAIT_OBJECT_0
}
