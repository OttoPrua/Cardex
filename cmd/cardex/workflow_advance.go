package main

import (
	"fmt"
	"strings"
)

type workflowAdvanceOpts struct {
	OptIn           bool
	MaxActions      int
	RepairDirection string
}

type workflowAdvanceResult struct {
	ActionsUsed       int
	AcceptanceCreated int
	RepairCreated     int
	RepairTaskID      string
	StopReason        string
	Held              bool
	GoalAccepted      bool
}

func advanceOptInWorkflow(root string, cfg *Config, wf *WorkflowRecord, opts workflowAdvanceOpts) (*workflowAdvanceResult, error) {
	res := &workflowAdvanceResult{}
	if !opts.OptIn {
		res.StopReason = "no-opt-in"
		res.Held = true
		return res, fmt.Errorf("workflow advance requires explicit -opt-in; tick stays non-advancing and max-rounds is not opt-in")
	}
	if opts.MaxActions < 1 {
		res.StopReason = "missing-ceiling"
		res.Held = true
		return res, fmt.Errorf("workflow advance requires a positive finite -max-actions ceiling")
	}
	repairName := strings.TrimSpace(opts.RepairDirection)
	if repairName != "" && (strings.ContainsAny(repairName, ",;|") || strings.Contains(repairName, " ") && strings.Contains(repairName, ",")) {
		res.StopReason = "repair-refused"
		res.Held = true
		return res, fmt.Errorf("known-direction repair will not guess a wider scope")
	}
	for res.ActionsUsed < opts.MaxActions {
		if err := refreshWorkflow(root, cfg, wf); err != nil {
			return res, err
		}
		if ok, _ := acceptanceAdmittedLocked(root, wf); ok {
			if err := noteGoalAccepted(root, cfg, wf); err != nil {
				return res, err
			}
			res.GoalAccepted = true
			res.StopReason = "accepted"
			return res, nil
		}
		if wf.GoalCompleted {
			if nerr := noteGoalAccepted(root, cfg, wf); nerr == nil {
				res.GoalAccepted = true
				res.StopReason = "accepted"
				return res, nil
			} else if err := refreshWorkflow(root, cfg, wf); err != nil {
				return res, err
			} else if wf.GoalCompleted {
				return res, nerr
			}
		}
		readyErr := workflowMembersReadyForAdvance(root, wf)
		if readyErr != nil {
			if repairName != "" && res.RepairCreated == 0 && namedDirectionIsSoleRepairableFailure(root, wf, repairName) {
				return consumeKnownDirectionRepair(root, cfg, wf, repairName, res, opts)
			}
			res.StopReason = "held"
			res.Held = true
			return res, fmt.Errorf("held: %w", readyErr)
		}
		before := strings.TrimSpace(wf.AcceptanceTaskID)
		acc, err := createOverallAcceptance(root, cfg, wf)
		if err != nil {
			res.StopReason = "held"
			res.Held = true
			return res, err
		}
		if acc != nil && (before == "" || acc.ID != before) {
			res.AcceptanceCreated++
			res.ActionsUsed++
			if res.ActionsUsed >= opts.MaxActions {
				res.StopReason = "acceptance-created"
				return res, nil
			}
		}
		if ok, reason := acceptanceAdmittedLocked(root, wf); ok {
			if err := noteGoalAccepted(root, cfg, wf); err != nil {
				return res, err
			}
			res.GoalAccepted = true
			res.StopReason = "accepted"
			return res, nil
		} else if repairName != "" && res.RepairCreated == 0 && namedDirectionRepairableFromCurrentFindings(root, wf, repairName) {
			return consumeKnownDirectionRepair(root, cfg, wf, repairName, res, opts)
		} else if res.AcceptanceCreated > 0 {
			res.StopReason = "acceptance-created"
			return res, nil
		} else if before != "" {
			res.StopReason = "idempotent-acceptance"
			return res, nil
		} else {
			if reason == "" {
				reason = "acceptance-not-pass"
			}
			res.StopReason = reason
			res.Held = true
			return res, fmt.Errorf("held: %s", reason)
		}
	}
	res.StopReason = "action-ceiling"
	return res, nil
}

