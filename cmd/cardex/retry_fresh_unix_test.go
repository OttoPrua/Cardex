//go:build !windows

package main

import (
	"strings"
	"testing"
)

// These process fixtures use the Unix-only bindLiveAttempt helper.
func TestRetryFreshRefusesLiveOrdinaryAttemptAllowsDefault(t *testing.T) {
	root := testRoot(t)
	tk := persistLegalFailedCard(t, root, "live attempt", "sess-live")
	cmd := bindLiveAttempt(t, root, tk)
	if cmd.Process == nil {
		t.Fatal("expected live process")
	}
	before := taskFileBytes(t, root, tk.ID)
	err := cmdSetStatus([]string{"-root", root, "-fresh", tk.ID}, "retry")
	if err == nil || (!strings.Contains(err.Error(), "仍在运行") && !strings.Contains(err.Error(), "执行进程仍在")) {
		t.Fatalf("live attempt: %v", err)
	}
	if string(taskFileBytes(t, root, tk.ID)) != string(before) {
		t.Fatal("live fresh retry mutated task")
	}
	if err := cmdSetStatus([]string{"-root", root, tk.ID}, "retry"); err != nil {
		t.Fatalf("default retry must stay legal with live attempt: %v", err)
	}
	got := loadMust(t, root, tk.ID)
	if got.SessionID != "sess-live" {
		t.Fatalf("default retry dropped session: %q", got.SessionID)
	}
}

func TestRetryFreshAllowsExitedAttemptAndIgnoresForeignWorkspace(t *testing.T) {
	root := testRoot(t)
	exited := persistLegalFailedCard(t, root, "exited attempt", "sess-exited")
	attachRetainedAttempt(t, root, exited, &AttemptRecord{State: attemptExited, PID: 0})
	if err := cmdSetStatus([]string{"-root", root, "-fresh", exited.ID}, "retry"); err != nil {
		t.Fatalf("exited attempt must allow fresh: %v", err)
	}
	if loadMust(t, root, exited.ID).SessionID != "" {
		t.Fatal("exited fresh kept session")
	}

	self := persistLegalFailedCard(t, root, "self no attempt", "sess-self")
	other := persistLegalFailedCard(t, root, "other live", "sess-other")
	other.Dir = self.Dir
	if err := saveTask(root, other); err != nil {
		t.Fatal(err)
	}
	_ = bindLiveAttempt(t, root, other)
	if err := cmdSetStatus([]string{"-root", root, "-fresh", self.ID}, "retry"); err != nil {
		t.Fatalf("unrelated workspace lease must not block this card: %v", err)
	}
	if loadMust(t, root, self.ID).SessionID != "" {
		t.Fatal("self fresh kept session")
	}
}
