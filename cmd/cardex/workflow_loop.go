package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Every step below is driven by an explicit `cardex workflow` invocation.
// Nothing here is wired into tick: the accepted design keeps Cardex from
// growing a second state machine that re-plans work on a timer. Each step is
// also written to be replay-safe, because a manager that crashes mid-round
// will re-run the same command after restart.

// workflowRoleType maps a workflow role onto the Cardex card type that role
// must use. Reviewers are read-only cards and never carry a write domain.
func workflowRoleType(role string) string {
	if role == "reviewer" {
		return typeReview
	}
	return typeSequence
}

func taskIsLive(t *Task) bool {
	switch t.Status {
	case statusQueued, statusRunning, statusLimitPaused, statusHeld:
		return true
	}
	return false
}

// taskIsDispatchable is the narrower question of whether tick could still pick
// the card up. A held card is live for dedupe purposes but cannot run.
func taskIsDispatchable(t *Task) bool {
	switch t.Status {
	case statusQueued, statusRunning, statusLimitPaused:
		return true
	}
	return false
}

// workflowDispatchableCards lists cards tick could still run for this workflow.
func workflowDispatchableCards(root string, wf *WorkflowRecord) ([]*Task, error) {
	tasks, err := loadTasks(root)
	if err != nil {
		return nil, err
	}
	var out []*Task
	for _, t := range tasks {
		if t != nil && t.WorkflowID == wf.ID && taskIsDispatchable(t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// workflowActiveRole finds a live card already holding the given role for this
// workflow. A load failure returns fail-closed (found=true, task=nil) so a
// caller can never mint a duplicate writer just because the disk was unreadable.
func workflowActiveRole(root string, wf *WorkflowRecord, role string) (*Task, bool) {
	tasks, err := loadTasks(root)
	if err != nil {
		return nil, true
	}
	want := workflowRoleType(role)
	for _, t := range tasks {
		if t == nil || t.WorkflowID != wf.ID || t.Type != want {
			continue
		}
		// The held integration card is a sequence card too, but it is not a writer.
		if role == "writer" && t.IntegrationGate != nil {
			continue
		}
		if !taskIsLive(t) {
			continue
		}
		return t, true
	}
	return nil, false
}

// duplicateRoleErr renders a duplicate consistently whether or not the rival
// card could be identified.
func duplicateRoleErr(role string, active *Task) error {
	if active == nil {
		return fmt.Errorf("%w: %s (identity unreadable)", errWorkflowDuplicateRole, role)
	}
	return fmt.Errorf("%w: %s %s", errWorkflowDuplicateRole, role, active.ID)
}

func writeWorkflowProgress(root string, wf *WorkflowRecord) error {
	if err := os.MkdirAll(workflowsDir(root), 0o755); err != nil {
		return err
	}
	payload := map[string]any{
		"schema":              "cardex.workflow.progress.v1",
		"workflow_id":         wf.ID,
		"mode":                wf.Mode,
		"module_id":           wf.ModuleID,
		"goal_id":             wf.GoalID,
		"parent_id":           wf.ParentID,
		"status":              wf.Status,
		"round":               wf.CurrentRound,
		"max_rounds":          wf.MaxRounds,
		"writer_task_id":      wf.WriterTaskID,
		"reviewer_task_id":    wf.ReviewerTaskID,
		"integration_task_id": wf.IntegrationTaskID,
		"candidate":           wf.Candidate,
		"review":              wf.Review,
		"effect_gates":        wf.EffectGates,
		"design_lineage":      wf.DesignLineage,
		"updated_at":          wf.UpdatedAt,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	jsonPath := workflowProgressJSONPath(root, wf.ID)
	if err := atomicWriteSync(jsonPath, append(data, '\n')); err != nil {
		return err
	}
	verdict, hold := "", ""
	if wf.Review != nil {
		verdict, hold = wf.Review.Verdict, wf.Review.HoldReason
	}
	commit, tree := "", ""
	if wf.Candidate != nil {
		commit, tree = wf.Candidate.Commit, wf.Candidate.Tree
	}
	md := fmt.Sprintf(`# Workflow %s

- mode: %s
- module: %s
- goal: %s (%s)
- parent: %s
- status: %s
- round: %d / %d
- writer: %s
- reviewer: %s
- integration card: %s
- gates: integration=%s live=%s cutover=%s
- candidate: %s / %s
- review verdict: %s
- hold reason: %s

Routine progress stays in this file. Root is notified only for an admissible
review pass, a true external dependency, an owner choice, or an exhausted route.
Integration release is never live authority; live and cutover stay held.
`, wf.ID, wf.Mode, wf.ModuleID, wf.GoalID, wf.Goal, orDash(wf.ParentID), wf.Status,
		wf.CurrentRound, wf.MaxRounds, orDash(wf.WriterTaskID), orDash(wf.ReviewerTaskID),
		orDash(wf.IntegrationTaskID), wf.EffectGates.Integration, wf.EffectGates.Live,
		wf.EffectGates.Cutover, orDash(commit), orDash(tree), orDash(verdict), orDash(hold))
	mdPath := workflowProgressMDPath(root, wf.ID)
	if err := atomicWriteSync(mdPath, []byte(md)); err != nil {
		return err
	}
	wf.Progress = WorkflowProgressCoords{JSON: jsonPath, Markdown: mdPath}
	return nil
}

// emitRootNotify writes a material receipt. Routine round-to-round progress is
// not material and must never reach here.
func emitRootNotify(root string, wf *WorkflowRecord, kind, summary string) error {
	switch kind {
	case rootNotifyReviewPassed, rootNotifyExternal, rootNotifyOwner, rootNotifyExhausted:
	default:
		return fmt.Errorf("%w: non-material notify kind %q", errWorkflowMalformed, kind)
	}
	if err := os.MkdirAll(workflowNotifyDir(root), 0o755); err != nil {
		return err
	}
	path := workflowNotifyPath(root, wf.ID, kind)
	n := &WorkflowNotify{
		Kind:     kind,
		At:       time.Now().Format(time.RFC3339),
		Summary:  summary,
		Receipt:  path,
		Workflow: wf.ID,
	}
	if wf.Candidate != nil {
		n.Candidate, n.Tree = wf.Candidate.Commit, wf.Candidate.Tree
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWriteSync(path, append(data, '\n')); err != nil {
		return err
	}
	wf.MaterialNotify = n
	return nil
}

// persistWorkflow writes the progress projection and the record together so a
// reader never sees a record whose progress file describes an older round.
func persistWorkflow(root string, cfg *Config, wf *WorkflowRecord) error {
	if err := writeWorkflowProgress(root, wf); err != nil {
		return err
	}
	return saveWorkflow(root, cfg, wf)
}

func copyWriteDomain(d WriteDomain) *WriteDomain {
	return &WriteDomain{
		ID:        d.ID,
		Lineage:   d.Lineage,
		Component: d.Component,
		Paths:     append([]string(nil), d.Paths...),
		Resources: append([]ResourceClaim(nil), d.Resources...),
	}
}

// inheritWriteDomain deep-copies an optional claim across a card lineage, so a
// derived card never aliases (and cannot silently drop) its parent's claim.
func inheritWriteDomain(d *WriteDomain) *WriteDomain {
	if d == nil {
		return nil
	}
	return copyWriteDomain(*d)
}

// createHeldIntegrationTask mints the module/program integration card. It is
// created `held` with a gate attached, which is the whole point: the card
// exists so the DAG can name it, not so it can run.
func createHeldIntegrationTask(root string, cfg *Config, wf *WorkflowRecord) (*Task, error) {
	prompt := fmt.Sprintf(
		"只集成 workflow %s（模块 %s）已被采信的候选。本卡默认 held：只有机器核验独立审核 verdict=pass 且 p0/p1 为空、"+
			"候选 commit/tree 与冻结记录一致、custody 一致后才可能 release。释放集成不等于 live；live 与 cutover 是另外的 held 门。",
		wf.ID, wf.ModuleID)
	t := newTask(root, cfg, typeSequence, "workflow integrate: "+wf.ModuleID, wf.Worktree, []string{prompt}, 5)
	t.Project = wf.ModuleID
	t.WorkflowID = wf.ID
	t.Status = statusHeld
	t.ReviewAfter = false
	t.WriteDomain = copyWriteDomain(wf.WriteDomain)
	t.IntegrationGate = &IntegrationGate{WorkflowID: wf.ID}
	if err := configureWorkflowWriter(cfg, wf, t); err != nil {
		return nil, err
	}
	if err := saveTask(root, t); err != nil {
		return nil, err
	}
	emitTaskEvent(root, t.ID, evHeld, "workflow:integration", statusHeld, 0,
		withCostTelemetry(map[string]any{"workflow": wf.ID, "reason": "default-held-integration"}, t))
	wf.IntegrationTaskID = t.ID
	wf.EffectGates.Integration = effectGateHeld
	return t, nil
}

// syncIntegrationGate mirrors the current writer/reviewer/candidate identities
// onto the integration card. It only ever narrows what the gate will accept;
// the gate still re-derives admissibility from the review transcript.
func syncIntegrationGate(root string, wf *WorkflowRecord) error {
	if wf.IntegrationTaskID == "" {
		return nil
	}
	integ, err := loadTask(root, wf.IntegrationTaskID)
	if err != nil {
		return err
	}
	if integ.IntegrationGate == nil {
		integ.IntegrationGate = &IntegrationGate{}
	}
	wantMethod, wantReceipt, wantReceiptSHA := "", "", ""
	if wf.Review != nil && wf.Review.Method == reviewMethodExternalIndependentLocal {
		wantMethod = reviewMethodExternalIndependentLocal
		wantReceipt = wf.Review.ReceiptPath
		wantReceiptSHA = wf.Review.ReceiptSHA256
	}
	wantCommit, wantTree := "", ""
	if wf.Candidate != nil {
		wantCommit, wantTree = wf.Candidate.Commit, wf.Candidate.Tree
	}
	g := integ.IntegrationGate
	if g.WorkflowID == wf.ID && g.WriterTaskID == wf.WriterTaskID && g.ReviewTaskID == wf.ReviewerTaskID &&
		g.CandidateCommit == wantCommit && g.CandidateTree == wantTree &&
		g.ReviewMethod == wantMethod && g.ExternalReceiptPath == wantReceipt && g.ExternalReceiptSHA256 == wantReceiptSHA {
		return nil
	}
	g.WorkflowID = wf.ID
	g.WriterTaskID = wf.WriterTaskID
	g.ReviewTaskID = wf.ReviewerTaskID
	g.CandidateCommit = wantCommit
	g.CandidateTree = wantTree
	g.ReviewMethod = wantMethod
	g.ExternalReceiptPath = wantReceipt
	g.ExternalReceiptSHA256 = wantReceiptSHA
	return saveTask(root, integ)
}

// admitWorkflowWriter queues the single active writer for this workflow.
// Replaying it while a writer is live is a duplicate, not a second attempt.
func admitWorkflowWriter(root string, cfg *Config, wf *WorkflowRecord, prompt string) (*Task, error) {
	return publishWorkflowWriter(root, cfg, wf, prompt, nil)
}

// publishWorkflowWriter constructs the complete writer (including any Goal
// binding) inside one admission lock, then saves once. A concurrent tick
// therefore never sees an ordinary queued Goal-less card.
func publishWorkflowWriter(root string, cfg *Config, wf *WorkflowRecord, prompt string, goal *TaskGoalBinding) (*Task, error) {
	var out *Task
	err := withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		t, err := publishWorkflowWriterLocked(root, cfg, wf, prompt, goal, false)
		out = t
		return err
	})
	return out, err
}

// publishWorkflowWriterLocked constructs the complete writer inside the existing
// workflow-admit lock. skipRefresh is true when the caller already refreshed
// and holds in-memory DesignLineage/round that must not be clobbered.
func publishWorkflowWriterLocked(root string, cfg *Config, wf *WorkflowRecord, prompt string, goal *TaskGoalBinding, skipRefresh bool) (*Task, error) {
	if !skipRefresh {
		if err := refreshWorkflow(root, cfg, wf); err != nil {
			return nil, err
		}
	}
	if goal != nil {
		if err := reverifyGoalDesignProof(root, wf); err != nil {
			return nil, err
		}
	}
	if active, found := workflowActiveRole(root, wf, "writer"); found {
		return active, duplicateRoleErr("writer", active)
	}
	explicitPrompt := strings.TrimSpace(prompt)
	if explicitPrompt != "" {
		prompt = explicitPrompt
	} else if tpl, err := loadTemplate(root, "workflow-writer"); err == nil && strings.TrimSpace(tpl) != "" {
		prompt = renderTemplate(tpl, map[string]string{
			"MODULE":   wf.ModuleID,
			"DIR":      wf.Worktree,
			"GOAL":     wf.Goal,
			"CRITERIA": wf.TerminalCriteria,
			"ROUND":    fmt.Sprintf("%d", wf.CurrentRound),
			"MODE":     wf.Mode,
		})
	} else {
		prompt = wf.Goal + "\n\n完成标准:\n" + wf.TerminalCriteria
	}
	t := newTask(root, cfg, typeSequence, "workflow writer: "+wf.ModuleID, wf.Worktree, []string{prompt}, 8)
	t.Project = wf.ModuleID
	t.WorkflowID = wf.ID
	// The workflow owns the review gate itself; an extra auto-review child card
	// would be a second, unaccounted reviewer for the same candidate.
	t.ReviewAfter = false
	t.FixRound = wf.CurrentRound
	t.MaxFixRounds = wf.MaxRounds
	t.WriteDomain = copyWriteDomain(wf.WriteDomain)
	if err := configureWorkflowWriter(cfg, wf, t); err != nil {
		return nil, err
	}
	t.Goal = goal
	if goal != nil {
		goal.Provider = t.PreferRunner
	}
	if t.PreferRunner == grokBuildRunnerName {
		_ = freezeGrokAttemptModel(context.Background(), cfg, t)
		freezeGoalRouteSnapshot(t)
	} else if err := admitTaskRoute(cfg, t); err != nil {
		return nil, err
	}
	if hook := goalWriterPublishHook; hook != nil {
		hook(t)
	}
	if err := saveTask(root, t); err != nil {
		return nil, err
	}
	emitTaskEvent(root, t.ID, evQueued, "workflow:writer", statusQueued, 0, map[string]any{
		"workflow": wf.ID, "module": wf.ModuleID, "round": wf.CurrentRound, "engine": t.PreferRunner,
	})
	wf.WriterTaskID = t.ID
	// A new writer invalidates the previous round's reviewer and verdict.
	wf.ReviewerTaskID = ""
	wf.Review = nil
	wf.Status = workflowStatusWriting
	if err := syncIntegrationGate(root, wf); err != nil {
		return t, err
	}
	if err := persistWorkflow(root, cfg, wf); err != nil {
		return t, err
	}
	return t, nil
}

// admitWorkflowReviewer queues an independent read-only reviewer bound to the
// frozen candidate. It refuses to run before the candidate is frozen, so a
// reviewer can never be pointed at moving bytes.
func admitWorkflowReviewer(root string, cfg *Config, wf *WorkflowRecord) (*Task, error) {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return nil, err
	}
	if wf.WriterTaskID == "" {
		return nil, fmt.Errorf("%w: no writer to review", errWorkflowMalformed)
	}
	if wf.Candidate == nil || (wf.Candidate.Commit == "" && wf.Candidate.Tree == "") {
		return nil, fmt.Errorf("%w: freeze a candidate before review", errWorkflowMalformed)
	}
	if active, found := workflowActiveRole(root, wf, "reviewer"); found {
		return active, duplicateRoleErr("reviewer", active)
	}
	if active, found := workflowActiveRole(root, wf, "writer"); found {
		return active, fmt.Errorf("%w: writer still live, candidate is not frozen", errWorkflowDuplicateRole)
	}
	writer, err := findTaskAnywhere(root, wf.WriterTaskID)
	if err != nil {
		return nil, err
	}
	if writer.Status != statusDone {
		return nil, fmt.Errorf("%w: writer %s is %s, not done", errWorkflowMalformed, writer.ID, writer.Status)
	}
	tpl, err := loadTemplate(root, typeReview)
	if err != nil {
		return nil, err
	}
	focus := fmt.Sprintf(
		"独立审核 workflow %s 模块 %s 的冻结候选 commit %s / tree %s。只读：不得修改候选、不得替换 writer、不得拼接旧 attempt 输出。"+
			"结论按模板收尾，verdict 只能是 pass、concerns 或 block；pass 仅当 p0 与 p1 皆空。",
		wf.ID, wf.ModuleID, orDash(wf.Candidate.Commit), orDash(wf.Candidate.Tree))
	prompt := renderTemplate(tpl, map[string]string{"DIR": wf.Worktree, "FOCUS": focus})

	t := newTask(root, cfg, typeReview, "workflow review: "+wf.ModuleID, wf.Worktree, []string{prompt}, writer.Priority)
	t.Project = wf.ModuleID
	t.WorkflowID = wf.ID
	t.ReviewOf = writer.ID
	t.ReviewCandidate = &WorkflowCandidate{Commit: wf.Candidate.Commit, Tree: wf.Candidate.Tree}
	t.FixRound = writer.FixRound
	t.MaxFixRounds = wf.MaxRounds
	// A reviewer that inherits the writer's session is not independent, and a
	// reviewer that claims paths is not read-only.
	t.SessionID = ""
	t.WriteDomain = nil
	t.PreferRunner = wf.ReviewerEngine
	t.RunnerExplicit = true
	if err := saveTask(root, t); err != nil {
		return nil, err
	}
	emitTaskEvent(root, t.ID, evQueued, "workflow:reviewer", statusQueued, 0, map[string]any{
		"workflow": wf.ID, "review_of": writer.ID, "round": wf.CurrentRound, "engine": t.PreferRunner,
	})
	wf.ReviewerTaskID = t.ID
	wf.Review = nil
	wf.Status = workflowStatusReviewing
	if err := syncIntegrationGate(root, wf); err != nil {
		return t, err
	}
	return t, persistWorkflow(root, cfg, wf)
}

// ingestWorkflowReview re-derives the verdict from the review transcript and
// records it. Calling it twice on the same terminal yields the same snapshot.
func ingestWorkflowReview(root string, cfg *Config, wf *WorkflowRecord) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	if wf.ReviewerTaskID == "" {
		return fmt.Errorf("%w: no reviewer to ingest", errWorkflowMalformed)
	}
	review, err := findTaskAnywhere(root, wf.ReviewerTaskID)
	if err != nil {
		return err
	}
	raw := loadTaskResultForGate(root, review)
	v := parseReviewVerdict(raw)
	snap := &WorkflowReview{
		TaskID:     review.ID,
		Round:      wf.CurrentRound,
		HoldReason: reviewHoldReason(v, raw),
	}
	if v != nil {
		snap.Verdict = v.Verdict
		snap.P0 = append([]string(nil), v.P0...)
		snap.P1 = append([]string(nil), v.P1...)
		snap.P2 = append([]string(nil), v.P2...)
	}
	if review.ReviewOutput != nil {
		snap.CandidateCommit, snap.CandidateTree = review.ReviewOutput.CandidateCommit, review.ReviewOutput.CandidateTree
	}
	if snap.HoldReason == "" {
		switch {
		case wf.Candidate == nil || !candidateIdentitiesMatch(&IntegrationGate{CandidateCommit: wf.Candidate.Commit, CandidateTree: wf.Candidate.Tree}, snap, wf.Candidate):
			snap.HoldReason = holdReasonCandidateMismatch
		default:
			var writer *Task
			if wf.WriterTaskID != "" {
				writer, _ = findTaskAnywhere(root, wf.WriterTaskID)
			}
			if ok, reason := integrationCustodyOK(root, review, writer); !ok {
				snap.HoldReason = reason
			} else {
				snap.Admissible = true
			}
		}
	}
	wf.Review = snap
	// Integration stays held either way. An admissible pass only makes the card
	// *eligible* for a separate, explicit release step.
	wf.EffectGates.Integration = effectGateHeld
	if snap.Admissible {
		wf.Status = workflowStatusReviewPassed
		if err := emitRootNotify(root, wf, rootNotifyReviewPassed,
			"admissible review pass; integration, live and cutover all remain held"); err != nil {
			return err
		}
	} else {
		wf.Status = workflowStatusIntegrationHeld
	}
	if err := syncIntegrationGate(root, wf); err != nil {
		return err
	}
	return persistWorkflow(root, cfg, wf)
}

// admitWorkflowRepair opens the next bounded repair round. Exceeding
// max_rounds terminalizes the route and notifies Root instead of looping.
func admitWorkflowRepair(root string, cfg *Config, wf *WorkflowRecord, findings, summary string) (*Task, error) {
	var out *Task
	err := withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		t, err := admitWorkflowRepairLocked(root, cfg, wf, findings, summary)
		out = t
		return err
	})
	return out, err
}

