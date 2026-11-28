package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// A stale slot is intentionally busy too: a crash can leave a Git child
	// behind, so the absence of its scheduler PID is not proof of quiescence.
	ErrStrictPreparationBusy = errors.New("workspace: strict preparation slot held; recover only after verifying the owner and its Git children have exited")
	ErrStrictPreparationLost = errors.New("workspace: strict preparation ownership is not current")
	ErrStrictCheckoutOwned   = errors.New("workspace: strict continuation found an unverified branch checkout")
)

const strictPreparationDir = "cardex-strict-preparation"

// StrictPreparation is a durable, repository-wide Git-preparation slot. A
// linked checkout and the main repository resolve to the same common Git dir.
// The slot intentionally has no timeout or automatic take-over.
type StrictPreparation struct {
	common  string
	dir     string
	attempt string
	marker  string
	once    sync.Once
}

func canonicalGitDir(ctx context.Context, repo string) (string, error) {
	dir, err := gitCommonDir(ctx, repo)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(dir)
}

// ReserveStrictPreparation must run after read-only routing, but before any
// shared ref or worktree mutation. An occupied slot never reaches git add,
// checkout, or branch creation.
func (Git) ReserveStrictPreparation(ctx context.Context, repo, attempt string) (*StrictPreparation, error) {
	if attempt == "" || len(attempt) > 100 || strings.IndexFunc(attempt, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) >= 0 {
		return nil, ErrStrictPreparationLost
	}
	common, err := canonicalGitDir(ctx, repo)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(common, strictPreparationDir)
	if err := os.Mkdir(dir, 0o700); errors.Is(err, os.ErrExist) {
		return nil, ErrStrictPreparationBusy
	} else if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(dir, "owner-"+attempt+"-")
	if err != nil {
		// No permission to start; keep an uncertain slot fail-closed.
		return nil, err
	}
	host, _ := os.Hostname()
	_, err = fmt.Fprintf(file, "attempt=%s\nhost=%s\npid=%d\nstaged=%s\n", attempt, host, os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	return &StrictPreparation{common: common, dir: dir, attempt: attempt, marker: file.Name()}, nil
}

func (p *StrictPreparation) held(ctx context.Context, repo, attempt string) error {
	if p == nil || p.attempt != attempt {
		return ErrStrictPreparationLost
	}
	common, err := canonicalGitDir(ctx, repo)
	if err != nil {
		return err
	}
	if common != p.common {
		return ErrStrictPreparationLost
	}
	dir, err := os.Lstat(p.dir)
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		return ErrStrictPreparationLost
	}
	info, err := os.Lstat(p.marker)
	if err != nil || !info.Mode().IsRegular() {
		return ErrStrictPreparationLost
	}
	return nil
}

// Release follows return from all this owner's synchronous Git operations.
// Removing this owner's specific marker first prevents a stale cleanup from
// releasing a winner's slot. Never RemoveAll the slot or an unrelated marker.
func (p *StrictPreparation) Release() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		if err := os.Remove(p.marker); err == nil {
			_ = os.Remove(p.dir)
		}
	})
}

// CheckStrictContinuation is read-only. Call before even creating the
// integration ref; Create checks again immediately before detaching anything.
func (g Git) CheckStrictContinuation(ctx context.Context, req CreateRequest) error {
	if err := req.StrictOwner.held(ctx, req.RepoPath, req.AttemptID); err != nil {
		return err
	}
	if req.Continue && !req.Detach {
		_, err := strictCheckedOutPath(ctx, req.RepoPath, "cardex/"+req.ObjectiveID+"/c/"+req.CardID, req.ClosedCheckouts)
		return err
	}
	return nil
}

func (g Git) EnsureStrictIntegrationBranch(ctx context.Context, req CreateRequest) (string, error) {
	if err := req.StrictOwner.held(ctx, req.RepoPath, req.AttemptID); err != nil {
		return "", err
	}
	return g.EnsureIntegrationBranch(ctx, req.RepoPath, req.IntegrationBranch)
}

// DropStrictLaunch acts only for the slot owner and only on its exact
// checkout. Shared branch refs are never deleted by strict cleanup.
func (g Git) DropStrictLaunch(ctx context.Context, owner *StrictPreparation, req CreateRequest, worktree, branch, base string) {
	if err := owner.held(ctx, req.RepoPath, req.AttemptID); err != nil {
		return
	}
	want := filepath.Join(req.WorktreeRoot, req.CardID+"-"+req.AttemptID)
	if filepath.Clean(worktree) != filepath.Clean(want) {
		return
	}
	g.DropLaunch(ctx, req.RepoPath, worktree, branch, base, true, false)
}

func strictCheckedOutPath(ctx context.Context, repo, branch string, closed []string) (string, error) {
	raw, err := runGitRaw(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", err
	}
	want := "refs/heads/" + branch
	var path, found string
	for _, field := range strings.Split(string(raw), "\x00") {
		if val, ok := strings.CutPrefix(field, "worktree "); ok {
			path = val
		} else if field == "branch "+want && path != "" {
			if found != "" {
				return "", ErrStrictCheckoutOwned
			}
			found = path
		}
	}
	if found == "" {
		return "", nil
	}
	for _, allowed := range closed {
		if filepath.Clean(allowed) == filepath.Clean(found) {
			return found, nil
		}
	}
	return "", ErrStrictCheckoutOwned
}
