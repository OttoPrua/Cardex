package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	harvestModeOff    = "off"
	harvestModeDryRun = "dry-run"
	harvestModeOn     = "on"

	harvestVerdictDone        = "done"
	harvestVerdictNeedsReview = "needs_review"
	harvestVerdictRetry       = "retry"
	harvestVerdictAttention   = "attention"

	harvestReasonReadOnlyText       = "read_only_text"
	harvestReasonVerifyPassed       = "verify_passed"
	harvestReasonVerifyFailed       = "verify_failed"
	harvestReasonUnverifiedOutput   = "unverified_output"
	harvestReasonReportNoChanges    = "report_without_changes"
	harvestReasonNoOutput           = "no_output"
	harvestReasonNoOutputAfterRetry = "no_output_after_retry"

	harvestReportRel       = ".cardex/REPORT.md"
	harvestReportCap       = 4000
	harvestFinalTextCap    = 2000
	harvestVerifyTailCap   = 3000
	harvestVerifyTimeout   = 15 * time.Minute
	harvestPatchCap        = 5 << 20
	harvestUntrackedCap    = 256 << 10
	harvestCommitCap       = 20
	harvestHistoricalSlack = 30 * time.Minute
)

// recordDispatchBaseCommit stores worktree HEAD the first time a card is
// dispatched. A directory without its own .git (a temp dir inside some other
// repo, or a non-repo) is left blank. Failure to read HEAD is silent.
func recordDispatchBaseCommit(t *Task) {
	if t == nil || strings.TrimSpace(t.BaseCommit) != "" || strings.TrimSpace(t.Dir) == "" {
		return
	}
	gitMeta := filepath.Join(t.Dir, ".git")
	st, err := os.Lstat(gitMeta)
	if err != nil || (!st.IsDir() && !st.Mode().IsRegular()) {
		return
	}
	out, err := gitOutput(t.Dir, "rev-parse", "HEAD")
	if err != nil {
		return
	}
	if head := strings.TrimSpace(string(out)); head != "" {
		t.BaseCommit = head
	}
}

// codexWritableRootsArg is the `-c` value for sandbox_workspace_write.writable_roots.
// It always includes <workDir>/.git. When git can resolve them, git-dir and
// git-common-dir are added too (linked worktrees: .git is a file pointing at
// the common dir, and commits need both). Identical paths are dropped. If git
// fails, the result is today's single <workDir>/.git entry.
func codexWritableRootsArg(workDir string) string {
	roots := codexWritableRoots(workDir)
	encoded, err := json.Marshal(roots)
	if err != nil {
		encoded = []byte(fmt.Sprintf("[%q]", filepath.Join(workDir, ".git")))
	}
	return "sandbox_workspace_write.writable_roots=" + string(encoded)
}

func codexWritableRoots(workDir string) []string {
	primary := filepath.Join(workDir, ".git")
	roots := []string{primary}
	out, err := gitOutput(workDir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return roots
	}
	for _, line := range strings.Split(string(out), "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		dup := false
		for _, have := range roots {
			if sameGitPath(have, p) {
				dup = true
				break
			}
		}
		if !dup {
			roots = append(roots, p)
		}
	}
	return roots
}

func sameGitPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && ra == rb
}

// errHarvestAcceptSuccess tells the runner to finish this attempt through the
// existing provider-success path. It is never returned to the operator.
var errHarvestAcceptSuccess = errors.New("harvest accept success")

// harvestFacts is the pure input to decideHarvest. VerifyExit is nil when the
// acceptance command was not run.
type harvestFacts struct {
	ReadOnly          bool
	HasChanges        bool
	NewCommits        int
	VerifyConfigured  bool
	VerifyExit        *int
	ReportNonEmpty    bool
	FinalTextNonEmpty bool
	Attempts          int
	MaxAttempts       int
}

// harvestMode normalizes cfg.HarvestMode to off, dry-run, or on.
// Empty and any unrecognized value are off, which keeps today's hold behavior.
func harvestMode(cfg *Config) string {
	if cfg == nil {
		return harvestModeOff
	}
	switch strings.ToLower(strings.TrimSpace(cfg.HarvestMode)) {
	case harvestModeOn:
		return harvestModeOn
	case harvestModeDryRun, "dry_run":
		return harvestModeDryRun
	default:
		return harvestModeOff
	}
}

