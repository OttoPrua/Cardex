package host_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"cardex/internal/host"
	"cardex/internal/model"
	"cardex/internal/runner"
	"cardex/internal/store"
	"cardex/internal/workspace"
)

type recordingManager struct {
	calls int
	msg   string
	hash  string
	err   error
}

func (m *recordingManager) Commit(_ context.Context, _, message string) (string, error) {
	m.calls++
	m.msg = message
	return m.hash, m.err
}

func (m *recordingManager) Create(context.Context, workspace.CreateRequest) (workspace.Worktree, error) {
	return workspace.Worktree{}, errors.New("not used")
}

func (m *recordingManager) Diff(context.Context, string, string) (workspace.Diff, error) {
	return workspace.Diff{}, errors.New("not used")
}

func (m *recordingManager) Remove(context.Context, string) error { return errors.New("not used") }

func (m *recordingManager) BlockPush(context.Context, string) error { return errors.New("not used") }

func seedAttempt(t *testing.T, hardTokens int64, build func(work string) any) (home, id, work string) {
	t.Helper()
	home = t.TempDir()
	id = "a1"
	work = filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ch := model.Channel{
		ID: "ch1", Runner: model.RunnerFake, Health: model.HealthOK, Tier: model.TierT2,
		Family: "fake", Models: []string{"fake"},
	}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "c1", Kind: model.KindDev, Status: model.CardReady, Title: "t",
		Objective: "o", Node: "n", Budget: model.Budget{SoftTokens: 1, HardTokens: hardTokens},
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(build(work))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "attempts", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(home, "prompt.md")
	if err := os.WriteFile(prompt, []byte("prompt body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempt := model.Attempt{
		ID: id, Card: "c1", Index: 0,
		Route:      model.Route{Channel: "ch1", Model: "fake"},
		Worktree:   work,
		PromptPath: prompt,
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return home, id, work
}

func loadAttempt(t *testing.T, home, id string) (model.Attempt, []model.Event) {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	attempt, err := db.GetAttempt(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.ListEvents(ctx, "attempt:"+id)
	if err != nil {
		t.Fatal(err)
	}
	return attempt, events
}

func eventData(t *testing.T, events []model.Event) string {
	t.Helper()
	var b strings.Builder
	for _, ev := range events {
		b.WriteString(ev.Type)
		b.WriteByte('\n')
		b.Write(ev.Data)
		b.WriteByte('\n')
	}
	return b.String()
}

func eventType(events []model.Event, kind string) (model.Event, bool) {
	for _, ev := range events {
		if ev.Type == kind {
			return ev, true
		}
	}
	return model.Event{}, false
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func TestRunCapturesBothStreamsUsageAndPrompt(t *testing.T) {
	payload := strings.Repeat("A", 140*1024) + "\nSTDOUT-MARKER-r2a\ntokens=42\n"
	home, id, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{
			"attempts": map[string]any{
				"0": map[string]any{
					"output": payload,
					"stderr": "STDERR-MARKER-r2a\n",
					"env":    map[string]string{"ANTHROPIC_AUTH_TOKEN": "SECRET-FAKE-TOKEN"},
				},
			},
		}
	})
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"engines":{"kimi":{"base_url":"https://api.kimi.com/coding/","auth_value":"SECRET-ENGINE-VALUE"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := &recordingManager{hash: "deadbeef"}
	drives := runner.FakeDriveCount()
	if err := host.Run(host.WithWorkspace(context.Background(), mgr), home, id); err != nil {
		t.Fatal(err)
	}
	if runner.FakeDriveCount() != drives {
		t.Fatal("Drive ran without a PTY")
	}
	logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(logb)
	if !strings.Contains(log, payload) || !strings.Contains(log, "STDERR-MARKER-r2a") {
		t.Fatalf("log dropped a stream (%d bytes)", len(logb))
	}
	if !strings.Contains(log, "tty=0") {
		t.Fatalf("non-PTY child did not report tty=0: %s", log[:min(200, len(log))])
	}
	prompt := filepath.Join(home, "prompt.md")
	if !strings.Contains(log, "prompt="+prompt) {
		t.Fatalf("prompt path did not reach the child: %s", log[:min(300, len(log))])
	}
	attempt, events := loadAttempt(t, home, id)
	if attempt.Exit == nil || *attempt.Exit != 0 {
		t.Fatalf("exit %#v", attempt.Exit)
	}
	if attempt.EndedAt.IsZero() || attempt.PID != os.Getpid() {
		t.Fatalf("ended %v pid %d", attempt.EndedAt, attempt.PID)
	}
	if attempt.Usage.Tokens != 42 || attempt.Usage.Source != "fake" || attempt.Usage.Confidence != 1 {
		t.Fatalf("usage %+v", attempt.Usage)
	}
	if attempt.Outcome != "" {
		t.Fatalf("outcome %q", attempt.Outcome)
	}
	if attempt.HeadCommit != "deadbeef" || mgr.calls != 1 || !strings.Contains(mgr.msg, "c1") || !strings.Contains(mgr.msg, id) {
		t.Fatalf("commit calls=%d msg=%q head=%q", mgr.calls, mgr.msg, attempt.HeadCommit)
	}
	start, ok := eventType(events, "host_start")
	if !ok || strings.Contains(string(start.Data), `"pty":true`) || strings.Contains(string(start.Data), `"drive":true`) {
		t.Fatalf("start event %#v", start)
	}
	row, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET-FAKE-TOKEN", "SECRET-ENGINE-VALUE"} {
		if strings.Contains(log, secret) || strings.Contains(eventData(t, events), secret) || strings.Contains(string(row), secret) {
			t.Fatalf("secret %s leaked", secret)
		}
	}
}

func TestRunPTYCallsDrive(t *testing.T) {
	home, id, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{
			"needs_pty": true,
			"attempts": map[string]any{
				"0": map[string]any{"output": "pty-marker\n"},
			},
		}
	})
	drives := runner.FakeDriveCount()
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	if runner.FakeDriveCount() != drives+1 {
		t.Fatalf("Drive count %d want %d", runner.FakeDriveCount(), drives+1)
	}
	logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(logb)
	if !strings.Contains(log, "tty=1") || !strings.Contains(log, "pty-marker") {
		t.Fatalf("pty log %q", log)
	}
	_, events := loadAttempt(t, home, id)
	start, ok := eventType(events, "host_start")
	if !ok || !strings.Contains(string(start.Data), `"pty":true`) || !strings.Contains(string(start.Data), `"drive":true`) {
		t.Fatalf("start event %s", start.Data)
	}
}

func TestRunKillsProcessGroup(t *testing.T) {
	var home, id, work string
	home, id, work = seedAttempt(t, 1, func(work string) any {
		return map[string]any{
			"attempts": map[string]any{
				"0": map[string]any{
					"sleep":             "30s",
					"grandchild_marker": filepath.Join(work, "late.txt"),
					"grandchild_after":  "15s",
					"output":            "should-not-finish\n",
				},
			},
		}
	})
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"host":{"hard_duration":"5s"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed > 20*time.Second {
		t.Fatalf("host waited %s", elapsed)
	}
	raw, err := os.ReadFile(filepath.Join(work, "grandchild.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pid) {
		t.Fatalf("grandchild %d still alive", pid)
	}
	if _, err := os.Stat(filepath.Join(work, "late.txt")); !os.IsNotExist(err) {
		t.Fatalf("grandchild marker: %v", err)
	}
	logb, _ := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if strings.Contains(string(logb), "should-not-finish") {
		t.Fatal("child ran to completion")
	}
	attempt, _ := loadAttempt(t, home, id)
	if attempt.Exit == nil || *attempt.Exit == 0 {
		t.Fatalf("exit %#v", attempt.Exit)
	}
	if attempt.Outcome != "" || attempt.EndedAt.IsZero() {
		t.Fatalf("attempt ended %v outcome %q", attempt.EndedAt, attempt.Outcome)
	}
}

func TestHardTokensDoNotArmTimer(t *testing.T) {
	home, id, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{
			"attempts": map[string]any{
				"0": map[string]any{"sleep": "1500ms", "output": "slept\ntokens=3\n"},
			},
		}
	})
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"host":{"hard_duration":"not-a-duration"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if err != nil || !strings.Contains(string(logb), "slept") {
		t.Fatalf("sleep did not finish: %v %s", err, logb)
	}
	attempt, events := loadAttempt(t, home, id)
	if attempt.Exit == nil || *attempt.Exit != 0 || attempt.Usage.Tokens != 3 {
		t.Fatalf("exit %#v usage %+v", attempt.Exit, attempt.Usage)
	}
	if _, ok := eventType(events, "host_config"); !ok {
		t.Fatal("invalid hard_duration produced no warning")
	}
}

func TestCommitFailureStillWritesBack(t *testing.T) {
	home, id, _ := seedAttempt(t, 9, func(string) any {
		return map[string]any{
			"attempts": map[string]any{
				"0": map[string]any{"output": "ok\ntokens=8\n", "exit_code": 4},
			},
		}
	})
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	attempt, events := loadAttempt(t, home, id)
	if attempt.Exit == nil || *attempt.Exit != 4 || attempt.EndedAt.IsZero() {
		t.Fatalf("exit %#v ended %v", attempt.Exit, attempt.EndedAt)
	}
	if attempt.Usage.Tokens != 8 || attempt.HeadCommit != "" || attempt.Outcome != "" {
		t.Fatalf("attempt %+v", attempt)
	}
	ev, ok := eventType(events, "workspace_commit")
	if !ok || !strings.Contains(string(ev.Data), "not implemented") {
		t.Fatalf("commit event %s", ev.Data)
	}
}

func TestReconcileLiveAndDead(t *testing.T) {
	home, _, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{"attempts": map[string]any{"0": map[string]any{"output": "x"}}}
	})
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	live := model.Attempt{
		ID: "live", Card: "c1", Route: model.Route{Channel: "ch1", Model: "fake"},
		PID: os.Getpid(), ErrorExcerpt: "keep", Worktree: filepath.Join(home, "work"),
	}
	if err := db.CreateAttempt(ctx, &live); err != nil {
		t.Fatal(err)
	}
	deadPID := 0
	for pid := 1_000_000; pid < 1_000_400; pid++ {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			deadPID = pid
			break
		}
	}
	if deadPID == 0 {
		t.Fatal("no dead pid")
	}
	dead := model.Attempt{
		ID: "dead", Card: "c1", Route: model.Route{Channel: "ch1", Model: "fake"},
		PID: deadPID, Worktree: filepath.Join(home, "work"),
	}
	if err := db.CreateAttempt(ctx, &dead); err != nil {
		t.Fatal(err)
	}
	before, err := db.GetAttempt(ctx, "live")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Reconcile(ctx, home); err != nil {
		t.Fatal(err)
	}
	gotLive, err := db.GetAttempt(ctx, "live")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotLive, before) || !host.LiveHost(gotLive) {
		t.Fatalf("live attempt changed\n got %#v\nwant %#v", gotLive, before)
	}
	gotDead, err := db.GetAttempt(ctx, "dead")
	if err != nil {
		t.Fatal(err)
	}
	if host.LiveHost(gotDead) || gotDead.EndedAt.IsZero() || gotDead.PID != deadPID || gotDead.Outcome != "" {
		t.Fatalf("dead attempt %#v", gotDead)
	}
	if gotDead.ErrorExcerpt != "host process exited before recording a result" {
		t.Fatalf("excerpt %q", gotDead.ErrorExcerpt)
	}
	ended := gotDead.EndedAt
	time.Sleep(15 * time.Millisecond)
	if err := host.Reconcile(ctx, home); err != nil {
		t.Fatal(err)
	}
	again, err := db.GetAttempt(ctx, "dead")
	if err != nil {
		t.Fatal(err)
	}
	if !again.EndedAt.Equal(ended) {
		t.Fatalf("second reconcile moved end time %s -> %s", ended, again.EndedAt)
	}
	events, err := db.ListEvents(ctx, "attempt:dead")
	if err != nil {
		t.Fatal(err)
	}
	lost := 0
	for _, ev := range events {
		if ev.Type == "host_lost" {
			lost++
		}
	}
	if lost != 1 {
		t.Fatalf("host_lost events %d", lost)
	}
	liveEvents, err := db.ListEvents(ctx, "attempt:live")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range liveEvents {
		if ev.Type == "host_lost" {
			t.Fatal("live host recorded host_lost")
		}
	}
}
