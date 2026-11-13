//go:build darwin || linux

package pty

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

func setWinsize(f *os.File, rows, cols uint16) error {
	ws := struct{ Row, Col, X, Y uint16 }{rows, cols, 0, 0}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(&ws))); errno != 0 {
		return errno
	}
	return nil
}

// Start opens a PTY and starts cmd on the slave side.
// The child is a session leader so its process group can be signalled as a unit.
func Start(cmd *exec.Cmd) (*PTY, error) {
	if cmd == nil {
		return nil, os.ErrInvalid
	}
	master, slave, err := openPTY()
	if err != nil {
		return nil, err
	}
	if err := setWinsize(slave, Rows, Cols); err != nil {
		master.Close()
		slave.Close()
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Setsid already creates a new process group. Setting Setpgid as well
	// makes exec.Start fail on some kernels.
	cmd.SysProcAttr.Setpgid = false
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return nil, err
	}
	slave.Close()
	return &PTY{Master: master, Cmd: cmd}, nil
}

func openPTY() (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := grantUnlockPTY(master); err != nil {
		master.Close()
		return nil, nil, err
	}
	name, err := ptySlaveName(master)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	slave, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
