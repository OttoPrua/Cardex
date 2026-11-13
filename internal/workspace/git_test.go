package workspace

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestBlockPushRefusesLongerPushInsteadOf(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		configArgs []string
		pushArgs   []string
		advertise  bool
	}{
		{name: "long prefix direct address", kind: "long-prefix"},
		{name: "long prefix signed false", kind: "long-prefix", pushArgs: []string{"--signed=false"}},
		{name: "long prefix no signed", kind: "long-prefix", pushArgs: []string{"--no-signed"}},
		{name: "long prefix signed if-asked", kind: "long-prefix", pushArgs: []string{"--signed=if-asked"}},
		{name: "long prefix gpgSign false", kind: "long-prefix", configArgs: []string{"-c", "push.gpgSign=false"}},
		{name: "long prefix signed false advertise", kind: "long-prefix", pushArgs: []string{"--signed=false"}, advertise: true},
		{name: "long prefix no signed advertise", kind: "long-prefix", pushArgs: []string{"--no-signed"}, advertise: true},
		{name: "long prefix signed if-asked advertise", kind: "long-prefix", pushArgs: []string{"--signed=if-asked"}, advertise: true},
		{name: "long prefix gpgSign false advertise", kind: "long-prefix", configArgs: []string{"-c", "push.gpgSign=false"}, advertise: true},
		{name: "other prefix", kind: "other-prefix"},
		{name: "other prefix signed false", kind: "other-prefix", pushArgs: []string{"--signed=false"}},
		{name: "other prefix no signed", kind: "other-prefix", pushArgs: []string{"--no-signed"}},
		{name: "other prefix signed if-asked", kind: "other-prefix", pushArgs: []string{"--signed=if-asked"}},
		{name: "other prefix gpgSign false", kind: "other-prefix", configArgs: []string{"-c", "push.gpgSign=false"}},
		{name: "other prefix signed false advertise", kind: "other-prefix", pushArgs: []string{"--signed=false"}, advertise: true},
		{name: "other prefix no signed advertise", kind: "other-prefix", pushArgs: []string{"--no-signed"}, advertise: true},
		{name: "other prefix signed if-asked advertise", kind: "other-prefix", pushArgs: []string{"--signed=if-asked"}, advertise: true},
		{name: "other prefix gpgSign false advertise", kind: "other-prefix", configArgs: []string{"-c", "push.gpgSign=false"}, advertise: true},
		{name: "long prefix push option signed false advertise", kind: "long-prefix", pushArgs: []string{"--push-option=review-probe", "--signed=false"}, advertise: true},
		{name: "long prefix push option no signed advertise", kind: "long-prefix", pushArgs: []string{"--push-option=review-probe", "--no-signed"}, advertise: true},
		{name: "long prefix push option signed if-asked advertise", kind: "long-prefix", pushArgs: []string{"--push-option=review-probe", "--signed=if-asked"}, advertise: true},
		{name: "long prefix short option signed false advertise", kind: "long-prefix", pushArgs: []string{"-o", "review-probe", "--signed=false"}, advertise: true},
		{name: "long prefix short option no signed advertise", kind: "long-prefix", pushArgs: []string{"-o", "review-probe", "--no-signed"}, advertise: true},
		{name: "long prefix short option signed if-asked advertise", kind: "long-prefix", pushArgs: []string{"-o", "review-probe", "--signed=if-asked"}, advertise: true},
		{name: "long prefix cleared push option signed false advertise", kind: "long-prefix", configArgs: []string{"-c", "push.pushOption=", "-c", "push.pushOption=review-probe"}, pushArgs: []string{"--signed=false"}, advertise: true},
		{name: "long prefix cleared push option no signed advertise", kind: "long-prefix", configArgs: []string{"-c", "push.pushOption=", "-c", "push.pushOption=review-probe"}, pushArgs: []string{"--no-signed"}, advertise: true},
		{name: "long prefix cleared push option signed if-asked advertise", kind: "long-prefix", configArgs: []string{"-c", "push.pushOption=", "-c", "push.pushOption=review-probe"}, pushArgs: []string{"--signed=if-asked"}, advertise: true},
		{name: "long prefix push option gpgSign false advertise", kind: "long-prefix", configArgs: []string{"-c", "push.gpgSign=false"}, pushArgs: []string{"--push-option=review-probe"}, advertise: true},
		{name: "long prefix push option followTags false advertise", kind: "long-prefix", configArgs: []string{"-c", "push.followTags=false"}, pushArgs: []string{"--push-option=review-probe", "--signed=false"}, advertise: true},
		{name: "direct bare path", kind: "direct"},
		{name: "dot", kind: "dot"},
		{name: "relative path", kind: "relative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertLongerPushInsteadOfRefused(t, tc.kind, tc.configArgs, tc.pushArgs, tc.advertise)
		})
	}
}

