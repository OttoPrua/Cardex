package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type gitEvidenceKey struct{}

type gitEvidence struct{ card, attempt, worktree string }

// WithGitEvidence attributes subsequent Git failures to an attempt. An outer
// dispatch can supply the card being run even when it checks out another card.
func WithGitEvidence(ctx context.Context, card, attempt, worktree string) context.Context {
	current, _ := ctx.Value(gitEvidenceKey{}).(gitEvidence)
	if current.card == "" {
		current.card = card
	}
	if current.attempt == "" {
		current.attempt = attempt
	}
	if current.worktree == "" {
		current.worktree = worktree
	}
	return context.WithValue(ctx, gitEvidenceKey{}, current)
}

type gitError struct {
	evidence gitEvidence
	dir      string
	command  string
	kind     string
	reason   string
	err      error
}

func (e *gitError) Error() string {
	message := fmt.Sprintf("git failure command=%q kind=%s: %s", e.command, e.kind, e.reason)
	var exit *exec.ExitError
	if errors.As(e.err, &exit) {
		message += fmt.Sprintf(" (exit=%d)", exit.ExitCode())
	}
	if e.evidence.card != "" {
		message += fmt.Sprintf(" card=%q", e.evidence.card)
	}
	if e.evidence.attempt != "" {
		message += fmt.Sprintf(" attempt=%q", e.evidence.attempt)
	}
	if e.evidence.worktree != "" {
		message += fmt.Sprintf(" worktree=%q", e.evidence.worktree)
	}
	if e.dir != "" && e.dir != e.evidence.worktree {
		message += fmt.Sprintf(" dir=%q", e.dir)
	}
	return message
}

func (e *gitError) Unwrap() error { return e.err }

// GitFailure renders a failed Git invocation. Only allowlisted command names
// and fixed reasons are printed: argv and command output may contain secrets
// (including Git URLs, config values, and content printed by hooks).
func GitFailure(ctx context.Context, dir string, args []string, err error, output []byte) error {
	if err == nil {
		return nil
	}
	evidence, _ := ctx.Value(gitEvidenceKey{}).(gitEvidence)
	command, quietBase := gitCommand(args)
	var exit *exec.ExitError
	if len(output) == 0 && errors.As(err, &exit) {
		output = exit.Stderr
	}
	kind, reason := gitCause(command, quietBase, err, string(output))
	return &gitError{evidence: evidence, dir: dir, command: command, kind: kind, reason: reason, err: err}
}

func gitCommand(args []string) (name string, quietBase bool) {
	for len(args) >= 2 && (args[0] == "-c" || args[0] == "-C" || args[0] == "--git-dir") {
		args = args[2:]
	}
	if len(args) != 0 {
		switch args[0] {
		case "rev-parse":
			return "git rev-parse", len(args) == 4 && args[1] == "--verify" && args[2] == "--quiet" && strings.HasSuffix(args[3], "^{commit}")
		case "worktree":
			if len(args) > 1 {
				switch args[1] {
				case "add", "list", "remove":
					return "git worktree " + args[1], false
				}
			}
			return "git worktree", false
		case "add", "branch", "checkout", "commit", "config", "diff", "ls-files", "remote",
			"show", "show-ref", "status", "update-ref":
			return "git " + args[0], false
		}
	}
	return "git", false
}

func gitCause(command string, quietBase bool, err error, output string) (string, string) {
	var path *os.PathError
	if errors.As(err, &path) && path.Op == "chdir" && errors.Is(path, os.ErrNotExist) {
		return "not_repository", "checkout directory does not exist"
	}
	for _, line := range strings.Split(strings.ToLower(output), "\n") {
		line = strings.TrimSpace(line)
		// Git prepends these severity markers; the remaining data is still
		// untrusted and is matched but never interpolated into evidence.
		for _, prefix := range []string{"fatal: ", "error: ", "warning: "} {
			line = strings.TrimPrefix(line, prefix)
		}
		switch {
		case strings.HasPrefix(line, "not a git repository"), strings.HasPrefix(line, "this operation must be run in a work tree"):
			return "not_repository", "not a Git working tree or repository"
		case strings.HasPrefix(line, "cannot change to ") && strings.HasSuffix(line, "no such file or directory"):
			return "not_repository", "checkout directory does not exist"
		case strings.HasPrefix(line, "conflict ("), strings.HasPrefix(line, "you need to resolve your current index first"),
			strings.HasPrefix(line, "committing is not possible because you have unmerged files"),
			strings.HasPrefix(line, "merging is not possible because you have unmerged files"):
			return "conflict", "unmerged files or an unresolved Git conflict"
		}
		switch command {
		case "git worktree add", "git rev-parse", "git diff":
			switch {
			case strings.HasPrefix(line, "invalid reference:"), strings.HasPrefix(line, "not a valid object name"),
				strings.HasPrefix(line, "needed a single revision"), strings.HasPrefix(line, "bad revision"),
				strings.HasPrefix(line, "ambiguous argument ") && strings.Contains(line, "unknown revision or path"):
				return "missing_base", "base revision does not resolve"
			}
		}
		if strings.HasPrefix(line, "usage:") {
			return "other", "Git printed command usage"
		}
	}
	var exit *exec.ExitError
	if quietBase && strings.TrimSpace(output) == "" && errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "missing_base", "card base revision does not resolve"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "other", "Git command canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "other", "Git command timed out"
	case errors.Is(err, exec.ErrNotFound):
		return "other", "Git executable not found"
	default:
		return "other", "Git could not complete the command"
	}
}
