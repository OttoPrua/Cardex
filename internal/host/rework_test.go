package host_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"cardex/internal/host"
	"cardex/internal/model"
	"cardex/internal/runner"
	"cardex/internal/store"
)

func outputHasLine(text, marker string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == marker {
			return true
		}
	}
	return false
}

func writeDuration(t *testing.T, home, id, text string) {
	t.Helper()
	path := filepath.Join(home, "attempts", id, "hard-duration")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDistinctAttemptDeadlines(t *testing.T) {
	limits := []string{"350ms", "1100ms"}
	var elapsed []time.Duration
	var classes []model.ErrorClass
	for _, limit := range limits {
		home, id, _ := seedAttempt(t, 1, func(string) any {
			return map[string]any{
				"attempts": map[string]any{
					"0": map[string]any{"sleep": "30s", "output": "should-not-finish\n"},
				},
			}
		})
		writeDuration(t, home, id, limit)
		// A global one-millisecond key must not replace this attempt's limit.
		decoy := []byte(`{"host":{"` + "hard_" + "duration" + `":"1ms"}}`)
		if err := os.WriteFile(filepath.Join(home, "config.json"), decoy, 0o644); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := host.Run(context.Background(), home, id); err != nil {
			t.Fatal(err)
		}
		elapsed = append(elapsed, time.Since(start))
		attempt, events := loadAttempt(t, home, id)
		classes = append(classes, attempt.ErrorClass)
		if attempt.ErrorClass != model.ErrorTimeout || !strings.Contains(attempt.ErrorExcerpt, "hard-limit timeout") {
			t.Fatalf("limit %s class %q excerpt %q", limit, attempt.ErrorClass, attempt.ErrorExcerpt)
		}
		ev, ok := eventType(events, "host_finish")
		if !ok || !strings.Contains(string(ev.Data), "hard_limit_timeout") {
			t.Fatalf("limit %s finish %#v", limit, ev)
		}
		if _, ok := eventType(events, "budget_duration"); ok {
			t.Fatalf("limit %s recorded a missing duration", limit)
		}
	}
	if elapsed[0] > 3*time.Second || elapsed[1] > 5*time.Second {
		t.Fatalf("deadlines too slow: %s %s", elapsed[0], elapsed[1])
	}
	if elapsed[1] < elapsed[0]+400*time.Millisecond {
		t.Fatalf("attempts did not die on their own limits: %s then %s classes %v", elapsed[0], elapsed[1], classes)
	}
}

func TestDescendantHoldingPipeCannotOutliveDeadline(t *testing.T) {
	var marker string
	home, id, work := seedAttempt(t, 1, func(work string) any {
		marker = filepath.Join(work, "late.txt")
		return map[string]any{
			"attempts": map[string]any{
				"0": map[string]any{
					"sleep":             "80ms",
					"grandchild_marker": marker,
					"grandchild_after":  "20s",
					"output":            "parent-done\n",
				},
			},
		}
	})
	writeDuration(t, home, id, "500ms")
	start := time.Now()
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 6*time.Second {
		t.Fatalf("host waited %s for a descendant holding the log pipe", time.Since(start))
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
		t.Fatalf("grandchild %d still alive after the deadline", pid)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("grandchild marker: %v", err)
	}
	attempt, _ := loadAttempt(t, home, id)
	if attempt.ErrorClass != model.ErrorTimeout || !strings.Contains(attempt.ErrorExcerpt, "hard-limit timeout") {
		t.Fatalf("class %q excerpt %q", attempt.ErrorClass, attempt.ErrorExcerpt)
	}
}

func TestDeadlineCoversPTYDrive(t *testing.T) {
	home, id, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{
			"needs_pty": true,
			"attempts": map[string]any{
				"0": map[string]any{"sleep": "30s", "output": "late-output\nprompt\n"},
			},
		}
	})
	writeDuration(t, home, id, "400ms")
	before := runner.FakeDriveCount()
	start := time.Now()
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 6*time.Second {
		t.Fatalf("timer started after Drive: %s", time.Since(start))
	}
	if runner.FakeDriveCount() != before+1 {
		t.Fatalf("Drive count %d", runner.FakeDriveCount()-before)
	}
	logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logb), "late-output") {
		t.Fatal("sleeper finished during Drive")
	}
	attempt, events := loadAttempt(t, home, id)
	if attempt.ErrorClass != model.ErrorTimeout {
		t.Fatalf("class %q", attempt.ErrorClass)
	}
	ev, ok := eventType(events, "host_finish")
	if !ok || !strings.Contains(string(ev.Data), "hard_limit_timeout") {
		t.Fatalf("finish %#v", ev)
	}
}

