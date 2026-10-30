package sandbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cardex/internal/sandbox"
)

func TestProfileAllowsReadNetworkAndWriteList(t *testing.T) {
	dir := t.TempDir()
	profile := sandbox.Profile([]string{dir})
	for _, needle := range []string{
		"(allow default)",
		"(allow file-read*)",
		"(allow network*)",
		"(deny file-write*)",
		"(literal \"/dev/null\")",
		"(literal \"/dev/dtracehelper\")",
	} {
		if !strings.Contains(profile, needle) {
			t.Fatalf("profile missing %s\n%s", needle, profile)
		}
	}
	if !strings.Contains(profile, dir) && !strings.Contains(profile, mustReal(t, dir)) {
		t.Fatalf("profile missing writable dir %s\n%s", dir, profile)
	}
	tmp := mustReal(t, os.TempDir())
	if !strings.Contains(profile, tmp) && !strings.Contains(profile, os.TempDir()) {
		t.Fatalf("profile missing TMPDIR %s\n%s", tmp, profile)
	}
}

func TestSkipRecordsOneEvent(t *testing.T) {
	cmd := exec.Command("/bin/echo", "hi")
	out, err := sandbox.Skip(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if out.Path != cmd.Path || strings.Contains(strings.Join(out.Args, " "), "sandbox-exec") {
		t.Fatalf("skip wrapped the command: %v", out.Args)
	}
	ev := sandbox.Consume(out)
	if len(ev) != 1 {
		t.Fatalf("events %#v", ev)
	}
	if ev[0].Type != "sandbox_skipped" || !strings.Contains(ev[0].Data, "not macOS") {
		t.Fatalf("event %#v", ev[0])
	}
	if again := sandbox.Consume(out); len(again) != 0 {
		t.Fatalf("event recorded twice: %#v", again)
	}
}

func TestWrapDeniesOutsideWrite(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	inside := t.TempDir()
	outside := outsideDir(t)
	if err := os.WriteFile(filepath.Join(outside, "seed.txt"), []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpFile := filepath.Join(os.TempDir(), "cardex-sandbox-"+filepath.Base(inside))
	t.Cleanup(func() { _ = os.Remove(tmpFile) })

	script := `cat "$OUTSIDE/seed.txt"; echo in > "$INSIDE/f"; echo tmp > "$TMPFILE"; echo bad > "$OUTSIDE/newfile"`
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(),
		"INSIDE="+inside,
		"OUTSIDE="+outside,
		"TMPFILE="+tmpFile,
	)
	wrapped, err := sandbox.Wrap(cmd, []string{inside})
	if err != nil {
		t.Fatal(err)
	}
	if len(sandbox.Consume(wrapped)) != 0 {
		t.Fatal("darwin wrap recorded a skip event")
	}
	if !strings.Contains(strings.Join(wrapped.Args, "\n"), "sandbox-exec") {
		t.Fatalf("not wrapped: %v", wrapped.Args[:1])
	}
	out, runErr := wrapped.CombinedOutput()
	if !strings.Contains(string(out), "readable") {
		t.Fatalf("read outside the write list failed: %v %s", runErr, out)
	}
	if body, err := os.ReadFile(filepath.Join(inside, "f")); err != nil || string(body) != "in\n" {
		t.Fatalf("inside write: %q %v", body, err)
	}
	if body, err := os.ReadFile(tmpFile); err != nil || string(body) != "tmp\n" {
		t.Fatalf("TMPDIR write: %q %v %s", body, err, out)
	}
	if _, err := os.Stat(filepath.Join(outside, "newfile")); !os.IsNotExist(err) {
		t.Fatalf("outside create was allowed: %v", err)
	}
	text := strings.ToLower(string(out))
	if !strings.Contains(text, "operation not permitted") && !strings.Contains(text, "deny file-write") && runErr == nil {
		t.Fatalf("outside denial not visible: err=%v out=%s", runErr, out)
	}
}

func TestWrapAllowsLocalNetwork(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	// Port 1 refuses the connection. A sandbox network deny would be "not permitted"
	// rather than "connection refused".
	cmd := exec.Command("/usr/bin/nc", "-z", "-G", "1", "127.0.0.1", "1")
	wrapped, err := sandbox.Wrap(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, runErr := wrapped.CombinedOutput()
	text := strings.ToLower(string(out))
	if runErr != nil {
		text += " " + strings.ToLower(runErr.Error())
	}
	if strings.Contains(text, "deny network") || strings.Contains(text, "operation not permitted") {
		t.Fatalf("network denied: %v %s", runErr, out)
	}
}

func mustReal(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return real
}

func outsideDir(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	tmpReal := mustReal(t, os.TempDir())
	baseReal := mustReal(t, base)
	if baseReal == tmpReal || strings.HasPrefix(baseReal, tmpReal+string(os.PathSeparator)) || strings.HasPrefix(tmpReal, baseReal+string(os.PathSeparator)) {
		base = "/var/tmp"
	}
	dir, err := os.MkdirTemp(base, "cardex-outside-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
