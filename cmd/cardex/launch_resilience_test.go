package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"cardex/internal/host"
	"cardex/internal/model"
	"cardex/internal/sched"
	"cardex/internal/store"
)

func TestCLIFirstDispatchCreatesIntegrationBranch(t *testing.T) {
	for _, primary := range []string{"main", "master"} {
		t.Run(primary, func(t *testing.T) {
			home := t.TempDir()
			repo := t.TempDir()
			wt := t.TempDir()
			initGit(t, repo, primary)
			writeSchedules(t, home)
			mustCLI(t, "--home", home, "project", "add", "--id", "p", "--repo", repo, "--worktree-root", wt, "--test-cmd", "true")
			mustCLI(t, "--home", home, "channel", "set", "--id", "ch", "--runner", "grok", "--model", "m", "--concurrency", "2")
			mustCLI(t, "--home", home, "channel", "set", "--id", "ch", "--runner", "fake", "--model", "m", "--concurrency", "2")
			mustCLI(t, "--home", home, "objective", "add", "--id", "o", "--project", "p", "--title", "o", "--mode", "manual", "--channel", "ch", "--model", "m")
			mustCLI(t, "--home", home, "card", "add", "--id", "c1", "--objective", "o", "--title", "c1", "--kind", "dev", "--tier", "T2")
			primarySHA := gitRev(t, repo, "refs/heads/"+primary)
			tickHome(t, home)
			db := openHome(t, home)
			defer db.Close()
			ctx := context.Background()
			if gitRev(t, repo, "refs/heads/cardex/o/main") != primarySHA {
				t.Fatalf("integration ref is not %s HEAD", primary)
			}
			if gitRev(t, repo, "refs/heads/"+primary) != primarySHA {
				t.Fatal("primary moved")
			}
			card := mustGetCard(t, db, "c1")
			if card.Status != model.CardRunning {
				t.Fatalf("status %s", card.Status)
			}
			attempts := mustAttempts(t, db, "c1")
			if len(attempts) != 1 || attempts[0].Worktree == "" {
				t.Fatalf("attempts %+v", attempts)
			}
			if _, err := os.Stat(attempts[0].Worktree); err != nil {
				t.Fatal(err)
			}
			stopAttempts(home, attempts)
			if primary == "main" {
				mustCLI(t, "--home", home, "card", "add", "--id", "c2", "--objective", "o", "--title", "c2", "--kind", "dev", "--tier", "T2")
				tickHome(t, home)
				if gitRev(t, repo, "refs/heads/cardex/o/main") != primarySHA {
					t.Fatal("second card moved the integration ref")
				}
				names := gitLines(t, repo, "for-each-ref", "--format=%(refname)", "refs/heads/cardex/o/main")
				if names != "refs/heads/cardex/o/main" {
					t.Fatalf("refs %q", names)
				}
				second := mustGetCard(t, db, "c2")
				if second.Status != model.CardRunning {
					t.Fatalf("second %s", second.Status)
				}
				more := mustAttempts(t, db, "c2")
				stopAttempts(home, more)
			}
			_ = ctx
		})
	}
}

func TestCLIImportLegacyDispatchCreatesIntegrationBranch(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	initGit(t, repo, "main")
	mainSHA := gitRev(t, repo, "refs/heads/main")
	v1 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(v1, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"id": "q1", "title": "Queue the lighthouse", "status": "queued", "project": "northwind",
		"dir": repo, "model": "should-not-pin",
		"frozen_route": map[string]any{"primary": map[string]any{"runner": "grok", "model": "grok-test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v1, "tasks", "queued.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	mustCLI(t, "--home", home, "import-legacy", v1)
	writeSchedules(t, home)
	mustCLI(t, "--home", home, "channel", "set", "--id", "ch", "--runner", "grok", "--model", "m", "--concurrency", "2")
	mustCLI(t, "--home", home, "channel", "set", "--id", "ch", "--runner", "fake", "--model", "m", "--concurrency", "2")
	prop := map[string]any{
		"id": "prop1", "status": "proposed", "diff": "",
		"entries": []map[string]any{{
			"id": "pos1", "model": "m", "family": "fake",
			"tiers": []string{"T2"}, "capabilities": []string{}, "preference": 1,
		}},
	}
	raw, err := json.Marshal(prop)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "positioning", "proposals")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prop1.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	mustCLI(t, "--home", home, "positioning", "approve", "prop1")
	tickHome(t, home)
	db := openHome(t, home)
	defer db.Close()
	card := mustGetCard(t, db, "v1-q1")
	if card.Status != model.CardRunning {
		t.Fatalf("status %s reason %s", card.Status, card.WaitReason)
	}
	obj, err := db.GetObjective(context.Background(), card.Objective)
	if err != nil {
		t.Fatal(err)
	}
	if gitRev(t, repo, "refs/heads/"+obj.IntegrationBranch) != mainSHA {
		t.Fatalf("integration %s", obj.IntegrationBranch)
	}
	attempts := mustAttempts(t, db, "v1-q1")
	if len(attempts) != 1 {
		t.Fatalf("attempts %+v", attempts)
	}
	if _, err := os.Stat(attempts[0].Worktree); err != nil {
		t.Fatal(err)
	}
	stopAttempts(home, attempts)
}

func mustCLI(t *testing.T, args ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cardex %s: %d\n%s%s", strings.Join(args, " "), code, stdout.String(), stderr.String())
	}
}

func tickHome(t *testing.T, home string) {
	t.Helper()
	if err := sched.New(home).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func openHome(t *testing.T, home string) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func mustGetCard(t *testing.T, db *store.Store, id string) model.Card {
	t.Helper()
	card, err := db.GetCard(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func mustAttempts(t *testing.T, db *store.Store, card string) []model.Attempt {
	t.Helper()
	items, err := db.ListAttempts(context.Background(), card)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func writeSchedules(t *testing.T, home string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{\"schedules\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initGit(t *testing.T, dir, branch string) {
	t.Helper()
	gitCmd(t, dir, "init", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "README")
	gitCmd(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "seed")
}

func gitRev(t *testing.T, dir, ref string) string {
	t.Helper()
	return gitCmd(t, dir, "rev-parse", ref)
}

func gitLines(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitCmd(t, dir, args...)
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func stopAttempts(home string, attempts []model.Attempt) {
	for _, attempt := range attempts {
		if attempt.PID > 1 {
			_ = host.SignalProcess(attempt.PID, syscall.SIGKILL)
		}
		pgidPath := filepath.Join(home, "attempts", attempt.ID, "executor.pgid")
		raw, err := os.ReadFile(pgidPath)
		if err != nil {
			continue
		}
		pgid := 0
		for _, c := range strings.TrimSpace(string(raw)) {
			if c < '0' || c > '9' {
				pgid = 0
				break
			}
			pgid = pgid*10 + int(c-'0')
		}
		if pgid > 1 {
			_ = host.SignalGroup(pgid, syscall.SIGKILL)
		}
	}
}
