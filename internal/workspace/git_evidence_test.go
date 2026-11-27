package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestM3WorkspaceGitEvidence(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "wt")
	req := CreateRequest{CardID: "c-m3", AttemptID: "a-m3", ObjectiveID: "o", RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main"}

	t.Run("missing repository", func(t *testing.T) {
		bad := req
		bad.RepoPath = t.TempDir()
		_, err := (Git{}).Create(ctx, bad)
		assertGitEvidence(t, err, "worktree add", "not_repository", req, filepath.Join(root, "c-m3-a-m3"))
	})

	t.Run("missing repository directory", func(t *testing.T) {
		bad := req
		bad.RepoPath = filepath.Join(t.TempDir(), "no-checkout")
		_, err := (Git{}).Create(ctx, bad)
		assertGitEvidence(t, err, "worktree add", "not_repository", req, filepath.Join(root, "c-m3-a-m3"))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing directory not preserved: %v", err)
		}
	})

	t.Run("missing base", func(t *testing.T) {
		bad := req
		bad.StartPoint = "private-basename-do-not-print"
		_, err := (Git{}).Create(ctx, bad)
		assertGitEvidence(t, err, "rev-parse", "missing_base", req, filepath.Join(root, "c-m3-a-m3"))
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() == 0 {
			t.Fatalf("Git exit status not preserved: %v", err)
		}
		if strings.Contains(err.Error(), bad.StartPoint) {
			t.Fatalf("Git argument leaked: %v", err)
		}
	})

	t.Run("other error without command arguments or output", func(t *testing.T) {
		gctx := WithGitEvidence(ctx, req.CardID, req.AttemptID, repo)
		_, err := runGit(gctx, repo, "-c", "http.extraHeader=Authorization: Bearer m3-synthetic-token", "status", "--m3-synthetic-option")
		assertGitEvidence(t, err, "status", "other", req, repo)
		if strings.Contains(err.Error(), "m3-synthetic-") || !strings.Contains(err.Error(), "Git printed command usage") {
			t.Fatalf("argv, stderr or irrelevant reason: %v", err)
		}
	})

	t.Run("successful checkout and diff", func(t *testing.T) {
		w, err := (Git{}).Create(ctx, req)
		if err != nil || w.BaseCommit != gitOut(t, repo, "rev-parse", "main") || w.Path != filepath.Join(root, "c-m3-a-m3") {
			t.Fatalf("worktree %+v, %v", w, err)
		}
		diff, err := (Git{}).Diff(ctx, w.Path, w.BaseCommit)
		if err != nil || len(diff.Files) != 0 || diff.Patch != "" {
			t.Fatalf("diff %+v, %v", diff, err)
		}
	})

	t.Run("unmerged checkout", func(t *testing.T) {
		prior, err := (Git{}).Create(ctx, CreateRequest{CardID: "conflict", AttemptID: "prior", ObjectiveID: "o", RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main"})
		if err != nil {
			t.Fatal(err)
		}
		writeAndCommit := func(dir, content string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			gitRun(t, dir, "add", "same.txt")
			gitRun(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", content)
		}
		writeAndCommit(repo, "main change")
		writeAndCommit(prior.Path, "card change")
		out, err := exec.Command("git", "-C", prior.Path, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "merge", "main").CombinedOutput()
		if err == nil || !strings.Contains(string(out), "CONFLICT") {
			t.Fatalf("expected actual merge conflict: %v %s", err, out)
		}
		next := req
		next.CardID, next.Continue = "conflict", true
		_, err = (Git{}).Create(ctx, next)
		assertGitEvidence(t, err, "checkout", "conflict", next, filepath.Join(root, "conflict-a-m3"))
		if !strings.Contains(err.Error(), "dir="+strconv.Quote(prior.Path)) {
			t.Fatalf("missing prior Git invocation directory: %v", err)
		}
		if gitOut(t, prior.Path, "ls-files", "-u") == "" {
			t.Fatal("diagnostic changed conflict/index state")
		}
	})
}

func assertGitEvidence(t *testing.T, err error, command, kind string, req CreateRequest, worktree string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected Git failure")
	}
	s := err.Error()
	for _, field := range []string{"command=\"git " + command + "\"", "kind=" + kind, "card=\"" + req.CardID + "\"", "attempt=\"" + req.AttemptID + "\"", "worktree=" + strconv.Quote(worktree)} {
		if !strings.Contains(s, field) {
			t.Errorf("missing %s in %q", field, s)
		}
	}
	if strings.Contains(strings.ToLower(s), "usage:") || strings.Contains(s, "\n") || len(s) > 1024 {
		t.Fatalf("expected one short line without usage: %q", s)
	}
}
