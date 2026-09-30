package main

import (
	"testing"
)

func TestNativeDoneHoldReasonFacts(t *testing.T) {
	t.Parallel()
	impl := &Task{Type: typeSequence, Dir: t.TempDir()}
	review := &Task{Type: typeReview, Dir: t.TempDir()}

	t.Run("open tools", func(t *testing.T) {
		got := nativeDoneHoldReason(impl, nativeDoneFacts{OpenTools: true, ObservationComplete: true})
		if got != nativeDoneHoldOpenTools {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("fake executing cpu still on", func(t *testing.T) {
		got := nativeDoneHoldReason(impl, nativeDoneFacts{ProcessAlive: true, ObservationComplete: true, ResultText: "Done"})
		if got != nativeDoneHoldFakeExecuting {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("promised files missing", func(t *testing.T) {
		got := nativeDoneHoldReason(impl, nativeDoneFacts{
			Prompt: "Write promised_artifact.go and add tests", ObservationComplete: true,
		})
		if got != nativeDoneHoldMissingFiles {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("promised tests missing", func(t *testing.T) {
		got := nativeDoneHoldReason(impl, nativeDoneFacts{
			Prompt: "Write promised_artifact.go and add tests", WorktreeFiles: []string{"promised_artifact.go"},
			ObservationComplete: true, WorktreeHasDiff: true,
		})
		if got != nativeDoneHoldMissingTests {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("wf02 waiting for id zero diff", func(t *testing.T) {
		got := nativeDoneHoldReason(impl, nativeDoneFacts{
			Prompt: "implement the patch", ResultText: "waiting for an id to start",
			ObservationComplete: true,
		})
		if got != nativeDoneHoldWF02WaitingForID {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("idle turn_ended zero diff not done", func(t *testing.T) {
		got := nativeDoneHoldReason(impl, nativeDoneFacts{
			Prompt: "implement the patch", ResultText: "Idle: turn_ended",
			ObservationComplete: true,
		})
		if got != nativeDoneHoldFakeExecuting {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("turn_ended plus cpu not started or done", func(t *testing.T) {
		facts := nativeDoneFacts{
			Prompt: "implement the patch", ResultText: "Idle: turn_ended",
			ObservationComplete: true, ProcessAlive: true, WorktreeHasDiff: true,
		}
		if nativeLooksStartedOrDone(facts) {
			t.Fatal("turn_ended plus CPU is not started/done")
		}
		got := nativeDoneHoldReason(impl, facts)
		if got != nativeDoneHoldFakeExecuting {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("permission_requested idle not started or done", func(t *testing.T) {
		facts := nativeDoneFacts{
			Prompt: "implement the patch", ResultText: "permission_requested idle",
			ObservationComplete: true, WorktreeHasDiff: true,
		}
		if nativeLooksStartedOrDone(facts) {
			t.Fatal("permission_requested idle is not started/done")
		}
		got := nativeDoneHoldReason(impl, facts)
		if got != nativeDoneHoldPermissionIdle {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("tui input-box goal not started or done", func(t *testing.T) {
		facts := nativeDoneFacts{
			Prompt:              "implement the patch",
			ResultText:          "/goal Read and implement the complete stage contract at /tmp/x",
			ObservationComplete: true,
		}
		if nativeLooksStartedOrDone(facts) {
			t.Fatal("TUI input-box Goal is not started/done")
		}
		got := nativeDoneHoldReason(impl, facts)
		if got != nativeDoneHoldTUIGoalNotStarted {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("process residue fail-closed", func(t *testing.T) {
		got := nativeDoneHoldReason(impl, nativeDoneFacts{
			Prompt: "implement the patch", ResultText: "OK", ObservationComplete: true,
			ProcessResidue: true,
		})
		if got != nativeDoneHoldProcessResidue {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("read-only review no diff ok", func(t *testing.T) {
		got := nativeDoneHoldReason(review, nativeDoneFacts{
			Prompt: "review the candidate", ResultText: `{"verdict":"pass"}`,
			ObservationComplete: true,
		})
		if got != "" {
			t.Fatalf("read-only report must be allowed, got %q", got)
		}
	})
	t.Run("review protocol incomplete with result ok", func(t *testing.T) {
		got := nativeDoneHoldReason(review, nativeDoneFacts{
			Prompt: "review the candidate", ResultText: `{"verdict":"pass"}`,
		})
		if got != "" {
			t.Fatalf("review+result must not hold on incomplete observation alone, got %q", got)
		}
	})
	t.Run("implementation incomplete observation not done", func(t *testing.T) {
		got := nativeDoneHoldReason(impl, nativeDoneFacts{Prompt: "implement the patch", ResultText: "OK"})
		if got != nativeDoneHoldIncompleteObs {
			t.Fatalf("got %q", got)
		}
	})
}

func TestNativeDoneAppliesNativeRunnersOnly(t *testing.T) {
	t.Parallel()
	if nativeDoneApplies("") || nativeDoneApplies("codex") {
		t.Fatal("claude/codex are not the native done gate")
	}
	if !nativeDoneApplies("opencode") || !nativeDoneApplies(kimiCLIRunnerName) || !nativeDoneApplies(grokBuildRunnerName) {
		t.Fatal("grok/kimi/opencode must consult the native done gate")
	}
}
