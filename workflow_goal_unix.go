//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
	"unsafe"
)

func fileIsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(ioctlReadTermios), uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}

func stdinIsTTY() bool {
	return fileIsTerminal(os.Stdin)
}

func manualGoalTTYFile(cmd *exec.Cmd) *os.File {
	if cmd != nil && cmd.Stdin != nil {
		if f, ok := cmd.Stdin.(*os.File); ok {
			if fileIsTerminal(f) {
				return f
			}
			return nil
		}
		return nil
	}
	if stdinIsTTY() {
		return os.Stdin
	}
	return nil
}

// setupManualGoalProcGroup arms the manual-only child with Foreground+Ctty
// before exec. Headless paths keep the ordinary Setpgid group. The returned
// restore function puts the original foreground group back and preserves
// ioctl errors. This does not use the global afterCmdStart hook.
func setupManualGoalProcGroup(cmd *exec.Cmd) (restore func() error, err error) {
	noop := func() error { return nil }
	if cmd == nil {
		return noop, fmt.Errorf("manual goal: nil command")
	}
	tty := manualGoalTTYFile(cmd)
	if tty == nil {
		setupProcGroup(cmd)
		return noop, nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cmd.SysProcAttr.Setctty {
		return noop, fmt.Errorf("manual goal: Foreground and Setctty cannot both be set")
	}
	origPgid := syscall.Getpgrp()
	fd := int(os.Stdin.Fd())
	if tty != os.Stdin {
		fd = int(tty.Fd())
		origPgrp, err := syscall.Getpgid(0)
		if err == nil && origPgrp > 0 {
			origPgid = origPgrp
		}
	}
	cmd.SysProcAttr.Foreground = true
	cmd.SysProcAttr.Ctty = 0
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 10 * time.Second
	}
	return func() error {
		return ioctlSetForegroundPgrp(fd, origPgid)
	}, nil
}

func ioctlSetForegroundPgrp(fd, pgid int) error {
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	_, _, errno := syscall.RawSyscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TIOCSPGRP), uintptr(unsafe.Pointer(&pgid)))
	if errno != 0 {
		return errno
	}
	return nil
}