func admitWorkflowRepairLocked(root string, cfg *Config, wf *WorkflowRecord, findings, summary string) (*Task, error) {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return nil, err
	}
	writer, err := findTaskAnywhere(root, wf.WriterTaskID)
	if err == nil && writer != nil && writer.Goal != nil {
		return nil, fmt.Errorf("%w: Goal repair is refused; use design-result -decision revise with a fresh independent design", errGoalDesignResult)
	}
	if wf.Review == nil {
		return nil, fmt.Errorf("%w: no ingested review to repair from", errWorkflowMalformed)
	}
	if wf.Review.Admissible {
		return nil, fmt.Errorf("%w: review already passed", errWorkflowMalformed)
	}
	if wf.CurrentRound+1 > wf.MaxRounds {
		wf.Status = workflowStatusExhausted
		if err := emitRootNotify(root, wf, rootNotifyExhausted,
			fmt.Sprintf("repair rounds exhausted at %d/%d; candidate stays unaccepted and integration held",
				wf.CurrentRound, wf.MaxRounds)); err != nil {
			return nil, err
		}
		if err := persistWorkflow(root, cfg, wf); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %d/%d", errWorkflowRoundsExceeded, wf.CurrentRound, wf.MaxRounds)
	}
	if active, found := workflowActiveRole(root, wf, "writer"); found {
		return active, duplicateRoleErr("writer", active)
	}
	if active, found := workflowActiveRole(root, wf, "reviewer"); found {
		return active, duplicateRoleErr("reviewer", active)
	}
	if err != nil {
		return nil, err
	}
	round := wf.CurrentRound + 1
	tpl, err := loadTemplate(root, "fix-cycle")
	if err != nil {
		return nil, err
	}
	prompt := renderTemplate(tpl, map[string]string{
		"TITLE":       writer.Title,
		"VERDICT":     wf.Review.Verdict,
		"ROUND":       fmt.Sprintf("%d", round),
		"SUMMARY":     summary,
		"FINDINGS":    findings,
		"ORIG_PROMPT": strings.Join(writer.Prompts, "\n"),
		"REVIEW_LOG":  taskLogPath(root, wf.Review.TaskID),
	})
	t := newTask(root, cfg, typeSequence, fmt.Sprintf("workflow repair R%d: %s", round, wf.ModuleID),
		wf.Worktree, []string{prompt}, writer.Priority)
	t.Project = wf.ModuleID
	t.WorkflowID = wf.ID
	t.ReviewAfter = false
	t.FixRound = round
	t.MaxFixRounds = wf.MaxRounds
	t.SkipPermissions = writer.SkipPermissions
	t.WriteDomain = copyWriteDomain(wf.WriteDomain)
	if err := configureWorkflowWriter(cfg, wf, t); err != nil {
		return nil, err
	}
	if hook := goalWriterPublishHook; hook != nil {
		hook(t)
	}
	if err := saveTask(root, t); err != nil {
		return nil, err
	}
	emitTaskEvent(root, t.ID, evQueued, "workflow:repair", statusQueued, 0, map[string]any{
		"workflow": wf.ID, "round": round, "engine": t.PreferRunner,
	})
	wf.WriterTaskID = t.ID
	wf.CurrentRound = round
	wf.Status = workflowStatusRepairing
	// The repaired bytes are a new candidate; the previous freeze and verdict
	// must not survive into the next review.
	wf.ReviewerTaskID = ""
	wf.Review = nil
	wf.Candidate = nil
	if err := syncIntegrationGate(root, wf); err != nil {
		return t, err
	}
	return t, persistWorkflow(root, cfg, wf)
}

