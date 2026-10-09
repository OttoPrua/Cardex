package main

import (
	"flag"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

const (
	sessionRoleAuthor   = "author"
	sessionRoleReviewer = "reviewer"
	// acceptanceInputStandard versions the overall-acceptance prompt contract.
	// Changing it (or the required keys) invalidates coverage of older cards
	// without rewriting those cards' prompt or evidence bytes.
	acceptanceInputStandard = "overall-v1-goal-criteria-design-artifacts"
)

// NativeGoalDirectionSpec carries one direction of an already-admitted goal.
// Empty fields reuse the workflow worktree and write domain.
type NativeGoalDirectionSpec struct {
	Name      string
	Dir       string
	Domain    *WriteDomain
	DependsOn []string
}

func cmdWorkflowFanout(args []string) error {
	fs := flag.NewFlagSet("workflow fanout", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	design := fs.String("design", "", "已完成的设计卡 ID")
	directions := fs.String("directions", "", "逗号分隔的独立执行方向")
	dir := fs.String("dir", "", "这些方向的工作树；默认沿用 workflow worktree")
	domainID := fs.String("write-domain-id", "", "这些方向写域；省略则沿用 workflow 写域")
	lineage := fs.String("write-domain-lineage", "", "这些方向写域 lineage")
	component := fs.String("write-domain-component", "", "这些方向写域 component")
	writePaths := fs.String("write-paths", "", "这些方向路径，逗号分隔")
	writeResources := fs.String("write-resources", "", "这些方向 closed resources")
	depends := fs.String("depends-on", "", "可选额外前驱，逗号分隔")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow fanout <id> -design <task> -directions a,b [-dir ...] [-write-paths ...]")
	if err != nil {
		return err
	}
	var domain *WriteDomain
	if strings.TrimSpace(*writePaths) != "" || strings.TrimSpace(*domainID) != "" {
		if strings.TrimSpace(*writePaths) == "" || strings.TrimSpace(*domainID) == "" {
			return fmt.Errorf("explicit direction claims need both -write-domain-id and -write-paths")
		}
		d := WriteDomain{
			ID:        strings.TrimSpace(*domainID),
			Lineage:   firstNonBlank(*lineage, *domainID+"-lineage"),
			Component: firstNonBlank(*component, *domainID),
			Paths:     splitComma(*writePaths),
		}
		if d.Resources, err = parseResourceClaims(*writeResources); err != nil {
			return err
		}
		domain = &d
	}
	specs := stagedDirectionSpecs(splitComma(*directions))
	extra := splitComma(*depends)
	for i := range specs {
		specs[i].Dir = strings.TrimSpace(*dir)
		specs[i].Domain = domain
		specs[i].DependsOn = extra
	}
	_, err = scheduleStagedDirectionSpecs(root, cfg, wf, *design, specs)
	return err
}

func cmdWorkflowAccept(args []string) error {
	fs := flag.NewFlagSet("workflow accept", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	complete := fs.Bool("complete", false, "验收卡已经完成时才把目标标为已验收")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow accept <id> [-complete]")
	if err != nil {
		return err
	}
	if _, err := createOverallAcceptance(root, cfg, wf); err != nil {
		return err
	}
	if *complete {
		return noteGoalAccepted(root, cfg, wf)
	}
	return nil
}

func cmdWorkflowGoalRound(args []string) error {
	fs := flag.NewFlagSet("workflow goal-round", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow goal-round <id>")
	if err != nil {
		return err
	}
	_, err = scheduleGoalRound(root, cfg, wf)
	return err
}

func cmdWorkflowGoalDirection(args []string) error {
	fs := flag.NewFlagSet("workflow goal-direction", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	direction := fs.String("direction", "", "独立方向名")
	dir := fs.String("dir", "", "该方向工作树；默认沿用 workflow worktree")
	domainID := fs.String("write-domain-id", "", "该方向写域；省略则沿用 workflow 写域")
	lineage := fs.String("write-domain-lineage", "", "该方向写域 lineage")
	component := fs.String("write-domain-component", "", "该方向写域 component")
	writePaths := fs.String("write-paths", "", "该方向路径，逗号分隔")
	writeResources := fs.String("write-resources", "", "该方向 closed resources")
	depends := fs.String("depends-on", "", "可选前驱任务，逗号分隔")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, cfg, wf, err := workflowTarget(fs, rootFlag, "cardex workflow goal-direction <id> -direction <name> [-dir ...] [-write-paths ...]")
	if err != nil {
		return err
	}
	spec := NativeGoalDirectionSpec{Name: *direction, Dir: strings.TrimSpace(*dir), DependsOn: splitComma(*depends)}
	if strings.TrimSpace(*writePaths) != "" || strings.TrimSpace(*domainID) != "" {
		if strings.TrimSpace(*writePaths) == "" || strings.TrimSpace(*domainID) == "" {
			return fmt.Errorf("explicit direction claims need both -write-domain-id and -write-paths")
		}
		domain := WriteDomain{
			ID:        strings.TrimSpace(*domainID),
			Lineage:   firstNonBlank(*lineage, *domainID+"-lineage"),
			Component: firstNonBlank(*component, *domainID),
			Paths:     splitComma(*writePaths),
		}
		if domain.Resources, err = parseResourceClaims(*writeResources); err != nil {
			return err
		}
		spec.Domain = &domain
	}
	_, err = admitNativeGoalDirection(root, cfg, wf, spec)
	return err
}

func cmdWorkflowGoalLaunch(args []string) error {
	fs := flag.NewFlagSet("workflow goal-launch", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	taskID := fs.String("task", "", "原生 Goal 卡 ID")
	hosted := fs.Bool("hosted", false, "Cardex 自有 PTY，注入 /goal（默认）")
	manual := fs.Bool("manual", false, "交互式 TUI，由操作者输入 /goal")
	budget := fs.Int64("budget", 0, "软 token 预算，写入 /goal --budget（不是 grok 顶层 flag）")
	sandbox := fs.String("sandbox", "", "existing applicable Grok --sandbox profile for this invocation only")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*taskID) == "" {
		return fmt.Errorf("用法: cardex workflow goal-launch -root <root> -task <id> [-hosted|-manual] [-budget N] [-sandbox NAME]")
	}
	if *hosted && *manual {
		return fmt.Errorf("goal-launch 不能同时 -hosted 与 -manual")
	}
	if *budget < 0 {
		return fmt.Errorf("goal-launch -budget must be >= 0")
	}
	root := resolveRoot(*rootFlag)
	cfg, err := loadConfig(root)
	if err != nil {
		return err
	}
	return launchNativeGoalDirection(root, cfg, *taskID, !*manual, *budget, strings.TrimSpace(*sandbox))
}

func stagedDirectionSpecs(names []string) []NativeGoalDirectionSpec {
	out := make([]NativeGoalDirectionSpec, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out = append(out, NativeGoalDirectionSpec{Name: name})
	}
	return out
}

func currentRoundStagedCard(task *Task, wf *WorkflowRecord, name string) bool {
	if task == nil || wf == nil || task.WorkflowID != wf.ID || task.IntegrationGate != nil {
		return false
	}
	return task.SessionStage == name && task.FixRound == wf.CurrentRound
}

func completedDesignResultPointer(root string, wf *WorkflowRecord, design *Task) string {
	if design != nil && wf != nil && wf.DesignLineage != nil && wf.DesignLineage.LatestValid != nil {
		node := wf.DesignLineage.LatestValid
		if strings.TrimSpace(node.DesignTaskID) != "" && node.DesignTaskID == design.ID {
			if p := firstNonBlank(node.ResultPath, node.ReceiptPath); p != "" {
				return p
			}
		}
	}
	if design != nil && strings.TrimSpace(design.ID) != "" {
		return taskLogPath(root, design.ID)
	}
	return ""
}

func stagedExecutorPrompt(root string, wf *WorkflowRecord, design *Task, spec NativeGoalDirectionSpec) string {
	designID := ""
	if design != nil {
		designID = design.ID
	}
	return "goal: " + strings.TrimSpace(wf.Goal) +
		"\nacceptance: " + strings.TrimSpace(wf.TerminalCriteria) +
		"\ndesign_id: " + designID +
		"\ndesign_result: " + completedDesignResultPointer(root, wf, design) +
		"\nscope: " + strings.TrimSpace(spec.Name) + "\n"
}

func scheduleStagedDirectionSpecs(root string, cfg *Config, wf *WorkflowRecord, designID string, specs []NativeGoalDirectionSpec) ([]*Task, error) {
	var created []*Task
	err := withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		if err := refreshWorkflow(root, cfg, wf); err != nil {
			return err
		}
		design, err := loadTask(root, strings.TrimSpace(designID))
		if err != nil {
			return fmt.Errorf("设计卡不可读，未创建执行卡：%w", err)
		}
		if design.Status != statusDone {
			return fmt.Errorf("设计卡 %s 尚未完成（status=%s），未创建执行卡", design.ID, design.Status)
		}
		wf.DesignTaskID = design.ID
		existing, err := loadTasks(root)
		if err != nil {
			return err
		}
		current := make([]string, 0, len(wf.DirectionTaskIDs))
		seen := map[string]bool{}
		for _, id := range wf.DirectionTaskIDs {
			t, loadErr := loadTask(root, id)
			if loadErr != nil || t == nil || t.FixRound != wf.CurrentRound {
				continue
			}
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			current = append(current, t.ID)
		}
		for _, spec := range specs {
			name := strings.TrimSpace(spec.Name)
			if name == "" {
				continue
			}
			var found *Task
			for _, task := range existing {
				if currentRoundStagedCard(task, wf, name) {
					found = task
					break
				}
			}
			if found != nil {
				created = append(created, found)
				if !seen[found.ID] {
					current = append(current, found.ID)
					seen[found.ID] = true
				}
				continue
			}
			clearStaleAcceptance(wf)
			dir := strings.TrimSpace(spec.Dir)
			if dir == "" {
				dir = wf.Worktree
			}
			domain := spec.Domain
			if domain == nil {
				domain = copyWriteDomain(wf.WriteDomain)
			} else {
				norm, nerr := NormalizeWriteDomain(dir, *domain)
				if nerr != nil {
					return nerr
				}
				domain = copyWriteDomain(norm)
			}
			deps := []string{design.ID}
			for _, id := range spec.DependsOn {
				id = strings.TrimSpace(id)
				if id == "" || id == design.ID {
					continue
				}
				deps = append(deps, id)
			}
			task := newTask(root, cfg, typeSequence, "staged direction "+name, dir, []string{stagedExecutorPrompt(root, wf, design, spec)}, 5)
			task.Project = wf.ModuleID
			task.WorkflowID = wf.ID
			task.DependsOn = deps
			task.WriteDomain = domain
			task.PreferRunner = wf.WriterEngine
			task.RunnerExplicit = true
			task.ReviewAfter = false
			task.SessionStage = name
			task.SessionRole = sessionRoleAuthor
			task.FixRound = wf.CurrentRound
			if err := saveTask(root, task); err != nil {
				return err
			}
			created = append(created, task)
			current = append(current, task.ID)
			seen[task.ID] = true
			existing = append(existing, task)
		}
		wf.DirectionTaskIDs = current
		if err := persistWorkflow(root, cfg, wf); err != nil {
			return err
		}
		fmt.Printf("staged fanout design=%s directions=%d acceptance_created=0\n", design.ID, len(created))
		return nil
	})
	return created, err
}

func acceptanceMemberIDs(wf *WorkflowRecord) []string {
	if wf == nil {
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
	for _, id := range wf.DirectionTaskIDs {
		add(id)
	}
	for _, id := range wf.NativeGoalTaskIDs {
		add(id)
	}
	sort.Strings(ids)
	return ids
}

func clearStaleAcceptance(wf *WorkflowRecord) {
	if wf == nil {
		return
	}
	wf.AcceptanceTaskID = ""
	wf.AcceptanceCoverage = ""
	wf.GoalCompleted = false
}

func readLocalSourceCandidate(worktree string) *WorkflowCandidate {
	cand := &WorkflowCandidate{Branch: "local-source"}
	dir := strings.TrimSpace(worktree)
	if dir == "" {
		return cand
	}
	commitOut, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return cand
	}
	treeOut, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD^{tree}").Output()
	if err != nil {
		return cand
	}
	commit := strings.TrimSpace(string(commitOut))
	tree := strings.TrimSpace(string(treeOut))
	if commit == "" || tree == "" {
		return cand
	}
	return &WorkflowCandidate{Commit: commit, Tree: tree, Branch: "HEAD"}
}

func acceptanceSourceDirs(wf *WorkflowRecord, members []*Task) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	if wf != nil {
		add(wf.Worktree)
	}
	for _, task := range members {
		if task != nil {
			add(task.Dir)
		}
	}
	sort.Strings(dirs)
	return dirs
}

func acceptanceDesignFacts(root string, wf *WorkflowRecord) (pointer, digest string) {
	var design *Task
	if wf != nil && strings.TrimSpace(wf.DesignTaskID) != "" {
		design, _ = loadTask(root, wf.DesignTaskID)
	}
	pointer = completedDesignResultPointer(root, wf, design)
	if wf != nil && wf.DesignLineage != nil && wf.DesignLineage.LatestValid != nil {
		digest = strings.TrimSpace(wf.DesignLineage.LatestValid.Digest)
	}
	return pointer, digest
}

func acceptancePromptMeetsInputStandard(prompt string) bool {
	return strings.Contains(prompt, "overall_goal:") &&
		strings.Contains(prompt, "completion_criteria:") &&
		strings.Contains(prompt, "design_result:") &&
		strings.Contains(prompt, "design_digest:") &&
		strings.Contains(prompt, "member id=") &&
		strings.Contains(prompt, "artifact=")
}

func acceptanceFingerprint(root string, wf *WorkflowRecord) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "input_standard=%s\n", acceptanceInputStandard)
	if wf != nil {
		fmt.Fprintf(&b, "goal=%s\n", strings.TrimSpace(wf.Goal))
		fmt.Fprintf(&b, "terminal=%s\n", strings.TrimSpace(wf.TerminalCriteria))
		pointer, digest := acceptanceDesignFacts(root, wf)
		fmt.Fprintf(&b, "design_result=%s\n", pointer)
		fmt.Fprintf(&b, "design_digest=%s\n", digest)
	}
	if wf != nil && wf.Candidate != nil {
		fmt.Fprintf(&b, "commit=%s\ntree=%s\nbranch=%s\n", wf.Candidate.Commit, wf.Candidate.Tree, wf.Candidate.Branch)
		paths := append([]string(nil), wf.Candidate.ChangedPaths...)
		sort.Strings(paths)
		fmt.Fprintf(&b, "paths=%s\n", strings.Join(paths, ","))
	}
	members := make([]*Task, 0, len(acceptanceMemberIDs(wf)))
	for _, id := range acceptanceMemberIDs(wf) {
		task, err := loadTask(root, id)
		if err != nil || task == nil {
			return "", fmt.Errorf("方向 %s 状态未知，未创建验收卡，也未把目标标为已验收", id)
		}
		goal := ""
		if task.Goal != nil {
			goal = task.Goal.NativeGoalID
		}
		fmt.Fprintf(&b, "member=%s session=%s goal=%s\n", task.ID, task.SessionID, goal)
		members = append(members, task)
	}
	for _, dir := range acceptanceSourceDirs(wf, members) {
		fp, err := capturePolicyWorkspaceFingerprint(dir)
		if err != nil || fp == nil || strings.TrimSpace(fp.Digest) == "" {
			return "", fmt.Errorf("当前源码指纹不可读（%s）：%v", dir, err)
		}
		fmt.Fprintf(&b, "source=%s digest=%s\n", fp.Root, fp.Digest)
	}
	return sha256Hex(b.String()), nil
}

