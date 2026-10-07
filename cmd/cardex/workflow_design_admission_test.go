package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

func captureWorkflowCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	cmdErr := cmdWorkflow(args)
	_ = w.Close()
	os.Stdout = old
	raw, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(raw), cmdErr
}

func snapshotWorkflowIdentity(wf *WorkflowRecord) (id, integ, domainID, lineage, component string, paths []string) {
	if wf == nil {
		return "", "", "", "", "", nil
	}
	return wf.ID, wf.IntegrationTaskID, wf.WriteDomain.ID, wf.WriteDomain.Lineage, wf.WriteDomain.Component, append([]string(nil), wf.WriteDomain.Paths...)
}

func assertWorkflowIdentity(t *testing.T, wf *WorkflowRecord, id, integ, domainID, lineage, component string, paths []string) {
	t.Helper()
	gotID, gotInteg, gotDomain, gotLineage, gotComp, gotPaths := snapshotWorkflowIdentity(wf)
	if gotID != id || gotInteg != integ || gotDomain != domainID || gotLineage != lineage || gotComp != component || !reflect.DeepEqual(gotPaths, paths) {
		t.Fatalf("workflow identity drifted: id=%s/%s integ=%s/%s domain=%s/%s lineage=%s/%s component=%s/%s paths=%v/%v",
			gotID, id, gotInteg, integ, gotDomain, domainID, gotLineage, lineage, gotComp, component, gotPaths, paths)
	}
}

func patchUnboundWorkflow(t *testing.T, root string, id string, fn func(*WorkflowRecord)) *WorkflowRecord {
	t.Helper()
	cfg := workflowTestCfg(t, root)
	wf, err := loadWorkflow(root, cfg, id)
	if err != nil {
		t.Fatal(err)
	}
	fn(wf)
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	return wf
}

func TestLateInitialDesignBindAdmitsManualWriter(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	if wf.DesignLineage != nil || wf.WriterTaskID != "" || wf.Status != workflowStatusDesign {
		t.Fatalf("fixture must start unbound/unstarted: %+v", wf)
	}
	id, integ, domainID, lineage, component, paths := snapshotWorkflowIdentity(wf)
	receipt := writeAstraDesignReceipt(t)

	out, err := captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-receipt", receipt)
	if err != nil {
		t.Fatalf("design-bind: %v", err)
	}
	if !strings.Contains(out, wf.ID) || !strings.Contains(out, "design-bind") {
		t.Fatalf("stdout must name the inited id: %q", out)
	}

	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkflowIdentity(t, wf, id, integ, domainID, lineage, component, paths)
	if wf.Status != workflowStatusDesign || wf.WriterTaskID != "" || wf.CurrentRound != 0 {
		t.Fatalf("bind must not start execution: %+v", wf)
	}
	if wf.DesignLineage == nil || wf.DesignLineage.Initial == nil || wf.DesignLineage.LatestValid == nil {
		t.Fatal("bind must set Initial and LatestValid")
	}
	if wf.DesignLineage.RepairCount != 0 || wf.DesignLineage.Initial.Provenance != goalDesignProvenanceExt {
		t.Fatalf("bind must not call design-repair or invent repair lineage: %+v", wf.DesignLineage)
	}
	if wf.DesignLineage.Initial.Digest == "" || wf.DesignLineage.Initial.Digest != wf.DesignLineage.LatestValid.Digest {
		t.Fatalf("Initial/LatestValid digest missing or diverged: %+v", wf.DesignLineage)
	}
	if strings.TrimSpace(wf.DesignLineage.Initial.ConsumedCommit) != "" || strings.TrimSpace(wf.DesignLineage.Initial.ConsumedTree) != "" {
		t.Fatalf("must not fabricate a consumed candidate: %+v", wf.DesignLineage.Initial)
	}

	out, err = captureWorkflowCmd(t, "writer", wf.ID, "-root", root, "-mode", "manual")
	if err != nil {
		t.Fatalf("writer -mode manual: %v", err)
	}
	if !strings.Contains(out, wf.ID) || !strings.Contains(out, "writer=") || !strings.Contains(out, "mode=manual") {
		t.Fatalf("writer stdout must keep id and mode=manual: %q", out)
	}

	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkflowIdentity(t, wf, id, integ, domainID, lineage, component, paths)
	writer, err := workflowWriterTask(root, wf)
	if err != nil || writer.Goal == nil || writer.Goal.Started || writer.ActiveAttemptID != "" || writer.SessionID != "" {
		t.Fatalf("offline admission must not execute a provider: %+v %v", writer, err)
	}
	if err := cmdWorkflow([]string{"writer", wf.ID, "-root", root, "-mode", "manual"}); !errors.Is(err, errWorkflowDuplicateRole) {
		t.Fatalf("producer distinctness/duplicate writer still applies: %v", err)
	}
	show := captureWorkflowShow(t, root, wf.ID)
	lineageObj, _ := show["design_lineage"].(map[string]any)
	if lineageObj == nil {
		t.Fatalf("show JSON missing design_lineage: %v", show)
	}
	initial, _ := lineageObj["initial"].(map[string]any)
	latest, _ := lineageObj["latest_valid"].(map[string]any)
	if initial["digest"] != wf.DesignLineage.Initial.Digest || latest["digest"] != wf.DesignLineage.LatestValid.Digest {
		t.Fatalf("show digest mismatch: initial=%v latest=%v", initial["digest"], latest["digest"])
	}
}

