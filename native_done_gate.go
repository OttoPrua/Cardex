package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	nativeDoneHoldOpenTools         = "open_tools"
	nativeDoneHoldIncompleteObs     = "incomplete_observation"
	nativeDoneHoldProcessResidue    = "process_residue"
	nativeDoneHoldMissingFiles      = "missing_promised_files"
	nativeDoneHoldMissingTests      = "missing_promised_verification"
	nativeDoneHoldWF02WaitingForID  = "wf02_waiting_for_id_no_diff"
	nativeDoneHoldFakeExecuting     = "fake_executing_no_contract_evidence"
	nativeDoneHoldTUIGoalNotStarted = "tui_goal_not_started"
	nativeDoneHoldPermissionIdle    = "permission_requested_idle"
)

var (
	promisedFileVerbRe    = regexp.MustCompile(`(?i)\b(?:write|create|add|implement|emit|touch|save)\s+` + "`?" + `([A-Za-z0-9_./-]+\.[A-Za-z][A-Za-z0-9]*)`)
	promisedFileTickRe    = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.[A-Za-z][A-Za-z0-9]*)`")
	promisedTestsRe       = regexp.MustCompile(`(?i)(?:\bgo test\b|\badd tests\b|\bwrite tests\b|\bunit tests\b|\bwith tests\b|and add tests)`)
	waitForIDRe           = regexp.MustCompile(`(?i)wait(?:ing)?\s+for\s+(?:an?\s+)?(?:(?:task|goal|hosted)\s+)?id\b`)
	idleTurnEndedRe       = regexp.MustCompile(`(?i)(?:\bturn[_\s-]*ended\b|\bIdle\b)`)
	permissionRequestedRe = regexp.MustCompile(`(?i)permission[_ -]*requested`)
	tuiComposerGoalRe     = regexp.MustCompile(`(?i)(?:tui\s+(?:input[-\s]*box|composer)|input[-\s]*box[^\n]{0,48}\bgoal\b|(?:^|\n)\s*/goal\b)`)
)

// nativeDoneFacts is the contract-evidence snapshot the native-done persist
// path consults. Tests may construct it directly; production collects it from
// the live task, prompt/contract text, and worktree.
type nativeDoneFacts struct {
	Prompt              string
	ResultText          string
	TaskType            string
	WorktreeFiles       []string
	WorktreeHasDiff     bool
	OpenTools           bool
	ObservationComplete bool
	TerminalEvents      int
	ProcessResidue      bool
	ProcessAlive        bool
}

func nativeDoneApplies(via string) bool {
	return grokBuildVia(via) || kimiCLIVia(via) || via == "opencode"
}

func collectNativeDoneFacts(t *Task, prompt, result string, res *claudeResult) nativeDoneFacts {
	dir := ""
	typ := ""
	if t != nil {
		dir = t.Dir
		typ = t.Type
	}
	files := nativeWorktreeRelFiles(dir)
	facts := nativeDoneFacts{
		Prompt:          prompt,
		ResultText:      result,
		TaskType:        typ,
		WorktreeFiles:   files,
		WorktreeHasDiff: nativeWorktreeHasDiff(dir, files),
	}
	if res != nil {
		facts.ObservationComplete = res.ObservationComplete
		facts.TerminalEvents = res.TerminalEvents
		switch res.Subtype {
		case kimiCLISubtypeUnmatchedTool, kimiCLISubtypeDuplicateTool:
			facts.OpenTools = true
		}
		if !res.ObservationComplete && res.ToolEvents > 0 && res.TerminalEvents != 1 {
			facts.OpenTools = true
		}
	}
	if t != nil {
		if t.LastRouteAttempt != nil {
			facts.ProcessResidue = t.LastRouteAttempt.ProcessResidue
		}
		if anyTaskProcAlive(t.ID) {
			facts.ProcessAlive = true
		}
		if taskProcessResidue(t.ID) {
			facts.ProcessResidue = true
		}
	}
	return facts
}

// collectNativeDoneFactsForPersist is the persist-done snapshot. A nil live
// result (no_more_prompts / crash-retry) must not invent an incomplete
// observation, but promised files, tests, WF-02, and live CPU still bind.
func collectNativeDoneFactsForPersist(t *Task, prompt, result string, res *claudeResult) nativeDoneFacts {
	facts := collectNativeDoneFacts(t, prompt, result, res)
	if res == nil {
		facts.ObservationComplete = true
	}
	return facts
}

func nativeDoneContractPrompt(t *Task, current string) string {
	current = strings.TrimSpace(current)
	if t == nil || len(t.Prompts) == 0 {
		return current
	}
	joined := strings.TrimSpace(strings.Join(t.Prompts, "\n"))
	if current == "" {
		return joined
	}
	if current == joined || strings.Contains(joined, current) {
		return joined
	}
	return joined + "\n" + current
}

// nativeDoneHoldReason returns a closed hold reason if native done must not be
// persisted. An empty string allows done.
func nativeDoneHoldReason(t *Task, facts nativeDoneFacts) string {
	if facts.OpenTools {
		return nativeDoneHoldOpenTools
	}
	if facts.ProcessAlive {
		return nativeDoneHoldFakeExecuting
	}
	if facts.ProcessResidue {
		return nativeDoneHoldProcessResidue
	}
	if !facts.ObservationComplete {
		reviewTextOK := t != nil && taskIsReviewOrText(t) && strings.TrimSpace(facts.ResultText) != ""
		if !reviewTextOK {
			return nativeDoneHoldIncompleteObs
		}
	}
	if len(missingPromisedFiles(facts.Prompt, facts.WorktreeFiles, t)) > 0 {
		return nativeDoneHoldMissingFiles
	}
	if contractPromisesTests(facts.Prompt) && !nativeVerificationPresent(facts) {
		return nativeDoneHoldMissingTests
	}
	if resultWaitsForTaskID(facts.ResultText) && !facts.WorktreeHasDiff {
		return nativeDoneHoldWF02WaitingForID
	}
	if reason := nativeIdleNotStartedOrDoneReason(facts); reason != "" {
		return reason
	}
	// Idle/turn_ended with no worktree diff is fake executing, not native done.
	// Read-only report/text cards may complete with no diff.
	if resultLooksIdleTurnEnded(facts.ResultText) && !facts.WorktreeHasDiff && !taskIsReviewOrText(t) {
		return nativeDoneHoldFakeExecuting
	}
	return ""
}

// nativeIdleNotStartedOrDoneReason is the shared fail-closed for TUI input-box
// Goal, permission_requested idle, and turn_ended plus live CPU. None of these
// are started or native done.
func nativeIdleNotStartedOrDoneReason(facts nativeDoneFacts) string {
	if resultLooksTUIComposerGoal(facts.ResultText) && !facts.WorktreeHasDiff {
		return nativeDoneHoldTUIGoalNotStarted
	}
	if resultLooksPermissionRequestedIdle(facts.ResultText) {
		return nativeDoneHoldPermissionIdle
	}
	if resultLooksIdleTurnEnded(facts.ResultText) && facts.ProcessAlive {
		return nativeDoneHoldFakeExecuting
	}
	return ""
}

func nativeLooksStartedOrDone(facts nativeDoneFacts) bool {
	return nativeIdleNotStartedOrDoneReason(facts) == "" &&
		(facts.ProcessAlive || facts.WorktreeHasDiff || facts.TerminalEvents > 0) &&
		!facts.OpenTools
}

func missingPromisedFiles(prompt string, files []string, t *Task) []string {
	promised := promisedFilesFromContract(prompt)
	if len(promised) == 0 {
		return nil
	}
	present := map[string]bool{}
	for _, f := range files {
		present[filepath.ToSlash(f)] = true
		present[filepath.Base(f)] = true
	}
	dir := ""
	if t != nil {
		dir = t.Dir
	}
	var missing []string
	for _, p := range promised {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if present[p] || present[filepath.Base(p)] {
			continue
		}
		if dir != "" {
			if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err == nil && !st.IsDir() {
				continue
			}
		}
		missing = append(missing, p)
	}
	return missing
}

func promisedFilesFromContract(prompt string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, m := range promisedFileVerbRe.FindAllStringSubmatch(prompt, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range promisedFileTickRe.FindAllStringSubmatch(prompt, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	return out
}

func contractPromisesTests(prompt string) bool {
	return promisedTestsRe.MatchString(prompt)
}

func nativeVerificationPresent(facts nativeDoneFacts) bool {
	for _, f := range facts.WorktreeFiles {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_test.go") || strings.Contains(base, "_test.") || strings.HasSuffix(base, "_spec.ts") {
			return true
		}
	}
	return false
}

func resultWaitsForTaskID(text string) bool {
	return waitForIDRe.MatchString(text)
}

func resultLooksIdleTurnEnded(text string) bool {
	return idleTurnEndedRe.MatchString(text)
}

func resultLooksPermissionRequestedIdle(text string) bool {
	return permissionRequestedRe.MatchString(text)
}

func resultLooksTUIComposerGoal(text string) bool {
	return tuiComposerGoalRe.MatchString(text)
}

func nativeWorktreeRelFiles(dir string) []string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files
}

func nativeWorktreeHasDiff(dir string, files []string) bool {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		if out, err := gitOutput(dir, "status", "--porcelain"); err == nil {
			return strings.TrimSpace(string(out)) != ""
		}
	}
	return len(files) > 0
}

func holdNativeContractEvidence(root string, t *Task, via, reason string) error {
	if t == nil {
		return nil
	}
	if t.LastRouteAttempt != nil {
		t.LastRouteAttempt.FailureKind = reason
		t.LastRouteAttempt.FailureClass = "contract_evidence"
	}
	t.ReviewOutput = nil
	t.Status = statusHeld
	t.LastError = "native done held: " + reason
	t.touch()
	return finishIfStopped(persistTaskEvent(root, t, evHeld, "runner:native-done-gate", statusHeld, t.Step,
		withCostTelemetry(withRouteAttempt(map[string]any{
			"reason": reason, "runner": via, "reason_class": "contract_evidence",
		}, t), t)))
}