func acceptanceTaskCovers(task *Task, wf *WorkflowRecord, fingerprint string) bool {
	if task == nil || wf == nil || fingerprint == "" || wf.AcceptanceCoverage != fingerprint {
		return false
	}
	if wf.Candidate == nil {
		return false
	}
	if task.ReviewCandidate == nil || task.ReviewCandidate.Commit != wf.Candidate.Commit || task.ReviewCandidate.Tree != wf.Candidate.Tree || task.ReviewCandidate.Branch != wf.Candidate.Branch {
		return false
	}
	if !sameStringSet(task.DependsOn, acceptanceMemberIDs(wf)) {
		return false
	}
	return acceptancePromptMeetsInputStandard(strings.Join(task.Prompts, "\n"))
}

func memberReadyForAcceptance(root string, task *Task) error {
	if task == nil {
		return fmt.Errorf("方向状态未知，未创建验收卡，也未把目标标为已验收")
	}
	if task.Status != statusDone {
		return fmt.Errorf("方向 %s 状态为 %s（部分完成、暂停、失败或未知），未创建验收卡，也未把目标标为已验收", task.ID, task.Status)
	}
	if task.Goal == nil || task.Goal.WriterMode != goalWriterManual {
		return nil
	}
	if strings.TrimSpace(task.Goal.NativeGoalID) == "" {
		return fmt.Errorf("原生 Goal %s 缺少 native goal_id，未创建验收卡，也未把目标标为已验收", task.ID)
	}
	if strings.TrimSpace(task.SessionID) == "" {
		return fmt.Errorf("原生 Goal %s 会话身份缺失，未创建验收卡，也未把目标标为已验收", task.ID)
	}
	if task.Goal.Observation != goalObsDone || !task.Goal.EvidenceComplete || !task.Goal.CustodyReleased {
		return fmt.Errorf("原生 Goal %s 终态未核实（observation=%s），未创建验收卡，也未把目标标为已验收", task.ID, task.Goal.Observation)
	}
	if !goalCustodyReleased(root, task) {
		return fmt.Errorf("原生 Goal %s 归属未释放，未创建验收卡，也未把目标标为已验收", task.ID)
	}
	return nil
}

