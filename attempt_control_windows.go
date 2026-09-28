//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var windowsKernel = syscall.NewLazyDLL("kernel32.dll")
var lockFileEx = windowsKernel.NewProc("LockFileEx")
var unlockFileEx = windowsKernel.NewProc("UnlockFileEx")

func withControlFileLock(root string, taskID string, fn func() error) error {
	path := controlLockPath(root, taskID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	var overlapped syscall.Overlapped
	ok, _, callErr := lockFileEx.Call(f.Fd(), 2, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		return fmt.Errorf("lock task control: %w", callErr)
	}
	defer unlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	return fn()
}

func attemptProcessPGID(pid int) int { return pid }

func canonicalWorkspaceID(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return strings.ToLower(filepath.Clean(abs))
}

func processStartIdentity(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	return fmtWindowsPIDIdentity(pid)
}

func fmtWindowsPIDIdentity(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	h, err := syscall.OpenProcess(0x1000, false, uint32(pid)) // PROCESS_QUERY_LIMITED_INFORMATION
	if err != nil {
		return "", false
	}
	defer syscall.CloseHandle(h)
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", false
	}
	return fmt.Sprintf("windows:%08x%08x", created.HighDateTime, created.LowDateTime), true
}

func verifyAttemptProcess(rec *AttemptRecord) bool {
	if rec == nil || rec.PID <= 0 {
		return false
	}
	if rec.StartIdentity == "" || rec.WorkspaceLeaseID == "" {
		return false
	}
	got, ok := processStartIdentity(rec.PID)
	return ok && got == rec.StartIdentity && (rec.PGID == 0 || rec.PGID == rec.PID) && processAlive(rec.PID) && workspaceLeaseHeld(rec.WorkspaceLeaseID)
}
