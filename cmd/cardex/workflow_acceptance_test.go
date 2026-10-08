package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func dumpScratch(t *testing.T, name, body string) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("CARDEX_BATCH1_SCRATCH"))
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func oldStyleAcceptancePrompt(wf *WorkflowRecord, members []*Task, root, fingerprint string) string {
	var b strings.Builder
	b.WriteString("overall acceptance workflow=" + wf.ID + " module=" + wf.ModuleID + "\n")
	if wf.Candidate != nil {
		b.WriteString("candidate commit=" + wf.Candidate.Commit + " tree=" + wf.Candidate.Tree + "\n")
	}
	b.WriteString("coverage=" + fingerprint + "\n")
	for _, task := range members {
		b.WriteString("member id=" + task.ID + " session=" + task.SessionID + " goal= status=" + task.Status + " artifact=" + taskLogPath(root, task.ID) + "\n")
	}
	b.WriteString("结论只能是 pass、concerns 或 block。pass 仅当 p0 与 p1 皆空。block 或 concerns 不得把目标标为已验收。\n")
	return b.String()
}

func readyAcceptFixture(t *testing.T) (root, dir string, cfg *Config, wf *WorkflowRecord, members []*Task) {
	t.Helper()
	root, dir = workflowTestRoot(t)
	cfg = workflowTestCfg(t, root)
	wf = initTestWorkflow(t, root, dir)
	design := newTask(root, cfg, typeSequence, "design card", dir, []string{"supplied design plan"}, 1)
	design.Status = statusDone
	design.WorkflowID = wf.ID
	if err := saveTask(root, design); err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowFanout([]string{"-root", root, wf.ID, "-design", design.ID, "-directions", "alpha,beta"}); err != nil {
		t.Fatal(err)
	}
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(dir, "design-result.txt")
	if err := os.WriteFile(resultPath, []byte("design pointer body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wf.DesignLineage = &WorkflowDesignLineage{
		LatestValid: &WorkflowDesignNode{
			Role:         goalDesignRole,
			ReadOnly:     true,
			DesignTaskID: design.ID,
			ResultPath:   resultPath,
			Digest:       "d9e20bfaa6b6c1f08fef8d45a7c32ca005c89609b95e1b3b089fd30076184294",
		},
	}
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	for _, id := range wf.DirectionTaskIDs {
		task, err := loadTask(root, id)
		if err != nil {
			t.Fatal(err)
		}
		task.Status = statusDone
		if err := writeTaskFile(root, task); err != nil {
			t.Fatal(err)
		}
		members = append(members, task)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	return root, dir, cfg, wf, members
}

func assertNewAcceptanceInputs(t *testing.T, prompt, resultPath, digest string) {
	t.Helper()
	for _, key := range []string{
		"overall_goal:",
		"completion_criteria:",
		"design_result:",
		"design_digest:",
		"member id=",
		"artifact=",
	} {
		if !strings.Contains(prompt, key) {
			t.Fatalf("new acceptance prompt missing %q:\n%s", key, prompt)
		}
	}
	if !strings.Contains(prompt, "ship an independently reviewed auth token vertical") {
		t.Fatalf("prompt missing overall goal text:\n%s", prompt)
	}
	if !strings.Contains(prompt, "independent review pass with empty p0/p1") {
		t.Fatalf("prompt missing completion criteria:\n%s", prompt)
	}
	if !strings.Contains(prompt, resultPath) {
		t.Fatalf("prompt missing design_result pointer %q:\n%s", resultPath, prompt)
	}
	if !strings.Contains(prompt, digest) {
		t.Fatalf("prompt missing design_digest %q:\n%s", digest, prompt)
	}
	if !acceptancePromptMeetsInputStandard(prompt) {
		t.Fatal("prompt failed input-standard predicate")
	}
}

func TestOverallAcceptanceNewCardIncludesRequiredInputs(t *testing.T) {
	root, dir, cfg, wf, members := readyAcceptFixture(t)
	if len(members) != 2 {
		t.Fatalf("members=%d", len(members))
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := loadTask(root, wf.AcceptanceTaskID)
	if err != nil {
		t.Fatal(err)
	}
	prompt := strings.Join(acc.Prompts, "\n")
	resultPath := filepath.Join(dir, "design-result.txt")
	assertNewAcceptanceInputs(t, prompt, resultPath, "d9e20bfaa6b6c1f08fef8d45a7c32ca005c89609b95e1b3b089fd30076184294")
	dumpScratch(t, "accept-run-1.txt", prompt)
}

func TestOverallAcceptanceSecondAcceptReusesAndMatchesPrompt(t *testing.T) {
	root, dir, cfg, wf, _ := readyAcceptFixture(t)
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	wf, err := loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstID := wf.AcceptanceTaskID
	first, err := loadTask(root, firstID)
	if err != nil {
		t.Fatal(err)
	}
	firstPrompt := strings.Join(first.Prompts, "\n")
	firstBytes, err := os.ReadFile(taskPath(root, firstID))
	if err != nil {
		t.Fatal(err)
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.AcceptanceTaskID != firstID {
		t.Fatalf("second accept created %s after %s", wf.AcceptanceTaskID, firstID)
	}
	second, err := loadTask(root, firstID)
	if err != nil {
		t.Fatal(err)
	}
	secondPrompt := strings.Join(second.Prompts, "\n")
	if firstPrompt != secondPrompt {
		t.Fatal("reused acceptance prompt bytes changed")
	}
	secondBytes, err := os.ReadFile(taskPath(root, firstID))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatal("reused acceptance card file was rewritten")
	}
	assertNewAcceptanceInputs(t, secondPrompt, filepath.Join(dir, "design-result.txt"),
		"d9e20bfaa6b6c1f08fef8d45a7c32ca005c89609b95e1b3b089fd30076184294")
	dumpScratch(t, "accept-run-2.txt", secondPrompt)
}

func TestOldAcceptanceCardsKeepBytesAndDoNotCoverNewStandard(t *testing.T) {
	t.Run("in-flight", func(t *testing.T) {
		assertOldAcceptancePreserved(t, statusQueued, false)
	})
	t.Run("already-passing", func(t *testing.T) {
		assertOldAcceptancePreserved(t, statusDone, true)
	})
}

func assertOldAcceptancePreserved(t *testing.T, status string, withEvidence bool) {
	t.Helper()
	root, _, cfg, wf, members := readyAcceptFixture(t)
	if wf.Candidate == nil || strings.TrimSpace(wf.Candidate.Commit) == "" {
		wf.Candidate = readLocalSourceCandidate(wf.Worktree)
	}
	fp, err := acceptanceFingerprint(root, wf)
	if err != nil {
		t.Fatal(err)
	}
	oldPrompt := oldStyleAcceptancePrompt(wf, members, root, fp)
	if acceptancePromptMeetsInputStandard(oldPrompt) {
		t.Fatal("old-style fixture accidentally meets the new input standard")
	}
	old := newTask(root, cfg, typeReview, "overall acceptance "+wf.ModuleID, wf.Worktree, []string{oldPrompt}, 6)
	old.Status = status
	old.Project = wf.ModuleID
	old.WorkflowID = wf.ID
	old.DependsOn = append([]string(nil), wf.DirectionTaskIDs...)
	old.ReviewCandidate = &WorkflowCandidate{
		Commit: wf.Candidate.Commit,
		Tree:   wf.Candidate.Tree,
		Branch: wf.Candidate.Branch,
	}
	if err := saveTask(root, old); err != nil {
		t.Fatal(err)
	}
	evidencePath := taskLogPath(root, old.ID)
	evidence := []byte("PASSING-EVIDENCE-BYTES-DO-NOT-REWRITE\n")
	if withEvidence {
		if err := os.MkdirAll(filepath.Dir(evidencePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidencePath, evidence, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	beforeCard, err := os.ReadFile(taskPath(root, old.ID))
	if err != nil {
		t.Fatal(err)
	}
	wf.AcceptanceTaskID = old.ID
	wf.AcceptanceCoverage = fp
	if err := persistWorkflow(root, cfg, wf); err != nil {
		t.Fatal(err)
	}
	if acceptanceTaskCovers(old, wf, fp) {
		t.Fatal("old covering card must not satisfy the new input standard")
	}
	if err := cmdWorkflowAccept([]string{"-root", root, wf.ID}); err != nil {
		t.Fatal(err)
	}
	afterCard, err := os.ReadFile(taskPath(root, old.ID))
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeCard) != string(afterCard) {
		t.Fatal("old acceptance card bytes were rewritten")
	}
	if withEvidence {
		got, err := os.ReadFile(evidencePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(evidence) {
			t.Fatal("old passing evidence bytes were rewritten")
		}
	}
	wf, err = loadWorkflow(root, cfg, wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wf.AcceptanceTaskID == "" || wf.AcceptanceTaskID == old.ID {
		t.Fatalf("accept reused old card id=%s", wf.AcceptanceTaskID)
	}
	fresh, err := loadTask(root, wf.AcceptanceTaskID)
	if err != nil {
		t.Fatal(err)
	}
	assertNewAcceptanceInputs(t, strings.Join(fresh.Prompts, "\n"),
		filepath.Join(wf.Worktree, "design-result.txt"),
		"d9e20bfaa6b6c1f08fef8d45a7c32ca005c89609b95e1b3b089fd30076184294")
	if wf.GoalCompleted {
		t.Fatal("old passing card was treated as the new-standard cover")
	}
}

func TestWorkflowAcceptBinaryCreatesRequiredInputsTwice(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("CARDEX_BATCH1_BIN"))
	if bin == "" {
		bin = buildCardexCLI(t)
	}
	root, dir, cfg, wf, _ := readyAcceptFixture(t)
	run := func() (string, string) {
		t.Helper()
		out, err := exec.Command(bin, "workflow", "accept", "-root", root, wf.ID).CombinedOutput()
		if err != nil {
			t.Fatalf("cardex workflow accept: %v\n%s", err, out)
		}
		fresh, err := loadWorkflow(root, cfg, wf.ID)
		if err != nil {
			t.Fatal(err)
		}
		acc, err := loadTask(root, fresh.AcceptanceTaskID)
		if err != nil {
			t.Fatal(err)
		}
		return string(out), strings.Join(acc.Prompts, "\n")
	}
	stdout1, prompt1 := run()
	stdout2, prompt2 := run()
	assertNewAcceptanceInputs(t, prompt1, filepath.Join(dir, "design-result.txt"),
		"d9e20bfaa6b6c1f08fef8d45a7c32ca005c89609b95e1b3b089fd30076184294")
	if prompt1 != prompt2 {
		t.Fatal("two binary accept runs produced different prompts")
	}
	dumpScratch(t, "accept-run-1.txt", stdout1+"\n---PROMPT---\n"+prompt1)
	dumpScratch(t, "accept-run-2.txt", stdout2+"\n---PROMPT---\n"+prompt2)
}

func TestAcceptancePromptJSONRoundTripStable(t *testing.T) {
	root, _, cfg, wf, _ := readyAcceptFixture(t)
	created, err := createOverallAcceptance(root, cfg, wf)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(created.Prompts)
	if !strings.Contains(string(raw), "overall_goal") {
		t.Fatalf("serialized prompt lost overall_goal: %s", raw)
	}
}
