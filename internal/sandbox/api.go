// Package sandbox wraps an executor so it cannot write outside its worktree. Design §13.
package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

// ErrNotImplemented is returned when a platform cannot build a sandbox wrapper.
var ErrNotImplemented = errors.New("not implemented")

// Event is one sandbox fact. Host persists it; the wrapper does not open the store.
type Event struct {
	Type string
	Data string
}

// testingTesting is testing.Testing. The outer-sandbox regression replaces it
// to prove a production binary ignores CARDEX_OUTER_SANDBOX.
var testingTesting = testing.Testing

func outerSandbox() bool {
	return testingTesting() && os.Getenv("CARDEX_OUTER_SANDBOX") == "1"
}

// Wrap returns cmd restricted to the writable paths.
// On non-macOS the command is not wrapped and one sandbox_skipped event is recorded.
// A test process with CARDEX_OUTER_SANDBOX=1 takes that same path. A production binary ignores the variable.
func Wrap(cmd *exec.Cmd, writable []string) (*exec.Cmd, error) {
	if cmd == nil {
		return nil, errors.New("sandbox: nil command")
	}
	if outerSandbox() {
		return Skip(cmd)
	}
	return wrapOS(cmd, writable)
}

// SkipDenialTest skips a test that asserts real sandbox-exec denial while this
// process is a test under CARDEX_OUTER_SANDBOX=1. Nested sandbox-exec is not
// permitted there. Those tests run outside the outer sandbox.
func SkipDenialTest(t *testing.T) {
	t.Helper()
	if outerSandbox() {
		t.Skip("CARDEX_OUTER_SANDBOX=1: this test asserts real sandbox denial, and nested sandbox-exec is not permitted inside the outer sandbox")
	}
}
