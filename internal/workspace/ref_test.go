package workspace

import (
	"context"
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
	got, err := g.EnsureIntegrationBranch(ctx, repo, "cardex/obj/main")
	if err != nil {
		t.Fatal(err)
	}
	if got != mainSHA {
		t.Fatalf("integration %s want %s", got, mainSHA)
	}
	if gitOut(t, repo, "rev-parse", "refs/heads/main") != mainSHA {
		t.Fatal("main moved")
	}
	again, err := g.EnsureIntegrationBranch(ctx, repo, "cardex/obj/main")
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
			sha, err := g.EnsureIntegrationBranch(ctx, repo, "cardex/overlap/main")
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
	got, err := g.EnsureIntegrationBranch(ctx, repo, "cardex/obj/main")
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
	got, err := g.EnsureIntegrationBranch(ctx, repo, "cardex/obj/main")
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
	_, err := g.EnsureIntegrationBranch(ctx, repo, "cardex/obj/main")
	if err == nil || !strings.Contains(err.Error(), "workspace: no main or master branch") {
		t.Fatalf("err %v", err)
	}
	if ok, exErr := g.RefExists(ctx, repo, "cardex/obj/main"); exErr != nil || ok {
		t.Fatalf("ref exists %v %v", ok, exErr)
	}
}

func TestDropLaunchKeepsCommittedBranch(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	var g Git
	if _, err := g.EnsureIntegrationBranch(ctx, repo, "cardex/obj/main"); err != nil {
		t.Fatal(err)
	}
	mainSHA := gitOut(t, repo, "rev-parse", "refs/heads/main")
	wt, err := g.Create(ctx, CreateRequest{
		CardID: "c", AttemptID: "a1", ObjectiveID: "obj", RepoPath: repo,
		WorktreeRoot: filepath.Join(t.TempDir(), "wt"), IntegrationBranch: "cardex/obj/main",
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
		WorktreeRoot: filepath.Join(t.TempDir(), "wt"), IntegrationBranch: "cardex/obj/main",
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
		WorktreeRoot: filepath.Join(t.TempDir(), "wt"), IntegrationBranch: "cardex/obj/main",
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
