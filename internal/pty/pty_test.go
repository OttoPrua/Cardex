package pty_test

import (
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/pty"
)

func TestStartReadsChildOutput(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "echo pty-marker")
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
	waitErr := cmd.Wait()
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
