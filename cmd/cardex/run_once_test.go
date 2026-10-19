package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func onceTestCfg(t *testing.T, claudeBin string) *Config {
	t.Helper()
	cfg := runTaskCfg(t, claudeBin)
	cfg.HarvestMode = harvestModeOff
	cfg.DrainRescanSec = 1
	cfg.RetryBackoffMin = 0
	cfg.MaxAttempts = 3
	cfg.MaxParallel = 2
	return cfg
}

func saveOnceCfg(t *testing.T, root string, cfg *Config) {
	t.Helper()
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
}

func fakeClaude429Counted(t *testing.T, countPath, session string) string {
	t.Helper()
	payload := `{"type":"result","result":"API Error: Request rejected (429) · Service Unavailable","session_id":"` + session + `","num_turns":1,"total_cost_usd":0.01,"duration_ms":10,"is_error":true}`
	return countingClaudeBin(t, countPath, payload)
}

func writeFakeClaude429ArgvCapture(t *testing.T, countPath, session string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version|-v) echo 'claude 1.0'; exit 0;; esac\n" +
		"cat >/dev/null\n" +
		"printf x >> " + shSingleQuote(countPath) + "\n" +
		"printf '%s\\n' \"$@\" > .cardex-fake-claude-argv\n" +
		"cat <<'JSON_EOF'\n" +
		`{"type":"result","result":"API Error: Request rejected (429) · Service Unavailable","session_id":"` + session + `","num_turns":1,"total_cost_usd":0.01,"duration_ms":10,"is_error":true}` + "\n" +
		"JSON_EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readMaybe(path string) []byte {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return append([]byte(nil), raw...)
}

func neighborControlSnapshot(t *testing.T, root string, ids ...string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte, len(ids)*3)
	for _, id := range ids {
		out["task:"+id] = readMaybe(taskPath(root, id))
		out["events:"+id] = readMaybe(eventsPath(root, id))
		out["archive:"+id] = readMaybe(filepath.Join(archiveDir(root), id+".json"))
	}
	return out
}

func assertSnapshotUnchanged(t *testing.T, before, after map[string][]byte) {
	t.Helper()
	for k, want := range before {
		got := after[k]
		if !bytes.Equal(want, got) {
			t.Fatalf("non-target %s mutated", k)
		}
	}
}

func TestParseRunOnceRejectsMissingEmptyMultipleAllQueue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
	}{
		{"missing", []string{"-once"}},
		{"missing-with-root", []string{"-once", "-root", "/no-such-cardex-root"}},
		{"empty", []string{"-once", ""}},
		{"whitespace", []string{"-once", "  "}},
		{"multiple", []string{"-once", "t-a", "t-b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseRunArgs(tc.args)
			if err == nil {
				t.Fatal("parseRunArgs must reject")
			}
			if !errors.Is(err, errRunOnceUsage) {
				t.Fatalf("want errRunOnceUsage, got %v", err)
			}
		})
	}
	err := cmdRun([]string{"-once"})
	if !errors.Is(err, errRunOnceUsage) {
		t.Fatalf("cmdRun -once without ID must fail before loadConfig, got %v", err)
	}
	err = cmdRun([]string{"-once", "-root", "/no-such-cardex-root"})
	if !errors.Is(err, errRunOnceUsage) {
		t.Fatalf("cmdRun -once all-queue must fail before loadConfig, got %v", err)
	}
}

