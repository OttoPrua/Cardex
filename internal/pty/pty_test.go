package pty_test

import (
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/pty"
)

func TestStartReadsChildOutput(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "echo pty-marker")
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", "echo pty-marker")
	}
	session, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Master.Close()

	readDone := make(chan struct{})
	var buf []byte
	var readErr error
	go func() {
		buf, readErr = io.ReadAll(session.Master)
		close(readDone)
	}()
	waitErr := session.Wait()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading the PTY master")
	}
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	if readErr != nil && len(buf) == 0 {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(buf), "pty-marker") {
		t.Fatalf("master output %q", buf)
	}
}

func TestStartSetsTerminalSize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ConPTY size is set at CreatePseudoConsole")
	}
	session, err := pty.Start(exec.Command("/bin/sh", "-c", "stty size"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Master.Close()
	done := make(chan []byte, 1)
	go func() { b, _ := io.ReadAll(session.Master); done <- b }()
	_ = session.Wait()
	var out []byte
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading the PTY master")
	}
	if !strings.Contains(string(out), "50 200") {
		t.Fatalf("stty size %q", out)
	}
}
