package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEnsureIntegrationBranchFromMain(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	mainSHA := gitOut(t, repo, "rev-parse", "refs/heads/main")
	var g Git
	got, err := g.EnsureIntegrationBranch(ctx, repo, "github.com/OttoPrua/Cardex/obj/main")
	if err != nil {
		t.Fatal(err)
	}
	if got != mainSHA {
		t.Fatalf("integration %s want %s", got, mainSHA)
	}
	if gitOut(t, repo, "rev-parse", "refs/heads/main") != mainSHA {
		t.Fatal("main moved")
	}
	again, err := g.EnsureIntegrationBranch(ctx, repo, "github.com/OttoPrua/Cardex/obj/main")
	if err != nil {
		t.Fatal(err)
	}
	if again != mainSHA {
		t.Fatalf("reuse %s", again)
	}
	if n := gitOut(t, repo, "rev-list", "--count", "refs/heads/cardex/obj/main"); n != "1" {
		t.Fatalf("history %s", n)
	}
	names := gitOut(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads/cardex/obj/main")
	if names != "refs/heads/cardex/obj/main" {
		t.Fatalf("refs %q", names)
	}
}

func TestEnsureIntegrationBranchOverlap(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	mainSHA := gitOut(t, repo, "rev-parse", "refs/heads/main")
	var g Git
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sha, err := g.EnsureIntegrationBranch(ctx, repo, "github.com/OttoPrua/Cardex/overlap/main")
			if err != nil {
				errCh <- err
				return
			}
			if sha != mainSHA {
				errCh <- errSHA(sha)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if gitOut(t, repo, "rev-parse", "refs/heads/cardex/overlap/main") != mainSHA {
		t.Fatal("overlap tip")
	}
	if gitOut(t, repo, "rev-parse", "refs/heads/main") != mainSHA {
		t.Fatal("main moved")
	}
}

type errSHA string

func (e errSHA) Error() string { return "sha " + string(e) }

func TestEnsureIntegrationBranchFromMaster(t *testing.T) {
	ctx := context.Background()
	repo := initNamedRepo(t, "master")
	masterSHA := gitOut(t, repo, "rev-parse", "refs/heads/master")
	var g Git
	got, err := g.EnsureIntegrationBranch(ctx, repo, "github.com/OttoPrua/Cardex/obj/main")
	if err != nil {
		t.Fatal(err)
	}
	if got != masterSHA {
		t.Fatalf("integration %s want %s", got, masterSHA)
	}
	if gitOut(t, repo, "rev-parse", "refs/heads/master") != masterSHA {
		t.Fatal("master moved")
	}
}

func TestEnsureIntegrationBranchIgnoresMainTag(t *testing.T) {
	ctx := context.Background()
	repo := initNamedRepo(t, "master")
	gitRun(t, repo, "tag", "main", "HEAD")
	masterSHA := gitOut(t, repo, "rev-parse", "refs/heads/master")
	var g Git
	got, err := g.EnsureIntegrationBranch(ctx, repo, "github.com/OttoPrua/Cardex/obj/main")
	if err != nil {
		t.Fatal(err)
	}
	if got != masterSHA {
		t.Fatalf("picked %s want master %s", got, masterSHA)
	}
}

func TestEnsureIntegrationBranchNeitherPrimary(t *testing.T) {
	ctx := context.Background()
	repo := initNamedRepo(t, "side")
	var g Git
	_, err := g.EnsureIntegrationBranch(ctx, repo, "github.com/OttoPrua/Cardex/obj/main")
	if err == nil || !strings.Contains(err.Error(), "workspace: no main or master branch") {
		t.Fatalf("err %v", err)
	}
	if ok, exErr := g.RefExists(ctx, repo, "github.com/OttoPrua/Cardex/obj/main"); exErr != nil || ok {
		t.Fatalf("ref exists %v %v", ok, exErr)
	}
}

func TestEnsureIntegrationBranchIgnoresPrimaryAdvance(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	h1 := gitOut(t, repo, "rev-parse", "refs/heads/main")
	h2 := gitOut(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit-tree", "HEAD^{tree}", "-p", h1, "-m", "advance")
	installPrimaryAdvanceHook(t, repo, h2)
	var g Git
	got, err := g.EnsureIntegrationBranch(ctx, repo, "github.com/OttoPrua/Cardex/obj/main")
	if err != nil {
		t.Fatal(err)
	}
	if got != h1 {
		t.Fatalf("integration %s want %s", got, h1)
	}
	if tip := gitOut(t, repo, "rev-parse", "refs/heads/cardex/obj/main"); tip != h1 {
		t.Fatalf("ref %s want %s", tip, h1)
	}
	if main := gitOut(t, repo, "rev-parse", "refs/heads/main"); main != h2 {
		t.Fatalf("main %s want %s (hook did not advance primary)", main, h2)
	}
	again, err := g.EnsureIntegrationBranch(ctx, repo, "github.com/OttoPrua/Cardex/obj/main")
	if err != nil {
		t.Fatal(err)
	}
	if again != h1 {
		t.Fatalf("existing ref %s", again)
	}
	if main := gitOut(t, repo, "rev-parse", "refs/heads/main"); main != h2 {
		t.Fatalf("second ensure moved main to %s", main)
	}
}

func installPrimaryAdvanceHook(t *testing.T, repo, h2 string) {
	t.Helper()
	hooks := t.TempDir()
	empty := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" != "prepared" ]; then
  exit 0
fi
while read -r old new ref; do
  case "$ref" in
    refs/heads/cardex/*)
      git -C %q -c core.hooksPath=%q update-ref refs/heads/main %q || exit 1
      ;;
  esac
done
exit 0
`, repo, empty, h2)
	path := filepath.Join(hooks, "reference-transaction")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "config", "core.hooksPath", hooks)
}

func TestDropLaunchKeepsCommittedBranch(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	var g Git
	if _, err := g.EnsureIntegrationBranch(ctx, repo, "github.com/OttoPrua/Cardex/obj/main"); err != nil {
		t.Fatal(err)
	}
	mainSHA := gitOut(t, repo, "rev-parse", "refs/heads/main")
	wt, err := g.Create(ctx, CreateRequest{
		CardID: "c", AttemptID: "a1", ObjectiveID: "obj", RepoPath: repo,
		WorktreeRoot: filepath.Join(t.TempDir(), "wt"), IntegrationBranch: "github.com/OttoPrua/Cardex/obj/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	g.DropLaunch(ctx, repo, wt.Path, wt.Branch, wt.BaseCommit, true, true)
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree remains %v", err)
	}
	if ok, _ := g.RefExists(ctx, repo, wt.Branch); ok {
		t.Fatal("uncommitted branch remains")
	}
	if gitOut(t, repo, "rev-parse", "refs/heads/main") != mainSHA {
		t.Fatal("main moved")
	}
	if gitOut(t, repo, "rev-parse", "refs/heads/cardex/obj/main") != mainSHA {
		t.Fatal("integration deleted")
	}

	wt, err = g.Create(ctx, CreateRequest{
		CardID: "c", AttemptID: "a2", ObjectiveID: "obj", RepoPath: repo,
		WorktreeRoot: filepath.Join(t.TempDir(), "wt"), IntegrationBranch: "github.com/OttoPrua/Cardex/obj/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "keep.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt.Path, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "keep.txt")
	gitRun(t, wt.Path, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "keep")
	committed := gitOut(t, wt.Path, "rev-parse", "HEAD")
	g.DropLaunch(ctx, repo, wt.Path, wt.Branch, wt.BaseCommit, true, true)
	if gitOut(t, repo, "rev-parse", wt.Branch) != committed {
		t.Fatal("committed branch deleted")
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatal(err)
	}

	fresh, err := g.Create(ctx, CreateRequest{
		CardID: "d", AttemptID: "a3", ObjectiveID: "obj", RepoPath: repo,
		WorktreeRoot: filepath.Join(t.TempDir(), "wt"), IntegrationBranch: "github.com/OttoPrua/Cardex/obj/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	g.DropLaunch(ctx, repo, fresh.Path, fresh.Branch, fresh.BaseCommit, true, false)
	if ok, _ := g.RefExists(ctx, repo, fresh.Branch); !ok {
		t.Fatal("pre-existing flag deleted the branch")
	}
	if _, err := os.Stat(fresh.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree %v", err)
	}
}

func initNamedRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README")
	gitRun(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "seed")
	return dir
}
