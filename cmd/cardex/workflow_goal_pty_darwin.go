//go:build darwin

package main

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

func grantUnlockPTY(master *os.File) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCPTYGRANT, 0); errno != 0 {
		return errno
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCPTYUNLK, 0); errno != 0 {
		return errno
	}
	return nil
}

func ptySlaveName(master *os.File) (string, error) {
	var buf [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return "", errno
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	name := strings.TrimSpace(string(buf[:n]))
	if !strings.HasPrefix(name, "/dev/") {
		name = "/dev/" + strings.TrimPrefix(name, "/")
	}
	return name, nil
}