func TestPTYDriveReceivesTerminalBytes(t *testing.T) {
	markers := []string{"prompt", "completion", "budget_limited"}
	home, id, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{
			"needs_pty": true,
			"attempts": map[string]any{
				"0": map[string]any{"output": "prompt\ncompletion\nbudget_limited\n"},
			},
		}
	})
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := runner.FakeDriveCapture()
	if string(got) != string(logb) {
		t.Fatalf("Drive saw %q\nlog    %q", got, logb)
	}
	for _, marker := range markers {
		if !outputHasLine(string(got), marker) {
			t.Fatalf("Drive missed %q in %q", marker, got)
		}
	}
}

func TestPTYDriveErrorIsApplied(t *testing.T) {
	home, id, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{
			"needs_pty": true,
			"attempts": map[string]any{
				"0": map[string]any{"output": "drive_error: prompt missing\ncompletion\n"},
			},
		}
	})
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	attempt, events := loadAttempt(t, home, id)
	ev, ok := eventType(events, "host_finish")
	if !ok || !strings.Contains(string(ev.Data), "prompt missing") {
		t.Fatalf("finish %#v", ev)
	}
	if !strings.Contains(attempt.ErrorExcerpt, "prompt missing") {
		t.Fatalf("excerpt %q", attempt.ErrorExcerpt)
	}
	logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if err != nil || !strings.Contains(string(logb), "completion") {
		t.Fatalf("log %q %v", logb, err)
	}
}

func TestDurationMissingOrUnparseableIsRecorded(t *testing.T) {
	cases := []struct {
		file   string
		reason string
	}{
		{file: "", reason: "missing"},
		{file: "not-a-duration", reason: "unparseable"},
		{file: "0s", reason: "unparseable"},
	}
	for _, tc := range cases {
		t.Run(tc.reason+"/"+tc.file, func(t *testing.T) {
			home, id, _ := seedAttempt(t, 1, func(string) any {
				return map[string]any{
					"attempts": map[string]any{
						"0": map[string]any{"sleep": "200ms", "output": "slept\ntokens=4\n"},
					},
				}
			})
			if tc.file != "" {
				writeDuration(t, home, id, tc.file)
			}
			if err := host.Run(context.Background(), home, id); err != nil {
				t.Fatal(err)
			}
			logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
			if err != nil || !strings.Contains(string(logb), "slept") {
				t.Fatalf("run did not finish: %v %s", err, logb)
			}
			attempt, events := loadAttempt(t, home, id)
			if attempt.Exit == nil || *attempt.Exit != 0 || attempt.Usage.Tokens != 4 || attempt.Usage.Source != "fake" {
				t.Fatalf("attempt exit %#v usage %+v", attempt.Exit, attempt.Usage)
			}
			if attempt.ErrorClass == model.ErrorTimeout {
				t.Fatal("bad duration armed a timer")
			}
			ev, ok := eventType(events, "budget_duration")
			if !ok || !strings.Contains(string(ev.Data), tc.reason) {
				t.Fatalf("event %#v want %s", ev, tc.reason)
			}
		})
	}
}

