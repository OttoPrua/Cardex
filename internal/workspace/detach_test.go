package workspace

import (
	"context"
	"testing"
)

func TestCreateDetachedMakesNoBranch(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	head := gitOut(t, repo, "rev-parse", "HEAD")
	wt, err := Git{}.Create(ctx, CreateRequest{
		CardID: "c1", AttemptID: "a1", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: t.TempDir(), IntegrationBranch: "main",
		StartPoint: head, Detach: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if wt.Branch != "" || wt.BaseCommit != head {
		t.Fatalf("worktree %+v want detached at %s", wt, head)
	}
	if out := gitOut(t, repo, "branch", "--list", "cardex/*"); out != "" {
		t.Fatalf("detached create made a branch: %q", out)
	}
	if out := gitOut(t, wt.Path, "rev-parse", "--abbrev-ref", "HEAD"); out != "HEAD" {
		t.Fatalf("worktree HEAD %q, want detached", out)
	}
}