// verifyWorkflowCandidate proves an operator-supplied commit/tree pair against
// the repository object database and returns the fully-resolved SHAs. Three
// separate refusals, each fail-closed:
//   - the commit must resolve to a commit object (`rev-parse --verify ^{commit}`);
//   - the tree must resolve to an object of type tree (`cat-file -t`), so a
//     commit or blob id pasted into -tree is not silently accepted;
//   - the tree must be exactly the tree of that commit, so the pair cannot name
//     two unrelated snapshots.
//
// `--end-of-options` keeps a hostile identifier from being parsed as a git
// flag. A worktree that is not a git repository refuses too: an unverifiable
// candidate identity must never be frozen.
func verifyWorkflowCandidate(worktree, commitArg, treeArg string) (commit, tree string, err error) {
	out, gitErr := gitOutput(worktree, "rev-parse", "--verify", "--quiet", "--end-of-options", commitArg+"^{commit}")
	if gitErr != nil {
		return "", "", fmt.Errorf("%w: candidate commit %q does not resolve to a commit object in %s",
			errWorkflowMalformed, commitArg, worktree)
	}
	commit = strings.TrimSpace(string(out))
	out, gitErr = gitOutput(worktree, "rev-parse", "--verify", "--quiet", "--end-of-options", treeArg)
	if gitErr != nil {
		return "", "", fmt.Errorf("%w: candidate tree %q does not resolve to an object in %s",
			errWorkflowMalformed, treeArg, worktree)
	}
	tree = strings.TrimSpace(string(out))
	out, gitErr = gitOutput(worktree, "cat-file", "-t", tree)
	if gitErr != nil || strings.TrimSpace(string(out)) != "tree" {
		return "", "", fmt.Errorf("%w: candidate tree %q is not a tree object in %s",
			errWorkflowMalformed, treeArg, worktree)
	}
	out, gitErr = gitOutput(worktree, "rev-parse", "--verify", "--quiet", "--end-of-options", commit+"^{tree}")
	if gitErr != nil {
		return "", "", fmt.Errorf("%w: cannot derive the tree of commit %s in %s",
			errWorkflowMalformed, commit, worktree)
	}
	if derived := strings.TrimSpace(string(out)); derived != tree {
		return "", "", fmt.Errorf("%w: candidate tree %s is not the tree of commit %s (its tree is %s)",
			errWorkflowMalformed, tree, commit, derived)
	}
	return commit, tree, nil
}

