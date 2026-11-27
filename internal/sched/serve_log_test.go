package sched

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServeKeepsLegacyAndLimitsIdleWrites(t *testing.T) {
	home := t.TempDir()
	writeEmptySchedules(t, home)
	legacy := filepath.Join(home, "serve-loop.log")
	const history = "historical operator evidence\n"
	if err := os.WriteFile(legacy, []byte(history), 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out lockedBuf
	done := make(chan error, 1)
	go func() { done <- Serve(WithLog(ctx, &out), home, "127.0.0.1:0") }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "serve listening ") {
		if time.Now().After(deadline) {
			t.Fatal("scheduler did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Long enough for at least several 150ms scheduler rounds, but well
	// below the production heartbeat interval.
	time.Sleep(850 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop")
	}
	after, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	outcome := out.String()
	t.Logf("legacy append calls=%d added bytes=%d output records=%d", strings.Count(string(raw), "\n")-1, len(raw)-len(history), strings.Count(outcome, "\n"))
	if string(raw) != history || !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() || !os.SameFile(before, after) {
		t.Errorf("modified historical log: %q", raw)
	}
	if strings.Count(outcome, " heartbeat ") != 1 || strings.Count(outcome, "\n") != 2 {
		t.Errorf("expected listening and one idle heartbeat, got %q", outcome)
	}
}

func serveWithFastHeartbeats(ctx context.Context, home string) error {
	current := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	return serve(ctx, home, "127.0.0.1:0", func() time.Time { return current }, func(ctx context.Context) bool {
		current = current.Add(time.Minute)
		return waitServeTick(ctx)
	})
}

func readServeLog(home string) ([]byte, error) {
	names, err := filepath.Glob(filepath.Join(home, serveLogDir, "segment-*.log"))
	if err != nil {
		return nil, err
	}
	var res []byte
	for _, name := range names {
		body, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		res = append(res, body...)
	}
	return res, nil
}

func TestServeFakeClockLogsActualTickErrorAndRecovery(t *testing.T) {
	home := filepath.Join(t.TempDir(), "private-repo-SECRET-canary")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	writeEmptySchedules(t, home)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out lockedBuf
	ctx = WithLog(ctx, &out)
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	start := now
	var round, records, bytes int
	err := serve(ctx, home, "127.0.0.1:0", func() time.Time { return now }, func(context.Context) bool {
		round++
		raw, err := readServeLog(home)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		records = strings.Count(body, "\n") - 1
		bytes = len(body)
		if strings.Contains(body, "SECRET") || strings.Contains(out.String(), "SECRET") || strings.Contains(body, "config.json") {
			t.Fatalf("scheduler log leaked filesystem details: %q", body)
		}
		config := filepath.Join(home, "config.json")
		switch round {
		case 1:
			if records != 1 || !strings.Contains(body, "heartbeat tick=1") {
				t.Fatalf("initial heartbeat: %q", body)
			}
			now = start.Add(time.Minute - time.Nanosecond)
		case 2:
			if records != 1 {
				t.Fatalf("idle tick wrote before the exact deadline: %q", body)
			}
			if err := os.Remove(config); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(config, 0o700); err != nil {
				t.Fatal(err)
			}
		case 3:
			if records != 3 || !strings.Contains(body, "error step=schedules tick=3") || !strings.Contains(body, "transition state=degraded tick=3") {
				t.Fatalf("missing immediate error/degraded transition: %q", body)
			}
			if err := os.Remove(config); err != nil {
				t.Fatal(err)
			}
			writeEmptySchedules(t, home)
		case 4:
			if records != 4 || !strings.Contains(body, "transition state=healthy tick=4") {
				t.Fatalf("missing immediate recovery: %q", body)
			}
			now = start.Add(time.Minute)
		case 5:
			if records != 5 || !strings.Contains(body, "heartbeat tick=5") {
				t.Fatalf("missing exact-boundary heartbeat: %q", body)
			}
			return false
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual production tick calls=%d journal writes=%d bytes=%d (two heartbeats; idle round 2 wrote zero bytes)", round, records, bytes)
}

func TestServeJournalBoundedAcrossRestartAndPreservesOtherFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, serveLogDir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	preexisting := map[string]string{
		filepath.Join(home, "serve-loop.log"):   "legacy",
		filepath.Join(home, "serve-loop.log.1"): "archived",
		filepath.Join(dir, "unowned.log"):       "another writer",
		filepath.Join(dir, segmentName(2)):      "same pattern, not ours",
	}
	for p, body := range preexisting {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(home, "serve-loop.log"), filepath.Join(dir, segmentName(3))); err != nil {
		t.Logf("hardlink unavailable: %v", err)
	} else {
		preexisting[filepath.Join(dir, segmentName(3))] = "legacy"
	}
	const maxSize = int64(len(serveSegmentMark) + 12)
	const maxFiles = 3
	j := serveJournal{home: home, maxSize: maxSize, maxFiles: maxFiles}
	owned := func() []string {
		t.Helper()
		names, err := filepath.Glob(filepath.Join(dir, "segment-*.log"))
		if err != nil {
			t.Fatal(err)
		}
		var found []string
		for _, p := range names {
			if _, unrelated := preexisting[p]; unrelated {
				continue
			}
			body, err := os.ReadFile(p)
			if err != nil || int64(len(body)) > maxSize || !strings.HasPrefix(string(body), serveSegmentMark) {
				t.Fatalf("invalid segment %s: %q: %v", p, body, err)
			}
			found = append(found, string(body))
		}
		if len(found) > maxFiles {
			t.Fatalf("%d retained, cap %d", len(found), maxFiles)
		}
		return found
	}
	write := func(s string) {
		t.Helper()
		if err := j.append(s); err != nil {
			t.Fatal(err)
		}
		owned()
	}
	write("00000\n")
	write("11111\n")
	if files := owned(); len(files) != 1 || int64(len(files[0])) != maxSize {
		t.Fatalf("exact byte limit rotated prematurely: %+v", files)
	}
	for _, line := range []string{"22222\n", "33333\n", "44444\n", "55555\n", "66666\n"} {
		write(line)
	}
	if err := j.append(strings.Repeat("x", int(maxSize))); err == nil {
		t.Fatal("oversize record accepted")
	}
	j.close()
	j = serveJournal{home: home, maxSize: maxSize, maxFiles: maxFiles}
	defer j.close()
	write("77777\n")
	files := owned()
	if got := strings.Join(files, ""); len(files) != maxFiles || strings.Contains(got, "33333") || !strings.Contains(got, "44444\n55555\n") || !strings.Contains(got, "77777") {
		t.Fatalf("retention across restart: %+v", files)
	}
	for path, want := range preexisting {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("historical/unowned %s: %q %v", path, got, err)
		}
	}
	t.Logf("retained segments=%d cap=%d bytes/segment, window cap=%d bytes", len(files), maxSize, maxFiles*maxSize)
}

func TestServeJournalRejectsSymlinkNamespace(t *testing.T) {
	home := t.TempDir()
	history := t.TempDir()
	pre := filepath.Join(history, "serve-loop.log")
	if err := os.WriteFile(pre, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(history, filepath.Join(home, serveLogDir)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	j := serveJournal{home: home, maxSize: serveSegmentSize, maxFiles: serveSegments}
	defer j.close()
	if err := j.append("heartbeat\n"); err == nil {
		t.Fatal("journal followed namespace symlink")
	}
	if files, err := os.ReadDir(history); err != nil || len(files) != 1 {
		t.Fatalf("history directory touched: %v %v", files, err)
	}
	got, err := os.ReadFile(pre)
	if err != nil || string(got) != "unchanged\n" {
		t.Fatalf("history touched: %q %v", got, err)
	}
}

func TestServeFakeClockIdleWriteBudget(t *testing.T) {
	home := t.TempDir()
	writeEmptySchedules(t, home)
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	rounds := 0
	err := serve(t.Context(), home, "127.0.0.1:0", func() time.Time { return now }, func(context.Context) bool {
		rounds++
		now = now.Add(150 * time.Millisecond)
		return rounds < 401
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readServeLog(home)
	if err != nil {
		t.Fatal(err)
	}
	writes := strings.Count(string(raw), "\n") - 1
	if writes != 2 || !strings.Contains(string(raw), "heartbeat tick=1\n") || !strings.Contains(string(raw), "heartbeat tick=401\n") {
		t.Fatalf("idle budget: %q", raw)
	}
	t.Logf("actual idle tick calls=%d journal writes=%d total bytes=%d", rounds, writes, len(raw))
}

func TestServeLogsScheduleTransitionWithoutPrivateFields(t *testing.T) {
	home, db, repo := newHome(t)
	_, objective := seedRepo(t, db, repo, "true")
	config := fmt.Sprintf(`{"schedules":[{"name":"SECRET_CANARY_NAME","every":"1h","card_template":{"objective":%q,"title":"SECRET_CANARY_TITLE","body":"SECRET_CANARY_BODY"}}]}`, objective)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var out lockedBuf
	ctx := WithLog(t.Context(), &out)
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	if err := serve(ctx, home, "127.0.0.1:0", func() time.Time { return now }, func(context.Context) bool {
		raw, err := readServeLog(home)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "transition state=schedule_enqueued tick=1") {
			t.Fatalf("schedule transition not visible on the first tick: %q", raw)
		}
		if strings.Contains(string(raw), "SECRET_CANARY") || strings.Contains(out.String(), "SECRET_CANARY") {
			t.Fatal("schedule's private fields leaked into journal or CLI output")
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
	cards, err := db.ListCards(t.Context(), objective)
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range cards {
		if card.Title == "SECRET_CANARY_TITLE" && card.Body == "SECRET_CANARY_BODY" {
			return
		}
	}
	t.Fatal("schedule fixture did not create the expected card")
}

func TestServeRepeatedFaultRemainsDegradedWithoutLogChurn(t *testing.T) {
	home := t.TempDir()
	writeEmptySchedules(t, home)
	legacy := filepath.Join(home, "serve-loop.log")
	const history = "pre-existing history\n"
	if err := os.WriteFile(legacy, []byte(history), 0o600); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	now := base
	round := 0
	config := filepath.Join(home, "config.json")
	var out lockedBuf
	err := serve(WithLog(t.Context(), &out), home, "127.0.0.1:0", func() time.Time { return now }, func(context.Context) bool {
		round++
		switch round {
		case 1:
			if err := os.Remove(config); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(config, 0o700); err != nil {
				t.Fatal(err)
			}
		case 402:
			now = base.Add(150*time.Millisecond + tickErrorWindow - time.Nanosecond)
			return true
		case 403:
			now = now.Add(time.Nanosecond)
			return true
		case 404:
			if err := os.Remove(config); err != nil {
				t.Fatal(err)
			}
			writeEmptySchedules(t, home)
		case 405:
			return false
		}
		now = now.Add(150 * time.Millisecond)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readServeLog(home)
	if err != nil {
		t.Fatal(err)
	}
	body, cli := string(raw), out.String()
	for _, got := range []string{body, cli} {
		errs := strings.Count(got, "error step=schedules ")
		t.Logf("same fault across 403 rounds: error records=%d total bytes=%d", errs, len(got))
		if errs != 2 || !strings.Contains(got, "error step=schedules tick=2\n") || !strings.Contains(got, "error step=schedules tick=404\n") {
			t.Errorf("wanted the first fault and one fault at the exact cooldown bound; got %d", errs)
		}
		if strings.Count(got, "transition state=degraded ") != 1 || !strings.Contains(got, "transition state=degraded tick=2\n") ||
			strings.Count(got, "transition state=healthy ") != 1 || !strings.Contains(got, "transition state=healthy tick=405\n") {
			t.Errorf("health did not stay degraded through suppressed faults")
		}
		if strings.Contains(got, "config.json") || strings.Contains(got, home) {
			t.Errorf("fault exposed private data")
		}
	}
	if b, err := os.ReadFile(legacy); err != nil || string(b) != history {
		t.Fatalf("legacy log modified: %q %v", b, err)
	}
}

func TestServeDistinctFaultInSameStageIsImmediate(t *testing.T) {
	home, db, repo := newHome(t)
	writeEmptySchedules(t, home)
	_, objective := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(t.Context(), objective)
	if err != nil {
		t.Fatal(err)
	}
	obj.Mode = "auto"
	if err := db.UpdateObjective(t.Context(), &obj); err != nil {
		t.Fatal(err)
	}
	// Objectives are advanced in ID order; observe the first fault from the
	// second objective before the enclosing Tick has completed.
	observer := obj
	observer.ID = "z-observer"
	if err := db.CreateObjective(t.Context(), &observer); err != nil {
		t.Fatal(err)
	}
	var out lockedBuf
	runner := New(home)
	round := 1
	runner.Engine = func(_ context.Context, id string) error {
		if id == observer.ID {
			if round == 1 {
				raw, err := readServeLog(home)
				if err != nil || !strings.Contains(string(raw), "transition state=degraded tick=1\n") {
					t.Fatalf("degraded transition was not visible before Tick completed: %q %v", raw, err)
				}
			}
			return nil
		}
		switch round {
		case 1, 2:
			return errors.New("secret-canary-first")
		case 3:
			return errors.New("secret-canary-second")
		default:
			return nil
		}
	}
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	if err := serveTicks(WithLog(t.Context(), &out), runner, func() time.Time { return now }, func(context.Context) bool {
		round++
		now = now.Add(150 * time.Millisecond)
		return round <= 4
	}, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := readServeLog(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []string{string(raw), out.String()} {
		if strings.Count(got, "error step=advance ") != 2 || !strings.Contains(got, "error step=advance tick=1\n") ||
			!strings.Contains(got, "error step=advance tick=3\n") {
			t.Errorf("distinct first faults not immediately visible: %q", got)
		}
		if strings.Contains(got, "secret-canary") {
			t.Error("fault text leaked into operational logs")
		}
	}
}

func TestServeFaultLimiterBoundsRetainedFingerprints(t *testing.T) {
	seen := make(serveFaultLimiter)
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	for i := 0; i < serveFaultLimit*2; i++ {
		fault := serveFault{stage: strconv.Itoa(i)}
		if !seen.allow(now, fault) || seen.allow(now, fault) {
			t.Fatalf("first fault or duplicate mishandled at %d", i)
		}
		if len(seen) > serveFaultLimit {
			t.Fatal("fingerprint memory budget exceeded")
		}
		now = now.Add(time.Millisecond)
	}
}
