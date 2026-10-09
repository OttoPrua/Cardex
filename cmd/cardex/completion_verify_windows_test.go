//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Runs under the existing TestWindows CI selection with the real cmd.exe.
func TestWindowsCompletionVerifyLocalShell(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work with spaces")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "proof.txt"), []byte("WINDOWS_VERIFY_CWD"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, command, reason string
		exit                  int
	}{
		{"pass", `type "proof.txt"`, completionVerifyReasonPassed, 0},
		{"fail", "exit /b 7", completionVerifyReasonFailed, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runCompletionVerify(context.Background(), nil, &Task{Dir: work, Verify: tc.command})
			if res.Exit != tc.exit || res.Reason != tc.reason || res.Passed != (tc.exit == 0) {
				t.Fatalf("unexpected native verification: %+v", res)
			}
			if res.Remote || res.Dir != work || !strings.Contains(strings.ToLower(res.Shell), "cmd.exe") {
				t.Fatalf("wrong shell or cwd: %+v", res)
			}
			if tc.exit == 0 && !strings.Contains(res.Tail, "WINDOWS_VERIFY_CWD") {
				t.Fatalf("command did not read the selected cwd: %+v", res)
			}
		})
	}
}
