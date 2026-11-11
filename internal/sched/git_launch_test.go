package sched

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestServeLaunchUsesGit(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.txt")
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	proj := model.Project{ID: "p", RepoPath: repo, TestCmd: "true"}
	obj := model.Objective{
		ID: "o", Project: "p", Title: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main",
	}
	if err := db.CreateProject(ctx, &proj); err != nil || db.CreateObjective(ctx, &obj) != nil {
		t.Fatal("seed")
	}
	ch := model.Channel{ID: "ch", Runner: model.RunnerFake, Health: model.HealthOK, Models: []string{"fake"}}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	card := model.Card{ID: "c", Objective: "o", Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardRunning}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	id := "a-git"
	dir := filepath.Join(home, "attempts", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := []byte(`{"attempts":{"0":{"write_files":[{"path":"note.txt","content":"shipped\n"}],"exit_code":0}}}`)
	if err := os.WriteFile(filepath.Join(dir, "script.json"), script, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("prompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hard-duration"), []byte("30s"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(home, "wt")
	git(t, repo, "worktree", "add", "-b", "github.com/OttoPrua/Cardex/o/c/c", wt, "main")
	attempt := model.Attempt{
		ID: id, Card: "c", Index: 0, Route: model.Route{Channel: "ch", Model: "fake"},
		Worktree: wt, PromptPath: filepath.Join(dir, "prompt.txt"), StartedAt: time.Now().UTC(),
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	pid, err := launchDetached(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if pid > 1 {
		t.Cleanup(func() { killHost(home, id, pid) })
	}
	deadline := time.Now().Add(20 * time.Second)
	var data string
	for time.Now().Before(deadline) {
		opened, err := store.Open(filepath.Join(home, "cardex.db"))
		if err != nil {
			time.Sleep(30 * time.Millisecond)
			continue
		}
		events, err := opened.ListEvents(ctx, "attempt:"+id)
		_ = opened.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Type == "workspace_commit" {
				data = string(event.Data)
			}
		}
		if strings.Contains(data, "head") && !strings.Contains(data, "not implemented") {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !strings.Contains(data, "head") || strings.Contains(data, "not implemented") {
		logBody, _ := os.ReadFile(filepath.Join(dir, "launcher.log"))
		opened, err := store.Open(filepath.Join(home, "cardex.db"))
		var attempt model.Attempt
		if err == nil {
			attempt, _ = opened.GetAttempt(ctx, id)
			_ = opened.Close()
		}
		t.Fatalf("serve launch workspace_commit %s exit=%v excerpt=%q\nlauncher: %s", data, attempt.Exit, attempt.ErrorExcerpt, logBody)
	}
}