func consumeKnownDirectionRepair(root string, cfg *Config, wf *WorkflowRecord, named string, res *workflowAdvanceResult, opts workflowAdvanceOpts) (*workflowAdvanceResult, error) {
	if res.ActionsUsed >= opts.MaxActions {
		res.StopReason = "action-ceiling"
		return res, nil
	}
	repair, err := admitKnownDirectionRepair(root, cfg, wf, named)
	if err != nil {
		res.StopReason = "repair-refused"
		res.Held = true
		return res, err
	}
	res.RepairCreated++
	res.RepairTaskID = repair.ID
	res.ActionsUsed++
	res.StopReason = "repair-created"
	return res, nil
}

func namedDirectionIsSoleRepairableFailure(root string, wf *WorkflowRecord, named string) bool {
	named = strings.TrimSpace(named)
	if named == "" {
		return false
	}
	var found bool
	for _, id := range acceptanceMemberIDs(wf) {
		task, err := loadTask(root, id)
		if err != nil || task == nil {
			return false
		}
		ready := memberReadyForAcceptance(root, task) == nil
		isNamed := task.SessionStage == named || (task.Goal != nil && task.Goal.Scope == named)
		if isNamed {
			found = true
			if ready {
				return false
			}
			if task.Status == statusLimitPaused || task.Status == statusHeld || task.Status == statusRunning {
				return false
			}
			if task.Goal != nil && (task.Goal.Observation == goalObsHeld || task.Goal.Observation == goalObsUnknown || task.Goal.Observation == goalObsRunning) {
				return false
			}
			if task.Status != statusFailed {
				return false
			}
			continue
		}
		if !ready {
			return false
		}
	}
	return found
}

func namedDirectionRepairableFromCurrentFindings(root string, wf *WorkflowRecord, named string) bool {
	named = strings.TrimSpace(named)
	if named == "" || wf == nil || strings.TrimSpace(wf.AcceptanceTaskID) == "" {
		return false
	}
	acc, err := loadTask(root, wf.AcceptanceTaskID)
	if err != nil || acc == nil || acc.Status != statusDone {
		return false
	}
	raw := loadTaskResultForGate(root, acc)
	v := parseReviewVerdict(raw)
	if v == nil || reviewVerdictIsAdmissiblePass(v) {
		return false
	}
	reason := reviewHoldReason(v, raw)
	if reason == "" || reason == holdReasonUnknownVocabulary || reason == holdReasonMissingOutput {
		return false
	}
	if v.Verdict != "concerns" && v.Verdict != "block" && len(v.P0) == 0 && len(v.P1) == 0 {
		return false
	}
	var found bool
	for _, id := range acceptanceMemberIDs(wf) {
		task, loadErr := loadTask(root, id)
		if loadErr != nil || task == nil {
			return false
		}
		isNamed := task.SessionStage == named || (task.Goal != nil && task.Goal.Scope == named)
		if isNamed {
			found = true
			if task.Status == statusLimitPaused || task.Status == statusHeld || task.Status == statusRunning {
				return false
			}
			continue
		}
		if memberReadyForAcceptance(root, task) != nil {
			return false
		}
	}
	return found
}

func workflowMembersReadyForAdvance(root string, wf *WorkflowRecord) error {
	ids := acceptanceMemberIDs(wf)
	if len(ids) == 0 {
		return fmt.Errorf("no necessary directions")
	}
	members := make([]*Task, 0, len(ids))
	for _, id := range ids {
		task, err := loadTask(root, id)
		if err != nil || task == nil {
			return fmt.Errorf("direction %s unknown or missing", id)
		}
		if task.Status == statusLimitPaused || (task.Goal != nil && task.Goal.Observation == goalObsHeld) {
			return fmt.Errorf("direction %s is paused/held", id)
		}
		if task.Goal != nil && (task.Goal.Observation == goalObsUnknown || task.Goal.FailureClass == goalFailControlUnsupported) {
			return fmt.Errorf("direction %s is unknown", id)
		}
		if err := memberReadyForAcceptance(root, task); err != nil {
			return err
		}
		if task.Goal != nil && task.Goal.WriterMode == goalWriterManual {
			if task.Status == statusDone && (!task.Goal.CustodyReleased || !task.Goal.EvidenceComplete) {
				return fmt.Errorf("direction %s done but custody not released", id)
			}
		}
		members = append(members, task)
	}
	return nativeSessionsDistinct(members)
}