func TestParseRunOrdinaryDefaultsUnchanged(t *testing.T) {
	t.Parallel()
	opts, err := parseRunArgs([]string{"-root", "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.once || opts.id != "" || opts.root != "/x" || opts.force || opts.quiet {
		t.Fatalf("ordinary no-ID parse: %+v", opts)
	}
	opts, err = parseRunArgs([]string{"-root", "/x", "-quiet", "t-keep"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.once || opts.id != "t-keep" || !opts.quiet {
		t.Fatalf("ordinary ID parse: %+v", opts)
	}
	opts, err = parseRunArgs([]string{"-once", "-root", "/x", "-quiet", "t-one"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.once || opts.id != "t-one" || opts.root != "/x" || !opts.quiet {
		t.Fatalf("once parse: %+v", opts)
	}
	_, err = parseRunArgs([]string{"t-a", "t-b"})
	if err == nil {
		t.Fatal("ordinary multiple IDs must still reject")
	}
	if errors.Is(err, errRunOnceUsage) {
		t.Fatal("ordinary multiple-ID error must stay the existing usage path")
	}
}

func TestCmdRunIDReentersAfterImmediateRequeueWithoutOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider")
	}
	root := testRoot(t)
	count := filepath.Join(t.TempDir(), "calls")
	cfg := onceTestCfg(t, fakeClaude429Counted(t, count, "sess-reenter"))
	saveOnceCfg(t, root, cfg)
	tk := newTask(root, cfg, typeSequence, "ordinary-reenter", t.TempDir(), []string{"p"}, 5)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	_, err := captureCmdRun(t, []string{"-root", root, "-quiet", tk.ID})
	if err != nil {
		t.Fatalf("ordinary run ID requeue currently returns nil after drain, got %v", err)
	}
	n := providerCallCount(t, count)
	if n < 2 {
		t.Fatalf("ordinary run ID must re-enter after immediate requeue, provider calls=%d", n)
	}
}

func TestCmdRunOnceOneAttemptOn429IncludingElapsedBackoff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider")
	}
	root := testRoot(t)
	count := filepath.Join(t.TempDir(), "calls")
	session := "717ba90e-568f-4241-8dca-1ae0ba338150"
	cfg := onceTestCfg(t, fakeClaude429Counted(t, count, session))
	saveOnceCfg(t, root, cfg)
	work := t.TempDir()
	tk := newTask(root, cfg, typeSequence, "once-429", work, []string{"p"}, 5)
	tk.Attempts = 1
	tk.SessionID = session
	tk.LastError = "success: API Error: Request rejected (429) · Service Unavailable"
	tk.NotBeforeEpoch = 1
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	entry := loadMust(t, root, tk.ID)
	out, err := captureCmdRun(t, []string{"-once", "-root", root, tk.ID})
	if err == nil {
		t.Fatalf("once 429 must be nonzero, stdout=%q", out)
	}
	if !errors.Is(err, errRunOnceIncomplete) {
		t.Fatalf("want errRunOnceIncomplete, got %v stdout=%q", err, out)
	}
	if !strings.Contains(err.Error(), "429") && !strings.Contains(out, "429") {
		t.Fatalf("429 must appear in reported disposition: err=%v stdout=%q", err, out)
	}
	if strings.Contains(out, "完成") {
		t.Fatalf("requeue must not print success: %q", out)
	}
	if providerCallCount(t, count) != 1 {
		t.Fatalf("once must dispatch one attempt, calls=%d", providerCallCount(t, count))
	}
	got := loadMust(t, root, tk.ID)
	if got.Status != statusQueued {
		t.Fatalf("status=%s want queued", got.Status)
	}
	if got.Attempts != entry.Attempts+1 {
		t.Fatalf("attempts=%d want entry %d plus one", got.Attempts, entry.Attempts)
	}
	if got.SessionID == "" {
		t.Fatal("session cleared")
	}
	if !strings.Contains(got.LastError, "429") {
		t.Fatalf("last_error lost 429: %q", got.LastError)
	}
}

func TestCmdRunOncePreservesPinnedSessionResume(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider")
	}
	root := testRoot(t)
	count := filepath.Join(t.TempDir(), "calls")
	session := "sess-pinned-once"
	cfg := onceTestCfg(t, writeFakeClaude429ArgvCapture(t, count, session))
	saveOnceCfg(t, root, cfg)
	work := t.TempDir()
	tk := newTask(root, cfg, typeSequence, "once-resume", work, []string{"p"}, 5)
	tk.SessionID = session
	tk.Attempts = 1
	tk.NotBeforeEpoch = 1
	tk.LastError = "API Error: Request rejected (429) · Service Unavailable"
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	out, err := captureCmdRun(t, []string{"-once", "-root", root, tk.ID})
	if err == nil {
		t.Fatalf("once 429 resume must be nonzero, stdout=%q", out)
	}
	if providerCallCount(t, count) != 1 {
		t.Fatalf("calls=%d", providerCallCount(t, count))
	}
	argv := readCapturedArgv(t, work)
	if !argvHasResume(argv, session) {
		t.Fatalf("pinned session must --resume %s, argv=%v stdout=%s", session, argv, out)
	}
	got := loadMust(t, root, tk.ID)
	if got.SessionID != session {
		t.Fatalf("session rewritten: %q", got.SessionID)
	}
}