// decideHarvest judges a finished attempt by outcome, not stream shape.
// Read-only types (design-review, progress-pull) are done when they produced
// a report or final text. Writers are done only when a configured verify
// command exits 0; any other writer output is needs_review. Silence retries
// while attempts remain, then attention.
func decideHarvest(facts harvestFacts) (verdict, reason string) {
	hasText := facts.ReportNonEmpty || facts.FinalTextNonEmpty
	hasOutput := facts.HasChanges || facts.NewCommits > 0
	if facts.ReadOnly {
		if hasText {
			return harvestVerdictDone, harvestReasonReadOnlyText
		}
		return harvestNoOutputVerdict(facts)
	}
	if hasOutput {
		if facts.VerifyConfigured {
			if facts.VerifyExit != nil && *facts.VerifyExit == 0 {
				return harvestVerdictDone, harvestReasonVerifyPassed
			}
			return harvestVerdictNeedsReview, harvestReasonVerifyFailed
		}
		return harvestVerdictNeedsReview, harvestReasonUnverifiedOutput
	}
	if hasText {
		return harvestVerdictNeedsReview, harvestReasonReportNoChanges
	}
	return harvestNoOutputVerdict(facts)
}

// attemptsRemain matches the runner retry gate: the failure about to be
// recorded still lands strictly below MaxAttempts, so it will requeue rather
// than become the terminal failure.
func harvestNoOutputVerdict(facts harvestFacts) (string, string) {
	if facts.Attempts+1 < facts.MaxAttempts {
		return harvestVerdictRetry, harvestReasonNoOutput
	}
	return harvestVerdictAttention, harvestReasonNoOutputAfterRetry
}

func effectiveMaxAttempts(cfg *Config, t *Task) int {
	if t != nil && t.MaxAttempts > 0 {
		return t.MaxAttempts
	}
	if cfg != nil && cfg.MaxAttempts > 0 {
		return cfg.MaxAttempts
	}
	return 0
}

// harvestCmdFunc runs a command and returns combined output plus exit code.
// A non-zero exit is not an error; err is reserved for failures to start.
type harvestCmdFunc func(ctx context.Context, dir, name string, args ...string) (out []byte, exitCode int, err error)

var (
	harvestExecMu sync.Mutex
	harvestExecFn harvestCmdFunc = defaultHarvestExec
	harvestReadFn                = os.ReadFile
)

func callHarvestExec(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
	harvestExecMu.Lock()
	fn := harvestExecFn
	harvestExecMu.Unlock()
	if fn == nil {
		fn = defaultHarvestExec
	}
	return fn(ctx, dir, name, args...)
}

func callHarvestRead(path string) ([]byte, error) {
	harvestExecMu.Lock()
	fn := harvestReadFn
	harvestExecMu.Unlock()
	if fn == nil {
		fn = os.ReadFile
	}
	return fn(path)
}

func defaultHarvestExec(ctx context.Context, dir, name string, args ...string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err == nil {
		return buf.Bytes(), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return buf.Bytes(), ee.ExitCode(), nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return buf.Bytes(), -1, err
	}
	return buf.Bytes(), -1, err
}

// harvestOpts tunes collection. Historical is the `cardex harvest` read of
// cards that never recorded BaseCommit. SkipVerify is the dry-run of that
// command. SharedDirNote is appended to diagnostics when other tasks share Dir.
type harvestOpts struct {
	SkipVerify    bool
	Historical    bool
	SharedDirNote string
}

// harvestAttempt collects worktree, report, and verify facts, decides a
// verdict, and fills the evidence record. Patch-write failures are diagnostics.
func harvestAttempt(root string, cfg *Config, t *Task, via string, res *claudeResult, runErr error) (*HarvestEvidence, string, string) {
	return harvestAttemptOpts(root, cfg, t, via, res, runErr, harvestOpts{})
}