func TestLateInitialDesignBindRejectsInvalidReceipt(t *testing.T) {
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
		"not completed":    {"status": "running"},
	} {
		t.Run(name, func(t *testing.T) {
			root, dir := workflowTestRoot(t)
			cfg := workflowTestCfg(t, root)
			wf := initTestWorkflow(t, root, dir)
			receipt := writeAstraDesignReceiptWith(t, fields)
			if _, err := captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-receipt", receipt); !errors.Is(err, errGoalDesignProof) {
				t.Fatalf("invalid evidence admitted: %v", err)
			}
			fresh, err := loadWorkflow(root, cfg, wf.ID)
			if err != nil || fresh.DesignLineage != nil || fresh.WriterTaskID != "" {
				t.Fatalf("rejected bind must not overwrite: %+v %v", fresh, err)
			}
		})
	}
}

func TestLateInitialDesignBindRequiresExactlyOneInput(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	receipt := writeAstraDesignReceipt(t)

	if _, err := captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("neither input must reject: %v", err)
	}
	if _, err := captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-receipt", receipt, "-design-task", "task-does-not-exist"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("both inputs must reject: %v", err)
	}
	fresh, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil || fresh.DesignLineage != nil {
		t.Fatalf("exclusive-input reject must leave unbound: %+v %v", fresh, err)
	}
}

func TestLateInitialDesignBindRejectsReplacement(t *testing.T) {
	receipt := writeAstraDesignReceipt(t)
	cases := []struct {
		name  string
		patch func(*WorkflowRecord)
		want  string
	}{
		{"started active", func(wf *WorkflowRecord) { wf.Status = workflowStatusWriting }, "unstarted design-stage"},
		{"writer", func(wf *WorkflowRecord) { wf.WriterTaskID = "writer-already" }, "existing writer"},
		{"directions", func(wf *WorkflowRecord) { wf.DirectionTaskIDs = []string{"dir-1"} }, "existing directions"},
		{"native directions", func(wf *WorkflowRecord) { wf.NativeGoalTaskIDs = []string{"native-1"} }, "existing directions"},
		{"acceptance", func(wf *WorkflowRecord) { wf.AcceptanceTaskID = "acc-1" }, "existing acceptance"},
		{"existing contract", func(wf *WorkflowRecord) {
			wf.Candidate = &WorkflowCandidate{Commit: "c", Tree: "t"}
		}, "existing contract"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, dir := workflowTestRoot(t)
			cfg := workflowTestCfg(t, root)
			wf := initTestWorkflow(t, root, dir)
			id, integ, domainID, lineage, component, paths := snapshotWorkflowIdentity(wf)
			patchUnboundWorkflow(t, root, wf.ID, tc.patch)
			_, err := captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-receipt", receipt)
			if err == nil || !errors.Is(err, errWorkflowMalformed) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("replacement must reject %s: %v", tc.want, err)
			}
			fresh, err := loadWorkflow(root, cfg, wf.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.DesignLineage != nil {
				t.Fatalf("rejected replacement overwrote design: %+v", fresh.DesignLineage)
			}
			assertWorkflowIdentity(t, fresh, id, integ, domainID, lineage, component, paths)
		})
	}
}

func TestLateInitialDesignBindRejectsConflictingDesign(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	first := writeAstraDesignReceipt(t)
	if _, err := captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-receipt", first); err != nil {
		t.Fatal(err)
	}
	bound, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := bound.DesignLineage.LatestValid.Digest
	second := writeAstraDesignReceiptWith(t, map[string]any{"identity": "other-designer", "session_id": "other-session"})
	if _, err := captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-receipt", second); !errors.Is(err, errGoalDesignProof) || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting design must reject: %v", err)
	}
	fresh, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil || fresh.DesignLineage == nil || fresh.DesignLineage.LatestValid.Digest != wantDigest {
		t.Fatalf("conflict must not overwrite: %+v %v", fresh, err)
	}
}

