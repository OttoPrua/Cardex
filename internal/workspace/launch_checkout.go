package workspace

import (
	"context"
	"os"
	"path/filepath"
)

// LaunchCheckout is in-memory evidence from a successful linked checkout
// creation. A path, branch name, or unchanged commit alone is not ownership.
type LaunchCheckout struct {
	path, common, admin, head, headBinding string
	rootInfo, dotInfo, adminInfo           os.FileInfo
	marker                                 string
	markerInfo                             os.FileInfo
}

func (Git) CaptureLaunchCheckout(ctx context.Context, repo, path, base string) *LaunchCheckout {
	proof := inspectLaunchCheckout(ctx, repo, path, base)
	if proof == nil {
		return nil
	}
	marker, err := os.CreateTemp(proof.admin, ".cardex-launch-")
	if err != nil {
		return nil
	}
	proof.marker = marker.Name()
	proof.markerInfo, err = marker.Stat()
	closeErr := marker.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(proof.marker)
		return nil
	}
	return proof
}

func inspectLaunchCheckout(ctx context.Context, repo, path, base string) *LaunchCheckout {
	if !filepath.IsAbs(path) || base == "" {
		return nil
	}
	path = filepath.Clean(path)
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || physical != path {
		return nil
	}
	common, err := exactLaunchCommonDir(ctx, repo)
	if err != nil {
		return nil
	}
	ownCommon, err := exactLaunchCommonDir(ctx, path)
	if err != nil || ownCommon != common {
		return nil
	}
	admin, err := scopedReviewGit(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil || filepath.Dir(admin) != filepath.Join(common, "worktrees") {
		return nil
	}
	root, rootErr := os.Lstat(path)
	dot, dotErr := os.Lstat(filepath.Join(path, ".git"))
	ad, adErr := os.Lstat(admin)
	head, headErr := scopedReviewGit(ctx, path, "rev-parse", "--verify", "HEAD")
	binding, bindingErr := scopedReviewGit(ctx, path, "rev-parse", "--symbolic-full-name", "HEAD")
	if rootErr != nil || dotErr != nil || adErr != nil || headErr != nil || bindingErr != nil || binding == "" || head != base ||
		!root.IsDir() || !dot.Mode().IsRegular() || !ad.IsDir() {
		return nil
	}
	return &LaunchCheckout{path: path, common: common, admin: admin, head: head, headBinding: binding, rootInfo: root, dotInfo: dot, adminInfo: ad}
}

func exactLaunchCommonDir(ctx context.Context, dir string) (string, error) {
	common, err := scopedReviewGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(common)
}

func (proof *LaunchCheckout) matches() bool {
	for path, previous := range map[string]os.FileInfo{
		proof.path: proof.rootInfo, filepath.Join(proof.path, ".git"): proof.dotInfo, proof.admin: proof.adminInfo, proof.marker: proof.markerInfo,
	} {
		current, err := os.Lstat(path)
		if err != nil || !os.SameFile(previous, current) || previous.Mode().Type() != current.Mode().Type() {
			return false
		}
	}
	return true
}

// Release removes only this launch's own short-lived marker.
func (proof *LaunchCheckout) Release() {
	if proof != nil && proof.matches() {
		_ = os.Remove(proof.marker)
	}
}

// DropOwnedLaunchCheckout is called under the scheduler's writer barrier.
// Never use force or remove a branch. External Git operations are not
// serialized by that barrier; changed or unprovable identity is retained.
func (Git) DropOwnedLaunchCheckout(ctx context.Context, repo, path string, proof *LaunchCheckout) {
	if proof == nil || filepath.Clean(path) != proof.path || !proof.matches() {
		return
	}
	current := inspectLaunchCheckout(ctx, repo, path, proof.head)
	if current == nil || current.common != proof.common || current.admin != proof.admin || current.headBinding != proof.headBinding {
		return
	}
	// Reuse the conservative full-index proof. A clean status alone trusts
	// assume-unchanged and skip-worktree, and can hide materialized bytes.
	if !visibleReviewIndex(ctx, proof.path) {
		return
	}
	status, err := scopedReviewGit(ctx, proof.path, "status", "--porcelain=v1", "-z",
		"--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if err != nil || status != "" || !proof.matches() {
		return
	}
	_, _ = scopedReviewGit(ctx, repo, "worktree", "remove", proof.path)
}
