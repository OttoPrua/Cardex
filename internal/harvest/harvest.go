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
)

const defaultAcceptanceTimeout = 2 * time.Minute

// Harvest reads the exit code, the event stream, the diff, acceptance, and the result file.
func Harvest(ctx context.Context, in Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	dir := strings.TrimSpace(in.Attempt.Worktree)
	if dir == "" {
		dir = strings.TrimSpace(in.Repo)
	}
	if dir == "" {
		return Result{}, fmt.Errorf("harvest: worktree is required")
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
		return Result{}, err
	}
	acc, accOut, accLog, err := runAcceptance(ctx, dir, in.Card.Acceptance)
	if err != nil {
		return Result{}, err
	}
	raw, result, err := readResult(filepath.Join(dir, ".cardex-result.md"))
	if err != nil {
		return Result{}, err
	}
	outcome := decide(facts{
		ProducesCode: producesCode(in.Card.Kind),
		HasChanges:   len(changed) > 0,
		Acceptance:   acc,
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
	if outcome == model.OutcomeViolation {
		res.ErrorClass = model.ErrorViolation
	}
	if outcome == model.OutcomeTimeout && acc == acceptTimeout && !runnerClass(in.ErrorClass) {
		res.ErrorClass = model.ErrorTimeout
	}
	if werr := writeEvidence(in.Home, in.Attempt.ID, accLog, result != resultMissing, []byte(raw)); werr != nil {
		res.EvidenceErr = werr
	}
	return res, nil
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

func runAcceptance(ctx context.Context, dir string, commands []string) (acceptState, string, []byte, error) {
	var cmds []string
	for _, c := range commands {
		if c = strings.TrimSpace(c); c != "" {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		return acceptOK, "", nil, nil
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
	for _, command := range cmds {
		cmd := proc.Shell(actx, command)
		cmd.Dir = dir
		cmd.Env = os.Environ()
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
		// A later header starts on its own line. The separator is not part of the raw output.
		if log.Len() > 0 && log.Bytes()[log.Len()-1] != '\n' {
			log.WriteByte('\n')
		}
		fmt.Fprintf(&log, "command=%s exit=%d duration=%s\n", strconv.Quote(command), exitCode(err), time.Since(start))
		log.Write(out)
		all.Write(out)
		if err == nil {
			continue
		}
		if ctx.Err() == context.Canceled && actx.Err() != context.DeadlineExceeded {
			return acceptOK, "", nil, ctx.Err()
		}
		if actx.Err() == context.DeadlineExceeded {
			return acceptTimeout, tail(all.String()), log.Bytes(), nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return acceptFailed, tail(all.String()), log.Bytes(), nil
		}
		return acceptOK, "", nil, err
	}
	return acceptOK, tail(all.String()), log.Bytes(), nil
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
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
