package sandbox

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestWrapOuterSandbox(t *testing.T) {
	t.Setenv("CARDEX_OUTER_SANDBOX", "1")
	out := wrapEcho(t)
	if out.Path == "/usr/bin/sandbox-exec" || strings.Contains(strings.Join(out.Args, " "), "sandbox-exec") {
		t.Fatalf("outer sandbox still wrapped: path=%s args=%v", out.Path, out.Args)
	}
	skipped := countSkipped(Consume(out))
	if skipped != 1 {
		t.Fatalf("CARDEX_OUTER_SANDBOX=1: sandbox_skipped=%d, want 1", skipped)
	}
	t.Logf("CARDEX_OUTER_SANDBOX=1 path=%s sandbox_skipped=%d", out.Path, skipped)

	t.Setenv("CARDEX_OUTER_SANDBOX", "")
	out = wrapEcho(t)
	skipped = countSkipped(Consume(out))
	if runtime.GOOS == "darwin" {
		if out.Path != "/usr/bin/sandbox-exec" {
			t.Fatalf("unset env darwin path=%s, want /usr/bin/sandbox-exec", out.Path)
		}
		if skipped != 0 {
			t.Fatalf("unset env recorded sandbox_skipped=%d", skipped)
		}
	}
	t.Logf("unset env path=%s sandbox_skipped=%d", out.Path, skipped)

	prev := testingTesting
	testingTesting = func() bool { return false }
	t.Cleanup(func() { testingTesting = prev })
	t.Setenv("CARDEX_OUTER_SANDBOX", "1")
	out = wrapEcho(t)
	skipped = countSkipped(Consume(out))
	if runtime.GOOS == "darwin" {
		if out.Path != "/usr/bin/sandbox-exec" {
			t.Fatalf("testing.Testing guard missing: path=%s", out.Path)
		}
		if skipped != 0 {
			t.Fatalf("testing.Testing guard missing: sandbox_skipped=%d", skipped)
		}
	}
	t.Logf("testing.Testing false path=%s sandbox_skipped=%d", out.Path, skipped)
}

func wrapEcho(t *testing.T) *exec.Cmd {
	t.Helper()
	out, err := Wrap(exec.Command("/bin/echo", "hi"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func countSkipped(ev []Event) int {
	n := 0
	for _, e := range ev {
		if e.Type == "sandbox_skipped" {
			n++
		}
	}
	return n
}