func TestCmdRunOnceHeldAndGoalAndLiveWriterStillRefuse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider")
	}
	t.Run("held", func(t *testing.T) {
		root := testRoot(t)
		count := filepath.Join(t.TempDir(), "calls")
		cfg := onceTestCfg(t, countingClaudeBin(t, count, mkOKResultJSON("sess")))
		saveOnceCfg(t, root, cfg)
		tk := newTask(root, cfg, typeSequence, "held-once", t.TempDir(), []string{"p"}, 5)
		tk.Status = statusHeld
		tk.LastError = "native done held: fake_executing_no_contract_evidence"
		markControlTerminal(tk)
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		before := taskFileBytes(t, root, tk.ID)
		err := cmdRun([]string{"-once", "-root", root, "-quiet", tk.ID})
		if err == nil {
			t.Fatal("held -once must be nonzero")
		}
		if !errors.Is(err, errRunIDNotDispatched) {
			t.Fatalf("want errRunIDNotDispatched, got %v", err)
		}
		if !bytes.Equal(before, taskFileBytes(t, root, tk.ID)) {
			t.Fatal("held card bytes mutated")
		}
		if providerCallCount(t, count) != 0 {
			t.Fatalf("held dispatched provider, calls=%d", providerCallCount(t, count))
		}
	})
	t.Run("goal", func(t *testing.T) {
		root := testRoot(t)
		count := filepath.Join(t.TempDir(), "calls")
		cfg := onceTestCfg(t, countingClaudeBin(t, count, mkOKResultJSON("sess")))
		saveOnceCfg(t, root, cfg)
		tk := newTask(root, cfg, typeSequence, "goal-once", t.TempDir(), []string{"p"}, 5)
		tk.Goal = &TaskGoalBinding{WriterMode: goalWriterManual, Observation: goalObsUnstarted}
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		before := taskFileBytes(t, root, tk.ID)
		err := cmdRun([]string{"-once", "-root", root, "-quiet", tk.ID})
		if err == nil {
			t.Fatal("goal -once must be nonzero")
		}
		if !errors.Is(err, errRunIDNotDispatched) {
			t.Fatalf("want errRunIDNotDispatched, got %v", err)
		}
		if !bytes.Equal(before, taskFileBytes(t, root, tk.ID)) {
			t.Fatal("goal card bytes mutated")
		}
		if providerCallCount(t, count) != 0 {
			t.Fatalf("goal dispatched provider, calls=%d", providerCallCount(t, count))
		}
	})
	t.Run("live-writer", func(t *testing.T) {
		root := testRoot(t)
		count := filepath.Join(t.TempDir(), "calls")
		cfg := onceTestCfg(t, countingClaudeBin(t, count, mkOKResultJSON("sess")))
		saveOnceCfg(t, root, cfg)
		ws := t.TempDir()
		tk := newTask(root, cfg, typeSequence, "live-writer-once", ws, []string{"p"}, 5)
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sleep", "30")
		lease, err := prepareTaskProcessLease(cmd, tk.ID, ws)
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if lease != nil && lease.commit != nil {
			lease.commit()
		}
		if lease != nil && lease.cleanup != nil {
			defer lease.cleanup()
		}
		if lease != nil && lease.resume != nil {
			if err := lease.resume(); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
			}
		})
		deadline := time.Now().Add(2 * time.Second)
		for !workspaceLeaseHeld(ws) {
			if !time.Now().Before(deadline) {
				t.Fatal("descendant did not retain workspace lease")
			}
			time.Sleep(5 * time.Millisecond)
		}
		loaded := loadMust(t, root, tk.ID)
		if !taskHasLiveWriterProof(root, loaded) {
			t.Fatal("fixture must present live writer proof")
		}
		before := taskFileBytes(t, root, tk.ID)
		err = cmdRun([]string{"-once", "-root", root, "-quiet", tk.ID})
		if err == nil {
			t.Fatal("live writer -once must be nonzero")
		}
		if !errors.Is(err, errRunIDNotDispatched) {
			t.Fatalf("want errRunIDNotDispatched, got %v", err)
		}
		if !bytes.Equal(before, taskFileBytes(t, root, tk.ID)) {
			t.Fatal("live-writer card bytes mutated")
		}
		if providerCallCount(t, count) != 0 {
			t.Fatalf("live writer dispatched provider, calls=%d", providerCallCount(t, count))
		}
	})
}