func TestBlockPushKeepsReadAddress(t *testing.T) {
	type readOp struct {
		name   string
		args   []string
		getURL bool
	}
	type pushOp struct {
		name     string
		direct   bool
		noVerify bool
	}
	reads := []readOp{
		{name: "ls-remote --get-url", args: []string{"ls-remote", "--get-url"}, getURL: true},
		{name: "ls-remote", args: []string{"ls-remote"}},
		{name: "fetch", args: []string{"fetch"}},
	}
	pushes := []pushOp{
		{name: "inherited push"},
		{name: "inherited push --no-verify", noVerify: true},
		{name: "direct push", direct: true},
		{name: "direct push --no-verify", direct: true, noVerify: true},
	}
	for _, scheme := range []string{"http", "https", "ftp", "ftps"} {
		t.Run(scheme, func(t *testing.T) {
			fx := newSchemeReadFixture(t, scheme)
			for _, read := range reads {
				t.Run(read.name, func(t *testing.T) {
					fx.assertRead(t, read.getURL, append(append([]string{}, read.args...), fx.url)...)
				})
			}
			for _, push := range pushes {
				t.Run(push.name, func(t *testing.T) {
					fx.assertPush(t, push.direct, push.noVerify)
				})
			}
		})
	}
}

type schemeReadFixture struct {
	repo, bare, worktree, url string
	seed, before              string
	urlBefore, pushBefore     string
	hits                      *atomic.Int32
}

func newSchemeReadFixture(t *testing.T, scheme string) *schemeReadFixture {
	t.Helper()
	repo := initRepo(t)
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, repo, "remote", "add", "origin", bare)
	gitRun(t, repo, "push", bare, "HEAD:refs/heads/probe")
	seed := gitOut(t, bare, "rev-parse", "refs/heads/probe")
	before := gitOut(t, bare, "show-ref")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var hits atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			c.Close()
		}
	}()
	url := scheme + "://" + ln.Addr().String() + "/repo.git"
	gitRun(t, repo, "config", "--local", "--add", "remote.origin.pushurl", url)
	gitRun(t, repo, "config", "--local", "--add", "remote.origin.pushurl", bare)
	urlBefore := gitOut(t, repo, "config", "--local", "--get", "remote.origin.url")
	pushBefore := gitOut(t, repo, "config", "--local", "--get-all", "remote.origin.pushurl")

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
	fx := &schemeReadFixture{
		repo: repo, bare: bare, worktree: wt.Path, url: url,
		seed: seed, before: before,
		urlBefore: urlBefore, pushBefore: pushBefore,
		hits: &hits,
	}
	fx.assertMainUntouched(t)
	assertPushReadsWork(t, wt.Path, bare, "origin")
	return fx
}

func (f *schemeReadFixture) assertRead(t *testing.T, getURL bool, args ...string) {
	t.Helper()
	out, err := runIsolatedGit(f.worktree, args...)
	text := string(out)
	if strings.Contains(text, "invalid://cardex.blocked") {
		t.Fatalf("git %s rewrote the read address:\n%s", strings.Join(args, " "), text)
	}
	if strings.Contains(text, "not allowed") {
		t.Fatalf("git %s protocol denial:\n%s", strings.Join(args, " "), text)
	}
	if getURL {
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, text)
		}
		if strings.TrimSpace(text) != f.url {
			t.Fatalf("resolved %q want %s", strings.TrimSpace(text), f.url)
		}
		return
	}
	if err == nil {
		t.Fatalf("git %s succeeded against a non-git endpoint:\n%s", strings.Join(args, " "), text)
	}
	if !strings.Contains(text, f.url) {
		t.Fatalf("git %s did not use the original URL %s:\n%s", strings.Join(args, " "), f.url, text)
	}
}