// freezeWorkflowCandidate binds the exact bytes a review will be about. The
// operator-supplied commit/tree are claims, not evidence: both are re-derived
// from the repository itself and the record stores the fully-resolved SHAs, so
// a fabricated or typo'd identity can never become the label the integration
// gate later matches against.
func freezeWorkflowCandidate(root string, cfg *Config, wf *WorkflowRecord, cand WorkflowCandidate) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	if strings.TrimSpace(cand.Commit) == "" || strings.TrimSpace(cand.Tree) == "" {
		return fmt.Errorf("%w: candidate needs both commit and tree", errWorkflowMalformed)
	}
	if active, found := workflowActiveRole(root, wf, "writer"); found {
		return fmt.Errorf("%w: writer %s still live; bytes are not frozen",
			errWorkflowDuplicateRole, taskIDOrUnknown(active))
	}
	return bindFrozenWorkflowCandidate(root, cfg, wf, cand, "")
}

func bindFrozenWorkflowCandidate(root string, cfg *Config, wf *WorkflowRecord, cand WorkflowCandidate, freezeKind string) error {
	commit, tree, err := verifyWorkflowCandidate(wf.Worktree,
		strings.TrimSpace(cand.Commit), strings.TrimSpace(cand.Tree))
	if err != nil {
		return err
	}
	paths := append([]string(nil), cand.ChangedPaths...)
	var digests map[string]string
	if freezeKind == freezeKindRetainedHeldSource {
		if len(paths) == 0 {
			paths = append([]string(nil), wf.WriteDomain.Paths...)
		}
		if err := workflowPathsInsideWriteDomain(wf, paths); err != nil {
			return err
		}
		if err := assertGitDiffCoveredByDeclared(wf.Worktree, commit, paths, wf.WriteDomain.Paths); err != nil {
			return err
		}
		digests, err = digestPathsAtTree(wf.Worktree, tree, paths)
		if err != nil {
			return err
		}
	}
	wf.Candidate = &WorkflowCandidate{
		Commit:       commit,
		Tree:         tree,
		Branch:       strings.TrimSpace(cand.Branch),
		ChangedPaths: paths,
		PathDigests:  digests,
		FreezeKind:   freezeKind,
	}
	if err := syncIntegrationGate(root, wf); err != nil {
		return err
	}
	return persistWorkflow(root, cfg, wf)
}

