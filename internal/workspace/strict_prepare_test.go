package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStrictPreparationStaleCleanupCannotChangeWinner(t *testing.T) {
	repo := initRepo(t)
	g := Git{}
	ctx := t.Context()
	root := filepath.Join(t.TempDir(), "absent")
	req := CreateRequest{CardID: "c1", AttemptID: "same-attempt", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main", Strict: true}
	if _, err := g.Create(ctx, req); !errors.Is(err, ErrStrictPreparationLost) {
		t.Fatalf("unowned create: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unowned create touched checkout root: %v", err)
	}
	old, err := g.ReserveStrictPreparation(ctx, repo, req.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate manual recovery after verified quiescence, before a stale
	// cleanup callback becomes reachable. Reusing an attempt ID must not
	// give that callback authority over the winner.
	if err := os.Remove(old.marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(old.dir); err != nil {
		t.Fatal(err)
	}
	winner, err := g.ReserveStrictPreparation(ctx, repo, req.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer winner.Release()
	req.StrictOwner = winner
	wt, err := g.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := gitOut(t, repo, "worktree", "list", "--porcelain")
	g.DropStrictLaunch(ctx, old, req, wt.Path, wt.Branch, wt.BaseCommit)
	old.Release()
	if _, err := g.ReserveStrictPreparation(ctx, wt.Path, "third"); !errors.Is(err, ErrStrictPreparationBusy) {
		t.Fatalf("stale callback released the winner or linked path bypassed slot: %v", err)
	}
	if gitOut(t, wt.Path, "symbolic-ref", "HEAD") != "refs/heads/"+wt.Branch ||
		gitOut(t, repo, "worktree", "list", "--porcelain") != snapshot {
		t.Fatal("stale owner changed winner's checkout")
	}
}

func TestStrictPreparationDetachedReadDoesNotDetachCurrentCheckout(t *testing.T) {
	repo := initRepo(t)
	ctx := t.Context()
	g := Git{}
	req := CreateRequest{CardID: "c", AttemptID: "writer", ObjectiveID: "o",
		RepoPath: repo, WorktreeRoot: t.TempDir(), IntegrationBranch: "main"}
	writer, err := g.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.AttemptID, req.StartPoint, req.Detach, req.Strict = "reader", "refs/heads/"+writer.Branch, true, true
	reader, err := g.ReserveStrictPreparation(ctx, repo, req.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	req.StrictOwner = reader
	readTree, err := g.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if readTree.Branch != "" || readTree.BaseCommit != writer.BaseCommit ||
		gitOut(t, writer.Path, "symbolic-ref", "HEAD") != "refs/heads/"+writer.Branch {
		t.Fatalf("detached reader changed writer: %+v", readTree)
	}
}