func nativeSessionsDistinct(members []*Task) error {
	seen := map[string]string{}
	for _, task := range members {
		if task == nil || task.Goal == nil || strings.TrimSpace(task.Goal.NativeGoalID) == "" {
			continue
		}
		sid := strings.TrimSpace(task.SessionID)
		if prev, ok := seen[sid]; ok {
			return fmt.Errorf("原生 Goal %s 与 %s 共用会话 %s，未创建验收卡，也未把目标标为已验收", task.ID, prev, sid)
		}
		seen[sid] = task.ID
	}
	return nil
}

func acceptancePrompt(root string, wf *WorkflowRecord, members []*Task, fingerprint string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "overall acceptance workflow=%s module=%s\n", wf.ID, wf.ModuleID)
	fmt.Fprintf(&b, "overall_goal: %s\n", strings.TrimSpace(wf.Goal))
	fmt.Fprintf(&b, "completion_criteria: %s\n", strings.TrimSpace(wf.TerminalCriteria))
	pointer, digest := acceptanceDesignFacts(root, wf)
	fmt.Fprintf(&b, "design_result: %s\n", pointer)
	fmt.Fprintf(&b, "design_digest: %s\n", digest)
	if wf.Candidate != nil {
		fmt.Fprintf(&b, "candidate commit=%s tree=%s\n", wf.Candidate.Commit, wf.Candidate.Tree)
		if len(wf.Candidate.ChangedPaths) > 0 {
			fmt.Fprintf(&b, "candidate_paths=%s\n", strings.Join(wf.Candidate.ChangedPaths, ","))
		}
	}
	fmt.Fprintf(&b, "coverage=%s\n", fingerprint)
	fmt.Fprintf(&b, "input_standard=%s\n", acceptanceInputStandard)
	for _, task := range members {
		goal := ""
		if task.Goal != nil {
			goal = task.Goal.NativeGoalID
		}
		fmt.Fprintf(&b, "member id=%s session=%s goal=%s status=%s artifact=%s\n",
			task.ID, task.SessionID, goal, task.Status, taskLogPath(root, task.ID))
	}
	b.WriteString("结论只能是 pass、concerns 或 block。pass 仅当 p0 与 p1 皆空。block 或 concerns 不得把目标标为已验收。\n")
	return b.String()
}

