package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rewriteDesignReceipt(t *testing.T, path string, fields map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	for key, val := range fields {
		rec[key] = val
	}
	raw, err = json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDesignAdmissionCLIHasNoModelBrandGate(t *testing.T) {
	for _, model := range []string{"grok-4.6", "local-model-v1", "gpt-6-astra"} {
		t.Run(model, func(t *testing.T) {
			root, dir := workflowTestRoot(t)
			cfg := workflowTestCfg(t, root)
			receipt := writeAstraDesignReceiptWith(t, map[string]any{
				"actual_model": model, "actual_runner": "synthetic-review-runner",
				"identity": "separate-design-actor", "session_id": "design-context",
				"role": " DESIGN ", "input_identity": "",
			})
			var wf *WorkflowRecord
			warnings := captureStderr(t, func() {
				wf = initTestWorkflow(t, root, dir, "-design-receipt", receipt,
					"-design-actual-model", "display-override-must-not-win")
			})
			for _, want := range []string{"normalized", "derived from verified", "CLI identity labels are ignored"} {
				if !strings.Contains(warnings, want) {
					t.Fatalf("missing visible warning %q: %s", want, warnings)
				}
			}
			if err := cmdWorkflowWriter([]string{wf.ID, "-root", root, "-mode", "manual"}); err != nil {
				t.Fatal(err)
			}
			wf, _ = loadWorkflow(root, cfg, wf.ID)
			if err := reverifyGoalDesignProof(root, wf); err != nil {
				t.Fatal(err)
			}
			n := wf.DesignLineage.LatestValid
			if n.ActualModel != model || n.ActualRunner != "synthetic-review-runner" || n.DesignSessionID != "design-context" {
				t.Fatalf("actual identity overwritten: %+v", n)
			}
			writer, err := workflowWriterTask(root, wf)
			if err != nil || writer.Goal == nil || writer.Goal.Started {
				t.Fatalf("offline admission must not execute a provider: %+v %v", writer, err)
			}
			if err := cmdWorkflowWriter([]string{wf.ID, "-root", root, "-mode", "manual"}); !errors.Is(err, errWorkflowDuplicateRole) {
				t.Fatalf("brand-neutral admission must still reject duplicate writer: %v", err)
			}
		})
	}
}

func TestDesignAdmissionStillRequiresEvidence(t *testing.T) {
	t.Parallel()
	empty := filepath.Join(t.TempDir(), "empty-result.md")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, fields := range map[string]map[string]any{
		"missing result":   {"result_path": ""},
		"empty result":     {"result_path": empty},
		"missing artifact": {"result_path": empty + ".missing"},
		"wrong digest":     {"digest": strings.Repeat("0", 64)},
		"not read only":    {"read_only": false},
		"no input bytes":   {"inputs": []any{}},
		"unknown result":   {"status": "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			receipt := writeAstraDesignReceiptWith(t, fields)
			if err := bindInitialDesignProof(&WorkflowRecord{}, designBindRequest{ReceiptPath: receipt}); err == nil {
				t.Fatal("invalid evidence admitted")
			}
		})
	}
}

func TestDesignLegacyMetadataWarnsWithoutInventingIdentity(t *testing.T) {
	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"model": "", "actual_model": "", "runner": "", "actual_runner": "", "identity": "", "session_id": "",
	})
	wf := &WorkflowRecord{}
	warnings := captureStderr(t, func() {
		if err := bindInitialDesignProof(wf, designBindRequest{ReceiptPath: receipt}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(warnings, "metadata is incomplete") || !strings.Contains(warnings, "caller-attested") {
		t.Fatalf("missing attribution must stay visible: %s", warnings)
	}
	n := wf.DesignLineage.LatestValid
	if n.ActualModel != "" || n.ActualRunner != "" || n.Identity != "" || n.DesignSessionID != "" {
		t.Fatalf("must not fabricate attribution: %+v", n)
	}
	if err := normalizeDesignNode(n); err != nil {
		t.Fatalf("legacy object must remain readable: %v", err)
	}
}

func TestDesignProducerDriftBlockedBeforeStart(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"actual_model", "actual_runner", "identity", "session_id"} {
		t.Run(field, func(t *testing.T) {
			root, dir := workflowTestRoot(t)
			cfg := workflowTestCfg(t, root)
			receipt := writeAstraDesignReceiptWith(t, map[string]any{
				"actual_model": "grok-4.6", "actual_runner": "synthetic-runner", "session_id": "designer-session",
			})
			wf := initTestWorkflow(t, root, dir, "-design-receipt", receipt)
			writer := admitManualWriter(t, root, cfg, wf)
			// The result bytes/digest stay unchanged. Replacing only the claimed
			// producer must not escape the pre-start consumer's verification.
			rewriteDesignReceipt(t, receipt, map[string]any{field: "forged-replacement"})
			if err := admitManualGoalLaunchLocked(root, cfg, wf, writer, 1000, false); !errors.Is(err, errGoalDesignProof) {
				t.Fatalf("producer drift must be blocked at pre-start: %v", err)
			}
			onDisk, err := loadTask(root, writer.ID)
			if err != nil || onDisk.ActiveAttemptID != "" || onDisk.SessionID != "" || onDisk.Goal.Started {
				t.Fatalf("rejected start must have zero execution: %+v %v", onDisk, err)
			}
		})
	}
}

func TestDesignResultRejectsWriterContextEvenWithDifferentModel(t *testing.T) {
	t.Parallel()
	root, cfg, wf, writer, receipt := d4SuccessorFixture(t)
	writer.SessionID = "writer-context"
	if err := saveTask(root, writer); err != nil {
		t.Fatal(err)
	}
	rewriteDesignReceipt(t, receipt, map[string]any{
		"actual_model": "different-model", "session_id": " " + writer.SessionID + " ",
		"consumed_session_id": writer.SessionID, "consumed_revision": writer.Revision,
	})
	if _, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionStop, "budget_limited", receipt); !errors.Is(err, errGoalDesignProof) {
		t.Fatalf("different model in the same context is not independent: %v", err)
	}
}

