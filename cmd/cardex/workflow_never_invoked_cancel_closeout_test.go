//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNeverInvokedCanceledArchiveOwnerMark(t *testing.T) {
	t.Run("native queued-held-canceled owner mark", func(t *testing.T) {
		root, wf, writerID, before := nativeQueuedHeldCanceledArchive(t)
		if recordedAttemptCloseout(root, mustLoadArchivedWriter(t, root, writerID)) {
			t.Fatal("happy path used exited-attempt closeout")
		}
		out1, err := captureStdout(t, func() error {
			return cmdWorkflowMark([]string{"-root", root, wf.ID, "-kind", "owner", "-summary", "never-invoked cancel"})
		})
		if err != nil {
			t.Fatalf("owner mark: %v\n%s", err, out1)
		}
		if !strings.Contains(out1, workflowStatusOwnerChoice) {
			t.Fatalf("owner mark stdout missing owner_choice: %q", out1)
		}
		out2, err := captureStdout(t, func() error {
			return cmdWorkflowMark([]string{"-root", root, wf.ID, "-kind", "owner", "-summary", "never-invoked cancel again"})
		})
		if err != nil {
			t.Fatalf("second owner mark: %v\n%s", err, out2)
		}
		if !strings.Contains(out2, workflowStatusOwnerChoice) {
			t.Fatalf("second owner mark stdout missing owner_choice: %q", out2)
		}
		after, err := os.ReadFile(filepath.Join(archiveDir(root), writerID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("owner mark changed the canceled archive bytes")
		}
		archived := mustLoadArchivedWriter(t, root, writerID)
		if archived.Attempts > 0 {
			t.Fatalf("owner mark left Attempts=%d", archived.Attempts)
		}
		if archived.Goal != nil || strings.TrimSpace(archived.SessionID) != "" || strings.TrimSpace(archived.ActiveAttemptID) != "" {
			t.Fatalf("owner mark created invocation: goal=%v session=%q attempt=%q", archived.Goal != nil, archived.SessionID, archived.ActiveAttemptID)
		}
		has, err := taskHasAttemptRecord(root, writerID)
		if err != nil || has {
			t.Fatalf("owner mark created an attempt directory/record: has=%v err=%v", has, err)
		}
		if _, err := os.Stat(taskPath(root, writerID)); !os.IsNotExist(err) {
			t.Fatal("owner mark resurrected the live task")
		}
		marked, err := loadWorkflow(root, workflowTestCfg(t, root), wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		if marked.Status != workflowStatusOwnerChoice || workflowClaimsActive(marked) {
			t.Fatalf("status=%s active=%t", marked.Status, workflowClaimsActive(marked))
		}
	})

	for _, tc := range []struct {
		name            string
		mutate          func(*testing.T, string, string)
		want            error
		rewritesArchive bool
	}{
		{"absent-transition", mutateAbsentTransition, errWorkflowCustody, false},
		{"malformed-transition", mutateMalformedTransition, errWorkflowCustody, false},
		{"mismatched-transition-epoch", mutateMismatchedTransitionEpoch, errWorkflowCustody, false},
		{"prepared-transition", mutatePreparedTransition, errWorkflowCustody, false},
		{"leftover-transition-attempt-id", mutateLeftoverTransitionAttemptID, errWorkflowCustody, false},
		{"absent-event", mutateAbsentEvent, errWorkflowCustody, false},
		{"malformed-event", mutateMalformedEvent, errWorkflowCustody, false},
		{"mismatched-event-transition", mutateMismatchedEventTransition, errWorkflowCustody, false},
		{"missing-was-running", mutateMissingWasRunning, errWorkflowCustody, false},
		{"null-was-running", mutateNullWasRunning, errWorkflowCustody, false},
		{"wrong-type-was-running", mutateWrongTypeWasRunning, errWorkflowCustody, false},
		{"was-running-true", mutateWasRunningTrue, errWorkflowCustody, false},
		{"altered-cancel-detail", mutateAlteredCancelDetail, errWorkflowCustody, false},
		{"positive-attempts", mutatePositiveAttempts, errWorkflowCustody, true},
		{"invoked-goal", mutateInvokedGoal, errWorkflowCustody, true},
		{"unstarted-goal", mutateUnstartedGoal, errWorkflowCustody, true},
		{"invoked-session", mutateInvokedSession, errWorkflowCustody, true},
		{"invoked-attempt-file", mutateInvokedAttemptFile, errWorkflowCustody, false},
		{"unreadable-attempt-dir", mutateUnreadableAttemptDir, errWorkflowCustody, false},
		{"omitted-scheduling-eligible", mutateOmittedSchedulingEligible, errWorkflowCustody, true},
		{"wrong-workflow", mutateWrongWorkflow, errWorkflowCustody, true},
		{"live-archive-duplicate", mutateLiveArchiveDuplicate, errWorkflowCustody, false},
		{"duplicate-canceled-event", mutateDuplicateCanceledEvent, errWorkflowCustody, false},
		{"lease-conflict", mutateLeaseConflict, errWorkflowCustody, false},
		{"second-writer-queued", mutateSecondWriterQueued, errWorkflowDuplicateRole, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, wf, writerID, before := nativeQueuedHeldCanceledArchive(t)
			tc.mutate(t, root, writerID)
			out, err := captureStdout(t, func() error {
				return cmdWorkflowMark([]string{"-root", root, wf.ID, "-kind", "owner", "-summary", "refuse " + tc.name})
			})
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("want %v, got %v\n%s", tc.want, err, out)
			}
			assertWritingClaimHeld(t, root, wf.ID)
			after, readErr := os.ReadFile(filepath.Join(archiveDir(root), writerID+".json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !tc.rewritesArchive && !bytes.Equal(before, after) {
				t.Fatalf("%s changed archive bytes on refusal", tc.name)
			}
		})
	}
}

