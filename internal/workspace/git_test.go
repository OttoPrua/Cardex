package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateNamesBaseAndExclude(t *testing.T) {
	repo := initRepo(t)
	root := t.TempDir()
	ctx := context.Background()
	m := Git{}

	wt, err := m.Create(ctx, CreateRequest{
		CardID: "card1", AttemptID: "a1", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "card1-a1")
	if wt.Path != wantPath {
		t.Fatalf("path %s want %s", wt.Path, wantPath)
	}
	if wt.Branch != "cardex/obj/c/card1" {
		t.Fatalf("branch %s", wt.Branch)
	}
	mainHead := gitOut(t, repo, "rev-parse", "HEAD")
	if wt.BaseCommit != mainHead {
		t.Fatalf("base %s want integration HEAD %s", wt.BaseCommit, mainHead)
	}
	exclude := gitOut(t, wt.Path, "rev-parse", "--absolute-git-dir")
	body, err := os.ReadFile(filepath.Join(exclude, "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), ".cardex-result.md") {
		t.Fatalf("exclude missing result file:\n%s", body)
	}

	if err := os.WriteFile(filepath.Join(wt.Path, "keep.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, ".cardex-result.md"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := m.Commit(ctx, wt.Path, "keep")
	if err != nil {
		t.Fatal(err)
	}
	if sha == "" || sha == mainHead {
		t.Fatalf("commit sha %q", sha)
	}
	names := gitOut(t, wt.Path, "show", "--name-only", "--pretty=format:", "HEAD")
	if strings.Contains(names, ".cardex-result.md") {
		t.Fatalf("result file was committed:\n%s", names)
	}
	if !strings.Contains(names, "keep.txt") {
		t.Fatalf("keep.txt missing from commit:\n%s", names)
	}

	diff, err := m.Diff(ctx, wt.Path, mainHead)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(diff.Files, "keep.txt") {
		t.Fatalf("diff files %v", diff.Files)
	}
	if strings.Contains(strings.Join(diff.Files, "\n"), ".cardex-result.md") {
		t.Fatalf("diff listed result file: %v", diff.Files)
	}
	if !strings.Contains(diff.Patch, "hello") {
		t.Fatalf("patch missing change:\n%s", diff.Patch)
	}
}

func TestCommitCleanTree(t *testing.T) {
	repo := initRepo(t)
	root := t.TempDir()
	ctx := context.Background()
	m := Git{}
	wt, err := m.Create(ctx, CreateRequest{
		CardID: "card", AttemptID: "a", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	before := gitOut(t, wt.Path, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(wt.Path, ".cardex-result.md"), []byte("only result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := m.Commit(ctx, wt.Path, "nothing")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "" {
		t.Fatalf("clean tree committed %s", sha)
	}
	after := gitOut(t, wt.Path, "rev-parse", "HEAD")
	if after != before {
		t.Fatalf("HEAD moved %s -> %s", before, after)
	}
}

func TestCreateContinuationUsesCardBranch(t *testing.T) {
	repo := initRepo(t)
	root := t.TempDir()
	ctx := context.Background()
	m := Git{}
	first, err := m.Create(ctx, CreateRequest{
		CardID: "card", AttemptID: "a1", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "card.txt"), []byte("on card\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cardHead, err := m.Commit(ctx, first.Path, "card work")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, first.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("on main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "main.txt")
	gitRun(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "advance main")
	mainHead := gitOut(t, repo, "rev-parse", "HEAD")
	if mainHead == cardHead {
		t.Fatal("fixture did not move integration HEAD")
	}

	next, err := m.Create(ctx, CreateRequest{
		CardID: "card", AttemptID: "a2", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main", Continue: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Branch != "cardex/obj/c/card" {
		t.Fatalf("branch %s", next.Branch)
	}
	if next.BaseCommit != cardHead {
		t.Fatalf("continuation base %s want card HEAD %s (integration %s)", next.BaseCommit, cardHead, mainHead)
	}
	if next.Path != filepath.Join(root, "card-a2") {
		t.Fatalf("path %s", next.Path)
	}
}

func TestRemoveKeepsBranch(t *testing.T) {
	repo := initRepo(t)
	root := t.TempDir()
	ctx := context.Background()
	m := Git{}
	wt, err := m.Create(ctx, CreateRequest{
		CardID: "card", AttemptID: "a", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, wt.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	got := gitOut(t, repo, "rev-parse", "--verify", wt.Branch)
	if got == "" {
		t.Fatal("card branch was deleted")
	}
}

func TestBlockPushIsolatesMainRepo(t *testing.T) {
	repo := initRepo(t)
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, repo, "remote", "add", "origin", bare)
	gitRun(t, repo, "push", "origin", "HEAD:refs/heads/main")
	seed := gitOut(t, bare, "rev-parse", "refs/heads/main")

	urlBefore := gitOut(t, repo, "config", "--local", "--get", "remote.origin.url")
	if urlBefore != bare {
		t.Fatalf("origin url %s", urlBefore)
	}
	if _, err := gitOptional(repo, "config", "--local", "--get", "remote.origin.pushurl"); err == nil {
		t.Fatal("main repo already has pushurl")
	}
	if _, err := gitOptional(repo, "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Fatal("main repo already has hooksPath")
	}

	root := t.TempDir()
	ctx := context.Background()
	m := Git{}
	wt, err := m.Create(ctx, CreateRequest{
		CardID: "card", AttemptID: "a", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.BlockPush(ctx, wt.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "pushed.txt"), []byte("nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Commit(ctx, wt.Path, "should not land"); err != nil {
		t.Fatal(err)
	}
	assertPushReadsWork(t, wt.Path, bare, "origin")

	push := exec.Command("git", "-C", wt.Path, "push", "origin", "HEAD:refs/heads/from-worktree")
	push.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := push.CombinedOutput()
	if err == nil {
		t.Fatalf("worktree push succeeded:\n%s", out)
	}
	if _, missing := gitOptional(bare, "rev-parse", "--verify", "refs/heads/from-worktree"); missing == nil {
		t.Fatal("bare remote accepted the worktree push")
	}
	after := gitOut(t, bare, "rev-parse", "refs/heads/main")
	if after != seed {
		t.Fatalf("bare main moved %s -> %s", seed, after)
	}

	urlAfter := gitOut(t, repo, "config", "--local", "--get", "remote.origin.url")
	if urlAfter != urlBefore {
		t.Fatalf("main origin url changed %s -> %s", urlBefore, urlAfter)
	}
	if _, err := gitOptional(repo, "config", "--local", "--get", "remote.origin.pushurl"); err == nil {
		t.Fatal("main repo pushurl was set")
	}
	if _, err := gitOptional(repo, "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Fatal("main repo hooksPath was set")
	}
	hooks := gitOut(t, wt.Path, "config", "--worktree", "--get", "core.hooksPath")
	if hooks == "" {
		t.Fatal("worktree hooksPath empty")
	}
	if _, err := os.Stat(filepath.Join(hooks, "pre-push")); err != nil {
		t.Fatal(err)
	}
	pushURL := gitOut(t, wt.Path, "config", "--worktree", "--get", "remote.origin.pushurl")
	if pushURL != blockedPushURL {
		t.Fatalf("worktree pushurl %s", pushURL)
	}
}

func TestBlockPushBlocksInheritedPushURLs(t *testing.T) {
	tests := []struct {
		name       string
		pushCounts []int
	}{
		{name: "single existing pushurl", pushCounts: []int{1}},
		{name: "several pushurls on one remote", pushCounts: []int{2}},
		{name: "several remotes", pushCounts: []int{1, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertInheritedPushBlocked(t, tc.pushCounts)
		})
	}
}

func assertInheritedPushBlocked(t *testing.T, pushCounts []int) {
	t.Helper()
	repo := initRepo(t)
	names := []string{"origin", "other", "extra"}
	if len(pushCounts) > len(names) {
		t.Fatalf("too many remotes")
	}
	type target struct {
		remote string
		bares  []string
		seed   map[string]string
	}
	var targets []target
	for i, n := range pushCounts {
		name := names[i]
		tg := target{remote: name, seed: map[string]string{}}
		for j := 0; j < n; j++ {
			bare := filepath.Join(t.TempDir(), name+"-"+[]string{"0", "1", "2"}[j]+".git")
			gitRun(t, "", "init", "--bare", bare)
			tg.bares = append(tg.bares, bare)
		}
		gitRun(t, repo, "remote", "add", name, tg.bares[0])
		for _, bare := range tg.bares {
			gitRun(t, repo, "config", "--local", "--add", "remote."+name+".pushurl", bare)
			gitRun(t, repo, "push", bare, "HEAD:refs/heads/probe")
			tg.seed[bare] = gitOut(t, bare, "rev-parse", "refs/heads/probe")
		}
		targets = append(targets, tg)
	}

	urlBefore := map[string]string{}
	pushBefore := map[string]string{}
	for _, tg := range targets {
		urlBefore[tg.remote] = gitOut(t, repo, "config", "--local", "--get", "remote."+tg.remote+".url")
		pushBefore[tg.remote] = gitOut(t, repo, "config", "--local", "--get-all", "remote."+tg.remote+".pushurl")
	}
	if _, err := gitOptional(repo, "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Fatal("main repo already has hooksPath")
	}

	root := t.TempDir()
	ctx := context.Background()
	m := Git{}
	wt, err := m.Create(ctx, CreateRequest{
		CardID: "card", AttemptID: "a", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "pushed.txt"), []byte("nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Commit(ctx, wt.Path, "should not land"); err != nil {
		t.Fatal(err)
	}
	if err := m.BlockPush(ctx, wt.Path); err != nil {
		t.Fatal(err)
	}

	for _, tg := range targets {
		assertPushReadsWork(t, wt.Path, tg.bares[0], tg.remote)
		for _, verify := range []bool{false, true} {
			args := []string{"-C", wt.Path, "push"}
			if verify {
				args = append(args, "--no-verify")
			}
			args = append(args, tg.remote, "HEAD:refs/heads/probe", "HEAD:refs/heads/probe-new")
			push := exec.Command("git", args...)
			push.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			out, err := push.CombinedOutput()
			if err == nil {
				t.Fatalf("push %s no-verify=%t succeeded:\n%s", tg.remote, verify, out)
			}
			if strings.Contains(string(out), "not allowed") {
				t.Fatalf("push %s failed by protocol denial:\n%s", tg.remote, out)
			}
			for _, bare := range tg.bares {
				got := gitOut(t, bare, "rev-parse", "refs/heads/probe")
				if got != tg.seed[bare] {
					t.Fatalf("%s probe moved %s -> %s\n%s", bare, tg.seed[bare], got, out)
				}
				if _, missing := gitOptional(bare, "rev-parse", "--verify", "refs/heads/probe-new"); missing == nil {
					t.Fatalf("%s created probe-new\n%s", bare, out)
				}
			}
		}
		direct := exec.Command("git", "-C", wt.Path, "push", "--no-verify", tg.bares[0], "HEAD:refs/heads/direct-new")
		direct.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := direct.CombinedOutput()
		if err == nil {
			t.Fatalf("direct push %s succeeded:\n%s", tg.bares[0], out)
		}
		if _, missing := gitOptional(tg.bares[0], "rev-parse", "--verify", "refs/heads/direct-new"); missing == nil {
			t.Fatalf("%s created direct-new\n%s", tg.bares[0], out)
		}
	}

	for _, tg := range targets {
		urlAfter := gitOut(t, repo, "config", "--local", "--get", "remote."+tg.remote+".url")
		if urlAfter != urlBefore[tg.remote] {
			t.Fatalf("main %s url changed %s -> %s", tg.remote, urlBefore[tg.remote], urlAfter)
		}
		pushAfter := gitOut(t, repo, "config", "--local", "--get-all", "remote."+tg.remote+".pushurl")
		if pushAfter != pushBefore[tg.remote] {
			t.Fatalf("main %s pushurl changed %q -> %q", tg.remote, pushBefore[tg.remote], pushAfter)
		}
	}
	if _, err := gitOptional(repo, "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Fatal("main repo hooksPath was set")
	}
	for _, key := range []string{"protocol.allow", "protocol.file.allow", "protocol.https.allow", "protocol.ssh.allow", "protocol.http.allow", "protocol.git.allow"} {
		if _, err := gitOptional(repo, "config", "--local", "--get", key); err == nil {
			t.Fatalf("main repo %s was set", key)
		}
	}
}

func assertPushReadsWork(t *testing.T, worktree, bare, remote string) {
	t.Helper()
	for _, args := range [][]string{
		{"ls-remote", bare},
		{"ls-remote", ".", "HEAD"},
		{"fetch", remote},
	} {
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		if strings.Contains(string(out), "not allowed") {
			t.Fatalf("git %s protocol denial:\n%s", strings.Join(args, " "), out)
		}
	}
}

func TestCreateContinuationKeepsPreviousWorktree(t *testing.T) {
	repo := initRepo(t)
	root := t.TempDir()
	ctx := context.Background()
	m := Git{}
	first, err := m.Create(ctx, CreateRequest{
		CardID: "card", AttemptID: "a1", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "card.txt"), []byte("on card\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cardHead, err := m.Commit(ctx, first.Path, "card work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("on main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "main.txt")
	gitRun(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "advance main")
	mainHead := gitOut(t, repo, "rev-parse", "HEAD")
	if mainHead == cardHead {
		t.Fatal("fixture did not move integration HEAD")
	}

	next, err := m.Create(ctx, CreateRequest{
		CardID: "card", AttemptID: "a2", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: root, IntegrationBranch: "main", Continue: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.BaseCommit != cardHead {
		t.Fatalf("continuation base %s want card HEAD %s (integration %s)", next.BaseCommit, cardHead, mainHead)
	}
	if head := gitOut(t, next.Path, "rev-parse", "HEAD"); head != cardHead {
		t.Fatalf("new worktree HEAD %s want %s", head, cardHead)
	}
	body, err := os.ReadFile(filepath.Join(first.Path, "card.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "on card\n" {
		t.Fatalf("previous worktree file %q", body)
	}
	if gitOut(t, repo, "rev-parse", first.Branch) != cardHead {
		t.Fatal("card branch commit is gone")
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatal(err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README")
	gitRun(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "seed")
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := gitOptional(dir, args...); err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOptional(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func gitOptional(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &gitErr{args: args, err: err, out: out}
	}
	return strings.TrimSpace(string(out)), nil
}

type gitErr struct {
	args []string
	err  error
	out  []byte
}

func (e *gitErr) Error() string {
	return e.err.Error() + ": " + strings.TrimSpace(string(e.out))
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
