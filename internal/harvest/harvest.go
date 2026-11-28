package harvest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OttoPrua/Cardex/internal/claims"
	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/roots"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

const defaultAcceptanceTimeout = 10 * time.Minute

// Harvest reads the exit code, the event stream, the diff, acceptance, and the result file.
func Harvest(ctx context.Context, in Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if d := in.Attempt.RouteReason.Dispatch; d != nil && d.CompatibleBindingID != "" {
		// An old API1 child had no authority to create workflow evidence.
		// Refuse it even through callers outside the ordinary scheduler sweep.
		return Result{Outcome: model.OutcomeNoOutput, Excerpt: model.CompatibleRuntimeHold}, nil
	}
	var raw string
	var result resultState
	var complete *strongcustody.Result
	if in.CaptureStrongResult != nil {
		if in.Card.ID != in.Attempt.Card {
			return Result{}, model.ErrStrongArtifact
		}
		var err error
		complete, err = in.CaptureStrongResult.Seal(ctx, in.Attempt)
		if err != nil {
			return Result{}, err
		}
		result = resultMissing
		if complete != nil {
			raw = string(complete.Record().Body)
			result = resultValid
		}
	}
	if err := invalidateStageEvidence(in); err != nil {
		return Result{}, err
	}
	dir := strings.TrimSpace(in.Attempt.Worktree)
	if dir == "" {
		dir = strings.TrimSpace(in.Repo)
	}
	if dir == "" {
		return Result{}, fmt.Errorf("harvest: worktree is required")
	}
	ctx = runner.WithCompatibleCredentialEnv(ctx, in.Home)
	ctx = workspace.WithGitEvidence(ctx, in.Card.ID, in.Attempt.ID, dir)
	if in.Card.UsesStrictReviewObjects(in.Attempt) {
		ctx = proc.WithExactGitObjects(ctx)
	}
	events, truncated := readEvents(in.Output)
	base := strings.TrimSpace(in.Attempt.BaseCommit)
	if base == "" {
		out, err := gitOut(ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			return Result{}, err
		}
		base = out
	}
	changed, err := claims.WorktreeSymbols(ctx, dir, base)
	if err != nil {
		return Result{}, claimGitFailure(ctx, dir, err)
	}
	var cleanReviewCheckout *bool
	if in.Card.Kind == model.KindReview && in.Attempt.HeadCommit == "" && strings.TrimSpace(in.Attempt.BaseCommit) != "" {
		cleanReviewCheckout = checkReviewCheckout(ctx, dir, base)
	}
	frozenCandidate := stageCandidate(ctx, in, dir)
	acceptRan := hasAcceptance(in.Card.Acceptance)
	env := proc.AcceptanceEnv(proc.CompatibleChildEnv(ctx, os.Environ()))
	if proc.ExactGitObjects(ctx) {
		env = proc.GitObjectScopeEnv(env)
	}
	var scratchRoot string
	if acceptRan {
		scratchRoot = roots.Load(in.Home).EffectiveScratch(in.Scratch)
	}
	scratchDir, cleanup, err := roots.PrepareAttemptScratch(scratchRoot, in.Attempt.ID)
	if err != nil {
		return Result{}, err
	}
	acc, accOut, accLog, runs, err := runAcceptance(ctx, dir, in.Card.Acceptance, roots.ApplyTempEnv(env, scratchDir))
	// Scratch can be inside the candidate worktree, so clean it before the summary diff.
	_ = cleanup()
	if err != nil {
		return Result{}, err
	}
	if cleanReviewCheckout != nil && *cleanReviewCheckout {
		cleanReviewCheckout = checkReviewCheckout(ctx, dir, base)
	}
	if in.CaptureStrongResult == nil {
		raw, result, err = readResult(filepath.Join(dir, ".cardex-result.md"))
		if err != nil {
			return Result{}, err
		}
	}
	outcome := decide(facts{
		ProducesCode: producesCode(in.Card.Kind),
		HasChanges:   len(changed) > 0,
		Acceptance:   acc,
		AcceptRan:    acceptRan,
		Result:       result,
		Class:        in.ErrorClass,
		Exit:         in.Exit,
	})
	res := Result{
		Outcome:    outcome,
		ErrorClass: in.ErrorClass,
		Excerpt:    excerptFor(outcome, acc, accOut, in.Output),
		Claims:     mergeClaims(in.Card.ID, in.Card.Claims, changed),
		ResultText: raw,
		Evidence: fmt.Sprintf("exit=%d events=%d truncated=%t changes=%t acceptance=%s result=%s class=%s",
			in.Exit, events, truncated, len(changed) > 0, acceptLabel(acc), resultLabel(result), in.ErrorClass),
	}
	if complete != nil && in.Exit == 0 && outcome == model.OutcomePassed {
		res.StrongResult = complete
	}
	if outcome == model.OutcomeViolation {
		res.ErrorClass = model.ErrorViolation
	}
	if outcome == model.OutcomeTimeout && acc == acceptTimeout && !runnerClass(in.ErrorClass) {
		res.ErrorClass = model.ErrorTimeout
	}
	if werr := writeEvidence(in.Home, in.Attempt.ID, in.Attempt.PromptPath, accLog, result != resultMissing, []byte(raw)); werr != nil {
		res.EvidenceErr = werr
	}
	acceptance := acceptanceResults(ctx, in.Home, dir, base, runs, env, scratchRoot)
	sum := buildSummary(ctx, in, dir, base, acceptance, raw, result != resultMissing)
	sum.CleanReviewCheckout = cleanReviewCheckout
	if werr := writeSummary(in.Home, in.Attempt, sum); werr != nil {
		res.EvidenceErr = errors.Join(res.EvidenceErr, werr)
	}
	if frozenCandidate != "" {
		if werr := saveStageEvidence(ctx, in, dir, frozenCandidate, outcome, runs, sum); werr != nil {
			res.EvidenceErr = errors.Join(res.EvidenceErr, werr)
		}
	}
	return res, nil
}