func (f *schemeReadFixture) assertPush(t *testing.T, direct, noVerify bool) {
	t.Helper()
	beforeHits := f.hits.Load()
	ref := "refs/heads/inherited-new"
	args := []string{"push"}
	if noVerify {
		args = append(args, "--no-verify")
	}
	if direct {
		ref = "refs/heads/direct-new"
		args = append(args, f.url, "HEAD:"+ref)
	} else {
		args = append(args, "origin", "HEAD:refs/heads/probe", "HEAD:"+ref)
	}
	out, err := runIsolatedGit(f.worktree, args...)
	if err == nil {
		t.Fatalf("git %s succeeded:\n%s", strings.Join(args, " "), out)
	}
	if strings.Contains(string(out), "not allowed") {
		t.Fatalf("git %s protocol denial:\n%s", strings.Join(args, " "), out)
	}
	if got := gitOut(t, f.bare, "show-ref"); got != f.before {
		t.Fatalf("remote refs changed\nbefore:\n%s\nafter:\n%s\npush:\n%s", f.before, got, out)
	}
	if _, missing := gitOptional(f.bare, "rev-parse", "--verify", ref); missing == nil {
		t.Fatalf("created %s\n%s", ref, out)
	}
	if got := gitOut(t, f.bare, "rev-parse", "refs/heads/probe"); got != f.seed {
		t.Fatalf("probe moved %s -> %s", f.seed, got)
	}
	if delta := f.hits.Load() - beforeHits; delta != 0 {
		t.Fatalf("live push target was contacted %d times\n%s", delta, out)
	}
	f.assertMainUntouched(t)
}

func (f *schemeReadFixture) assertMainUntouched(t *testing.T) {
	t.Helper()
	if got := gitOut(t, f.repo, "config", "--local", "--get", "remote.origin.url"); got != f.urlBefore {
		t.Fatalf("main origin url changed %s -> %s", f.urlBefore, got)
	}
	if got := gitOut(t, f.repo, "config", "--local", "--get-all", "remote.origin.pushurl"); got != f.pushBefore {
		t.Fatalf("main pushurl changed %q -> %q", f.pushBefore, got)
	}
	if _, err := gitOptional(f.repo, "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Fatal("main repo hooksPath was set")
	}
	for _, key := range []string{
		"protocol.allow", "protocol.file.allow", "protocol.https.allow",
		"protocol.http.allow", "protocol.ftp.allow", "protocol.ftps.allow",
		"protocol.ssh.allow", "protocol.git.allow",
	} {
		if _, err := gitOptional(f.repo, "config", "--local", "--get", key); err == nil {
			t.Fatalf("main repo %s was set", key)
		}
	}
	if got, err := gitOptional(f.repo, "config", "--local", "--get-regexp", `url\..*\.insteadOf`); err == nil && strings.TrimSpace(got) != "" {
		t.Fatalf("main repo insteadOf was set: %s", got)
	}
	if got, err := gitOptional(f.repo, "config", "--local", "--get-regexp", `url\..*\.pushInsteadOf`); err == nil && strings.TrimSpace(got) != "" {
		t.Fatalf("main repo pushInsteadOf was set: %s", got)
	}
}