func acceptanceAdmittedLocked(root string, wf *WorkflowRecord) (bool, string) {
	if wf == nil || wf.AcceptanceTaskID == "" {
		return false, "还没有验收卡"
	}
	fp, err := acceptanceFingerprint(root, wf)
	if err != nil {
		return false, err.Error()
	}
	if wf.AcceptanceCoverage == "" || wf.AcceptanceCoverage != fp {
		return false, "coverage_changed"
	}
	acc, err := loadTask(root, wf.AcceptanceTaskID)
	if err != nil || acc == nil {
		return false, "验收卡状态未知"
	}
	if !acceptanceTaskCovers(acc, wf, fp) {
		return false, "coverage_changed"
	}
	for _, id := range acceptanceMemberIDs(wf) {
		task, loadErr := loadTask(root, id)
		if loadErr != nil || task == nil {
			return false, "方向未全部完成"
		}
		if readyErr := memberReadyForAcceptance(root, task); readyErr != nil {
			return false, "方向未全部完成"
		}
	}
	if acc.Status != statusDone {
		return false, fmt.Sprintf("验收卡 %s 尚未通过（status=%s）", acc.ID, acc.Status)
	}
	raw := loadTaskResultForGate(root, acc)
	v := parseReviewVerdict(raw)
	if !reviewVerdictIsAdmissiblePass(v) {
		reason := reviewHoldReason(v, raw)
		if reason == "" {
			reason = holdReasonUnknownVocabulary
		}
		return false, reason
	}
	var snap *WorkflowReview
	if acc.ReviewOutput != nil {
		snap = &WorkflowReview{CandidateCommit: acc.ReviewOutput.CandidateCommit, CandidateTree: acc.ReviewOutput.CandidateTree}
	}
	gate := &IntegrationGate{CandidateCommit: wf.Candidate.Commit, CandidateTree: wf.Candidate.Tree}
	if !candidateIdentitiesMatch(gate, snap, wf.Candidate) {
		return false, holdReasonCandidateMismatch
	}
	return true, ""
}

