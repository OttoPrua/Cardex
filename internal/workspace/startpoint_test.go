package workspace

import (
	"context"
	"strings"
	"testing"
)

func TestCreateStartsAtCardBase(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	first := gitOut(t, repo, "rev-parse", "HEAD")
	gitOut(t, repo, "branch", "feature")
	gitOut(t, repo, "commit", "--allow-empty", "-m", "main moves on")
	mainHead := gitOut(t, repo, "rev-parse", "HEAD")
	if mainHead == first {
		t.Fatal("main did not move")
	}

	wt, err := Git{}.Create(ctx, CreateRequest{
		CardID: "c1", AttemptID: "a1", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: t.TempDir(), IntegrationBranch: "main", StartPoint: "feature",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wt.BaseCommit != first {
		t.Fatalf("base %s want feature %s (main is %s)", wt.BaseCommit, first, mainHead)
	}

	_, err = Git{}.Create(ctx, CreateRequest{
		CardID: "c2", AttemptID: "a1", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: t.TempDir(), IntegrationBranch: "main", StartPoint: "no-such-ref",
	})
	if err == nil || !strings.Contains(err.Error(), "no-such-ref") {
		t.Fatalf("unknown base must fail loudly, got %v", err)
	}
}
