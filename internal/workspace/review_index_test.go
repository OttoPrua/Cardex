package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictReviewRejectsIndexFlagsThatHideMaterialization(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			repo := initRepo(t)
			head := gitOut(t, repo, "rev-parse", "HEAD")
			tree := filepath.Join(t.TempDir(), "detached")
			gitRun(t, repo, "worktree", "add", "--detach", tree, head)
			if !VerifyReviewObjectScope(t.Context(), repo, tree, head) {
				t.Fatal("clean ordinary index should attest")
			}
			gitRun(t, tree, "update-index", flag, "README")
			if err := os.WriteFile(filepath.Join(tree, "README"), []byte("DIFFERENT_HIDDEN_BYTES\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if status := gitOut(t, tree, "status", "--porcelain=v1"); status != "" {
				t.Fatalf("fixture must hide dirty bytes from status, got %q", status)
			}
			if VerifyReviewObjectScope(t.Context(), repo, tree, head) {
				t.Fatal("certified hidden different working-tree bytes")
			}
		})
	}
}

func TestStrictReviewRejectsSparseCheckoutWithUnavailablePaths(t *testing.T) {
	repo := initRepo(t)
	for _, dir := range []string{"included", "excluded"} {
		if err := os.Mkdir(filepath.Join(repo, dir), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, dir, "tracked.txt"), []byte(dir), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repo, "add", "included", "excluded")
	gitRun(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@invalid", "-c", "commit.gpgsign=false", "commit", "-m", "sparse fixture")
	head := gitOut(t, repo, "rev-parse", "HEAD")
	tree := filepath.Join(t.TempDir(), "detached")
	gitRun(t, repo, "worktree", "add", "--detach", tree, head)
	gitRun(t, tree, "sparse-checkout", "set", "--cone", "--sparse-index", "included")
	if _, err := os.Stat(filepath.Join(tree, "excluded", "tracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("fixture did not omit tracked bytes: %v", err)
	}
	if status := gitOut(t, tree, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("sparse fixture should appear clean, got %q", status)
	}
	if VerifyReviewObjectScope(t.Context(), repo, tree, head) {
		t.Fatal("certified partially materialized sparse checkout")
	}
}

func TestStrictReviewRejectsNonzeroIndexStages(t *testing.T) {
	repo := initRepo(t)
	head := gitOut(t, repo, "rev-parse", "HEAD")
	tree := filepath.Join(t.TempDir(), "detached")
	gitRun(t, repo, "worktree", "add", "--detach", tree, head)
	blob := gitOut(t, repo, "rev-parse", "HEAD:README")
	cmd := exec.Command("git", "update-index", "--index-info")
	cmd.Dir = tree
	cmd.Stdin = strings.NewReader(fmt.Sprintf("0 %s\tREADME\n100644 %s 1\tREADME\n100644 %s 2\tREADME\n", strings.Repeat("0", len(blob)), blob, blob))
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create nonzero index stages: %s %v", raw, err)
	}
	if stages := gitOut(t, tree, "ls-files", "--stage"); !strings.Contains(stages, " 1\tREADME") || !strings.Contains(stages, " 2\tREADME") {
		t.Fatalf("fixture did not create real index stages: %q", stages)
	}
	if visibleReviewIndex(t.Context(), tree) || VerifyReviewObjectScope(t.Context(), repo, tree, head) {
		t.Fatal("nonzero index stages were accepted")
	}
}
