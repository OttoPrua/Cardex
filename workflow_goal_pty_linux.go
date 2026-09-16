//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func grantUnlockPTY(master *os.File) error {
	var unlock int
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x40045431, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		return errno
	}
	return nil
}

func ptySlaveName(master *os.File) (string, error) {
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x80045430, uintptr(unsafe.Pointer(&n))); errno != 0 {
		return "", errno
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}