func assertLongerPushInsteadOfRefused(t *testing.T, kind string, configArgs, pushArgs []string, advertise bool) {
	t.Helper()
	repo := initRepo(t)
	bare := filepath.Join(t.TempDir(), "target.git")
	if strings.Contains(bare, "=") {
		t.Fatalf("bare path contains '=': %s", bare)
	}
	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, repo, "remote", "add", "origin", bare)
	gitRun(t, repo, "push", bare, "HEAD:refs/heads/probe")
	seed := gitOut(t, bare, "rev-parse", "refs/heads/probe")
	urlBefore := gitOut(t, repo, "config", "--local", "--get", "remote.origin.url")
	if _, err := gitOptional(repo, "config", "--local", "--get", "remote.origin.pushurl"); err == nil {
		t.Fatal("main repo already has pushurl")
	}
	if _, err := gitOptional(repo, "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Fatal("main repo already has hooksPath")
	}
	protocolKeys := []string{
		"protocol.allow", "protocol.file.allow", "protocol.https.allow",
		"protocol.ssh.allow", "protocol.http.allow", "protocol.git.allow",
	}
	for _, key := range protocolKeys {
		if _, err := gitOptional(repo, "config", "--local", "--get", key); err == nil {
			t.Fatalf("main repo %s already set", key)
		}
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

	dest := bare
	newRef := "refs/heads/blocked-new"
	args := []string{"push", "--no-verify"}
	args = append(args, pushArgs...)
	switch kind {
	case "long-prefix":
		args = append([]string{"-c", "url." + bare + "/.pushInsteadOf=review-local:"}, args...)
		args = append(args, "review-local:", "HEAD:"+newRef)
	case "other-prefix":
		args = append([]string{"-c", "url." + bare + "/.pushInsteadOf=inherited-push-target:"}, args...)
		args = append(args, "inherited-push-target:", "HEAD:"+newRef)
	case "direct":
		args = append(args, bare, "HEAD:"+newRef)
	case "dot":
		dest = repo
		newRef = "refs/heads/from-dot"
		args = append(args, ".", "HEAD:"+newRef)
	case "relative":
		dest = filepath.Join(wt.Path, "nested", "bare.git")
		gitRun(t, "", "init", "--bare", dest)
		head := gitOut(t, wt.Path, "rev-parse", "HEAD")
		gitRun(t, repo, "push", dest, head+":refs/heads/probe")
		newRef = "refs/heads/from-rel"
		args = append(args, "nested/bare.git", "HEAD:"+newRef)
	default:
		t.Fatalf("unknown kind %s", kind)
	}
	if len(configArgs) > 0 {
		front := make([]string, 0, len(configArgs)+len(args))
		front = append(front, configArgs...)
		args = append(front, args...)
	}
	if advertise {
		gitRun(t, dest, "config", "receive.advertisePushOptions", "true")
		if got := gitOut(t, dest, "config", "--local", "--get", "receive.advertisePushOptions"); got != "true" {
			t.Fatalf("destination advertisePushOptions %q", got)
		}
	}
	if strings.Contains(strings.Join(args, " "), "--dry-run") {
		t.Fatal("push refusal must be a real push")
	}
	before := gitOut(t, dest, "show-ref")
	out, err := runIsolatedGit(wt.Path, args...)
	if err == nil {
		t.Fatalf("git %s succeeded:\n%s", strings.Join(args, " "), out)
	}
	if strings.Contains(string(out), "not allowed") {
		t.Fatalf("git %s protocol denial:\n%s", strings.Join(args, " "), out)
	}
	if after := gitOut(t, dest, "show-ref"); after != before {
		t.Fatalf("destination refs changed\nbefore:\n%s\nafter:\n%s\npush:\n%s", before, after, out)
	}
	if _, missing := gitOptional(dest, "rev-parse", "--verify", newRef); missing == nil {
		t.Fatalf("created %s\n%s", newRef, out)
	}
	if kind == "long-prefix" || kind == "other-prefix" || kind == "direct" {
		if got := gitOut(t, bare, "rev-parse", "refs/heads/probe"); got != seed {
			t.Fatalf("probe moved %s -> %s", seed, got)
		}
	}
	assertPushReadsIsolated(t, wt.Path, bare, "origin")

	if got := gitOut(t, repo, "config", "--local", "--get", "remote.origin.url"); got != urlBefore {
		t.Fatalf("main origin url changed %s -> %s", urlBefore, got)
	}
	if _, err := gitOptional(repo, "config", "--local", "--get", "remote.origin.pushurl"); err == nil {
		t.Fatal("main repo pushurl was set")
	}
	if _, err := gitOptional(repo, "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Fatal("main repo hooksPath was set")
	}
	for _, key := range protocolKeys {
		if _, err := gitOptional(repo, "config", "--local", "--get", key); err == nil {
			t.Fatalf("main repo %s was set", key)
		}
	}
	if got, err := gitOptional(repo, "config", "--local", "--get-regexp", `url\..*\.pushInsteadOf`); err == nil && strings.TrimSpace(got) != "" {
		t.Fatalf("main repo pushInsteadOf was set: %s", got)
	}
}