func TestCmdRunOnceLeavesCanceledOrphanFanoutNeighborsUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider")
	}
	root := testRoot(t)
	count := filepath.Join(t.TempDir(), "calls")
	cfg := onceTestCfg(t, countingClaudeBin(t, count, mkOKResultJSON("once-ok")))
	saveOnceCfg(t, root, cfg)

	target := newTask(root, cfg, typeSequence, "once-target", t.TempDir(), []string{"p"}, 5)
	if err := saveTask(root, target); err != nil {
		t.Fatal(err)
	}
	sibling := newTask(root, cfg, typeSequence, "ready-sibling", t.TempDir(), []string{"p"}, 4)
	if err := saveTask(root, sibling); err != nil {
		t.Fatal(err)
	}
	canceled := newTask(root, cfg, typeSequence, "canceled-neighbor", t.TempDir(), []string{"p"}, 3)
	canceled.Status = statusCanceled
	markControlTerminal(canceled)
	if err := saveTask(root, canceled); err != nil {
		t.Fatal(err)
	}
	orphan := newTask(root, cfg, typeCrossCheck, "orphan-neighbor", t.TempDir(), []string{"s"}, 2)
	orphan.XRole, orphan.XKey, orphan.Status = "A", "once-orphan-k", statusDone
	markControlTerminal(orphan)
	if err := saveTask(root, orphan); err != nil {
		t.Fatal(err)
	}
	if err := writeCrossPeer(root, "once-orphan-k", "sidecar-must-stay"); err != nil {
		t.Fatal(err)
	}
	fanout := newTask(root, cfg, typeSequence, "fanout-neighbor", t.TempDir(), []string{"p"}, 1)
	fanout.EmitTasks = true
	if err := saveTask(root, fanout); err != nil {
		t.Fatal(err)
	}

	ids := []string{sibling.ID, canceled.ID, orphan.ID, fanout.ID}
	before := neighborControlSnapshot(t, root, ids...)
	peerBefore := readMaybe(crossPeerPath(root, "once-orphan-k"))

	out, err := captureCmdRun(t, []string{"-once", "-root", root, target.ID})
	if err != nil {
		t.Fatalf("once ready target: %v stdout=%q", err, out)
	}
	if !strings.Contains(out, target.ID) {
		t.Fatalf("stdout must name the started ID %s: %q", target.ID, out)
	}
	if strings.Contains(out, sibling.ID) || strings.Contains(out, canceled.ID) || strings.Contains(out, fanout.ID) {
		t.Fatalf("stdout named a non-target: %q", out)
	}
	if providerCallCount(t, count) != 1 {
		t.Fatalf("provider calls=%d want 1", providerCallCount(t, count))
	}
	got := loadMust(t, root, target.ID)
	if got.Status != statusDone {
		t.Fatalf("target status=%s want done", got.Status)
	}
	assertSnapshotUnchanged(t, before, neighborControlSnapshot(t, root, ids...))
	if !bytes.Equal(peerBefore, readMaybe(crossPeerPath(root, "once-orphan-k"))) {
		t.Fatal("orphan sidecar mutated")
	}
	if _, err := os.Stat(taskPath(root, canceled.ID)); err != nil {
		t.Fatalf("canceled neighbor archived: %v", err)
	}
	gotOrphan := loadMust(t, root, orphan.ID)
	if gotOrphan.Status != statusDone {
		t.Fatalf("orphan status=%s want done", gotOrphan.Status)
	}
}

func TestCmdRunOnceReviewAfterFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake provider")
	}
	root := testRoot(t)
	count := filepath.Join(t.TempDir(), "calls")
	cfg := onceTestCfg(t, countingClaudeBin(t, count, mkOKResultJSON("should-not-run")))
	saveOnceCfg(t, root, cfg)
	tk := newTask(root, cfg, typeSequence, "review-after-once", t.TempDir(), []string{"p"}, 5)
	tk.ReviewAfter = true
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	sibling := newTask(root, cfg, typeSequence, "review-after-sibling", t.TempDir(), []string{"p"}, 4)
	if err := saveTask(root, sibling); err != nil {
		t.Fatal(err)
	}
	before := neighborControlSnapshot(t, root, tk.ID, sibling.ID)
	err := cmdRun([]string{"-once", "-root", root, "-quiet", tk.ID})
	if err == nil {
		t.Fatal("review_after -once must fail closed")
	}
	if !errors.Is(err, errRunOnceIsolation) {
		t.Fatalf("want errRunOnceIsolation, got %v", err)
	}
	if providerCallCount(t, count) != 0 {
		t.Fatalf("fail-closed dispatched provider, calls=%d", providerCallCount(t, count))
	}
	assertSnapshotUnchanged(t, before, neighborControlSnapshot(t, root, tk.ID, sibling.ID))
}

func TestCmdRunOnceOrdinaryRunIDAndDrainUnchanged(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })
	var ran []string
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		ran = append(ran, tk.ID)
		finishTickTask(root, tk)
		return nil
	}

	root := testRoot(t)
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 2
	cfg.ClaudeBin = fakeClaudeBin(t, mkOKResultJSON("sess"), "", 0)
	saveOnceCfg(t, root, cfg)
	a := newTask(root, cfg, typeSequence, "ordinary-a", t.TempDir(), []string{"p"}, 5)
	b := newTask(root, cfg, typeSequence, "ordinary-b", t.TempDir(), []string{"p"}, 4)
	if err := saveTask(root, a); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root, b); err != nil {
		t.Fatal(err)
	}
	if err := cmdRun([]string{"-root", root, "-quiet", a.ID}); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != a.ID {
		t.Fatalf("ordinary run ID ran %v want only %s", ran, a.ID)
	}
	gotB := loadMust(t, root, b.ID)
	if gotB.Status != statusQueued {
		t.Fatalf("sibling status=%s want queued", gotB.Status)
	}

	ran = nil
	root2 := testRoot(t)
	saveOnceCfg(t, root2, cfg)
	c := newTask(root2, cfg, typeSequence, "drain-a", t.TempDir(), []string{"p"}, 5)
	d := newTask(root2, cfg, typeSequence, "drain-b", t.TempDir(), []string{"p"}, 4)
	if err := saveTask(root2, c); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root2, d); err != nil {
		t.Fatal(err)
	}
	if err := cmdRun([]string{"-root", root2, "-quiet"}); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 2 {
		t.Fatalf("ordinary no-ID run must drain, ran %v", ran)
	}
}