func createOverallAcceptance(root string, cfg *Config, wf *WorkflowRecord) (*Task, error) {
	var out *Task
	err := withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		task, err := createOverallAcceptanceLocked(root, cfg, wf)
		out = task
		return err
	})
	return out, err
}

func createOverallAcceptanceLocked(root string, cfg *Config, wf *WorkflowRecord) (*Task, error) {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return nil, err
	}
	memberIDs := acceptanceMemberIDs(wf)
	if len(memberIDs) == 0 {
		return nil, fmt.Errorf("没有执行方向，未创建验收卡，也未把目标标为已验收")
	}
	members := make([]*Task, 0, len(memberIDs))
	for _, id := range memberIDs {
		task, err := loadTask(root, id)
		if err != nil || task == nil {
			return nil, fmt.Errorf("方向 %s 状态未知，未创建验收卡，也未把目标标为已验收", id)
		}
		if err := memberReadyForAcceptance(root, task); err != nil {
			return nil, err
		}
		members = append(members, task)
	}
	if err := nativeSessionsDistinct(members); err != nil {
		return nil, err
	}
	if wf.Candidate == nil || (strings.TrimSpace(wf.Candidate.Commit) == "" && strings.TrimSpace(wf.Candidate.Tree) == "" && strings.TrimSpace(wf.Candidate.Branch) == "") {
		wf.Candidate = readLocalSourceCandidate(wf.Worktree)
	}
	fingerprint, err := acceptanceFingerprint(root, wf)
	if err != nil {
		return nil, err
	}
	if wf.AcceptanceTaskID != "" {
		existing, err := loadTask(root, wf.AcceptanceTaskID)
		if err != nil {
			return nil, fmt.Errorf("验收卡 %s 不可读，未再创建一张", wf.AcceptanceTaskID)
		}
		if acceptanceTaskCovers(existing, wf, fingerprint) {
			fmt.Printf("acceptance_created=0 existing=%s goal_accepted=%t\n", existing.ID, wf.GoalCompleted)
			return existing, nil
		}
		clearStaleAcceptance(wf)
	}
	prompt := acceptancePrompt(root, wf, members, fingerprint)
	task := newTask(root, cfg, typeReview, "overall acceptance "+wf.ModuleID, wf.Worktree, []string{prompt}, 6)
	task.Project = wf.ModuleID
	task.WorkflowID = wf.ID
	task.DependsOn = append([]string(nil), memberIDs...)
	task.PreferRunner = wf.ReviewerEngine
	task.RunnerExplicit = true
	task.SessionRole = sessionRoleReviewer
	task.FreshSteps = true
	task.SessionID = ""
	task.ReviewAfter = false
	task.ReviewCandidate = &WorkflowCandidate{
		Commit:       wf.Candidate.Commit,
		Tree:         wf.Candidate.Tree,
		Branch:       wf.Candidate.Branch,
		ChangedPaths: append([]string(nil), wf.Candidate.ChangedPaths...),
	}
	if err := saveTask(root, task); err != nil {
		return nil, err
	}
	wf.AcceptanceTaskID = task.ID
	wf.AcceptanceCoverage = fingerprint
	wf.GoalCompleted = false
	if err := persistWorkflow(root, cfg, wf); err != nil {
		return task, err
	}
	fmt.Printf("acceptance_created=1 id=%s goal_accepted=false\n", task.ID)
	return task, nil
}