func taskIDOrUnknown(t *Task) string {
	if t == nil {
		return "(unreadable)"
	}
	return t.ID
}

// tryReleaseWorkflowIntegration is the only path from held integration to
// queued. It re-derives the decision rather than trusting the stored snapshot,
// and it never touches the live or cutover gates.
func tryReleaseWorkflowIntegration(root string, cfg *Config, wf *WorkflowRecord) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	if wf.LocalIntegration != nil {
		return fmt.Errorf("%w: local integration already recorded; no model dispatch", errWorkflowHeld)
	}
	if wf.IntegrationTaskID == "" {
		return fmt.Errorf("%w: no integration card", errWorkflowMalformed)
	}
	if err := syncIntegrationGate(root, wf); err != nil {
		return err
	}
	integ, err := loadTask(root, wf.IntegrationTaskID)
	if err != nil {
		return err
	}
	dec := evaluateIntegrationRelease(root, cfg, integ)
	if !dec.Admit {
		// Fail closed: a queued integration card that no longer has admissible
		// evidence goes back on hold through the supported control-plane path.
		// An already-held card is refused without rewriting workflow/task bytes.
		if integ.Status == statusQueued {
			if err := terminalize(root, integ.ID, statusHeld, "workflow:integration-refused",
				dec.HoldReason, map[string]any{"workflow": wf.ID, "reason": dec.HoldReason}); err != nil {
				return err
			}
			if wf.Review != nil {
				wf.Review.Admissible = false
				wf.Review.HoldReason = dec.HoldReason
			}
			wf.EffectGates.Integration = effectGateHeld
			wf.Status = workflowStatusIntegrationHeld
			if err := persistWorkflow(root, cfg, wf); err != nil {
				return err
			}
		}
		return fmt.Errorf("%w: %s", errWorkflowHeld, dec.HoldReason)
	}
	if integ.Status == statusHeld {
		// Same control-plane transition `cardex release` performs: an epoch bump
		// is what makes held → queued a legal write.
		restoreScheduling(integ)
		integ.Status = statusQueued
		integ.NotBeforeEpoch = 0
		integ.touch()
		if err := saveTask(root, integ); err != nil {
			return err
		}
		emitTaskEvent(root, integ.ID, evQueued, "workflow:integration-released", statusQueued, integ.Step,
			map[string]any{"workflow": wf.ID, "review": wf.ReviewerTaskID})
	}
	wf.EffectGates.Integration = effectGateReleased
	wf.Status = workflowStatusIntegrating
	return persistWorkflow(root, cfg, wf)
}