func TestUnresolvedEngineProfileDoesNotStartClaude(t *testing.T) {
	cases := []struct {
		name   string
		config string
		reason string
	}{
		{name: "custom-engine", config: "", reason: "missing"},
		{name: "custom-engine", config: `{"engines":{}}`, reason: "unknown"},
		{name: "bad", config: `{"engines":{"bad":"nope"}}`, reason: "unparseable"},
		{name: "bad", config: `{`, reason: "unparseable"},
	}
	origPath := os.Getenv("PATH")
	for i, tc := range cases {
		// Subtest names must not contain '#': Go disambiguates duplicates with
		// that character, t.TempDir keeps it, and SQLite URI mode truncates there.
		t.Run(strconv.Itoa(i)+"_"+tc.reason, func(t *testing.T) {
			bin := t.TempDir()
			marker := filepath.Join(bin, "started")
			stub := "#!/bin/sh\ntouch " + strconv.Quote(marker) + "\n"
			if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+origPath)
			home, id, _ := seedPrepared(t, t.TempDir(), model.RunnerClaude, tc.name, 1, nil)
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(tc.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := host.Run(context.Background(), home, id); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("claude started: %v", err)
			}
			attempt, events := loadAttempt(t, home, id)
			if attempt.Exit != nil || attempt.EndedAt.IsZero() || !strings.Contains(attempt.ErrorExcerpt, tc.reason) {
				t.Fatalf("attempt exit %#v ended %v excerpt %q", attempt.Exit, attempt.EndedAt, attempt.ErrorExcerpt)
			}
			ev, ok := eventType(events, "needs_review")
			if !ok || !strings.Contains(string(ev.Data), tc.reason) {
				t.Fatalf("review event %#v", ev)
			}
			if _, ok := eventType(events, "host_start"); ok {
				t.Fatal("host started the default claude")
			}
			db, err := store.Open(filepath.Join(home, "cardex.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			card, err := db.GetCard(context.Background(), "c1")
			if err != nil {
				t.Fatal(err)
			}
			if card.Status != model.CardNeedsInput || !strings.Contains(card.WaitReason, tc.reason) {
				t.Fatalf("card status %q reason %q", card.Status, card.WaitReason)
			}
		})
	}
}

func TestReconcileDeadHostWithLiveChild(t *testing.T) {
	home, _, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{"attempts": map[string]any{"0": map[string]any{"output": "x"}}}
	})
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := child.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = child.Wait()
	})
	hostProc := exec.Command("/bin/sh", "-c", "exit 0")
	if err := hostProc.Start(); err != nil {
		t.Fatal(err)
	}
	hostPID := hostProc.Process.Pid
	if err := hostProc.Wait(); err != nil {
		t.Fatal(err)
	}
	if alive(hostPID) {
		t.Fatal("exited host pid still alive")
	}
	if !alive(pgid) {
		t.Fatal("child was not a live process")
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	live := model.Attempt{
		ID: "live", Card: "c1", Route: model.Route{Channel: "ch1", Model: "fake"},
		PID: os.Getpid(), Worktree: filepath.Join(home, "work"),
	}
	if err := db.CreateAttempt(ctx, &live); err != nil {
		t.Fatal(err)
	}
	orphan := model.Attempt{
		ID: "orphan", Card: "c1", Route: model.Route{Channel: "ch1", Model: "fake"},
		PID: hostPID, Worktree: filepath.Join(home, "work"),
	}
	if err := db.CreateAttempt(ctx, &orphan); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "attempts", "orphan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "executor.pgid"), []byte(strconv.Itoa(pgid)+"\n"), 0o644); err != nil {
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
	if !host.LiveHost(gotLive) || gotLive.PID != before.PID || !gotLive.EndedAt.IsZero() || gotLive.Exit != nil {
		t.Fatalf("live host changed %#v", gotLive)
	}
	got, err := db.GetAttempt(ctx, "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if !got.EndedAt.IsZero() || got.Exit != nil || !alive(pgid) {
		t.Fatalf("live child marked ended %#v alive %v", got, alive(pgid))
	}
	if err := host.Reconcile(ctx, home); err != nil {
		t.Fatal(err)
	}
	again, err := db.GetAttempt(ctx, "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if !again.EndedAt.IsZero() {
		t.Fatal("second reconcile ended a live child")
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if alive(pgid) {
		t.Fatal("child still alive after cleanup")
	}
	if err := host.Reconcile(ctx, home); err != nil {
		t.Fatal(err)
	}
	ended, err := db.GetAttempt(ctx, "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if ended.EndedAt.IsZero() || host.LiveHost(ended) || ended.PID != hostPID {
		t.Fatalf("ended orphan %#v", ended)
	}
}
