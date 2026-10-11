package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCmdRunIDFailsClosedWhenRootHeldThenStartsAfterRelease(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })

	var mu sync.Mutex
	var ran []string
	started := make(chan struct{})
	hold := make(chan struct{})
	var releaseHold sync.Once
	t.Cleanup(func() { releaseHold.Do(func() { close(hold) }) })

	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		mu.Lock()
		ran = append(ran, tk.ID)
		mu.Unlock()
		select {
		case <-started:
		default:
			close(started)
		}
		<-hold
		finishTickTask(root, tk)
		return nil
	}

	root := testRoot(t)
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 2
	cfg.ClaudeBin = fakeClaudeBin(t, mkOKResultJSON("sess"), "", 0)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	a := newTask(root, cfg, typeSequence, "holder", t.TempDir(), []string{"p"}, 5)
	b := newTask(root, cfg, typeSequence, "queued-sibling", t.TempDir(), []string{"p"}, 4)
	if err := saveTask(root, a); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root, b); err != nil {
		t.Fatal(err)
	}

	doneA := make(chan error, 1)
	go func() { doneA <- cmdRun([]string{"-root", root, "-quiet", a.ID}) }()
	select {
	case <-started:
	case err := <-doneA:
		t.Fatalf("holding run-ID exited before dispatch: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("holding run-ID did not dispatch")
	}

	err := cmdRun([]string{"-root", root, "-quiet", b.ID})
	if err == nil {
		t.Fatal("second run-ID must not exit 0 while another run holds the root")
	}
	if !errors.Is(err, errRunIDNotDispatched) {
		t.Fatalf("want errRunIDNotDispatched, got %v", err)
	}
	if !strings.Contains(err.Error(), b.ID) || !strings.Contains(err.Error(), "holds this root") {
		t.Fatalf("blocked reason must name the ID and the held root: %v", err)
	}

	gotB, err := loadTask(root, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Status != statusQueued {
		t.Fatalf("queued sibling status=%s want queued", gotB.Status)
	}
	mu.Lock()
	if len(ran) != 1 || ran[0] != a.ID {
		mu.Unlock()
		t.Fatalf("blocked sibling must not dispatch, ran %v", ran)
	}
	mu.Unlock()

	releaseHold.Do(func() { close(hold) })
	if err := <-doneA; err != nil {
		t.Fatalf("holding run-ID: %v", err)
	}

	if err := cmdRun([]string{"-root", root, "-quiet", b.ID}); err != nil {
		t.Fatalf("queued ID must be startable after holder ends: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 2 || ran[0] != a.ID || ran[1] != b.ID {
		t.Fatalf("after holder ended ran %v want [%s %s]", ran, a.ID, b.ID)
	}
	gotB, err = loadTask(root, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Status != statusDone {
		t.Fatalf("second ID status=%s want done", gotB.Status)
	}
}

func TestCmdRunNoIDSkipsWhenRootHeldAndStillDrainsAfterRelease(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })
	var mu sync.Mutex
	var ran []string
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		mu.Lock()
		ran = append(ran, tk.ID)
		mu.Unlock()
		finishTickTask(root, tk)
		return nil
	}

	root := testRoot(t)
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 2
	cfg.ClaudeBin = fakeClaudeBin(t, mkOKResultJSON("sess"), "", 0)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	a := newTask(root, cfg, typeSequence, "drain-a", t.TempDir(), []string{"p"}, 5)
	b := newTask(root, cfg, typeSequence, "drain-b", t.TempDir(), []string{"p"}, 4)
	if err := saveTask(root, a); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root, b); err != nil {
		t.Fatal(err)
	}

	if !acquireLock(root, time.Hour) {
		t.Fatal("test must hold the root lock")
	}
	if err := cmdRun([]string{"-root", root, "-quiet"}); err != nil {
		releaseLock(root)
		t.Fatalf("no-ID overlapping tick still skips with nil, got %v", err)
	}
	gotA, err := loadTask(root, a.ID)
	if err != nil {
		releaseLock(root)
		t.Fatal(err)
	}
	gotB, err := loadTask(root, b.ID)
	if err != nil {
		releaseLock(root)
		t.Fatal(err)
	}
	if gotA.Status != statusQueued || gotB.Status != statusQueued {
		releaseLock(root)
		t.Fatalf("no-ID skip must leave cards queued, a=%s b=%s", gotA.Status, gotB.Status)
	}
	mu.Lock()
	if len(ran) != 0 {
		mu.Unlock()
		releaseLock(root)
		t.Fatalf("no-ID skip must not dispatch, ran %v", ran)
	}
	mu.Unlock()

	releaseLock(root)
	if err := cmdRun([]string{"-root", root, "-quiet"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 2 {
		t.Fatalf("legacy no-ID tick still drains after lock release, ran %v", ran)
	}
}

func TestCmdRunIDNotDispatchedWhenHeldStatus(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		t.Fatal("held ID must not dispatch")
		return nil
	}

	root := testRoot(t)
	cfg := defaultConfig("claude")
	cfg.ClaudeBin = fakeClaudeBin(t, mkOKResultJSON("sess"), "", 0)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	tk := newTask(root, cfg, typeSequence, "held-card", t.TempDir(), []string{"p"}, 5)
	tk.Status = statusHeld
	tk.LastError = "native done held: fake_executing_no_contract_evidence"
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	err := cmdRun([]string{"-root", root, "-quiet", tk.ID})
	if err == nil {
		t.Fatal("run-ID of a held card must not exit 0")
	}
	if !errors.Is(err, errRunIDNotDispatched) {
		t.Fatalf("want errRunIDNotDispatched, got %v", err)
	}
	got, err := loadTask(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusHeld {
		t.Fatalf("held card status=%s want held", got.Status)
	}
}

func TestCmdRunHelpDocumentsHeldRootFailClosed(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var buf strings.Builder
	fs.SetOutput(&buf)
	setCmdRunUsage(fs)
	_ = fs.String("root", "", "数据目录")
	_ = fs.Bool("force", false, "忽略限额冷却强行尝试")
	_ = fs.Bool("quiet", false, "静默模式（launchd 用）")
	_ = fs.Parse([]string{"-h"})
	out := buf.String()
	for _, want := range []string{"[ID]", "cardex run -root ROOT ID", "未派发", "跳过本轮"} {
		if !strings.Contains(out, want) {
			t.Fatalf("run --help missing %q:\n%s", want, out)
		}
	}
}

func captureCmdRun(t *testing.T, args []string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := cmdRun(args)
	_ = w.Close()
	os.Stdout = old
	raw, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(raw), runErr
}

func killLockPID(root string) {
	data, err := os.ReadFile(lockPath(root))
	if err != nil {
		return
	}
	var li lockInfo
	if json.Unmarshal(data, &li) != nil || li.PID <= 0 {
		return
	}
	if p, err := os.FindProcess(li.PID); err == nil {
		_ = p.Kill()
		_, _ = p.Wait()
	}
}

func fakeClaudeStealLockBin(t *testing.T, root, countPath string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	lock := lockPath(root)
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version|-v) echo 'claude 1.0'; exit 0;; esac\n" +
		"n=$(cat " + shSingleQuote(countPath) + " 2>/dev/null || echo 0)\n" +
		"n=$((n+1)); printf '%s' \"$n\" > " + shSingleQuote(countPath) + "\n" +
		"sleep 60 &\n" +
		"printf '{\"pid\":%s,\"at\":\"%s\"}\\n' \"$!\" \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\" > " + shSingleQuote(lock) + "\n" +
		"cat <<'JSON_EOF'\n" + mkOKResultJSON("sess-reject") + "\nJSON_EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCmdRunIDRejectedTerminalPersistIsNonzero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX lock steal")
	}
	root := testRoot(t)
	t.Cleanup(func() { killLockPID(root) })
	countPath := filepath.Join(t.TempDir(), "calls")
	cfg := runTaskCfg(t, fakeClaudeStealLockBin(t, root, countPath))
	cfg.DrainRescanSec = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	tk := newTask(root, cfg, typeSequence, "persist-reject", work, []string{"p"}, 5)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}

	out, err := captureCmdRun(t, []string{"-root", root, tk.ID})
	if err == nil {
		t.Fatalf("rejected terminal persist must be nonzero, stdout=%q", out)
	}
	if !errors.Is(err, errSchedulerLockLost) && !errors.Is(err, errStaleTaskWrite) && !errors.Is(err, errProducerInvalidated) {
		t.Fatalf("want rejected persist error, got %v stdout=%q", err, out)
	}
	if strings.Contains(out, "完成") || strings.Contains(strings.ToLower(out), "done") {
		t.Fatalf("stdout must not report done: %q", out)
	}

	got, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if got.Status == statusDone {
		t.Fatalf("disk status must stay non-done, got %s", got.Status)
	}
	if !hasStaleWriteEvent(root, got.ID, got.ActiveAttemptID) && !hasStaleWriteEvent(root, got.ID, "") {
		t.Fatal("missing stale_attempt_write_rejected")
	}

	raw, _ := os.ReadFile(countPath)
	if strings.TrimSpace(string(raw)) != "1" {
		t.Fatalf("provider invoked %q, want 1", raw)
	}

	replay := persistTaskEvent(root, got, evDone, "runner", statusDone, got.Step, withCostTelemetry(nil, got))
	if replay == nil || (!errors.Is(replay, errStaleTaskWrite) && !errors.Is(replay, errSchedulerLockLost) && !errors.Is(replay, errProducerInvalidated) && !errors.Is(replay, errAlreadyTerminal)) {
		t.Fatalf("no-replay must block a second terminal write: %v", replay)
	}

	out2, err2 := captureCmdRun(t, []string{"-root", root, "-quiet", tk.ID})
	if err2 == nil {
		t.Fatalf("second run while lock is lost must not exit 0, stdout=%q", out2)
	}
	raw, _ = os.ReadFile(countPath)
	if strings.TrimSpace(string(raw)) != "1" {
		t.Fatalf("second run must not invoke provider again, count=%q", raw)
	}
}