// markWorkflowRoute terminalizes a route that needs a human: a true external
// dependency, an owner choice, or an exhausted set of options. Each writes one
// material Root receipt and releases the write-domain claim.
func markWorkflowRoute(root string, cfg *Config, wf *WorkflowRecord, kind, summary string) error {
	return withWorkflowSchedulerLock(root, cfg, func() error {
		return markWorkflowRouteLocked(root, cfg, wf, kind, summary)
	})
}

func markWorkflowRouteLocked(root string, cfg *Config, wf *WorkflowRecord, kind, summary string) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	var notifyKind, status string
	switch kind {
	case "external":
		notifyKind, status = rootNotifyExternal, workflowStatusExternalBlocked
	case "owner":
		notifyKind, status = rootNotifyOwner, workflowStatusOwnerChoice
	case "exhausted":
		notifyKind, status = rootNotifyExhausted, workflowStatusExhausted
	default:
		return fmt.Errorf("%w: unknown mark kind %q", errWorkflowMalformed, kind)
	}
	if err := workflowMayReleaseClaim(root, wf); err != nil {
		return err
	}
	wf.Status = status
	if err := emitRootNotify(root, wf, notifyKind, strings.TrimSpace(summary)); err != nil {
		return err
	}
	return persistWorkflow(root, cfg, wf)
}

