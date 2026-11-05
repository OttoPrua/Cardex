package host_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cardex/internal/host"
	"cardex/internal/model"
	"cardex/internal/store"
)

func TestGoalModeSetsFieldsAndConsumesExit(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		runGoal(t, "#!/bin/sh\nsleep 1\nexit 0\n", false, 0)
	})
	t.Run("crash", func(t *testing.T) {
		runGoal(t, "#!/bin/sh\nsleep 1\nkill -KILL $$\n", true, 9)
	})
}

func runGoal(t *testing.T, script string, wantCrash bool, wantSignal int) {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "grok")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	home, id, _ := seedPrepared(t, t.TempDir(), model.RunnerGrok, "", 1, nil)
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	card, err := db.GetCard(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	card.Requires = []string{"goal_mode"}
	if err := db.UpdateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	prompt := "GOAL-PROMPT-R7\n"
	if err := os.WriteFile(filepath.Join(home, "prompt.md"), []byte(prompt), 0o644); err != nil {
		t.Fatal(err)
	}
	plantGoalSession(t, filepath.Join(home, "attempts", id, "grok-session"))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	_, events := loadAttempt(t, home, id)
	start, ok := eventType(events, "host_start")
	if !ok || strings.Contains(string(start.Data), "--prompt-file") {
		t.Fatalf("goal argv %s", start.Data)
	}
	if !strings.Contains(string(start.Data), "--cwd") {
		t.Fatalf("goal argv missing cwd %s", start.Data)
	}
	ev, ok := eventType(events, "grok_process_exit")
	if !ok {
		t.Fatal("missing grok_process_exit")
	}
	var got struct {
		Signaled   bool   `json:"signaled"`
		Signal     int    `json:"signal"`
		Crash      bool   `json:"crash"`
		GoalMode   bool   `json:"goal_mode"`
		Prompt     string `json:"prompt"`
		SessionDir string `json:"session_dir"`
	}
	if err := json.Unmarshal(ev.Data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.GoalMode || got.Prompt != prompt || !strings.HasSuffix(got.SessionDir, filepath.Join("attempts", id, "grok-session")) {
		t.Fatalf("fields %+v", got)
	}
	if got.Crash != wantCrash || got.Signaled != wantCrash || got.Signal != wantSignal {
		t.Fatalf("exit fact %+v", got)
	}
}

func plantGoalSession(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "goal"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := []byte("{\"goal_id\":\"g1\",\"status\":\"complete\",\"last_classifier_verdict\":\"achieved\"}\n")
	if err := os.WriteFile(filepath.Join(dir, "goal", "state.json"), state, 0o644); err != nil {
		t.Fatal(err)
	}
	update := []byte("{\"params\":{\"sessionId\":\"s1\",\"update\":{\"sessionUpdate\":\"goal_updated\",\"goal_id\":\"g1\",\"status\":\"complete\",\"last_classifier_verdict\":\"achieved\"}}}\n")
	if err := os.WriteFile(filepath.Join(dir, "updates.jsonl"), update, 0o644); err != nil {
		t.Fatal(err)
	}
	summary := []byte("{\"info\":{\"id\":\"s1\"}}\n")
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), summary, 0o644); err != nil {
		t.Fatal(err)
	}
}
