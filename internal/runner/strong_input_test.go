package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrongInputStopsUnverifiedRunnerBeforeStaging(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	t.Setenv("TMP", base)
	t.Setenv("TEMP", base)
	for _, r := range []Runner{nil, Fake{}, Stub{}} {
		path, cleanup, err := PrepareStrongInput(context.Background(), r, []byte("PRIVATE-CONTENT"))
		if !errors.Is(err, ErrStrongIsolation) || path != "" || cleanup != nil {
			t.Fatalf("%T %q %v", r, path, err)
		}
	}
	if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
		t.Fatalf("wrote despite denied precondition: %+v %v", entries, err)
	}
}

func TestStrongInputSyntheticFileTransport(t *testing.T) {
	body := bytes.Repeat([]byte("LONG-MARKER-SYNTHETIC-"), 12000)
	capability := StrongInputCapability{ArgvVersion: "test-file-v1", FreshProcess: true, NoSession: true, NoMemoryOrSubagents: true, FileInput: true, HardFilesystem: true, HardNetwork: true, MaxBytes: len(body)}
	// The flags above exercise the positive transport branch only. They are
	// not evidence of hard isolation for any actual runner on any OS.
	if _, _, err := prepareStrongInput(context.Background(), capability, append(body, 'x')); !errors.Is(err, ErrStrongTransport) {
		t.Fatalf("overflow: %v", err)
	}
	path, cleanup, err := prepareStrongInput(context.Background(), capability, body)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStrongInputHelperProcess$", "--", path)
	cmd.Env = append(os.Environ(), "CARDEX_STRONG_INPUT_HELPER=1")
	if strings.Contains(strings.Join(cmd.Args, " "), "LONG-MARKER") || len(strings.Join(cmd.Args, " ")) > 2048 {
		t.Fatal("content leaked into argv")
	}
	out, err := cmd.Output()
	if err != nil || !bytes.Equal(out, body) {
		t.Fatalf("path-only transport length=%d err=%v", len(out), err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("input not cleaned: %v", err)
	}
}

func TestStrongInputHelperProcess(t *testing.T) {
	if os.Getenv("CARDEX_STRONG_INPUT_HELPER") != "1" {
		return
	}
	data, err := os.ReadFile(os.Args[len(os.Args)-1])
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprint(os.Stdout, string(data))
	os.Exit(0)
}