func assertPushReadsIsolated(t *testing.T, worktree, bare, remote string) {
	t.Helper()
	for _, args := range [][]string{
		{"ls-remote", bare},
		{"ls-remote", ".", "HEAD"},
		{"fetch", remote},
	} {
		out, err := runIsolatedGit(worktree, args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		if strings.Contains(string(out), "not allowed") {
			t.Fatalf("git %s protocol denial:\n%s", strings.Join(args, " "), out)
		}
	}
}

func runIsolatedGit(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = isolatedGitEnv()
	return cmd.CombinedOutput()
}

func isolatedGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GIT_") {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func assertInheritedPushBlocked(t *testing.T, pushCounts []int) {
	t.Helper()
	repo := initRepo(t)
	names := []string{"origin", "other", "extra"}
	if len(pushCounts) > len(names) {
		t.Fatalf("too many remotes")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var httpsHits atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			httpsHits.Add(1)
			c.Close()
		}
	}()
	httpsPush := "https://" + ln.Addr().String() + "/repo.git"
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
		gitRun(t, repo, "config", "--local", "--add", "remote."+name+".pushurl", httpsPush)
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
	assertDirectPushRefused(t, wt.Path, ".", "refs/heads/from-dot")
	relBare := filepath.Join(wt.Path, "nested", "bare.git")
	gitRun(t, "", "init", "--bare", relBare)
	// The worktree rewrites every push. Seed this bare from the main repo,
	// which does not carry that rewrite.
	head := gitOut(t, wt.Path, "rev-parse", "HEAD")
	gitRun(t, repo, "push", relBare, head+":refs/heads/probe")
	relSeed := gitOut(t, relBare, "rev-parse", "refs/heads/probe")
	assertDirectPushRefused(t, wt.Path, "nested/bare.git", "refs/heads/from-rel")
	if got := gitOut(t, relBare, "rev-parse", "refs/heads/probe"); got != relSeed {
		t.Fatalf("relative bare probe moved %s -> %s", relSeed, got)
	}
	if _, missing := gitOptional(relBare, "rev-parse", "--verify", "refs/heads/from-rel"); missing == nil {
		t.Fatal("relative bare created from-rel")
	}
	if hits := httpsHits.Load(); hits != 0 {
		t.Fatalf("https pushurl was contacted %d times", hits)
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

func assertDirectPushRefused(t *testing.T, dir, url, ref string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "push", "--no-verify", url, "HEAD:"+ref)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("git push %s succeeded:\n%s", url, out)
	}
	if strings.Contains(string(out), "not allowed") {
		t.Fatalf("git push %s protocol denial:\n%s", url, out)
	}
	if _, missing := gitOptional(dir, "rev-parse", "--verify", ref); missing == nil {
		t.Fatalf("push %s created %s\n%s", url, ref, out)
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

func TestRemoveReadOnlyTree(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	m := Git{}
	wt, err := m.Create(ctx, CreateRequest{
		CardID: "card", AttemptID: "ro", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: t.TempDir(), IntegrationBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Shape of a Go module cache inside the worktree: 0444 files in 0555 dirs.
	cache := filepath.Join(wt.Path, ".gomodcache", "example.com", "m@v1")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "go.mod"), []byte("module m\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{cache, filepath.Dir(cache), filepath.Join(wt.Path, ".gomodcache")} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { makeWritable(wt.Path) })
	if err := m.Remove(ctx, wt.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
}

func TestRemoveStaleGitdirIsReportedNotDeleted(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	m := Git{}
	wt, err := m.Create(ctx, CreateRequest{
		CardID: "card", AttemptID: "orphan", ObjectiveID: "obj",
		RepoPath: repo, WorktreeRoot: t.TempDir(), IntegrationBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	gitdir, stale := StaleGitdir(wt.Path)
	if stale || gitdir == "" {
		t.Fatalf("live worktree reported stale: %q %v", gitdir, stale)
	}
	// A half-finished git worktree remove drops the admin directory first.
	if err := os.RemoveAll(gitdir); err != nil {
		t.Fatal(err)
	}
	err = m.Remove(ctx, wt.Path)
	var se *StaleWorktreeError
	if !errors.As(err, &se) || se.Path != wt.Path {
		t.Fatalf("want StaleWorktreeError, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "README")); err != nil {
		t.Fatalf("orphan contents must stay for a person: %v", err)
	}
}
