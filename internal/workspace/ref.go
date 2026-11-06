package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RefExists reports whether ref is a branch in repo. A missing branch is
// false, not an error. Tags are not treated as branches.
func (Git) RefExists(ctx context.Context, repo, ref string) (bool, error) {
	repo = strings.TrimSpace(repo)
	full := headRef(ref)
	if repo == "" || full == "" {
		return false, fmt.Errorf("workspace: repo and ref required")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "show-ref", "--verify", "--quiet", full)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("workspace: ref %s: %w", full, err)
}

// EnsureIntegrationBranch creates branch from the primary branch HEAD when
// the ref is missing. It probes refs/heads/main, then refs/heads/master.
// An existing ref is left where it is. The primary branch is not moved and
// nothing is pushed. A concurrent create that wins the race is success.
func (g Git) EnsureIntegrationBranch(ctx context.Context, repo, branch string) (string, error) {
	repo = strings.TrimSpace(repo)
	short := shortBranch(branch)
	if repo == "" || short == "" {
		return "", fmt.Errorf("workspace: repo and integration branch required")
	}
	full := headRef(short)
	exists, err := g.RefExists(ctx, repo, full)
	if err != nil {
		return "", err
	}
	if exists {
		return runGit(ctx, repo, "rev-parse", full)
	}
	var primary, primarySHA string
	for _, name := range []string{"main", "master"} {
		ok, err := g.RefExists(ctx, repo, "refs/heads/"+name)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		sha, err := runGit(ctx, repo, "rev-parse", "refs/heads/"+name)
		if err != nil {
			return "", err
		}
		primary, primarySHA = name, sha
		break
	}
	if primary == "" {
		return "", fmt.Errorf("workspace: no main or master branch")
	}
	if _, err := runGit(ctx, repo, "branch", short, primary); err != nil {
		ok, exErr := g.RefExists(ctx, repo, full)
		if exErr == nil && ok {
			return runGit(ctx, repo, "rev-parse", full)
		}
		return "", err
	}
	again, err := runGit(ctx, repo, "rev-parse", "refs/heads/"+primary)
	if err != nil {
		return "", err
	}
	if again != primarySHA {
		return "", fmt.Errorf("workspace: primary branch %s moved", primary)
	}
	tip, err := runGit(ctx, repo, "rev-parse", full)
	if err != nil {
		return "", err
	}
	if tip != primarySHA {
		return "", fmt.Errorf("workspace: integration branch %s is not %s", short, primary)
	}
	return tip, nil
}

// DropLaunch removes a worktree and card branch created by a launch that
// never committed. A branch whose tip differs from base is left in place,
// as is any branch this call was not asked to delete. Errors are ignored.
func (g Git) DropLaunch(ctx context.Context, repo, worktree, branch, base string, dropWorktree, dropBranch bool) {
	repo = strings.TrimSpace(repo)
	branch = shortBranch(branch)
	base = strings.TrimSpace(base)
	if repo == "" {
		return
	}
	if branch != "" && base != "" {
		exists, err := g.RefExists(ctx, repo, branch)
		if err != nil || exists {
			head, herr := runGit(ctx, repo, "rev-parse", headRef(branch))
			baseSHA, berr := runGit(ctx, repo, "rev-parse", base)
			if herr != nil || berr != nil {
				dropBranch = false
			} else if head != baseSHA {
				return
			}
		}
	} else if base == "" {
		dropBranch = false
	}
	if dropWorktree && strings.TrimSpace(worktree) != "" {
		_ = Git{}.Remove(ctx, worktree)
	}
	if !dropBranch || branch == "" {
		return
	}
	exists, err := g.RefExists(ctx, repo, branch)
	if err != nil || !exists {
		return
	}
	_, _ = runGit(ctx, repo, "update-ref", "-d", headRef(branch))
}

func headRef(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "refs/") {
		return name
	}
	return "refs/heads/" + name
}

func shortBranch(name string) string {
	name = strings.TrimSpace(name)
	return strings.TrimPrefix(name, "refs/heads/")
}