// workflowMayReleaseClaim is the owner/exhausted/external terminalizer gate.
// Dispatchable cards, a still-live producer, unknown custody, and a missing
// referenced writer keep the claim. A disappeared directory is not release
// authority by itself.
func workflowMayReleaseClaim(root string, wf *WorkflowRecord) error {
	if wf == nil {
		return errWorkflowMalformed
	}
	if _, proven := provenWorkflowRepoRoot(wf); !proven && !historicalInactiveEvidence(root, wf) {
		return fmt.Errorf("%w: missing workflow root is not release authority", errWorkflowCustody)
	}
	live, err := workflowDispatchableCards(root, wf)
	if err != nil {
		return err
	}
	if len(live) > 0 {
		ids := make([]string, 0, len(live))
		for _, t := range live {
			ids = append(ids, t.ID+"("+t.Status+")")
		}
		return fmt.Errorf("%w: hold these cards before terminalizing the route: %s",
			errWorkflowDuplicateRole, strings.Join(ids, ", "))
	}
	tasks, err := loadTasks(root)
	if err != nil {
		return fmt.Errorf("%w: task list unreadable, unknown custody fail-closed", errWorkflowCustody)
	}
	seen := map[string]bool{}
	var ids []string
	for _, t := range tasks {
		if t == nil || t.WorkflowID != wf.ID || t.ID == "" {
			continue
		}
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		ids = append(ids, t.ID)
	}
	for _, id := range workflowReferencedMemberIDs(wf) {
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, id := range ids {
		t, err := loadReferencedMemberForClaimRelease(root, wf.ID, id)
		if err != nil {
			return err
		}
		if err := workflowCheckCardCustody(root, t); err != nil {
			return err
		}
	}
	return nil
}

func loadReferencedMemberForClaimRelease(root, workflowID, taskID string) (*Task, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("%w: referenced writer missing/unreadable, unknown custody fail-closed", errWorkflowCustody)
	}
	livePath := taskPath(root, taskID)
	archivePath := filepath.Join(archiveDir(root), taskID+".json")
	liveInfo, liveStatErr := os.Stat(livePath)
	archiveInfo, archiveStatErr := os.Stat(archivePath)
	liveExists := liveStatErr == nil && liveInfo != nil && !liveInfo.IsDir()
	archiveExists := archiveStatErr == nil && archiveInfo != nil && !archiveInfo.IsDir()
	if liveStatErr != nil && !os.IsNotExist(liveStatErr) {
		return nil, fmt.Errorf("%w: referenced writer %s missing/unreadable, unknown custody fail-closed",
			errWorkflowCustody, taskID)
	}
	if archiveStatErr != nil && !os.IsNotExist(archiveStatErr) {
		return nil, fmt.Errorf("%w: referenced writer %s missing/unreadable, unknown custody fail-closed",
			errWorkflowCustody, taskID)
	}

	if liveExists {
		t, err := loadTask(root, taskID)
		if err != nil || t == nil {
			if archiveExists {
				return nil, fmt.Errorf("%w: referenced writer %s live record unreadable, refusing archive fallback, unknown custody fail-closed",
					errWorkflowCustody, taskID)
			}
			return nil, fmt.Errorf("%w: referenced writer %s missing/unreadable, unknown custody fail-closed",
				errWorkflowCustody, taskID)
		}
		if archiveExists {
			return nil, fmt.Errorf("%w: referenced writer %s live+archive duplicates, unknown custody fail-closed",
				errWorkflowCustody, taskID)
		}
		if t.ID != taskID {
			return nil, fmt.Errorf("%w: referenced writer %s identity mismatch, unknown custody fail-closed",
				errWorkflowCustody, taskID)
		}
		return t, nil
	}

	if !archiveExists {
		return nil, fmt.Errorf("%w: referenced writer %s missing/unreadable, unknown custody fail-closed",
			errWorkflowCustody, taskID)
	}
	archived, err := loadArchivedTaskFile(root, taskID)
	if err != nil || archived == nil {
		return nil, fmt.Errorf("%w: referenced writer %s missing/unreadable, unknown custody fail-closed",
			errWorkflowCustody, taskID)
	}
	if archived.ID != taskID {
		return nil, fmt.Errorf("%w: referenced writer %s archive identity mismatch, unknown custody fail-closed",
			errWorkflowCustody, taskID)
	}
	if strings.TrimSpace(archived.WorkflowID) != strings.TrimSpace(workflowID) {
		return nil, fmt.Errorf("%w: referenced writer %s wrong workflow binding, unknown custody fail-closed",
			errWorkflowCustody, taskID)
	}
	if !archivedTerminalStatus(archived.Status) || archived.effectiveControlState() != controlTerminal {
		return nil, fmt.Errorf("%w: referenced writer %s is not a canceled/terminal archive, unknown custody fail-closed",
			errWorkflowCustody, taskID)
	}
	if !recordedAttemptCloseout(root, archived) {
		return nil, fmt.Errorf("%w: referenced writer %s has no genuine original-attempt closeout, unknown custody fail-closed",
			errWorkflowCustody, taskID)
	}
	return archived, nil
}

