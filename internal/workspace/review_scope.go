package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/OttoPrua/Cardex/internal/proc"
)

// VerifyReviewObjectScope attests to the exact detached, clean local checkout
// the read-only reviewer will use. Local object overlays are grounds to
// defer even though all queries here ignore them.
func VerifyReviewObjectScope(ctx context.Context, repo, worktree, commit string) bool {
	if repo == "" || worktree == "" || commit == "" {
		return false
	}
	common := func(root string) (os.FileInfo, string) {
		p, err := scopedReviewGit(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || !filepath.IsAbs(p) {
			return nil, ""
		}
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			return nil, ""
		}
		return info, p
	}
	ri, commonPath := common(repo)
	wi, _ := common(worktree)
	if ri == nil || wi == nil || !os.SameFile(ri, wi) {
		return false
	}
	top, err := scopedReviewGit(ctx, worktree, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil || !filepath.IsAbs(top) {
		return false
	}
	topInfo, topErr := os.Stat(top)
	worktreeInfo, worktreeErr := os.Stat(worktree)
	if topErr != nil || worktreeErr != nil || !topInfo.IsDir() || !os.SameFile(topInfo, worktreeInfo) {
		return false
	}
	if _, err := os.Lstat(filepath.Join(commonPath, "info", "grafts")); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	refs, err := scopedReviewGit(ctx, repo, "for-each-ref", "--count=1", "--format=%(refname)", "refs/replace/")
	if err != nil || refs != "" {
		return false
	}
	head, err := scopedReviewGit(ctx, worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != commit {
		return false
	}
	var exit *exec.ExitError
	_, err = scopedReviewGit(ctx, worktree, "symbolic-ref", "--quiet", "HEAD")
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return false
	}
	status, err := scopedReviewGit(ctx, worktree, "status", "--porcelain=v1", "-z",
		"--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	return err == nil && status == "" && visibleReviewIndex(ctx, worktree)
}

// git status trusts assume-unchanged/skip-worktree (and can report an
// unmaterialized sparse checkout as clean). Certify only a normal full
// index of visible stage-0 entries. This does not rewrite index flags.
func visibleReviewIndex(ctx context.Context, worktree string) bool {
	sparse, err := scopedReviewGit(ctx, worktree, "config", "--bool", "--get", "core.sparseCheckout")
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return false
		}
	} else if sparse != "false" {
		return false
	}
	entries, err := scopedReviewGit(ctx, worktree, "ls-files", "-v", "--stage", "--sparse", "-z")
	if err != nil {
		return false
	}
	for _, row := range strings.Split(entries, "\x00") {
		if row == "" {
			continue
		}
		header, name, found := strings.Cut(row, "\t")
		fields := strings.Fields(header)
		if !found || name == "" || len(fields) != 4 || fields[0] != "H" || fields[3] != "0" {
			return false
		}
		switch fields[1] {
		case "100644", "100755", "120000", "160000":
		default: // in particular, reject sparse directory (040000) records
			return false
		}
	}
	return true
}

func scopedReviewGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := proc.Git(proc.WithExactGitObjects(ctx), nil, append([]string{"-C", dir, "--no-replace-objects", "--no-optional-locks",
		"-c", "core.fsmonitor=false", "-c", "core.ignoreStat=false", "-c", "core.untrackedCache=false"}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
