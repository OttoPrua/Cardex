package loop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/roots"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

// Only this marked lane qualifies an exact integration commit. A fresh
// owned local clone performs normal Git materialization of the commit's own
// attributes under sealed settings (not raw blob-byte equality). No source
// worktree, admin attributes, hooks, local filters, or writable objects are
// shared. User-authored acceptance commands are not sandboxed by this check.
func (st *step) testComparisonAcceptance(pin comparisonAcceptancePin) (bool, error) {
	ctx := proc.WithExactGitObjects(st.ctx)
	repo := st.snap.project.RepoPath
	if !pin.valid() || !workspace.VerifyOptionalAcceptanceRepository(ctx, repo, st.comparisonWorktreeRoot()) {
		return false, nil
	}
	scratch := roots.Load(st.home).EffectiveScratch(st.snap.project.ScratchDir)
	var parent string
	var cleanup func() error
	var err error
	env := os.Environ()
	if strings.TrimSpace(scratch) == "" {
		parent, err = os.MkdirTemp("", "cardex-comparison-accept-")
		if err != nil {
			return false, err
		}
		cleanup = func() error { return os.RemoveAll(parent) }
	} else {
		parent, cleanup, err = roots.PrepareAttemptScratch(scratch, "comparison-accept")
		if err != nil {
			return false, err
		}
		env = roots.ApplyTempEnv(env, parent)
	}
	defer cleanup()
	// Canonicalize just our new directory, never the configured repo.
	physical, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return false, err
	}
	dir := filepath.Join(physical, "candidate")
	clone := exactAcceptanceGit(ctx, physical, "clone", "--no-local", "--no-checkout", "--no-tags", "--template=", "--", repo, dir)
	// Exactly this invocation may use Git's file transport with the
	// already verified literal local path; no network protocols allowed.
	clone.Env = append(clone.Env, "GIT_ALLOW_PROTOCOL=file")
	if err := clone.Run(); err != nil {
		return false, err
	}
	if _, err := acceptGit(ctx, dir, "remote", "remove", "origin"); err != nil {
		return false, err
	}
	checkout, ok := observeComparisonCheckout(dir)
	if !ok || !checkout.same(ctx, dir) {
		return false, nil
	}
	if typ, err := acceptGit(ctx, dir, "cat-file", "-t", pin.OID); err != nil || strings.TrimSpace(typ) != "commit" {
		return false, nil
	}
	if _, err := acceptGit(ctx, dir, "checkout", "--detach", pin.OID); err != nil {
		return false, err
	}
	atExactCommit := func() bool {
		if !checkout.same(ctx, dir) {
			return false
		}
		_, symErr := acceptGit(ctx, dir, "symbolic-ref", "--quiet", "HEAD")
		var exit *exec.ExitError
		if !errors.As(symErr, &exit) || exit.ExitCode() != 1 {
			return false
		}
		head, err := acceptGit(ctx, dir, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
		return err == nil && strings.TrimSpace(head) == pin.OID
	}
	if !atExactCommit() {
		return false, nil
	}
	for _, item := range st.snap.obj.Acceptance.Commands {
		cmd := proc.Shell(ctx, item)
		cmd.Dir = dir
		cmd.Env = exactAcceptanceGitEnv(proc.AcceptanceEnv(proc.CompatibleChildEnv(ctx, env)))
		if err := cmd.Run(); err != nil || !atExactCommit() {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			return false, nil
		}
	}
	return true, nil
}

type comparisonCheckout struct {
	dir, git os.FileInfo
	config   []byte
}

func observeComparisonCheckout(dir string) (comparisonCheckout, bool) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return comparisonCheckout{}, false
	}
	admin := filepath.Join(dir, ".git")
	git, err := os.Lstat(admin)
	if err != nil || !git.IsDir() {
		return comparisonCheckout{}, false
	}
	for _, rel := range []string{"info/attributes", "info/grafts", "objects/info/alternates", "objects/info/http-alternates", "config.worktree"} {
		if _, err := os.Lstat(filepath.Join(admin, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			return comparisonCheckout{}, false
		}
	}
	configPath := filepath.Join(admin, "config")
	cfgInfo, err := os.Lstat(configPath)
	if err != nil || !cfgInfo.Mode().IsRegular() {
		return comparisonCheckout{}, false
	}
	f, err := os.Open(configPath)
	if err != nil {
		return comparisonCheckout{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !os.SameFile(fi, cfgInfo) {
		return comparisonCheckout{}, false
	}
	const configLimit = 128 << 10
	cfg, err := io.ReadAll(io.LimitReader(f, configLimit+1))
	if err != nil || len(cfg) > configLimit {
		return comparisonCheckout{}, false
	}
	return comparisonCheckout{dir: info, git: git, config: cfg}, true
}

func (c comparisonCheckout) same(ctx context.Context, dir string) bool {
	got, ok := observeComparisonCheckout(dir)
	return ok && os.SameFile(c.dir, got.dir) && os.SameFile(c.git, got.git) && bytes.Equal(c.config, got.config) &&
		workspace.VerifyOptionalAcceptanceRepository(ctx, dir)
}
