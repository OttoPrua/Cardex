package host_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/store"
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
	for _, marker := range markers {
		if !outputHasLine(string(logb), marker) {
			t.Fatalf("log missed %q in %q", marker, logb)
		}
	}
	if !outputHasLine(string(got), "prompt") || !outputHasLine(string(got), "completion") {
		t.Fatalf("Drive missed the terminal prefix %q", got)
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

func TestDurationMissingOrUnparseableDoesNotStart(t *testing.T) {
	cases := []struct {
		name   string
		file   string
		remove bool
		reason string
	}{
		{name: "missing", remove: true, reason: "missing"},
		{name: "empty", file: "", reason: "missing"},
		{name: "blank", file: "   ", reason: "missing"},
		{name: "text", file: "not-a-duration", reason: "unparseable"},
		{name: "zero", file: "0s", reason: "unparseable"},
		{name: "negative", file: "-1s", reason: "unparseable"},
		{name: "unitless", file: "0", reason: "unparseable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, _ := seedAttempt(t, 9, func(string) any {
				return map[string]any{
					"attempts": map[string]any{
						"0": map[string]any{"sleep": "30s", "output": "slept\ntokens=4\n"},
					},
				}
			})
			path := filepath.Join(home, "attempts", id, "hard-duration")
			if tc.remove {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(tc.file), 0o644); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if err := host.Run(context.Background(), home, id); err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatalf("executor started: %s", time.Since(start))
			}
			if _, err := os.Stat(filepath.Join(home, "attempts", id, "output.log")); !os.IsNotExist(err) {
				t.Fatalf("executor output: %v", err)
			}
			attempt, events := loadAttempt(t, home, id)
			if attempt.Exit != nil || attempt.EndedAt.IsZero() || !strings.Contains(attempt.ErrorExcerpt, tc.reason) {
				t.Fatalf("attempt exit %#v ended %v excerpt %q", attempt.Exit, attempt.EndedAt, attempt.ErrorExcerpt)
			}
			if _, ok := eventType(events, "host_start"); ok {
				t.Fatal("host started the executor")
			}
			ev, ok := eventType(events, "needs_review")
			if !ok || !strings.Contains(string(ev.Data), tc.reason) {
				t.Fatalf("review event %#v", ev)
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
		{name: "custom", config: `{"engines":{"custom":{}}}`, reason: "unparseable"},
		{name: "custom", config: `{"engines":{"custom":{"not_a_real_field":"x"}}}`, reason: "unparseable"},
		{name: "custom", config: `{"engines":{"custom":{"base_url":" ","auth_env":" ","auth_value":"","auth_file":" ","models":{"opus":" "}}}}`, reason: "unparseable"},
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

func groupAlive(pgid int) bool {
	if pgid <= 1 {
		return false
	}
	err := host.SignalGroup(pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func TestDescendantClosingStdioCannotOutliveDeadline(t *testing.T) {
	var marker string
	home, id, work := seedAttempt(t, 1, func(work string) any {
		marker = filepath.Join(work, "late.txt")
		return map[string]any{
			"attempts": map[string]any{
				"0": map[string]any{
					"sleep":             "80ms",
					"grandchild_marker": marker,
					"grandchild_after":  "20s",
					"grandchild_detach": true,
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
		t.Fatalf("host waited %s after the descendant closed stdio", time.Since(start))
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

func TestDriveReturnStillDrainsLog(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		tail    string
		excerpt string
	}{
		{name: "completion", output: "prompt\ncompletion\n", tail: "AFTER-DRIVE-completion\n"},
		{name: "budget_limited", output: "budget_limited\n", tail: "AFTER-DRIVE-budget\n"},
		{name: "error", output: "drive_error: lost the prompt\n", tail: "AFTER-DRIVE-error\n", excerpt: "lost the prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, _ := seedAttempt(t, 1, func(string) any {
				return map[string]any{
					"needs_pty": true,
					"attempts": map[string]any{
						"0": map[string]any{
							"output":     tc.output,
							"tail_after": "400ms",
							"tail":       tc.tail,
						},
					},
				}
			})
			start := time.Now()
			if err := host.Run(context.Background(), home, id); err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > 6*time.Second {
				t.Fatalf("host did not finish after Drive returned: %s", time.Since(start))
			}
			logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(logb), strings.TrimSpace(tc.tail)) {
				t.Fatalf("log missing output produced after Drive returned: %s", logb)
			}
			if strings.Contains(string(runner.FakeDriveCapture()), strings.TrimSpace(tc.tail)) {
				t.Fatal("Drive kept reading after the terminal")
			}
			attempt, _ := loadAttempt(t, home, id)
			if attempt.EndedAt.IsZero() {
				t.Fatal("attempt was not written back")
			}
			if tc.excerpt != "" && !strings.Contains(attempt.ErrorExcerpt, tc.excerpt) {
				t.Fatalf("excerpt %q", attempt.ErrorExcerpt)
			}
		})
	}
}

func TestDriveBlockUnblockedByDeadlineOrCancel(t *testing.T) {
	big := "AFTER-DRIVE-BLOCK\n" + strings.Repeat("x", 256*1024) + "\n"
	script := func(string) any {
		return map[string]any{
			"needs_pty": true,
			"attempts": map[string]any{
				"0": map[string]any{
					"output":     "drive_return\n",
					"tail_after": "50ms",
					"tail":       big,
					"hold":       "30s",
				},
			},
		}
	}
	// waitBlocked fails if host.Run returns before the executor group is alive
	// and still held after Drive has returned.
	waitBlocked := func(t *testing.T, home, id string, errc <-chan error, settle time.Duration) int {
		t.Helper()
		var pgid int
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case err := <-errc:
				t.Fatalf("host returned before the executor was running: %v", err)
			default:
			}
			raw, err := os.ReadFile(filepath.Join(home, "attempts", id, "executor.pgid"))
			if err == nil {
				n, conv := strconv.Atoi(strings.TrimSpace(string(raw)))
				if conv == nil && n > 1 && groupAlive(n) {
					pgid = n
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		if pgid <= 1 {
			t.Fatal("executor did not start")
		}
		select {
		case err := <-errc:
			t.Fatalf("host returned before the capture was blocked: %v", err)
		case <-time.After(settle):
		}
		if !groupAlive(pgid) {
			t.Fatal("executor exited before the capture was released")
		}
		return pgid
	}
	waitGone := func(t *testing.T, pgid int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for groupAlive(pgid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if groupAlive(pgid) {
			t.Fatalf("executor group %d still alive", pgid)
		}
	}
	t.Run("deadline", func(t *testing.T) {
		home, id, _ := seedAttempt(t, 1, script)
		writeDuration(t, home, id, "2s")
		start := time.Now()
		errc := make(chan error, 1)
		go func() { errc <- host.Run(context.Background(), home, id) }()
		pgid := waitBlocked(t, home, id, errc, 300*time.Millisecond)
		select {
		case err := <-errc:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("deadline did not unblock capture")
		}
		if time.Since(start) < 1500*time.Millisecond {
			t.Fatalf("returned in %s, before the hard duration", time.Since(start))
		}
		waitGone(t, pgid)
		logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
		if err != nil || !strings.Contains(string(logb), "AFTER-DRIVE-BLOCK") {
			t.Fatalf("log %q %v", logb, err)
		}
		attempt, _ := loadAttempt(t, home, id)
		if attempt.ErrorClass != model.ErrorTimeout || attempt.EndedAt.IsZero() {
			t.Fatalf("class %q ended %v", attempt.ErrorClass, attempt.EndedAt)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		home, id, _ := seedAttempt(t, 1, script)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		errc := make(chan error, 1)
		go func() { errc <- host.Run(ctx, home, id) }()
		pgid := waitBlocked(t, home, id, errc, 400*time.Millisecond)
		cancel()
		select {
		case <-errc:
		case <-time.After(6 * time.Second):
			t.Fatal("cancel did not unblock capture")
		}
		waitGone(t, pgid)
		logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
		if err != nil || !strings.Contains(string(logb), "AFTER-DRIVE-BLOCK") {
			t.Fatalf("log %q %v", logb, err)
		}
	})
}

func TestUsableEngineProfileInjectsBaseURL(t *testing.T) {
	bin := t.TempDir()
	stub := "#!/bin/sh\nprintf 'BASE=%s\\n' \"$ANTHROPIC_BASE_URL\"\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	home, id, _ := seedPrepared(t, t.TempDir(), model.RunnerClaude, "custom", 1, nil)
	cfg := `{"engines":{"custom":{"base_url":"https://example.test/v1","auth_value":"token-value"}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logb), "BASE=https://example.test/v1") {
		t.Fatalf("log %s", logb)
	}
	_, events := loadAttempt(t, home, id)
	if _, ok := eventType(events, "needs_review"); ok {
		t.Fatal("usable profile was sent to review")
	}
}

func TestReconcileReapsOrphanAfterHardDuration(t *testing.T) {
	const limit = 4 * time.Second
	home, id, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{
			"attempts": map[string]any{
				"0": map[string]any{"sleep": "30s", "output": "should-not-finish\n"},
			},
		}
	})
	writeDuration(t, home, id, limit.String())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	proc.NewGroup(cmd)
	cmd.Env = append(os.Environ(),
		"CARDEX_HOST_REEXEC=1",
		"CARDEX_REEXEC_HOME="+home,
		"CARDEX_REEXEC_ATTEMPT="+id,
	)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	hostPID := cmd.Process.Pid
	var pgid int
	waited := false
	t.Cleanup(func() {
		if pgid > 1 && groupAlive(pgid) {
			_ = host.SignalGroup(pgid, syscall.SIGKILL)
		}
		if !waited && cmd.Process != nil {
			_ = host.SignalProcess(cmd.Process.Pid, syscall.SIGKILL)
			_, _ = cmd.Process.Wait()
		}
	})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(home, "attempts", id, "executor.pgid"))
		if err == nil {
			n, conv := strconv.Atoi(strings.TrimSpace(string(raw)))
			if conv == nil && n > 1 && groupAlive(n) {
				pgid = n
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pgid <= 1 || !groupAlive(pgid) {
		t.Fatalf("executor group did not start: %s", logs.String())
	}
	if err := host.SignalProcess(hostPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waited = true
	if alive(hostPID) {
		t.Fatal("host still alive")
	}
	if !groupAlive(pgid) {
		t.Fatal("killing the host killed the executor group")
	}

	sleeper := exec.Command("/bin/sleep", "30")
	proc.NewGroup(sleeper)
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	livePGID := sleeper.Process.Pid
	t.Cleanup(func() {
		_ = host.SignalGroup(livePGID, syscall.SIGKILL)
		_ = sleeper.Wait()
	})
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	live := model.Attempt{
		ID: "live", Card: "c1", Route: model.Route{Channel: "ch1", Model: "fake"},
		PID: os.Getpid(), StartedAt: time.Now().Add(-time.Hour).UTC(),
		Worktree: filepath.Join(home, "work"),
	}
	if err := db.CreateAttempt(ctx, &live); err != nil {
		t.Fatal(err)
	}
	liveDir := filepath.Join(home, "attempts", "live")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "executor.pgid"), []byte(strconv.Itoa(livePGID)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "hard-duration"), []byte("1ms"), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeLive, err := db.GetAttempt(ctx, "live")
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := db.GetAttempt(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if orphan.PID != hostPID {
		t.Fatalf("host pid %d stored %d", hostPID, orphan.PID)
	}
	if !time.Now().Before(orphan.StartedAt.Add(limit)) {
		t.Fatalf("deadline already passed: started %s", orphan.StartedAt)
	}
	if err := host.Reconcile(ctx, home); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAttempt(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.EndedAt.IsZero() || !groupAlive(pgid) {
		t.Fatalf("reconcile ended a group still inside its duration %#v alive %v", got, groupAlive(pgid))
	}
	gotLive, err := db.GetAttempt(ctx, "live")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotLive, beforeLive) || !groupAlive(livePGID) {
		t.Fatalf("live host changed %#v sleeper %v", gotLive, groupAlive(livePGID))
	}
	wait := time.Until(orphan.StartedAt.Add(limit)) + 200*time.Millisecond
	if wait > 0 {
		time.Sleep(wait)
	}
	if err := host.Reconcile(ctx, home); err != nil {
		t.Fatal(err)
	}
	if groupAlive(pgid) {
		t.Fatal("reconcile left the orphan group alive")
	}
	ended, err := db.GetAttempt(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if ended.EndedAt.IsZero() || host.LiveHost(ended) {
		t.Fatalf("attempt stored while the group lived or not at all %#v", ended)
	}
	againLive, err := db.GetAttempt(ctx, "live")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(againLive, beforeLive) || !groupAlive(livePGID) {
		t.Fatalf("live host changed after reap %#v", againLive)
	}
}