func noteGoalAccepted(root string, cfg *Config, wf *WorkflowRecord) error {
	return withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		if err := refreshWorkflow(root, cfg, wf); err != nil {
			return err
		}
		ok, reason := acceptanceAdmittedLocked(root, wf)
		if !ok {
			wf.GoalCompleted = false
			if reason == "coverage_changed" {
				clearStaleAcceptance(wf)
			}
			if err := persistWorkflow(root, cfg, wf); err != nil {
				return err
			}
			if reason == "coverage_changed" {
				return fmt.Errorf("验收覆盖已变化，未沿用旧验收，也未把目标标为已验收")
			}
			fmt.Printf("goal_accepted=false acceptance=%s reason=%s\n", wf.AcceptanceTaskID, reason)
			return fmt.Errorf("验收未通过（%s），未把目标标为已验收", reason)
		}
		wf.GoalCompleted = true
		if err := persistWorkflow(root, cfg, wf); err != nil {
			return err
		}
		fmt.Printf("goal_accepted=true acceptance=%s\n", wf.AcceptanceTaskID)
		return nil
	})
}

func scheduleGoalRound(root string, cfg *Config, wf *WorkflowRecord) (*Task, error) {
	var out *Task
	err := withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		if err := refreshWorkflow(root, cfg, wf); err != nil {
			return err
		}
		if wf.GoalCompleted {
			return fmt.Errorf("目标已完成，不再开启下一轮")
		}
		if ok, _ := acceptanceAdmittedLocked(root, wf); ok {
			wf.GoalCompleted = true
			_ = persistWorkflow(root, cfg, wf)
			return fmt.Errorf("目标已完成，不再开启下一轮")
		}
		if wf.CurrentRound+1 > wf.MaxRounds {
			return fmt.Errorf("已达轮次上限 %d，不再开启下一轮", wf.MaxRounds)
		}
		wf.CurrentRound++
		task, err := publishWorkflowWriterLocked(root, cfg, wf, "", nil, true)
		if err != nil {
			wf.CurrentRound--
			return err
		}
		task.FixRound = wf.CurrentRound
		task.SessionStage = fmt.Sprintf("round-%d", wf.CurrentRound)
		if err := saveTask(root, task); err != nil {
			return err
		}
		out = task
		fmt.Printf("goal_round=%d task=%s retained_previous=true\n", wf.CurrentRound, task.ID)
		return nil
	})
	return out, err
}

