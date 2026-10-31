//go:build darwin

package runner

import (
	"os"
	"syscall"
	"unsafe"
)

func stdoutIsTTY() bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