// claims.WorktreeSymbols can fail on a Git invocation or on local file I/O.
func claimGitFailure(ctx context.Context, dir string, err error) error {
	for _, command := range []string{"diff", "ls-files", "show"} {
		if strings.HasPrefix(err.Error(), "git "+command+" ") {
			return workspace.GitFailure(ctx, dir, []string{command}, err, nil)
		}
	}
	return err
}

func hasAcceptance(commands []string) bool {
	for _, c := range commands {
		if strings.TrimSpace(c) != "" {
			return true
		}
	}
	return false
}

func runnerClass(class model.ErrorClass) bool {
	_, ok := runnerOutcome(class)
	return ok
}

func mergeClaims(card string, declared, actual []model.Claim) []model.Claim {
	type key struct{ path, symbol string }
	seen := map[key]bool{}
	var out []model.Claim
	add := func(c model.Claim) {
		if strings.TrimSpace(c.Path) == "" {
			return
		}
		c.Card = card
		c.ID = ""
		k := key{c.Path, c.Symbol}
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, c)
	}
	for _, c := range declared {
		add(c)
	}
	for _, c := range actual {
		add(c)
	}
	return out
}

func runAcceptance(ctx context.Context, dir string, commands []string, env []string) (acceptState, string, []byte, []commandRun, error) {
	var cmds []string
	for _, c := range commands {
		if c = strings.TrimSpace(c); c != "" {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		return acceptOK, "", nil, nil, nil
	}
	timeout := defaultAcceptanceTimeout
	if dl, ok := ctx.Deadline(); ok {
		if remain := time.Until(dl); remain < timeout {
			timeout = remain
		}
	}
	if timeout < 0 {
		timeout = 0
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var all strings.Builder
	var log bytes.Buffer
	var runs []commandRun
	state := acceptOK
	for _, command := range cmds {
		run, err := runAcceptanceCommand(actx, dir, command, env)
		runs = append(runs, run)
		// A later header starts on its own line. The separator is not part of the raw output.
		if log.Len() > 0 && log.Bytes()[log.Len()-1] != '\n' {
			log.WriteByte('\n')
		}
		fmt.Fprintf(&log, "command=%s exit=%d duration=%s\n", strconv.Quote(command), run.Exit, run.Duration)
		log.Write(run.Output)
		if state == acceptOK || err != nil {
			all.Write(run.Output)
		}
		if err == nil {
			continue
		}
		if ctx.Err() == context.Canceled && actx.Err() != context.DeadlineExceeded {
			return acceptOK, "", nil, runs, ctx.Err()
		}
		if actx.Err() == context.DeadlineExceeded {
			return acceptTimeout, tail(all.String()), log.Bytes(), runs, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			state = acceptFailed
			continue
		}
		return acceptOK, "", nil, runs, err
	}
	return state, tail(all.String()), log.Bytes(), runs, nil
}

func runAcceptanceCommand(ctx context.Context, dir, command string, env []string) (commandRun, error) {
	cmd := proc.Shell(ctx, command)
	cmd.Dir = dir
	cmd.Env = proc.CompatibleChildEnv(ctx, append(append([]string{}, env...), "GIT_TERMINAL_PROMPT=0"))
	if proc.ExactGitObjects(ctx) {
		// Apply fixed non-secret controls at the final executable boundary.
		// The baseline and the candidate use this same runner.
		cmd.Env = append(proc.GitObjectScopeEnv(cmd.Env), "GIT_TERMINAL_PROMPT=0")
	}
	proc.NewGroup(cmd)
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return host.SignalGroup(cmd.Process.Pid, syscall.SIGKILL)
	}
	start := time.Now()
	out, err := cmd.CombinedOutput()
	return commandRun{Command: command, Exit: exitCode(err), Duration: time.Since(start), Output: out}, err
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func excerptFor(outcome model.Outcome, acc acceptState, accOut string, output []byte) string {
	switch outcome {
	case model.OutcomeTestsFailed, model.OutcomeTimeout:
		if acc == acceptTimeout && accOut == "" {
			return "acceptance timed out"
		}
		if accOut != "" {
			return accOut
		}
		return tail(string(output))
	case model.OutcomeResultUnparsable:
		return "result unparsable"
	default:
		return tail(string(output))
	}
}

func tail(s string) string {
	const max = 2000
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

func acceptLabel(s acceptState) string {
	switch s {
	case acceptFailed:
		return "failed"
	case acceptTimeout:
		return "timeout"
	default:
		return "ok"
	}
}

func resultLabel(s resultState) string {
	switch s {
	case resultValid:
		return "valid"
	case resultUnparsable:
		return "unparsable"
	default:
		return "missing"
	}
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitExec(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