func harvestAttemptOpts(root string, cfg *Config, t *Task, via string, res *claudeResult, runErr error, opts harvestOpts) (*HarvestEvidence, string, string) {
	ev := &HarvestEvidence{At: time.Now().Format(time.RFC3339)}
	if t == nil {
		verdict, reason := decideHarvest(harvestFacts{})
		ev.Reason = reason
		return ev, verdict, reason
	}
	ev.Attempt = t.Attempts
	var diags []string
	if note := strings.TrimSpace(opts.SharedDirNote); note != "" {
		diags = append(diags, note)
	}

	changed, insertions, deletions, commits, gitDiags := collectWorktreeFacts(t, opts.Historical)
	diags = append(diags, gitDiags...)
	ev.ChangedFiles = changed
	ev.Insertions = insertions
	ev.Deletions = deletions
	ev.NewCommits = commits

	reportPath := filepath.Join(t.Dir, filepath.FromSlash(harvestReportRel))
	if b, err := callHarvestRead(reportPath); err == nil {
		text := strings.TrimSpace(string(b))
		if text != "" {
			ev.ReportPath = reportPath
			ev.ReportExcerpt = capUTF8Bytes(text, harvestReportCap)
		}
	}

	// Final text is the provider result only. Synthesized error strings
	// (res.IsError, or a run error with no successful result) are not text.
	if res != nil && !res.IsError && runErr == nil {
		if text := strings.TrimSpace(res.Result); text != "" {
			ev.FinalText = capUTF8Bytes(text, harvestFinalTextCap)
		}
	}

	hasOutput := len(changed) > 0 || len(commits) > 0
	verifyConfigured := strings.TrimSpace(t.Verify) != ""
	var verifyExit *int
	if verifyConfigured && hasOutput && !opts.SkipVerify {
		ev.VerifyCommand = t.Verify
		code, tail, err := runHarvestVerify(t)
		verifyExit = &code
		ev.VerifyExit = verifyExit
		ev.VerifyTail = tail
		if err != nil {
			diags = append(diags, "verify: "+err.Error())
		}
	}

	if len(changed) > 0 {
		patchPath, patchDiag := writeHarvestPatch(root, t, changed)
		if patchPath != "" {
			ev.PatchPath = patchPath
		}
		if patchDiag != "" {
			diags = append(diags, patchDiag)
		}
	}

	facts := harvestFacts{
		ReadOnly:          taskIsReadOnlyType(t),
		HasChanges:        len(changed) > 0,
		NewCommits:        len(commits),
		VerifyConfigured:  verifyConfigured,
		VerifyExit:        verifyExit,
		ReportNonEmpty:    ev.ReportExcerpt != "",
		FinalTextNonEmpty: ev.FinalText != "",
		Attempts:          t.Attempts,
		MaxAttempts:       effectiveMaxAttempts(cfg, t),
	}
	verdict, reason := decideHarvest(facts)
	ev.Reason = reason
	if len(diags) > 0 {
		ev.Diagnostics = strings.Join(diags, "; ")
	}
	_ = via
	return ev, verdict, reason
}

// collectWorktreeFacts reads git status, numstat, and commits.
// Untracked paths count as changed files. Their insertions are the line
// counts of non-binary files (a NUL byte in the first 8KB marks binary and
// contributes 0). Tracked numstat lines are summed separately; "-" (binary
// diff) contributes 0.
func collectWorktreeFacts(t *Task, historical bool) (files []string, ins, del int, commits []string, diags []string) {
	if t == nil || strings.TrimSpace(t.Dir) == "" {
		return nil, 0, 0, nil, nil
	}
	ctx := context.Background()
	out, exit, err := callHarvestExec(ctx, t.Dir, "git", "rev-parse", "--is-inside-work-tree")
	if err != nil || exit != 0 || strings.TrimSpace(string(out)) != "true" {
		return nil, 0, 0, nil, nil
	}
	statusOut, statusExit, statusErr := callHarvestExec(ctx, t.Dir, "git", "status", "--porcelain=v1", "-uall")
	if statusErr != nil || statusExit != 0 {
		diags = append(diags, "git status: "+harvestCmdDiag(statusErr, statusExit))
	} else {
		var untracked []string
		files, untracked = parsePorcelainV1(string(statusOut))
		ins, del = untrackedLineCounts(t.Dir, untracked)
	}
	base := strings.TrimSpace(t.BaseCommit)
	numBase := base
	if numBase == "" {
		numBase = "HEAD"
	}
	numOut, numExit, numErr := callHarvestExec(ctx, t.Dir, "git", "diff", "--numstat", numBase)
	if numErr != nil || numExit != 0 {
		diags = append(diags, "git diff --numstat: "+harvestCmdDiag(numErr, numExit))
	} else {
		addIns, addDel := parseNumstat(string(numOut))
		ins += addIns
		del += addDel
	}
	switch {
	case base != "":
		logOut, logExit, logErr := callHarvestExec(ctx, t.Dir, "git", "log", "--format=%h %s", base+"..HEAD")
		if logErr != nil || logExit != 0 {
			diags = append(diags, "git log: "+harvestCmdDiag(logErr, logExit))
		} else {
			commits = capLines(string(logOut), harvestCommitCap)
		}
	case historical:
		since, until, ok := historicalCommitWindow(t)
		if !ok {
			diags = append(diags, "historical commit window: unparsable created_at/updated_at")
			break
		}
		diags = append(diags, "historical commits use created_at..updated_at+30m because base_commit is empty")
		logOut, logExit, logErr := callHarvestExec(ctx, t.Dir, "git", "log", "--format=%h %s", "--since="+since, "--until="+until)
		if logErr != nil || logExit != 0 {
			diags = append(diags, "git log: "+harvestCmdDiag(logErr, logExit))
		} else {
			commits = capLines(string(logOut), harvestCommitCap)
		}
	}
	return files, ins, del, commits, diags
}