func nativeQueuedHeldCanceledArchive(t *testing.T) (root string, wf *WorkflowRecord, writerID string, archive []byte) {
	t.Helper()
	root, dir := workflowTestRoot(t)
	wf = initTestWorkflow(t, root, dir)
	if err := cmdWorkflowWriter([]string{"-root", root, wf.ID}); err != nil {
		t.Fatalf("workflow writer: %v", err)
	}
	cfg := workflowTestCfg(t, root)
	loaded, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	wf = loaded
	writerID = wf.WriterTaskID
	if writerID == "" {
		t.Fatal("writer not admitted")
	}
	live, err := loadTask(root, writerID)
	if err != nil {
		t.Fatal(err)
	}
	if live.Status != statusQueued {
		t.Fatalf("admitted writer status=%s", live.Status)
	}
	if live.Goal != nil || strings.TrimSpace(live.SessionID) != "" || strings.TrimSpace(live.ActiveAttemptID) != "" {
		t.Fatalf("admit created invocation: goal=%v session=%q attempt=%q", live.Goal != nil, live.SessionID, live.ActiveAttemptID)
	}
	if err := cmdSetStatus([]string{"-root", root, writerID}, "hold"); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := cmdSetStatus([]string{"-root", root, writerID}, "cancel"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := loadTask(root, writerID); !os.IsNotExist(err) {
		t.Fatal("cancel must archive the writer")
	}
	archive, err = os.ReadFile(filepath.Join(archiveDir(root), writerID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	archived := mustLoadArchivedWriter(t, root, writerID)
	if archived.Attempts > 0 {
		t.Fatalf("native never-invoked cancel left Attempts=%d", archived.Attempts)
	}
	if archived.Goal != nil || strings.TrimSpace(archived.SessionID) != "" || strings.TrimSpace(archived.ActiveAttemptID) != "" {
		t.Fatal("native cancel created Goal, session, or active attempt")
	}
	has, err := taskHasAttemptRecord(root, writerID)
	if err != nil || has {
		t.Fatalf("native cancel created an attempt: has=%v err=%v", has, err)
	}
	if recordedAttemptCloseout(root, archived) {
		t.Fatal("never-invoked cancel must not satisfy exited-attempt closeout")
	}
	if !neverInvokedCanceledArchiveCloseout(root, archived) {
		t.Fatal("native queued-held-canceled archive must satisfy never-invoked closeout")
	}
	events, corrupt, err := loadTaskEvents(root, writerID)
	if err != nil || corrupt {
		t.Fatalf("events: corrupt=%v err=%v", corrupt, err)
	}
	var cancelEv *TaskEvent
	for i := range events {
		if events[i].Type == evCanceled {
			cancelEv = &events[i]
		}
	}
	if cancelEv == nil {
		t.Fatal("native cancel wrote no canceled event")
	}
	if !explicitJSONBoolFalse(cancelEv.Detail, "was_running") {
		t.Fatalf("native cancel was_running=%v type=%T", cancelEv.Detail["was_running"], cancelEv.Detail["was_running"])
	}
	journal, err := loadTransition(root, writerID, archived.LastCommittedTransitionID)
	if err != nil || journal == nil {
		t.Fatalf("committed cancel transition: %v", err)
	}
	if strings.TrimSpace(journal.DetailDigest) == "" {
		t.Fatal("native cancel journal missing DetailDigest")
	}
	if got := digestDetail(cancelEv.Detail); got != journal.DetailDigest {
		t.Fatalf("native cancel event detail digest=%s journal=%s", got, journal.DetailDigest)
	}
	return root, wf, writerID, archive
}

func mustLoadArchivedWriter(t *testing.T, root, writerID string) *Task {
	t.Helper()
	archived, err := loadArchivedTaskFile(root, writerID)
	if err != nil || archived == nil {
		t.Fatalf("archived writer: %v", err)
	}
	return archived
}

func mutateAbsentTransition(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	if err := os.Remove(transitionPath(root, writerID, archived.LastCommittedTransitionID)); err != nil {
		t.Fatal(err)
	}
}

func mutateMalformedTransition(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	if err := os.WriteFile(transitionPath(root, writerID, archived.LastCommittedTransitionID), []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mutateMismatchedTransitionEpoch(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	rewriteJSONFile(t, transitionPath(root, writerID, archived.LastCommittedTransitionID), func(obj map[string]any) {
		obj["control_epoch"] = archived.ControlEpoch + 7
	})
}

func mutatePreparedTransition(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	rewriteJSONFile(t, transitionPath(root, writerID, archived.LastCommittedTransitionID), func(obj map[string]any) {
		obj["state"] = transitionPrepared
	})
}

func mutateLeftoverTransitionAttemptID(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	rewriteJSONFile(t, transitionPath(root, writerID, archived.LastCommittedTransitionID), func(obj map[string]any) {
		obj["attempt_id"] = "atleftover00000001"
	})
}

func mutateAbsentEvent(t *testing.T, root, writerID string) {
	t.Helper()
	if err := os.Remove(archivedEventsPath(root, writerID)); err != nil {
		t.Fatal(err)
	}
}

func mutateMalformedEvent(t *testing.T, root, writerID string) {
	t.Helper()
	if err := os.WriteFile(archivedEventsPath(root, writerID), []byte("{not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mutateMismatchedEventTransition(t *testing.T, root, writerID string) {
	t.Helper()
	rewriteArchivedCanceledEvent(t, root, writerID, func(obj map[string]any) {
		obj["transition_id"] = "tr-mismatch-closeout"
	})
}

func mutateMissingWasRunning(t *testing.T, root, writerID string) {
	t.Helper()
	rewriteArchivedCanceledEvent(t, root, writerID, func(obj map[string]any) {
		detail, _ := obj["detail"].(map[string]any)
		if detail == nil {
			t.Fatal("canceled event missing detail")
		}
		delete(detail, "was_running")
		obj["detail"] = detail
	})
}

func mutateNullWasRunning(t *testing.T, root, writerID string) {
	t.Helper()
	rewriteArchivedCanceledEvent(t, root, writerID, func(obj map[string]any) {
		detail, _ := obj["detail"].(map[string]any)
		if detail == nil {
			t.Fatal("canceled event missing detail")
		}
		detail["was_running"] = nil
		obj["detail"] = detail
	})
}

func mutateWrongTypeWasRunning(t *testing.T, root, writerID string) {
	t.Helper()
	rewriteArchivedCanceledEvent(t, root, writerID, func(obj map[string]any) {
		detail, _ := obj["detail"].(map[string]any)
		if detail == nil {
			t.Fatal("canceled event missing detail")
		}
		detail["was_running"] = 0
		obj["detail"] = detail
	})
}

func mutateWasRunningTrue(t *testing.T, root, writerID string) {
	t.Helper()
	rewriteArchivedCanceledEvent(t, root, writerID, func(obj map[string]any) {
		detail, _ := obj["detail"].(map[string]any)
		if detail == nil {
			t.Fatal("canceled event missing detail")
		}
		detail["was_running"] = true
		obj["detail"] = detail
	})
}

func mutateAlteredCancelDetail(t *testing.T, root, writerID string) {
	t.Helper()
	rewriteArchivedCanceledEvent(t, root, writerID, func(obj map[string]any) {
		detail, _ := obj["detail"].(map[string]any)
		if detail == nil {
			t.Fatal("canceled event missing detail")
		}
		if !explicitJSONBoolFalse(detail, "was_running") {
			t.Fatalf("altered-detail fixture lost was_running=false: %v", detail["was_running"])
		}
		detail["reason"] = "edited cancel detail"
		obj["detail"] = detail
	})
}

func mutatePositiveAttempts(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	archived.Attempts = 1
	if err := writeArchivedTaskFile(root, archived); err != nil {
		t.Fatal(err)
	}
}

func mutateInvokedGoal(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	archived.Goal = &TaskGoalBinding{WriterMode: goalWriterManual, Observation: goalObsUnknown, Started: true}
	if err := writeArchivedTaskFile(root, archived); err != nil {
		t.Fatal(err)
	}
}

func mutateUnstartedGoal(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	archived.Goal = &TaskGoalBinding{WriterMode: goalWriterManual, Observation: goalObsUnstarted}
	if err := writeArchivedTaskFile(root, archived); err != nil {
		t.Fatal(err)
	}
}

func mutateOmittedSchedulingEligible(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	archived.SchedulingEligible = nil
	if err := writeArchivedTaskFile(root, archived); err != nil {
		t.Fatal(err)
	}
}

func mutateInvokedSession(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	archived.SessionID = "sess-never-invoked-counterexample"
	if err := writeArchivedTaskFile(root, archived); err != nil {
		t.Fatal(err)
	}
}

func mutateInvokedAttemptFile(t *testing.T, root, writerID string) {
	t.Helper()
	if err := os.MkdirAll(attemptsDir(root, writerID), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(attemptsDir(root, writerID), "atleftover00000001.json")
	if err := os.WriteFile(path, []byte(`{"task_id":"`+writerID+`","attempt_id":"atleftover00000001","state":"exited"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mutateUnreadableAttemptDir(t *testing.T, root, writerID string) {
	t.Helper()
	dir := attemptsDir(root, writerID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func mutateWrongWorkflow(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	archived.WorkflowID = "wf-other-binding"
	if err := writeArchivedTaskFile(root, archived); err != nil {
		t.Fatal(err)
	}
}

func mutateLiveArchiveDuplicate(t *testing.T, root, writerID string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(archiveDir(root), writerID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tasksDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath(root, writerID), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mutateDuplicateCanceledEvent(t *testing.T, root, writerID string) {
	t.Helper()
	path := archivedEventsPath(root, writerID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cancelLine []byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var obj map[string]any
		if json.Unmarshal(line, &obj) != nil {
			continue
		}
		if obj["type"] == evCanceled {
			obj["seq"] = float64(99)
			dup, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			cancelLine = dup
			break
		}
	}
	if len(cancelLine) == 0 {
		t.Fatal("no canceled event to duplicate")
	}
	if err := os.WriteFile(path, append(append(bytes.TrimRight(data, "\n"), '\n'), append(cancelLine, '\n')...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mutateLeaseConflict(t *testing.T, root, writerID string) {
	t.Helper()
	archived := mustLoadArchivedWriter(t, root, writerID)
	lease, err := acquireWorkspaceExecutionLease(archived.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if lease != nil {
			_ = lease.Close()
		}
	})
}

func mutateSecondWriterQueued(t *testing.T, root, writerID string) {
	t.Helper()
	cfg := workflowTestCfg(t, root)
	entries, err := os.ReadDir(workflowsDir(root))
	if err != nil {
		t.Fatal(err)
	}
	var wfID string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".progress.json") {
			continue
		}
		wfID = strings.TrimSuffix(e.Name(), ".json")
		break
	}
	if wfID == "" {
		t.Fatal("workflow id missing")
	}
	if err := cmdWorkflowWriter([]string{"-root", root, wfID}); err != nil {
		t.Fatalf("second writer: %v", err)
	}
	wf, err := loadWorkflow(root, cfg, wfID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.WriterTaskID == "" || wf.WriterTaskID == writerID {
		t.Fatalf("second writer not admitted: %s", wf.WriterTaskID)
	}
	second, err := loadTask(root, wf.WriterTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != statusQueued {
		t.Fatalf("second writer status=%s", second.Status)
	}
}

func rewriteJSONFile(t *testing.T, path string, mut func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	mut(obj)
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rewriteArchivedCanceledEvent(t *testing.T, root, writerID string, mut func(map[string]any)) {
	t.Helper()
	path := archivedEventsPath(root, writerID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines [][]byte
	found := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		trim := bytes.TrimSpace(line)
		if len(trim) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(trim, &obj); err != nil {
			t.Fatal(err)
		}
		if obj["type"] == evCanceled {
			mut(obj)
			found = true
		}
		out, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, out)
	}
	if !found {
		t.Fatal("no canceled event")
	}
	if err := os.WriteFile(path, append(bytes.Join(lines, []byte("\n")), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