func archivedTerminalStatus(status string) bool {
	switch status {
	case statusCanceled, statusDone, statusFailed:
		return true
	default:
		return false
	}
}

func historicalInactiveEvidence(root string, wf *WorkflowRecord) bool {
	if wf == nil {
		return false
	}
	switch wf.Status {
	case workflowStatusOwnerChoice, workflowStatusExhausted, workflowStatusExternalBlocked:
		return true
	}
	tasks, err := loadTasks(root)
	if err != nil {
		return false
	}
	for _, t := range tasks {
		if t != nil && t.WorkflowID == wf.ID {
			return true
		}
	}
	return len(workflowReferencedMemberIDs(wf)) > 0
}

func workflowReferencedMemberIDs(wf *WorkflowRecord) []string {
	if wf == nil || !workflowClaimsActive(wf) {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	add(wf.WriterTaskID)
	add(wf.ReviewerTaskID)
	add(wf.IntegrationTaskID)
	add(wf.AcceptanceTaskID)
	for _, id := range wf.DirectionTaskIDs {
		add(id)
	}
	for _, id := range wf.NativeGoalTaskIDs {
		add(id)
	}
	return ids
}

func workflowCheckCardCustody(root string, t *Task) error {
	if t == nil {
		return fmt.Errorf("%w: referenced member unreadable, unknown custody fail-closed", errWorkflowCustody)
	}
	if taskHasLiveWriterProof(root, t) {
		return fmt.Errorf("%w: live custody on %s is not released; directory disappearance does not authorize release",
			errWorkflowCustody, t.ID)
	}
	if workflowCardHasUnresolvedCustody(root, t) {
		if t.Status == statusHeld || t.effectiveControlState() == controlRevoking {
			return fmt.Errorf("%w: held/revoking %s has no recorded closeout; pid absence is not release authority",
				errWorkflowCustody, t.ID)
		}
		return fmt.Errorf("%w: unknown custody on %s is not released; held unknown stays writing",
			errWorkflowCustody, t.ID)
	}
	return nil
}

// recordedAttemptCloseout is the normal exited-attempt record. A missing PID,
// lease, or active attempt is not that record.
func recordedAttemptCloseout(root string, t *Task) bool {
	rec, err := loadRequiredGoalAttempt(root, t)
	if err != nil || rec == nil {
		return false
	}
	return rec.State == attemptExited
}

// taskHasAttemptRecord reports whether this card has any attempt file.
// A missing directory is a positive "never recorded" fact. An unreadable
// directory is unknown and must not be treated as absence.
func taskHasAttemptRecord(root, taskID string) (bool, error) {
	entries, err := os.ReadDir(attemptsDir(root, taskID))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		return true, nil
	}
	return false, nil
}

// neverInvokedHeldTerminal is a held card that reached terminal control without
// ever being launched. The positive facts are control terminal (not revoking),
// scheduling explicitly disabled, no Goal, no session, no active attempt, no
// attempt file, and no workspace lease. A never-started integration placeholder
// and a writer terminalized before any attempt are this shape. PID absence on
// an invoked, live, unknown, or revoking card is not.
func neverInvokedHeldTerminal(root string, t *Task) bool {
	if t == nil || t.Status != statusHeld {
		return false
	}
	if t.ControlState != controlTerminal || t.effectiveControlState() == controlRevoking {
		return false
	}
	if t.SchedulingEligible == nil || *t.SchedulingEligible {
		return false
	}
	if strings.TrimSpace(t.ActiveAttemptID) != "" || t.Goal != nil || strings.TrimSpace(t.SessionID) != "" {
		return false
	}
	hasAttempt, err := taskHasAttemptRecord(root, t.ID)
	if err != nil || hasAttempt {
		return false
	}
	if workspaceLeaseHeld(t.Dir) {
		return false
	}
	return true
}

// neverStartedIntegrationPlaceholder is the integration-card form of
// neverInvokedHeldTerminal. Callers that need the integration identity keep
// this predicate; custody release uses the shared never-invoked facts.
func neverStartedIntegrationPlaceholder(root string, t *Task) bool {
	return t != nil && t.IntegrationGate != nil && neverInvokedHeldTerminal(root, t)
}

func workflowCardHasUnresolvedCustody(root string, t *Task) bool {
	if t == nil {
		return true
	}
	if goalCustodyReleased(root, t) && recordedAttemptCloseout(root, t) {
		return false
	}
	// A real revoke stays unresolved. A held card that was never invoked does
	// not. Every other held card still needs a recorded closeout.
	if t.effectiveControlState() == controlRevoking || t.ControlState == controlRevoking {
		return true
	}
	if neverInvokedHeldTerminal(root, t) {
		return false
	}
	if t.Status == statusHeld {
		return true
	}
	id := goalAttemptID(t)
	started := t.Goal != nil && (t.Goal.Started || strings.TrimSpace(t.Goal.BoundAttemptID) != "")
	if id == "" && !started {
		return false
	}
	return !goalCustodyReleased(root, t)
}