func historicalCommitWindow(t *Task) (since, until string, ok bool) {
	created, err1 := time.Parse(time.RFC3339, strings.TrimSpace(t.CreatedAt))
	updated, err2 := time.Parse(time.RFC3339, strings.TrimSpace(t.UpdatedAt))
	if err1 != nil || err2 != nil {
		return "", "", false
	}
	return created.Format(time.RFC3339), updated.Add(harvestHistoricalSlack).Format(time.RFC3339), true
}

func harvestCmdDiag(err error, exit int) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("exit %d", exit)
}

// parsePorcelainV1 reads `git status --porcelain=v1 -uall`. Each line is
// "XY PATH" or "XY ORIG -> PATH". Untracked entries ("??") are returned in
// both slices so callers can count their lines separately from numstat.
func parsePorcelainV1(out string) (all, untracked []string) {
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		xy := line[:2]
		path := strings.TrimSpace(strings.Trim(porcelainPath(line[3:]), `"`))
		if path == "" {
			continue
		}
		all = append(all, path)
		if xy == "??" {
			untracked = append(untracked, path)
		}
	}
	return all, untracked
}

func porcelainPath(rest string) string {
	if i := strings.LastIndex(rest, " -> "); i >= 0 {
		return rest[i+4:]
	}
	return rest
}

func parseNumstat(out string) (ins, del int) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		if n, err := strconv.Atoi(fields[0]); err == nil {
			ins += n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			del += n
		}
	}
	return ins, del
}

func untrackedLineCounts(dir string, paths []string) (ins, del int) {
	for _, rel := range paths {
		b, err := callHarvestRead(filepath.Join(dir, rel))
		if err != nil {
			continue
		}
		ins += textLineCount(b)
	}
	return ins, del
}

// textLineCount counts lines in a text file. A NUL in the first 8KB marks the
// file binary; those files contribute 0 (documented, not an error).
func textLineCount(b []byte) int {
	sample := b
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return 0
	}
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte("\n"))
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

func capLines(out string, n int) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) >= n {
			break
		}
	}
	return lines
}

func capUTF8Bytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for i < n {
		_, size := utf8.DecodeRuneInString(s[i:])
		if size <= 0 || i+size > n {
			break
		}
		i += size
	}
	return s[:i]
}

func tailUTF8Bytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r != utf8.RuneError || size != 1 || s[0] < 0x80 {
			break
		}
		s = s[size:]
	}
	return s
}

func runHarvestVerify(t *Task) (code int, tail string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), harvestVerifyTimeout)
	defer cancel()
	out, exitCode, runErr := callHarvestExec(ctx, t.Dir, "sh", "-c", t.Verify)
	tail = tailUTF8Bytes(string(out), harvestVerifyTailCap)
	if runErr != nil {
		if exitCode < 0 {
			exitCode = 1
		}
		return exitCode, tail, runErr
	}
	return exitCode, tail, nil
}