func TestLateInitialDesignBindIdenticalRebindIsIdempotent(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	receipt := writeAstraDesignReceipt(t)
	if _, err := captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-receipt", receipt); err != nil {
		t.Fatal(err)
	}
	bound, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := bound.DesignLineage.LatestValid.Digest
	wantAt := bound.DesignLineage.LatestValid.At
	wantUpdated := bound.UpdatedAt
	warnings := captureStderr(t, func() {
		_, err = captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-receipt", receipt)
	})
	if err != nil {
		t.Fatalf("identical rebind: %v", err)
	}
	if !strings.Contains(warnings, "already bound") || !strings.Contains(warnings, "no state change") {
		t.Fatalf("identical rebind must warn: %s", warnings)
	}
	fresh, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.DesignLineage.LatestValid.Digest != wantDigest || fresh.DesignLineage.LatestValid.At != wantAt || fresh.UpdatedAt != wantUpdated {
		t.Fatalf("identical rebind changed state: %+v", fresh)
	}
}

func TestLateInitialDesignBindWarnsWithoutInventingIdentity(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	receipt := writeAstraDesignReceiptWith(t, map[string]any{
		"model": "", "actual_model": "", "runner": "", "actual_runner": "", "identity": "", "session_id": "",
	})
	var err error
	warnings := captureStderr(t, func() {
		_, err = captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-receipt", receipt)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warnings, "metadata is incomplete") || !strings.Contains(warnings, "caller-attested") {
		t.Fatalf("missing attribution must stay visible: %s", warnings)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := wf.DesignLineage.LatestValid
	if n.ActualModel != "" || n.ActualRunner != "" || n.Identity != "" || n.DesignSessionID != "" {
		t.Fatalf("must not fabricate attribution: %+v", n)
	}
}

func TestLateInitialDesignBindCompletedDesignTask(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	if _, err := captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-task", "missing-design-task"); !errors.Is(err, errGoalDesignProof) {
		t.Fatalf("missing design task must reject: %v", err)
	}

	review := newTask(root, cfg, typeReview, "independent design", dir, []string{"design the auth token vertical"}, 1)
	review.WriteDomain = nil
	review.GrokModel = "grok-4.6"
	review.PreferRunner = grokBuildRunnerName
	review.ReviewCandidate = &WorkflowCandidate{Commit: "designcommit", Tree: "designtree"}
	if err := saveTask(root, review); err != nil {
		t.Fatal(err)
	}
	markTaskDone(t, root, review.ID)
	writeReviewLog(t, root, review.ID, "independent completed design result for late bind")
	loaded, err := loadTask(root, review.ID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := loadCompletedDesignTask(root, loaded.ID)
	if err != nil {
		t.Fatalf("fixture must be a completed design task: %v", err)
	}

	out, err := captureWorkflowCmd(t, "design-bind", wf.ID, "-root", root, "-design-task", loaded.ID)
	if err != nil {
		t.Fatalf("design-bind -design-task: %v", err)
	}
	if !strings.Contains(out, wf.ID) || !strings.Contains(out, node.Digest) {
		t.Fatalf("task bind stdout: %q", out)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.DesignLineage.LatestValid.Provenance != goalDesignProvenanceTask || wf.DesignLineage.LatestValid.DesignTaskID != loaded.ID {
		t.Fatalf("task bind lineage: %+v", wf.DesignLineage.LatestValid)
	}
	if _, err := captureWorkflowCmd(t, "writer", wf.ID, "-root", root, "-mode", "manual"); err != nil {
		t.Fatalf("manual writer after task bind: %v", err)
	}
}

func TestLateInitialDesignBindConcurrentWithWriter(t *testing.T) {
	root, dir := workflowTestRoot(t)
	cfg := workflowTestCfg(t, root)
	wf := initTestWorkflow(t, root, dir)
	id, integ, domainID, lineage, component, paths := snapshotWorkflowIdentity(wf)
	receipt := writeAstraDesignReceipt(t)

	var wg sync.WaitGroup
	var bindErr, writerErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		bindErr = cmdWorkflow([]string{"design-bind", wf.ID, "-root", root, "-design-receipt", receipt})
	}()
	go func() {
		defer wg.Done()
		writerErr = cmdWorkflow([]string{"writer", wf.ID, "-root", root, "-mode", "manual"})
	}()
	wg.Wait()
	if bindErr != nil {
		t.Fatalf("serialized bind must succeed: %v (writer=%v)", bindErr, writerErr)
	}
	fresh, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkflowIdentity(t, fresh, id, integ, domainID, lineage, component, paths)
	if workflowBoundInitialDesign(fresh) == nil {
		t.Fatal("concurrent writer must not prevent the bind")
	}
	if fresh.WriterTaskID != "" && writerErr != nil {
		t.Fatalf("writer id set but writer failed: %v", writerErr)
	}
	if fresh.WriterTaskID == "" && writerErr == nil {
		t.Fatal("writer succeeded without persisting a writer")
	}
	if fresh.WriterTaskID != "" {
		writer, err := workflowWriterTask(root, fresh)
		if err != nil || writer.Goal == nil || writer.Goal.Started {
			t.Fatalf("concurrent winner writer must stay unstarted: %+v %v", writer, err)
		}
	}
}
