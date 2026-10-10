package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func archivedCanceledSuccessorFixture(t *testing.T) (string, *Config, *WorkflowRecord, *Task, string) {
	t.Helper()
	root, cfg, wf, tk, _ := d4SuccessorFixture(t)
	if err := terminalize(root, tk.ID, statusCanceled, "cli:cancel", "synthetic cancellation", nil); err != nil {
		t.Fatal(err)
	}
	tk, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := archiveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"consumed_task_id": tk.ID, "consumed_session_id": tk.SessionID,
		"consumed_attempt_id": tk.Goal.BoundAttemptID, "consumed_revision": tk.Revision,
		"consumed_observation": "canceled",
	})
	return root, cfg, wf, tk, receipt
}

func TestArchivedCanceledSuccessorCandidateCLI(t *testing.T) {
	root, _, wf, tk, receipt := archivedCanceledSuccessorFixture(t)
	archive := filepath.Join(archiveDir(root), tk.ID+".json")
	before, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	attemptPath := filepath.Join(attemptsDir(root, tk.ID), tk.Goal.BoundAttemptID+".json")
	attemptBefore, err := os.ReadFile(attemptPath)
	if err != nil {
		t.Fatal(err)
	}
	bin := resolveCardexCandidateBinary(t)
	args := []string{"workflow", "design-result", wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor", "-observation", "canceled"}
	out, err := exec.Command(bin, args...).CombinedOutput()
	t.Logf("actual candidate CLI: %s", out)
	if err != nil {
		t.Fatalf("successor: %v", err)
	}
	disk, err := loadWorkflow(root, nil, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.CurrentRound != 1 || disk.WriterTaskID == tk.ID {
		t.Fatalf("not one successor: %+v", disk)
	}
	next, err := loadTask(root, disk.WriterTaskID)
	if err != nil || next.Goal == nil || next.Goal.Started || next.Goal.Observation != goalObsUnstarted {
		t.Fatalf("new writer: %+v %v", next, err)
	}
	after, _ := os.ReadFile(archive)
	attemptAfter, _ := os.ReadFile(attemptPath)
	if !bytes.Equal(before, after) || !bytes.Equal(attemptBefore, attemptAfter) {
		t.Fatal("old canceled archive or attempt changed")
	}
	if _, err := os.Stat(taskPath(root, tk.ID)); !os.IsNotExist(err) {
		t.Fatal("old archive resurrected into tasks")
	}
	out, err = exec.Command(bin, args...).CombinedOutput()
	t.Logf("duplicate actual CLI: %s", out)
	if err == nil {
		t.Fatal("duplicate accepted")
	}
	replay, _ := loadWorkflow(root, nil, wf.ID)
	if replay.WriterTaskID != disk.WriterTaskID || replay.CurrentRound != 1 {
		t.Fatal("duplicate changed successor")
	}
}

func TestArchivedCanceledSuccessorRefusals(t *testing.T) {
	for _, variant := range []string{"stop-missing", "journal-missing", "stale-revision", "round-limit", "live-custody"} {
		t.Run(variant, func(t *testing.T) {
			root, cfg, wf, tk, receipt := archivedCanceledSuccessorFixture(t)
			archive := filepath.Join(archiveDir(root), tk.ID+".json")
			switch variant {
			case "stop-missing":
				tk.Goal.StopEvidence = false
			case "journal-missing":
				tk.LastCommittedTransitionID = "missing-transition"
			case "stale-revision":
				tk.Revision++
			case "round-limit":
				wf.CurrentRound = wf.MaxRounds
				if err := persistWorkflow(root, cfg, wf); err != nil {
					t.Fatal(err)
				}
			case "live-custody":
				cmd := exec.Command("sleep", "60")
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
				registerTaskInvoke(tk.ID, cmd.Process.Pid)
				t.Cleanup(func() { unregisterTaskInvoke(tk.ID, cmd.Process.Pid) })
			}
			// Synthetic fixture mutation only; preserve its rejected preimage.
			if variant != "live-custody" {
				if err := writeArchivedFixture(archive, tk); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(archive)
			if _, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionSuccessor, "canceled", receipt); err == nil {
				t.Fatal("invalid canceled successor accepted")
			}
			after, _ := os.ReadFile(archive)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected old archive changed")
			}
			disk, _ := loadWorkflow(root, cfg, wf.ID)
			if disk.WriterTaskID != tk.ID {
				t.Fatal("rejection created successor")
			}
		})
	}
}

func writeArchivedFixture(path string, tk *Task) error {
	b, err := json.MarshalIndent(tk, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
