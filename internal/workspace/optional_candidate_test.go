package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOptionalCandidateMergeRefOnlyFastForward(t *testing.T) {
	repo := optionalMainRepo(t)
	base := gitOut(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "checkout", "-b", "integration")
	optionalFixtureCommit(t, repo, "candidate.txt", "candidate")
	candidate := gitOut(t, repo, "rev-parse", "HEAD")
	hookDir := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const hook = "#!/bin/sh\nprintf hook > hook-executed\n"
	if err := os.WriteFile(filepath.Join(hookDir, "reference-transaction"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (Git{}).MergeOptionalCandidate(t.Context(), repo, "refs/heads/main", "refs/heads/integration", base, candidate); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, repo, "rev-parse", "refs/heads/main"); got != candidate {
		t.Fatalf("advanced to %q not exact candidate %q", got, candidate)
	}
	if got := gitOut(t, repo, "symbolic-ref", "HEAD"); got != "refs/heads/integration" {
		t.Fatalf("primary update changed current checkout: %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "hook-executed")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local ref hook ran: %v", err)
	}
}

func TestOptionalCandidateMergeHoldsWithoutTouchedCheckout(t *testing.T) {
	for _, mode := range []string{"checked-out", "linked-primary", "changed-integration", "changed-primary", "diverged-clean", "diverged-conflict", "preexisting-merge"} {
		t.Run(mode, func(t *testing.T) {
			repo := optionalMainRepo(t)
			base := gitOut(t, repo, "rev-parse", "HEAD")
			gitRun(t, repo, "checkout", "-b", "integration")
			file := "candidate.txt"
			if mode == "diverged-conflict" || mode == "preexisting-merge" {
				file = "README"
			}
			optionalFixtureCommit(t, repo, file, "candidate content")
			candidate := gitOut(t, repo, "rev-parse", "HEAD")
			want := ErrOptionalPrimaryCheckedOut
			switch mode {
			case "checked-out":
				gitRun(t, repo, "checkout", "main")
				if err := os.WriteFile(filepath.Join(repo, "README"), []byte("dirty operator bytes\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "linked-primary":
				gitRun(t, repo, "worktree", "add", filepath.Join(t.TempDir(), "primary"), "main")
			case "changed-integration":
				optionalFixtureCommit(t, repo, "new.txt", "different")
				want = ErrOptionalIntegrationChanged
			case "changed-primary":
				gitRun(t, repo, "update-ref", "refs/heads/main", candidate, base)
				want = ErrOptionalCandidateChanged
			case "diverged-clean", "diverged-conflict":
				gitRun(t, repo, "checkout", "main")
				other := "unrelated.txt"
				if mode == "diverged-conflict" {
					other = "README"
				}
				optionalFixtureCommit(t, repo, other, "primary content")
				base = gitOut(t, repo, "rev-parse", "HEAD")
				gitRun(t, repo, "checkout", "integration")
				want = ErrOptionalCandidateDiverged
			case "preexisting-merge":
				gitRun(t, repo, "checkout", "--detach", base)
				optionalFixtureCommit(t, repo, "README", "operator work")
				cmd := optionalGit(t.Context(), repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "merge", "--no-edit", candidate)
				if err := cmd.Run(); err == nil {
					t.Fatal("fixture did not leave a preexisting conflict")
				}
				want = ErrOptionalRepositoryBusy
			}
			beforePrimary := gitOut(t, repo, "rev-parse", "refs/heads/main")
			beforeHead := gitOut(t, repo, "rev-parse", "HEAD")
			beforeStatus := gitOut(t, repo, "status", "--porcelain=v1")
			err := (Git{}).MergeOptionalCandidate(t.Context(), repo, "refs/heads/main", "refs/heads/integration", base, candidate)
			if !errors.Is(err, ErrOptionalCandidateHeld) || !errors.Is(err, want) {
				t.Fatalf("hold = %v, want %v", err, want)
			}
			if gitOut(t, repo, "rev-parse", "refs/heads/main") != beforePrimary ||
				gitOut(t, repo, "rev-parse", "HEAD") != beforeHead || gitOut(t, repo, "status", "--porcelain=v1") != beforeStatus {
				t.Fatal("held operation changed primary or checkout")
			}
			if mode == "preexisting-merge" && gitOut(t, repo, "rev-parse", "MERGE_HEAD") != candidate {
				t.Fatal("operator-owned merge was cleared or altered")
			}
		})
	}
}

func optionalFixtureCommit(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "--", name)
	gitRun(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
}

func TestOptionalCandidateMergeHoldsLinkedDetachedOperation(t *testing.T) {
	for _, operation := range []string{"rebase", "bisect"} {
		t.Run(operation, func(t *testing.T) {
			repo := optionalMainRepo(t)
			base := gitOut(t, repo, "rev-parse", "HEAD")
			gitRun(t, repo, "checkout", "-b", "integration")
			optionalFixtureCommit(t, repo, "middle.txt", "intermediate")
			optionalFixtureCommit(t, repo, "README", "candidate bytes")
			candidate := gitOut(t, repo, "rev-parse", "HEAD")
			linked := filepath.Join(t.TempDir(), "operator-linked")
			gitRun(t, repo, "worktree", "add", "--detach", linked, base)
			if operation == "rebase" {
				optionalFixtureCommit(t, linked, "README", "operator bytes")
				cmd := optionalGit(t.Context(), linked, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid",
					"rebase", "--merge", candidate)
				if err := cmd.Run(); err == nil {
					t.Fatal("fixture rebase did not produce a conflict")
				}
			} else {
				gitRun(t, linked, "bisect", "start", candidate, base)
			}
			admin := gitOut(t, linked, "rev-parse", "--absolute-git-dir")
			marker := filepath.Join(admin, "rebase-merge", "orig-head")
			if operation == "bisect" {
				marker = filepath.Join(admin, "BISECT_START")
			}
			beforeMarker, err := os.ReadFile(marker)
			if err != nil {
				t.Fatalf("operation fixture not present: %v", err)
			}
			beforeHead := gitOut(t, linked, "rev-parse", "HEAD")
			beforeStatus := gitOut(t, linked, "status", "--porcelain=v1")
			err = (Git{}).MergeOptionalCandidate(t.Context(), repo, "refs/heads/main", "refs/heads/integration", base, candidate)
			if !errors.Is(err, ErrOptionalRepositoryBusy) {
				t.Fatalf("linked detached operation was not held: %v", err)
			}
			afterMarker, readErr := os.ReadFile(marker)
			if readErr != nil || string(afterMarker) != string(beforeMarker) ||
				gitOut(t, linked, "rev-parse", "HEAD") != beforeHead ||
				gitOut(t, linked, "status", "--porcelain=v1") != beforeStatus ||
				gitOut(t, repo, "rev-parse", "refs/heads/main") != base {
				t.Fatal("held operation changed linked state or primary ref")
			}
		})
	}
}

type optionalLostCommitIO struct {
	writeErr error
	got      string
}

func (f *optionalLostCommitIO) Write(p []byte) (int, error) {
	f.got += string(p)
	return len(p), f.writeErr
}
func (*optionalLostCommitIO) Close() error { return nil }

func TestOptionalCandidateCommitAckLossIsNotNoMutationHold(t *testing.T) {
	for _, mode := range []string{"write-error", "lost-ack", "lost-exit", "ack-and-exit"} {
		t.Run(mode, func(t *testing.T) {
			pipe := &optionalLostCommitIO{}
			if mode == "write-error" {
				pipe.writeErr = errors.New("simulated error after commit delivery")
			}
			err := optionalCommitTransaction(pipe, func(cmd string) bool { return cmd == "commit" && mode != "lost-ack" }, func() error {
				if mode == "lost-exit" {
					return errors.New("simulated lost final process response")
				}
				return nil
			})
			if pipe.got != "commit\n" {
				t.Fatal("test missed commit attempt")
			}
			if mode == "ack-and-exit" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrOptionalCandidateUpdateUnknown) || errors.Is(err, ErrOptionalCandidateHeld) {
				t.Fatalf("commit loss misrepresented as no mutation: %v", err)
			}
		})
	}
}

func TestOptionalCandidateMergeHoldsSymbolicRef(t *testing.T) {
	for _, which := range []string{"main", "integration"} {
		t.Run(which, func(t *testing.T) {
			repo := optionalMainRepo(t)
			base := gitOut(t, repo, "rev-parse", "HEAD")
			gitRun(t, repo, "checkout", "-b", "integration")
			optionalFixtureCommit(t, repo, "candidate.txt", "candidate")
			candidate := gitOut(t, repo, "rev-parse", "HEAD")
			gitRun(t, repo, "checkout", "--detach", candidate)
			from := "refs/heads/" + which
			onto := "refs/heads/symbolic-target"
			pin := base
			if which == "integration" {
				pin = candidate
			}
			gitRun(t, repo, "update-ref", onto, pin)
			gitRun(t, repo, "symbolic-ref", from, onto)
			err := (Git{}).MergeOptionalCandidate(t.Context(), repo, "refs/heads/main", "refs/heads/integration", base, candidate)
			want := ErrOptionalCandidateChanged
			if which == "integration" {
				want = ErrOptionalIntegrationChanged
			}
			if !errors.Is(err, want) || gitOut(t, repo, "symbolic-ref", from) != onto || gitOut(t, repo, "rev-parse", onto) != pin {
				t.Fatalf("optional update touched symbolic ref or target: %v", err)
			}
		})
	}
}

func TestOptionalCandidateMergeDoesNotTakeOverExistingRefLock(t *testing.T) {
	repo := optionalMainRepo(t)
	base := gitOut(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "checkout", "-b", "integration")
	optionalFixtureCommit(t, repo, "candidate.txt", "candidate")
	candidate := gitOut(t, repo, "rev-parse", "HEAD")
	admin := gitOut(t, repo, "rev-parse", "--absolute-git-dir")
	lock := filepath.Join(admin, "refs", "heads", "main.lock")
	const before = "foreign lock sentinel"
	if err := os.WriteFile(lock, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (Git{}).MergeOptionalCandidate(t.Context(), repo, "refs/heads/main", "refs/heads/integration", base, candidate)
	after, readErr := os.ReadFile(lock)
	if !errors.Is(err, ErrOptionalRefProofAmbiguous) || errors.Is(err, ErrOptionalCandidateUpdateUnknown) ||
		readErr != nil || string(after) != before || gitOut(t, repo, "rev-parse", "refs/heads/main") != base {
		t.Fatalf("existing lock could not be safely held: %v %v", err, readErr)
	}
}

func optionalMainRepo(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(initRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOptionalCandidateMergeRejectsUnprovedRepositoryRoot(t *testing.T) {
	for _, mode := range []string{"linked-input", "parent-link", "git-link", "git-file", "commondir", "include", "includeif", "alternates", "http-alternates"} {
		t.Run(mode, func(t *testing.T) {
			repo := optionalMainRepo(t)
			base := gitOut(t, repo, "rev-parse", "HEAD")
			gitRun(t, repo, "checkout", "-b", "integration")
			optionalFixtureCommit(t, repo, "candidate.txt", "candidate")
			candidate := gitOut(t, repo, "rev-parse", "HEAD")
			admin := filepath.Join(repo, ".git")
			path := repo
			foreign, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "linked-input":
				path = filepath.Join(foreign, "linked")
				gitRun(t, repo, "worktree", "add", "--detach", path, base)
			case "parent-link":
				path = filepath.Join(foreign, "alias")
				if err := os.Symlink(repo, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "git-link", "git-file":
				safe := filepath.Join(foreign, "admin")
				if err := os.Rename(admin, safe); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Remove(admin); _ = os.Rename(safe, admin) }()
				if mode == "git-link" {
					if err := os.Symlink(safe, admin); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
				} else if err := os.WriteFile(admin, []byte("gitdir: "+safe+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				if mode == "include" || mode == "includeif" {
					f, err := os.OpenFile(filepath.Join(admin, "config"), os.O_APPEND|os.O_WRONLY, 0o600)
					if err != nil {
						t.Fatal(err)
					}
					section := "[include]"
					if mode == "includeif" {
						section = `[includeIf "gitdir:**"]`
					}
					_, err = f.WriteString("\n" + section + "\npath = " + filepath.ToSlash(filepath.Join(foreign, "must-not-read")) + "\n")
					_ = f.Close()
					if err != nil {
						t.Fatal(err)
					}
				} else {
					name := mode
					if mode != "commondir" {
						name = filepath.Join("objects", "info", mode)
					}
					if err := os.WriteFile(filepath.Join(admin, name), []byte(foreign+"\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = (Git{}).MergeOptionalCandidate(t.Context(), path, "refs/heads/main", "refs/heads/integration", base, candidate)
			if !errors.Is(err, ErrOptionalCandidateHeld) || errors.Is(err, ErrOptionalCandidateUpdateUnknown) {
				t.Fatalf("unproved root was not held before update: %v", err)
			}
		})
	}
}

func TestOptionalPorcelainRejectsUnprovedRecords(t *testing.T) {
	const addr = "/fixture"
	const repo = "worktree " + addr + "\x00HEAD 0123456789abcdef0123456789abcdef01234567\x00"
	for _, suffix := range []string{"detached\x00\x00", "branch refs/heads/integration\x00\x00", "detached\x00mystery\x00\x00", "detached\x00", "bare\x00\x00", "detached\x00locked\x00\x00"} {
		err := optionalPorcelain(repo+suffix, "refs/heads/main", map[string]bool{addr: true}, 40)
		want := suffix == "detached\x00\x00" || strings.HasPrefix(suffix, "branch")
		if (err == nil) != want {
			t.Fatalf("%q valid=%v, err=%v", suffix, want, err)
		}
	}
}

func TestOptionalCandidatePreflightDoesNotAdvanceAndReportsIntegrationFirst(t *testing.T) {
	repo := optionalMainRepo(t)
	base := gitOut(t, repo, "rev-parse", "HEAD")
	gitRun(t, repo, "checkout", "-b", "integration")
	optionalFixtureCommit(t, repo, "one.txt", "candidate")
	candidate := gitOut(t, repo, "rev-parse", "HEAD")
	ctx := t.Context()
	g := Git{}
	if err := g.CheckOptionalCandidate(ctx, repo, "refs/heads/main", "refs/heads/integration", base, candidate); err != nil ||
		gitOut(t, repo, "rev-parse", "refs/heads/main") != base {
		t.Fatalf("read-only preflight advanced or failed: %v", err)
	}
	optionalFixtureCommit(t, repo, "two.txt", "newer-candidate")
	gitRun(t, repo, "checkout", "main")
	err := g.CheckOptionalCandidate(ctx, repo, "refs/heads/main", "refs/heads/integration", base, candidate)
	if !errors.Is(err, ErrOptionalIntegrationChanged) {
		t.Fatalf("primary checkout hid proven integration drift: %v", err)
	}
}

func TestOptionalCandidateAcceptanceRepoRejectsCheckoutFilters(t *testing.T) {
	repo := optionalMainRepo(t)
	if !VerifyOptionalAcceptanceRepository(t.Context(), repo) {
		t.Fatal("plain repo was not qualified")
	}
	for _, key := range []string{"filter.example.smudge", "core.attributesfile", "core.sparseCheckout"} {
		t.Run(key, func(t *testing.T) {
			gitRun(t, repo, "config", key, "synthetic-untrusted-value")
			defer gitRun(t, repo, "config", "--unset", key)
			if VerifyOptionalAcceptanceRepository(t.Context(), repo) {
				t.Fatal("unproved checkout substitution was qualified")
			}
		})
	}
}
