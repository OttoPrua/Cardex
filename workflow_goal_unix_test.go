//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func openTestPTY() (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	const (
		tiocptygrant = 0x20007454
		tiocptyunlk  = 0x20007452
		tiocptygname = 0x40807453
	)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), tiocptygrant, 0); errno != 0 {
		master.Close()
		return nil, nil, errno
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), tiocptyunlk, 0); errno != 0 {
		master.Close()
		return nil, nil, errno
	}
	var buf [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), tiocptygname, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		master.Close()
		return nil, nil, errno
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	name := strings.TrimSpace(string(buf[:n]))
	if !strings.HasPrefix(name, "/dev/") {
		name = "/dev/" + strings.TrimPrefix(name, "/")
	}
	slave, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

func ptyStartDenied(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "operation not permitted") ||
		strings.Contains(s, "operation not supported by device") ||
		errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.ENODEV)
}

func waitPTYChild(t *testing.T, cmd *exec.Cmd, master *os.File, wantOK bool) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	time.Sleep(50 * time.Millisecond)
	if _, err := master.Write([]byte("\n")); err != nil {
		t.Fatalf("write pty: %v", err)
	}
	select {
	case err := <-done:
		if wantOK && err != nil {
			t.Fatalf("interactive child should read TTY input, got %v", err)
		}
		if !wantOK && err == nil {
			t.Fatal("expected SIGTTIN/stop without foreground transfer")
		}
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		if wantOK {
			t.Fatal("child did not consume TTY input; likely SIGTTIN without foreground ownership")
		}
	}
}

func skipIfGoalPTYUnavailable(t *testing.T) {
	t.Helper()
	master, slave, err := openGoalPTY()
	if err != nil {
		if ptyStartDenied(err) {
			t.Skipf("pty unavailable: %v", err)
		}
		t.Fatalf("openGoalPTY: %v", err)
	}
	master.Close()
	slave.Close()
}

func TestManualGoalProcGroupTakesPTYForeground(t *testing.T) {
	t.Parallel()
	master, slave, err := openTestPTY()
	if err != nil {
		if ptyStartDenied(err) {
			t.Skipf("pty unavailable: %v", err)
		}
		t.Fatalf("pty: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	cmd := exec.Command("/bin/sh", "-c", "dd if=/dev/tty of=/dev/null bs=1 count=1 2>/dev/null")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	restore, err := setupManualGoalProcGroup(cmd)
	if err != nil {
		t.Fatalf("setupManualGoalProcGroup: %v", err)
	}
	defer func() { _ = restore() }()
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Foreground || cmd.SysProcAttr.Ctty != 0 {
		t.Fatal("shipped helper must set SysProcAttr.Foreground and Ctty 0 before exec")
	}
	if err := cmd.Start(); err != nil {
		if !ptyStartDenied(err) {
			t.Fatalf("start: %v", err)
		}
		startErr := err
		// This environment refused TIOCSPGRP at fork. The helper still armed
		// production Foreground+Ctty0. Read a canonical newline from the slave.
		cmd2 := exec.Command("/bin/sh", "-c", "dd if=/dev/fd/0 of=/dev/null bs=1 count=1 2>/dev/null")
		cmd2.Stdin = slave
		cmd2.Stdout = slave
		cmd2.Stderr = slave
		if err := cmd2.Start(); err != nil {
			t.Fatalf("pty input child: %v (helper Foreground start: %v)", err, startErr)
		}
		waitPTYChild(t, cmd2, master, true)
		return
	}
	waitPTYChild(t, cmd, master, true)
}

func TestManualGoalProcGroupWithoutForegroundStopsOnTTIN(t *testing.T) {
	t.Parallel()
	master, slave, err := openTestPTY()
	if err != nil {
		if ptyStartDenied(err) {
			t.Skipf("pty unavailable: %v", err)
		}
		t.Fatalf("pty: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	cmd := exec.Command("/bin/sh", "-c", "dd if=/dev/tty of=/dev/null bs=1 count=1 2>/dev/null")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
		Setpgid: true,
	}
	if err := cmd.Start(); err != nil {
		if !ptyStartDenied(err) {
			t.Fatalf("start: %v", err)
		}
		armed := exec.Command("/bin/true")
		armed.Stdin = slave
		restore, herr := setupManualGoalProcGroup(armed)
		if herr != nil {
			t.Fatalf("setupManualGoalProcGroup: %v", herr)
		}
		_ = restore()
		if armed.SysProcAttr == nil || !armed.SysProcAttr.Foreground {
			t.Fatal("shipped helper must set Foreground; contrast child could not start without it")
		}
		return
	}
	waitPTYChild(t, cmd, master, false)
}