func TestDesignWarningsCannotPromoteUnknownExecution(t *testing.T) {
	t.Parallel()
	root, cfg, wf, writer, receipt := d4SuccessorFixture(t)
	writer.Goal.LastNativeStatus = ""
	if err := saveTask(root, writer); err != nil {
		t.Fatal(err)
	}
	rewriteDesignReceipt(t, receipt, map[string]any{
		"actual_model": "grok-4.6", "consumed_revision": writer.Revision, "consumed_observation": goalObsUnknown,
	})
	for _, decision := range []string{goalDecisionAccept, goalDecisionRevise} {
		if _, err := applyWorkflowDesignResult(root, cfg, wf, decision, goalObsUnknown, receipt); !errors.Is(err, errGoalDesignResult) {
			t.Fatalf("unknown execution cannot %s: %v", decision, err)
		}
	}
	disk, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil || disk.WriterTaskID != writer.ID || disk.CurrentRound != 0 {
		t.Fatalf("unknown result must not create a successor: %+v %v", disk, err)
	}
	if _, err := applyWorkflowDesignResult(root, cfg, wf, goalDecisionStop, goalObsUnknown, receipt); err != nil {
		t.Fatalf("truthful stop remains available: %v", err)
	}
}

func TestDesignResultCLICompletedSuccessorNormalizesWithoutBypassingBounds(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	initial := writeAstraDesignReceiptWith(t, map[string]any{
		"actual_model": "grok-4.6", "identity": "separate-reviewer", "session_id": "design-context",
	})
	wf := initTestWorkflow(t, root, dir, "-design-receipt", initial)
	writer := admitManualWriter(t, root, cfg, wf)
	launchExitedManualGoal(t, root, cfg, wf, writer, "synthetic-complete")
	home := t.TempDir()
	writeGrokGoalFixture(t, home, dir, writer.SessionID, "synthetic-complete", "complete", grokNativeGoalStateFile{LastClassifierVerdict: "achieved"})
	writer, err := syncWorkflowGoal(root, cfg, wf, GoalSyncRequest{
		ExpectedRevision: writer.Revision, ExpectedSession: writer.SessionID, ExpectedGoalID: "synthetic-complete",
		ExpectedAttempt: writer.Goal.BoundAttemptID, GrokHome: home, GrokCWD: dir,
	})
	if err != nil || writer.Status != statusDone {
		t.Fatalf("synthetic terminal setup: %v", err)
	}
	// Same model as execution is allowed, different producer context is required.
	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"actual_model": "grok-4.6", "identity": "separate-reviewer", "session_id": "fresh-design-context",
		"consumed_task_id": writer.ID, "consumed_session_id": writer.SessionID,
		"consumed_attempt_id": writer.Goal.BoundAttemptID, "consumed_revision": writer.Revision,
		"consumed_observation": "complete",
	})
	warnings := captureStderr(t, func() {
		err = cmdWorkflowDesignResult([]string{wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor"})
	})
	if err != nil || !strings.Contains(warnings, "successor normalized to revise") {
		t.Fatalf("equivalent operation must warn and proceed: %v %s", err, warnings)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil || wf.CurrentRound != 1 || wf.DesignLineage.LastResultDecision != goalDecisionRevise || wf.WriterTaskID == writer.ID {
		t.Fatalf("normalized decision must advance exactly one existing round: %+v %v", wf, err)
	}
	if err := cmdWorkflowDesignResult([]string{wf.ID, "-root", root, "-design-receipt", receipt, "-decision", "successor"}); err == nil {
		t.Fatal("replayed result must not dispatch another writer")
	}
}