func admitNativeGoalDirection(root string, cfg *Config, wf *WorkflowRecord, spec NativeGoalDirectionSpec) (*Task, error) {
	direction := strings.TrimSpace(spec.Name)
	if direction == "" {
		return nil, fmt.Errorf("原生 Goal 方向不能为空")
	}
	var out *Task
	err := withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		if err := refreshWorkflow(root, cfg, wf); err != nil {
			return err
		}
		cap := nativeGoalCapability(firstNonBlank(wf.WriterEngine, grokBuildRunnerName))
		if cap.Rejected || cap.Level == goalCapUnsupported {
			return fmt.Errorf("%w: %s is %s (%s)", errGoalCapability, cap.Runner, cap.Level, cap.Reason)
		}
		if wf.WriterEngine != grokBuildRunnerName {
			return fmt.Errorf("%w: %s is %s (%s); native Goal directions use managed Grok /goal",
				errGoalCapability, cap.Runner, cap.Level, cap.Reason)
		}
		dir := strings.TrimSpace(spec.Dir)
		if dir == "" {
			dir = wf.Worktree
		}
		domain := spec.Domain
		if domain == nil {
			domain = copyWriteDomain(wf.WriteDomain)
		} else {
			norm, nerr := NormalizeWriteDomain(dir, *domain)
			if nerr != nil {
				return nerr
			}
			domain = copyWriteDomain(norm)
		}
		tasks, err := loadTasks(root)
		if err != nil {
			return fmt.Errorf("%w: 任务列表不可读，未知状态按失败关闭", errAdmissionDenied)
		}
		for _, existing := range tasks {
			if existing != nil && existing.WorkflowID == wf.ID && existing.Goal != nil && existing.Goal.Scope == direction && existing.Goal.WriterMode == goalWriterManual {
				fmt.Printf("native_goal existing=%s session=%s goal=%s\n", existing.ID, existing.SessionID, orDash(existing.Goal.NativeGoalID))
				out = existing
				return nil
			}
		}
		task := newTask(root, cfg, typeSequence, "native goal "+direction, dir, []string{"native goal direction " + direction}, 5)
		task.Project = wf.ModuleID
		task.WorkflowID = wf.ID
		task.PreferRunner = grokBuildRunnerName
		task.RunnerExplicit = true
		task.ReviewAfter = false
		task.WriteDomain = domain
		task.DependsOn = append([]string(nil), spec.DependsOn...)
		task.Goal = &TaskGoalBinding{
			Outcome:        wf.Goal,
			Scope:          direction,
			Acceptance:     wf.TerminalCriteria,
			Provider:       grokBuildRunnerName,
			WriterMode:     goalWriterManual,
			Observation:    goalObsUnstarted,
			GrokHome:       "",
			Stop:           goalStopSoftBudget,
			ReturnToDesign: true,
		}
		if err := saveTask(root, task); err != nil {
			return err
		}
		clearStaleAcceptance(wf)
		wf.NativeGoalTaskIDs = append(wf.NativeGoalTaskIDs, task.ID)
		if err := persistWorkflow(root, cfg, wf); err != nil {
			return err
		}
		fmt.Printf("native_goal id=%s session=%s goal=%s\n", task.ID, task.SessionID, orDash(task.Goal.NativeGoalID))
		out = task
		return nil
	})
	return out, err
}

