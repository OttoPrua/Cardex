//go:build linux

package pty

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func grantUnlockPTY(master *os.File) error {
	// TIOCSPTLCK: unlock the slave (0). Value matches the Linux uapi ioctl.
	var unlock int
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x40045431, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		return errno
	}
	return nil
}

func ptySlaveName(master *os.File) (string, error) {
	// TIOCGPTN: slave index. Value matches the Linux uapi ioctl.
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x80045430, uintptr(unsafe.Pointer(&n))); errno != 0 {
		return "", errno
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}