func TestCmdRunIDSuccessStillDone(t *testing.T) {
	root := testRoot(t)
	cfg := runTaskCfg(t, fakeClaudeBin(t, mkOKResultJSON("sess-ok"), "", 0))
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	tk := newTask(root, cfg, typeSequence, "ordinary-success", work, []string{"p"}, 5)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	out, err := captureCmdRun(t, []string{"-root", root, tk.ID})
	if err != nil {
		t.Fatalf("ordinary success: %v stdout=%q", err, out)
	}
	if !strings.Contains(out, "完成") {
		t.Fatalf("ordinary success stdout: %q", out)
	}
	got, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if got.Status != statusDone {
		t.Fatalf("ordinary success status=%s", got.Status)
	}
}

func TestCmdRunIDCancelKeepsSuccessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sleep holder")
	}
	root := testRoot(t)
	bin, started := fakeClaudeBlockOnNthCall(t, 1)
	cfg := runTaskCfg(t, bin)
	cfg.DrainRescanSec = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	tk := newTask(root, cfg, typeSequence, "cancel-contract", work, []string{"p"}, 5)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct {
		out string
		err error
	}, 1)
	go func() {
		out, err := captureCmdRun(t, []string{"-root", root, tk.ID})
		done <- struct {
			out string
			err error
		}{out, err}
	}()

	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case res := <-done:
			t.Fatalf("run exited before provider started: %v stdout=%q", res.err, res.out)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("provider did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmdSetStatus([]string{"-root", root, tk.ID}, "cancel"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("expected cancel/stop must keep success contract: %v stdout=%q", res.err, res.out)
		}
		if strings.Contains(res.out, "完成") {
			t.Fatalf("cancel stdout reported done: %q", res.out)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("canceled run did not return")
	}
	events, _, err := loadTaskEvents(root, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	sawCancel := false
	for _, ev := range events {
		if ev.Type == evCanceled {
			sawCancel = true
		}
		if ev.Type == evDone {
			t.Fatalf("cancel emitted done: %+v", events)
		}
	}
	if !sawCancel {
		t.Fatalf("cancel must persist canceled, events=%v", eventTypes(events))
	}
}