// writeHarvestPatch stores `git diff HEAD --binary` (capped at 5MB) plus a
// delimited section for each untracked text file under 256KB. Failures are
// diagnostics and never fail the harvest.
func writeHarvestPatch(root string, t *Task, changed []string) (string, string) {
	if t == nil || t.ID == "" || root == "" {
		return "", ""
	}
	ctx := context.Background()
	diffOut, diffExit, diffErr := callHarvestExec(ctx, t.Dir, "git", "diff", "HEAD", "--binary")
	var b strings.Builder
	if diffErr != nil || diffExit != 0 {
		return "", "patch: " + harvestCmdDiag(diffErr, diffExit)
	}
	diff := diffOut
	truncated := false
	if len(diff) > harvestPatchCap {
		diff = diff[:harvestPatchCap]
		truncated = true
	}
	b.Write(diff)
	if truncated {
		b.WriteString("\n# cardex-harvest: diff truncated at 5MB\n")
	}
	var notes []string
	for _, rel := range changed {
		body, err := callHarvestRead(filepath.Join(t.Dir, rel))
		if err != nil {
			continue
		}
		// Only untracked files are absent from `git diff HEAD`. Skip paths
		// that already appear in the diff by requiring the porcelain-only
		// files to be re-read; tracked modifications are already in diffOut.
		// We detect untracked by absence of a "diff --git" header for the path
		// only when the file was untracked. Cheaper: include a section when
		// the diff does not mention the path.
		if bytes.Contains(diffOut, []byte("b/"+filepath.ToSlash(rel))) {
			continue
		}
		if len(body) > harvestUntrackedCap {
			notes = append(notes, rel+": untracked file exceeds 256KB, omitted")
			continue
		}
		if bytes.IndexByte(body, 0) >= 0 {
			notes = append(notes, rel+": untracked binary omitted")
			continue
		}
		fmt.Fprintf(&b, "\n# cardex-harvest untracked: %s\n", filepath.ToSlash(rel))
		b.Write(body)
		if len(body) == 0 || body[len(body)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	n := t.Attempts
	dir := filepath.Join(root, "harvest", t.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "patch: " + err.Error()
	}
	path := filepath.Join(dir, fmt.Sprintf("attempt-%d.patch", n))
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", "patch: " + err.Error()
	}
	if len(notes) > 0 {
		return path, strings.Join(notes, "; ")
	}
	return path, ""
}

// prepareUnknownHarvest runs harvest at an unknown-outcome hold.
// continueLegacy is true for mode off, a live/residual process, and dry-run
// (after the verdict is stored). Mode on returns continueLegacy false: either
// errHarvestAcceptSuccess or the error from the hold/retry persist.
func prepareUnknownHarvest(root string, cfg *Config, t *Task, via string, res **claudeResult, runErr error, now time.Time) (continueLegacy bool, err error) {
	if harvestMode(cfg) == harvestModeOff || t == nil {
		return true, nil
	}
	if anyTaskProcAlive(t.ID) || taskProcessResidue(t.ID) {
		return true, nil
	}
	var result *claudeResult
	if res != nil {
		result = *res
	}
	ev, verdict, reason := harvestAttempt(root, cfg, t, via, result, runErr)
	t.Verdict = verdict
	t.Harvest = ev
	if harvestMode(cfg) == harvestModeDryRun {
		return true, nil
	}
	switch verdict {
	case harvestVerdictDone:
		applyHarvestSuccessResult(t, res)
		return false, errHarvestAcceptSuccess
	case harvestVerdictRetry:
		return false, harvestRequeue(root, cfg, t, now, reason)
	default:
		return false, harvestHold(root, t, verdict, reason)
	}
}

func applyHarvestSuccessResult(t *Task, res **claudeResult) {
	text := ""
	if t != nil && t.Harvest != nil {
		text = t.Harvest.FinalText
		if text == "" {
			text = t.Harvest.ReportExcerpt
		}
	}
	if res == nil {
		return
	}
	if *res == nil {
		*res = &claudeResult{}
	}
	(*res).IsError = false
	(*res).Result = text
}

func annotateHarvestHold(detail map[string]any, t *Task) map[string]any {
	if t == nil || t.Verdict == "" || t.Harvest == nil {
		return detail
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["harvest_verdict"] = t.Verdict
	detail["harvest_reason"] = t.Harvest.Reason
	return detail
}

func harvestHold(root string, t *Task, verdict, reason string) error {
	t.Status = statusHeld
	t.LastError = "harvest " + verdict + ": " + reason
	t.touch()
	detail := map[string]any{
		"reason":         "harvest_" + verdict,
		"reason_class":   "harvest",
		"verdict":        verdict,
		"harvest_reason": reason,
	}
	if t.Harvest != nil {
		detail["changed_files"] = len(t.Harvest.ChangedFiles)
		detail["insertions"] = t.Harvest.Insertions
		detail["deletions"] = t.Harvest.Deletions
		detail["new_commits"] = len(t.Harvest.NewCommits)
		if t.Harvest.VerifyExit != nil {
			detail["verify_exit"] = *t.Harvest.VerifyExit
		}
		if t.Harvest.PatchPath != "" {
			detail["patch_path"] = t.Harvest.PatchPath
		}
	}
	return finishIfStopped(persistTaskEvent(root, t, evHeld, "runner:harvest", statusHeld, t.Step,
		withCostTelemetry(detail, t)))
}

// harvestRequeue mirrors the runner retry_backoff branch: attempts increase,
// backoff scales with the attempt count, and the event carries harvest_reason.
func harvestRequeue(root string, cfg *Config, t *Task, now time.Time, reason string) error {
	msg := "harvest retry: " + reason
	t.Attempts++
	t.LastError = msg
	maxAttempts := effectiveMaxAttempts(cfg, t)
	if maxAttempts > 0 && t.Attempts >= maxAttempts {
		if t.MaxAttempts > 0 {
			t.Status = statusHeld
			t.touch()
			return finishIfStopped(persistTaskEvent(root, t, evHeld, "runner:harvest", statusHeld, t.Step,
				withCostTelemetry(map[string]any{
					"err": msg, "attempts": t.Attempts, "reason": "task_max_attempts_reached",
					"max_attempts": maxAttempts, "harvest_reason": reason, "reason_class": "harvest",
				}, t)))
		}
		t.Status = statusFailed
		t.touch()
		return finishIfStopped(persistTaskEvent(root, t, evFailed, "runner:harvest", statusFailed, t.Step,
			withCostTelemetry(map[string]any{
				"err": msg, "attempts": t.Attempts, "harvest_reason": reason,
			}, t)))
	}
	t.Status = statusQueued
	backoff := time.Duration(cfg.RetryBackoffMin) * time.Minute
	if !transientRe.MatchString(msg) {
		backoff *= time.Duration(t.Attempts)
	}
	t.NotBeforeEpoch = now.Add(backoff).Unix()
	t.touch()
	return finishIfStopped(persistTaskEvent(root, t, evRetry, "runner:harvest", statusQueued, t.Step, withRouteAttempt(map[string]any{
		"err": msg, "attempts": t.Attempts, "not_before": t.NotBeforeEpoch,
		"failure_class": "unknown", "harvest_reason": reason, "reason": "harvest_retry",
	}, t)))
}

func finishHarvestedAttempt(ctx context.Context, root string, cfg *Config, t *Task, via, prompt string, res *claudeResult, lg *os.File, useCodex, remote, useGemini, useOpenCode, useKimiCLI, useGrokBuild, useCursor bool, engineName string) error {
	cont, err := finishProviderSuccess(ctx, root, cfg, t, via, prompt, res, lg, useCodex, remote, useGemini, useOpenCode, useKimiCLI, useGrokBuild, useCursor, engineName)
	if err != nil {
		return err
	}
	if cont {
		return errHarvestContinueLoop
	}
	return nil
}

// errHarvestContinueLoop asks runTaskVia to keep walking prompts after a
// harvested non-final step. It is not an operator-visible failure.
var errHarvestContinueLoop = errors.New("harvest continue loop")

// dispatchUnknownHarvest is the runner hold-site hook.
// handled is false when the caller must keep the legacy hold (mode off,
// live process, or dry-run after the verdict was stored on the task).
// handled is true when mode on already persisted a hold/retry, or when err is
// errHarvestContinueLoop / nil after the success path finished the step.
func dispatchUnknownHarvest(ctx context.Context, root string, cfg *Config, t *Task, via string, res **claudeResult, runErr error, now time.Time, prompt string, lg *os.File, useCodex, remote, useGemini, useOpenCode, useKimiCLI, useGrokBuild, useCursor bool, engineName string) (handled bool, err error) {
	cont, herr := prepareUnknownHarvest(root, cfg, t, via, res, runErr, now)
	if cont {
		return false, nil
	}
	if errors.Is(herr, errHarvestAcceptSuccess) {
		var result *claudeResult
		if res != nil {
			result = *res
		}
		return true, finishHarvestedAttempt(ctx, root, cfg, t, via, prompt, result, lg, useCodex, remote, useGemini, useOpenCode, useKimiCLI, useGrokBuild, useCursor, engineName)
	}
	return true, herr
}