func admitKnownDirectionRepair(root string, cfg *Config, wf *WorkflowRecord, direction string) (*Task, error) {
	direction = strings.TrimSpace(direction)
	if direction == "" {
		return nil, fmt.Errorf("known-direction repair requires an explicit direction name")
	}
	if strings.ContainsAny(direction, ",;|") || strings.Contains(direction, " ") && strings.Contains(direction, ",") {
		return nil, fmt.Errorf("known-direction repair will not guess a wider scope")
	}
	var out *Task
	err := withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		if err := refreshWorkflow(root, cfg, wf); err != nil {
			return err
		}
		if wf.GoalCompleted {
			return fmt.Errorf("目标已完成，不再开启修复")
		}
		if strings.TrimSpace(wf.DesignTaskID) == "" {
			return fmt.Errorf("%w: needs-new-design; bind a completed design before repair", errGoalDesignResult)
		}
		design, err := loadTask(root, wf.DesignTaskID)
		if err != nil || design == nil || design.Status != statusDone {
			return fmt.Errorf("%w: needs-new-design; completed design is required", errGoalDesignResult)
		}
		if wf.CurrentRound+1 > wf.MaxRounds {
			wf.Status = workflowStatusExhausted
			if nerr := emitRootNotify(root, wf, rootNotifyExhausted,
				fmt.Sprintf("repair rounds exhausted at %d/%d; candidate stays unaccepted and integration held",
					wf.CurrentRound, wf.MaxRounds)); nerr != nil {
				return nerr
			}
			if perr := persistWorkflow(root, cfg, wf); perr != nil {
				return perr
			}
			return fmt.Errorf("%w: %d/%d", errWorkflowRoundsExceeded, wf.CurrentRound, wf.MaxRounds)
		}
		idx := -1
		var target *Task
		for i, id := range wf.DirectionTaskIDs {
			task, loadErr := loadTask(root, id)
			if loadErr != nil || task == nil {
				continue
			}
			if task.SessionStage == direction {
				idx = i
				target = task
				break
			}
		}
		if target == nil {
			for _, id := range wf.NativeGoalTaskIDs {
				task, loadErr := loadTask(root, id)
				if loadErr != nil || task == nil {
					continue
				}
				if (task.Goal != nil && task.Goal.Scope == direction) || task.SessionStage == direction {
					return fmt.Errorf("%w: Goal repair is refused; use design-result -decision revise with a fresh independent design", errGoalDesignResult)
				}
			}
			return fmt.Errorf("unknown direction %q; will not guess a wider repair scope", direction)
		}
		if target.Goal != nil && target.Goal.WriterMode == goalWriterManual {
			return fmt.Errorf("%w: Goal repair is refused; use design-result -decision revise with a fresh independent design", errGoalDesignResult)
		}
		if target.Goal != nil && (target.Goal.FailureClass == goalFailPlanningUnknown || strings.Contains(strings.ToLower(target.Goal.ObservationNote), "sealed")) {
			return fmt.Errorf("%w: native sealed producer stays held; no auto resume/successor/grant", errGoalDesignResult)
		}
		round := wf.CurrentRound + 1
		dir := target.Dir
		if strings.TrimSpace(dir) == "" {
			dir = wf.Worktree
		}
		prompt := stagedExecutorPrompt(root, wf, design, NativeGoalDirectionSpec{Name: direction})
		if findings := currentRepairFindingsText(root, wf, target); findings != "" {
			prompt += "findings:\n" + findings
		}
		task := newTask(root, cfg, typeSequence, "staged direction repair "+direction, dir, []string{prompt}, 5)
		task.Project = wf.ModuleID
		task.WorkflowID = wf.ID
		task.DependsOn = repairDependsOn(target, design)
		task.WriteDomain = inheritWriteDomain(target.WriteDomain)
		if err := configureWorkflowWriter(cfg, wf, task); err != nil {
			return err
		}
		inheritRepairEffectLimits(task, target)
		task.ReviewAfter = false
		task.SessionStage = direction
		task.SessionRole = sessionRoleAuthor
		task.FixRound = round
		if err := saveTask(root, task); err != nil {
			return err
		}
		ids := append([]string(nil), wf.DirectionTaskIDs...)
		ids[idx] = task.ID
		wf.DirectionTaskIDs = ids
		wf.CurrentRound = round
		wf.Status = workflowStatusRepairing
		clearStaleAcceptance(wf)
		if err := persistWorkflow(root, cfg, wf); err != nil {
			return err
		}
		out = task
		return nil
	})
	return out, err
}