func TestRestoreTaskFromDiskUsesArchiveAndFailsClosedOnUnreadable(t *testing.T) {
	root := testRoot(t)
	cfg := testCfg()
	tk := newTask(root, cfg, typeSequence, "archive-restore", t.TempDir(), []string{"p"}, 5)
	tk.Status = statusCanceled
	markControlTerminal(tk)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	if err := archiveTask(root, tk); err != nil {
		t.Fatal(err)
	}

	mem := *tk
	mem.Status = statusDone
	if err := restoreTaskFromDisk(root, &mem); err != nil {
		t.Fatalf("archived cancel must restore: %v", err)
	}
	if mem.Status != statusCanceled {
		t.Fatalf("archive recovery status=%s want canceled", mem.Status)
	}
	if _, err := os.Stat(taskPath(root, tk.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restore must not rewrite the archived card back into tasks/")
	}

	gone := *tk
	gone.ID = "t-missing-restore"
	gone.Status = statusDone
	if err := restoreTaskFromDisk(root, &gone); err == nil {
		t.Fatal("missing task must be an explicit read error")
	}
	if gone.Status == statusDone {
		t.Fatal("unreadable restore must not keep in-memory done")
	}

	corrupt := newTask(root, cfg, typeSequence, "corrupt-restore", t.TempDir(), []string{"p"}, 5)
	if err := saveTask(root, corrupt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath(root, corrupt.ID), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	lie := *corrupt
	lie.Status = statusDone
	before, err := os.ReadFile(taskPath(root, corrupt.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreTaskFromDisk(root, &lie); err == nil {
		t.Fatal("corrupt task JSON must be an explicit read error")
	}
	if lie.Status == statusDone {
		t.Fatal("corrupt restore must not keep in-memory done")
	}
	after, err := os.ReadFile(taskPath(root, corrupt.ID))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("read failure must not rewrite disk")
	}
}

func TestCmdRunDrainRejectedTerminalPersistIsNonzero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX lock steal")
	}
	root := testRoot(t)
	t.Cleanup(func() { killLockPID(root) })
	countPath := filepath.Join(t.TempDir(), "calls")
	cfg := runTaskCfg(t, fakeClaudeStealLockBin(t, root, countPath))
	cfg.DrainRescanSec = 1
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	tk := newTask(root, cfg, typeSequence, "drain-persist-reject", work, []string{"p"}, 5)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}

	out, err := captureCmdRun(t, []string{"-root", root})
	if err == nil {
		t.Fatalf("ordinary drain rejected terminal persist must be nonzero, stdout=%q", out)
	}
	if !terminalPersistRejected(err) {
		t.Fatalf("want terminal persist rejection, got %v stdout=%q", err, out)
	}
	if strings.Contains(out, "完成") || strings.Contains(strings.ToLower(out), "done") {
		t.Fatalf("drain stdout must not report done: %q", out)
	}
	got, lerr := loadTask(root, tk.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if got.Status == statusDone {
		t.Fatalf("disk status must stay non-done, got %s", got.Status)
	}
	raw, _ := os.ReadFile(countPath)
	if strings.TrimSpace(string(raw)) != "1" {
		t.Fatalf("provider invoked %q, want 1", raw)
	}
}