func nativeGoalBusy(root string, cfg *Config, candidate *Task) (bool, string) {
	tasks, err := loadTasks(root)
	if err != nil {
		return true, "任务列表不可读，未知状态按失败关闭"
	}
	live := mergeLiveWriterTasks(nil, reconstructLiveWriterClaims(root))
	if writerConflictsWithActive(candidate, live) {
		return true, "与活跃写者冲突，未启动第二个原生 Goal 会话"
	}
	running := 0
	for _, other := range tasks {
		if other == nil || candidate == nil || other.ID == candidate.ID || other.Goal == nil {
			continue
		}
		if strings.TrimSpace(other.SessionID) != "" && other.SessionID == candidate.SessionID && other.Status != statusDone && other.Status != statusCanceled && other.Status != statusFailed {
			return true, "同一会话已被 " + other.ID + " 认领，未成为第二个写者"
		}
		if other.Status == statusRunning {
			running++
		}
		if other.ActiveAttemptID != "" && other.Status != statusDone && other.Status != statusCanceled && other.Status != statusFailed && !goalCustodyReleased(root, other) {
			return true, "前次归属未知，未启动另一个原生 Goal 会话"
		}
	}
	maxPar := 1
	if cfg != nil && cfg.MaxParallel > 0 {
		maxPar = cfg.MaxParallel
	}
	if running >= maxPar {
		return true, fmt.Sprintf("已有 %d 个运行中的原生 Goal 写者，达到 max_parallel %d，未再启动", running, maxPar)
	}
	return false, ""
}

func launchNativeGoalDirection(root string, cfg *Config, taskID string, hosted bool, budget int64, sandboxProfile string) error {
	task, err := loadTask(root, taskID)
	if err != nil {
		return err
	}
	if task.Goal == nil || task.Goal.WriterMode != goalWriterManual {
		return fmt.Errorf("这张卡不是原生 Goal；普通 -p 不是原生 Goal")
	}
	if !grokBuildEnabled(cfg) {
		return fmt.Errorf("%w: grok_build 未启用，未把 claude -p 当作原生 Goal", errGoalCapability)
	}
	if busy, why := nativeGoalBusy(root, cfg, task); busy {
		return fmt.Errorf("%w: %s", errAdmissionDenied, why)
	}
	if _, _, err := resolveManualGrokTuple(cfg, task); err != nil {
		return err
	}
	if strings.TrimSpace(task.WorkflowID) == "" {
		return fmt.Errorf("%w: native Goal direction is missing its workflow", errWorkflowMalformed)
	}
	wf, err := loadWorkflow(root, cfg, task.WorkflowID)
	if err != nil {
		return err
	}
	if err := launchWorkflowGoalTask(root, cfg, wf, task, budget, hosted, sandboxProfile); err != nil {
		return err
	}
	fresh, err := loadTask(root, taskID)
	if err != nil {
		return err
	}
	goalID, obs := "", ""
	if fresh.Goal != nil {
		goalID, obs = fresh.Goal.NativeGoalID, fresh.Goal.Observation
	}
	fmt.Printf("native_goal_launched id=%s session=%s goal=%s observation=%s\n",
		fresh.ID, fresh.SessionID, orDash(goalID), orDash(obs))
	return nil
}