func TestRunOnceBuiltCLINamedCardAnd429(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX candidate CLI fixtures")
	}
	bin := buildCardexCLI(t)

	for i := 0; i < 2; i++ {
		root := testRoot(t)
		count := filepath.Join(t.TempDir(), "calls")
		cfg := onceTestCfg(t, countingClaudeBin(t, count, mkOKResultJSON("cli-once-ok")))
		saveOnceCfg(t, root, cfg)
		target := newTask(root, cfg, typeSequence, "cli-once-target", t.TempDir(), []string{"p"}, 5)
		if err := saveTask(root, target); err != nil {
			t.Fatal(err)
		}
		sibling := newTask(root, cfg, typeSequence, "cli-once-sibling", t.TempDir(), []string{"p"}, 4)
		if err := saveTask(root, sibling); err != nil {
			t.Fatal(err)
		}
		before := neighborControlSnapshot(t, root, sibling.ID)
		cmd := exec.Command(bin, "run", "-once", "-root", root, target.ID)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("pass %d built CLI once success: %v\n%s", i, err, out)
		}
		text := string(out)
		if !strings.Contains(text, target.ID) {
			t.Fatalf("pass %d stdout must name %s:\n%s", i, target.ID, text)
		}
		if strings.Contains(text, sibling.ID) {
			t.Fatalf("pass %d stdout named sibling:\n%s", i, text)
		}
		if providerCallCount(t, count) != 1 {
			t.Fatalf("pass %d provider calls=%d", i, providerCallCount(t, count))
		}
		got := loadMust(t, root, target.ID)
		if got.Status != statusDone {
			t.Fatalf("pass %d target status=%s", i, got.Status)
		}
		assertSnapshotUnchanged(t, before, neighborControlSnapshot(t, root, sibling.ID))
	}

	for i := 0; i < 2; i++ {
		root := testRoot(t)
		count := filepath.Join(t.TempDir(), "calls")
		cfg := onceTestCfg(t, fakeClaude429Counted(t, count, "cli-once-429"))
		saveOnceCfg(t, root, cfg)
		tk := newTask(root, cfg, typeSequence, "cli-once-429", t.TempDir(), []string{"p"}, 5)
		tk.Attempts = 1
		tk.SessionID = "cli-once-429"
		tk.NotBeforeEpoch = 1
		tk.LastError = "API Error: Request rejected (429) · Service Unavailable"
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
		sibling := newTask(root, cfg, typeSequence, "cli-once-429-sib", t.TempDir(), []string{"p"}, 4)
		if err := saveTask(root, sibling); err != nil {
			t.Fatal(err)
		}
		before := neighborControlSnapshot(t, root, sibling.ID)
		cmd := exec.Command(bin, "run", "-once", "-root", root, tk.ID)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("pass %d built CLI 429 must be nonzero, stdout=%q", i, out)
		}
		text := string(out)
		if strings.Contains(text, "完成") {
			t.Fatalf("pass %d 429 printed success:\n%s", i, text)
		}
		if !strings.Contains(text, "429") && !strings.Contains(text, "queued") && !strings.Contains(text, "requeued") {
			t.Fatalf("pass %d 429 output missing disposition:\n%s", i, text)
		}
		if !strings.Contains(text, tk.ID) {
			t.Fatalf("pass %d 429 output missing ID:\n%s", i, text)
		}
		if providerCallCount(t, count) != 1 {
			t.Fatalf("pass %d 429 provider calls=%d", i, providerCallCount(t, count))
		}
		got := loadMust(t, root, tk.ID)
		if got.Status != statusQueued {
			t.Fatalf("pass %d 429 status=%s", i, got.Status)
		}
		assertSnapshotUnchanged(t, before, neighborControlSnapshot(t, root, sibling.ID))
	}
}