func repairDependsOn(target, design *Task) []string {
	seen := map[string]bool{}
	var deps []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		deps = append(deps, id)
	}
	if target != nil {
		for _, id := range target.DependsOn {
			add(id)
		}
	}
	if design != nil {
		add(design.ID)
	}
	return deps
}

func inheritRepairEffectLimits(dst, src *Task) {
	if dst == nil || src == nil {
		return
	}
	dst.PermissionMode = src.PermissionMode
	dst.SkipPermissions = src.SkipPermissions
	dst.AllowedTools = append([]string(nil), src.AllowedTools...)
	dst.MaxAttempts = src.MaxAttempts
	dst.Effort = src.Effort
	dst.EffortExplicit = src.EffortExplicit
	dst.MaxFixRounds = src.MaxFixRounds
	if src.GrokModel != "" {
		dst.GrokModel = src.GrokModel
	}
	if src.GrokEffort != "" {
		dst.GrokEffort = src.GrokEffort
	}
	dst.FrozenRoute = inheritFrozenOwnerRoute(src.FrozenRoute)
}

func inheritFrozenOwnerRoute(src *frozenOwnerRoute) *frozenOwnerRoute {
	if src == nil {
		return nil
	}
	out := *src
	if len(src.Legs) > 0 {
		out.Legs = append([]frozenPolicyLeg(nil), src.Legs...)
	}
	out.Review = copyFrozenPolicyLeg(src.Review)
	out.Merge = copyFrozenPolicyLeg(src.Merge)
	out.ReleaseGate = copyFrozenPolicyLeg(src.ReleaseGate)
	out.ConditionalSol = copyFrozenPolicyLeg(src.ConditionalSol)
	return &out
}

func copyFrozenPolicyLeg(leg *frozenPolicyLeg) *frozenPolicyLeg {
	if leg == nil {
		return nil
	}
	cp := *leg
	return &cp
}

func currentRepairFindingsText(root string, wf *WorkflowRecord, target *Task) string {
	var b strings.Builder
	if target != nil {
		fmt.Fprintf(&b, "failed_member id=%s status=%s last_error=%s\n", target.ID, target.Status, target.LastError)
		if target.Goal != nil && strings.TrimSpace(target.Goal.FailureClass) != "" {
			fmt.Fprintf(&b, "failure_class=%s\n", target.Goal.FailureClass)
		}
	}
	if wf == nil || strings.TrimSpace(wf.AcceptanceTaskID) == "" {
		return b.String()
	}
	acc, err := loadTask(root, wf.AcceptanceTaskID)
	if err != nil || acc == nil || acc.Status != statusDone {
		return b.String()
	}
	raw := loadTaskResultForGate(root, acc)
	v := parseReviewVerdict(raw)
	if v == nil {
		return b.String()
	}
	fmt.Fprintf(&b, "acceptance_verdict=%s\n", v.Verdict)
	for i, p := range v.P0 {
		fmt.Fprintf(&b, "P0-%d: %s\n", i+1, p)
	}
	for i, p := range v.P1 {
		fmt.Fprintf(&b, "P1-%d: %s\n", i+1, p)
	}
	return b.String()
}
