package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCmdAddPersistsWriteDomainAndDependsOnSecretFree(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	dep := newTask(root, testCfg(), typeSequence, "dep", dir, []string{"p"}, 5)
	if err := saveTask(root, dep); err != nil {
		t.Fatal(err)
	}
	if err := cmdAdd([]string{
		"-root", root, "-dir", dir, "-title", "auth lane",
		"-write-domain-id", "auth-tokens",
		"-write-domain-lineage", "auth-tokens-lineage",
		"-write-domain-component", "auth",
		"-write-paths", "internal/auth",
		"-write-resources", "database:auth.primary",
		"-depends-on", dep.ID,
		"SECRET PROMPT TOKEN=abc",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error {
		return cmdList([]string{"-root", root, "-json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var tasks []Task
	if err := json.Unmarshal([]byte(out), &tasks); err != nil {
		t.Fatalf("list json: %v %s", err, out)
	}
	var got *Task
	for i := range tasks {
		if tasks[i].WriteDomain != nil {
			got = &tasks[i]
			break
		}
	}
	if got == nil || got.WriteDomain.ID != "auth-tokens" || len(got.DependsOn) != 1 || got.DependsOn[0] != dep.ID {
		t.Fatalf("persisted claims missing: %+v", tasks)
	}
	if got.WriteDomain.Paths[0] != "internal/auth" || got.WriteDomain.Resources[0].ID != "auth.primary" {
		t.Fatalf("domain: %+v", got.WriteDomain)
	}
	blob, _ := json.Marshal(got.WriteDomain)
	if bytes.Contains(bytes.ToLower(blob), []byte("secret")) || bytes.Contains(bytes.ToLower(blob), []byte("token=abc")) {
		t.Fatalf("write domain leaked prompt: %s", blob)
	}
}

func finishTickTask(root string, tk *Task) {
	tk.Status = statusDone
	markControlTerminal(tk)
	tk.touch()
	_ = writeTaskFile(root, tk)
}

func TestTickEnforcesDisjointLanesAndLegacySerial(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })

	overlapTick := func(root string, cfg *Config, hold time.Duration) int32 {
		var concurrent, maxC atomic.Int32
		tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
			n := concurrent.Add(1)
			for {
				old := maxC.Load()
				if n <= old || maxC.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(hold)
			concurrent.Add(-1)
			finishTickTask(root, tk)
			return nil
		}
		if err := tick(root, cfg, true, true); err != nil {
			t.Fatal(err)
		}
		return maxC.Load()
	}

	root := testRoot(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "billing"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 2
	cfg.DrainRescanSec = 1
	cfg.ClaudeBin = fakeClaudeBin(t, mkOKResultJSON("sess"), "", 0)
	_ = explicitTask(t, root, "auth", dir, "auth-tokens", "auth-tokens-lineage", "auth", []string{"internal/auth"}, nil)
	_ = explicitTask(t, root, "bill", dir, "billing-core", "billing-core-lineage", "billing", []string{"internal/billing"}, nil)
	if got := overlapTick(root, cfg, 80*time.Millisecond); got < 2 {
		t.Fatalf("disjoint explicit lanes must overlap in tick, concurrent=%d", got)
	}

	root2 := testRoot(t)
	same := t.TempDir()
	legacy := newTask(root2, testCfg(), typeSequence, "legacy a", same, []string{"p"}, 5)
	if err := saveTask(root2, legacy); err != nil {
		t.Fatal(err)
	}
	legacyB := newTask(root2, testCfg(), typeSequence, "legacy b", same, []string{"p"}, 5)
	if err := saveTask(root2, legacyB); err != nil {
		t.Fatal(err)
	}
	if got := overlapTick(root2, cfg, 80*time.Millisecond); got != 1 {
		t.Fatalf("legacy same-dir must serialize in tick, concurrent=%d", got)
	}
}

func TestTickWriteConflictDeferredEventDedupes(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })
	release := make(chan struct{})
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		<-release
		finishTickTask(root, tk)
		return nil
	}

	root := testRoot(t)
	dir := t.TempDir()
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 4
	cfg.DrainRescanSec = 1
	cfg.ClaudeBin = "/usr/bin/true"
	first := newTask(root, cfg, typeSequence, "legacy running", dir, []string{"p"}, 9)
	if err := saveTask(root, first); err != nil {
		t.Fatal(err)
	}
	waiter := newTask(root, cfg, typeSequence, "legacy waiting", dir, []string{"p"}, 1)
	if err := saveTask(root, waiter); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(2500 * time.Millisecond)
		close(release)
	}()
	if err := tick(root, cfg, true, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(eventsPath(root, waiter.ID))
	if err != nil {
		t.Fatal(err)
	}
	deferred := 0
	var blocked []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev TaskEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != evDeferred {
			continue
		}
		deferred++
		if ev.Detail["reason"] != "write_conflict" {
			t.Fatalf("deferred reason=%v", ev.Detail["reason"])
		}
		blocked = detailBlockedBy(ev.Detail["blocked_by"])
	}
	if deferred != 1 {
		t.Fatalf("waiting card must record one deferred write_conflict, got %d\n%s", deferred, data)
	}
	if len(blocked) != 1 || blocked[0] != first.ID {
		t.Fatalf("blocked_by=%v want [%s]", blocked, first.ID)
	}
}

func TestTickAutomaticCodexWeeklyBudgetDefersQueued(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })
	var ran atomic.Int32
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		ran.Add(1)
		finishTickTask(root, tk)
		return nil
	}

	root := testRoot(t)
	feed := filepath.Join(t.TempDir(), "usage.jsonl")
	now := time.Now().UTC()
	writeFeed := func(percent int) {
		t.Helper()
		line, err := json.Marshal(map[string]any{
			"provider": "codex", "sampledAt": now.Format(time.RFC3339),
			"usedPercent": percent, "windowMinutes": 10080, "windowKind": "secondary",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(feed, append(line, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := defaultConfig("claude")
	cfg.CodexBin = "/usr/bin/true"
	cfg.AutomaticCodexBudgetStopPercent = 65
	cfg.UsageFeed = feed
	cfg.UsageFeedMaxAgeMin = 90
	cfg.MaxParallel = 2
	cfg.DrainRescanSec = 1

	writeFeed(66)
	over := newTask(root, cfg, typeSequence, "weekly over stop", t.TempDir(), []string{"p"}, 5)
	over.PreferRunner = "codex"
	over.AutomaticCodex = true
	if err := saveTask(root, over); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Unix()
	if err := tick(root, cfg, true, true); err != nil {
		t.Fatal(err)
	}
	got, err := loadTask(root, over.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusQueued {
		t.Fatalf("status=%s want queued", got.Status)
	}
	if got.NotBeforeEpoch < before+29*60 {
		t.Fatalf("NotBeforeEpoch=%d want about 30m after %d", got.NotBeforeEpoch, before)
	}
	if ran.Load() != 0 {
		t.Fatalf("over-stop card was dispatched %d times", ran.Load())
	}
	ev, ok := lastTaskEvent(root, over.ID)
	if !ok || ev.Type != evDeferred || ev.Detail["reason"] != "automatic_codex_budget" || ev.Detail["window"] != "weekly" {
		t.Fatalf("deferred budget event: ok=%v ev=%+v", ok, ev)
	}
	if !strings.Contains(fmt.Sprint(ev.Detail["detail"]), "window=weekly") {
		t.Fatalf("reason detail=%v", ev.Detail["detail"])
	}

	writeFeed(40)
	evidence := currentAutomaticCodexBudgetEvidence(cfg, now.Add(time.Second))
	if !evidence.Available || evidence.UsedPercent != 40 || evidence.WindowMinutes != 10080 || evidence.WindowKind != "secondary" {
		t.Fatalf("weekly 40%% evidence: %+v", evidence)
	}
	allowed, reason := automaticCodexBudgetAllowed(&Task{AutomaticCodex: true}, evidence, 65)
	if !allowed || !strings.Contains(reason, "window=weekly") {
		t.Fatalf("weekly 40%% must be allowed, allowed=%v reason=%q", allowed, reason)
	}
}

func TestCmdRunSingleIDDoesNotDrainOthers(t *testing.T) {
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
	a := newTask(root, cfg, typeSequence, "one", t.TempDir(), []string{"p"}, 5)
	b := newTask(root, cfg, typeSequence, "two", t.TempDir(), []string{"p"}, 1)
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
		t.Fatalf("single-id ran %v want only %s", ran, a.ID)
	}
	gotB, err := loadTask(root, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Status != statusQueued {
		t.Fatalf("other task status=%s want queued", gotB.Status)
	}

	ran = nil
	root2 := testRoot(t)
	if err := saveConfig(root2, cfg); err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("no-ID run must drain, ran %v", ran)
	}

	// Flags parse before the positional ID. Go's FlagSet stops at the first
	// non-flag, so `run ID -root ROOT` must not silently start/drain.
	ran = nil
	root3 := testRoot(t)
	if err := saveConfig(root3, cfg); err != nil {
		t.Fatal(err)
	}
	e := newTask(root3, cfg, typeSequence, "flags-before", t.TempDir(), []string{"p"}, 5)
	f := newTask(root3, cfg, typeSequence, "flags-sibling", t.TempDir(), []string{"p"}, 4)
	if err := saveTask(root3, e); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root3, f); err != nil {
		t.Fatal(err)
	}
	if err := cmdRun([]string{e.ID, "-root", root3, "-quiet"}); err == nil {
		t.Fatal("positional ID before flags must fail closed, not drain")
	}
	if len(ran) != 0 {
		t.Fatalf("flags-after-ID must not start tasks, ran %v", ran)
	}
	gotF, err := loadTask(root3, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotF.Status != statusQueued {
		t.Fatalf("sibling status=%s want queued", gotF.Status)
	}

	t.Run("run help usage includes ID", func(t *testing.T) {
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		var buf bytes.Buffer
		fs.SetOutput(&buf)
		setCmdRunUsage(fs)
		_ = fs.String("root", "", "数据目录")
		_ = fs.Bool("force", false, "忽略限额冷却强行尝试")
		_ = fs.Bool("quiet", false, "静默模式（launchd 用）")
		_ = fs.Parse([]string{"-h"})
		out := buf.String()
		if !strings.Contains(out, "[ID]") {
			t.Fatalf("run --help must include [ID]:\n%s", out)
		}
		if !strings.Contains(out, "未派发") {
			t.Fatalf("run --help must say a held-root ID is not dispatched:\n%s", out)
		}
	})
}

func TestSpawnWriteDomainMutexSameCwdAndUnknownOwnership(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := &Task{ID: "writer-a", Dir: dir, Type: typeSequence}
	b := &Task{ID: "writer-b", Dir: dir, Type: typeSequence}
	if !writerConflictsWithActive(b, []*Task{a}) {
		t.Fatal("same normalized cwd: second writer spawn must be refused")
	}

	broken := t.TempDir()
	mustWriteFile(t, filepath.Join(broken, ".git"), "gitdir: /nope\n")
	if err := os.Chmod(filepath.Join(broken, ".git"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(broken, ".git"), 0o644) })
	unknown := &Task{ID: "writer-unknown", Dir: broken, Type: typeSequence}
	if writerClaimForTask(unknown).valid {
		t.Fatal("unknown ownership must not be a valid idle claim")
	}
	if !writerConflictsWithActive(unknown, nil) {
		t.Fatal("held/unknown ownership is not idle")
	}
}

func TestPrintUsageDocumentsSingleIDRun(t *testing.T) {
	out, err := captureStdout(t, func() error {
		printUsage()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cardex run -root ROOT ID") && !strings.Contains(out, "[ID]") {
		t.Fatalf("help must show the single-id run path:\n%s", out)
	}
}

func listTextRow(t *testing.T, out, id string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.Contains(trim, "下一个将派发") || strings.Contains(trim, "暂无就绪") {
			continue
		}
		if strings.HasPrefix(trim, id) {
			return line
		}
	}
	t.Fatalf("no text list row for %s in:\n%s", id, out)
	return ""
}

func TestCmdListTextDependencyReadinessAndFooter(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig("claude")
	dir := t.TempDir()
	now := time.Now()

	heldPred := newTask(root, cfg, typeSequence, "held predecessor", dir, []string{"pred"}, 5)
	heldPred.ID = "t-held-pred"
	heldPred.Status = statusHeld
	if err := saveTask(root, heldPred); err != nil {
		t.Fatal(err)
	}
	donePred := newTask(root, cfg, typeSequence, "done predecessor", dir, []string{"pred"}, 5)
	donePred.ID = "t-done-pred"
	donePred.Status = statusDone
	if err := saveTask(root, donePred); err != nil {
		t.Fatal(err)
	}

	blockedHeld := newTask(root, cfg, typeCoordinate, "blocked held-dep", dir, []string{"coord"}, 9)
	blockedHeld.ID = "t-blocked-held"
	blockedHeld.DependsOn = []string{heldPred.ID}
	if err := saveTask(root, blockedHeld); err != nil {
		t.Fatal(err)
	}
	blockedMissing := newTask(root, cfg, typeCoordinate, "blocked missing-dep", dir, []string{"coord"}, 2)
	blockedMissing.ID = "t-blocked-missing"
	blockedMissing.DependsOn = []string{"t-absent-pred"}
	if err := saveTask(root, blockedMissing); err != nil {
		t.Fatal(err)
	}
	recovered := newTask(root, cfg, typeCoordinate, "recovered dep", dir, []string{"coord"}, 3)
	recovered.ID = "t-recovered"
	recovered.DependsOn = []string{donePred.ID}
	if err := saveTask(root, recovered); err != nil {
		t.Fatal(err)
	}
	ready := newTask(root, cfg, typeSequence, "independent ready", dir, []string{"ready"}, 1)
	ready.ID = "t-ready-indep"
	if err := saveTask(root, ready); err != nil {
		t.Fatal(err)
	}
	delayed := newTask(root, cfg, typeSequence, "delayed retry", dir, []string{"later"}, 0)
	delayed.ID = "t-delayed-retry"
	delayed.NotBeforeEpoch = now.Unix() + 3600
	if err := saveTask(root, delayed); err != nil {
		t.Fatal(err)
	}
	archivedDone := newTask(root, cfg, typeSequence, "archived done predecessor", dir, []string{"pred"}, 5)
	archivedDone.ID = "t-archived-done"
	archivedDone.Status = statusDone
	if err := saveTask(root, archivedDone); err != nil {
		t.Fatal(err)
	}
	if err := archiveTask(root, archivedDone); err != nil {
		t.Fatal(err)
	}
	archivedChild := newTask(root, cfg, typeCoordinate, "archived-done dependent", dir, []string{"coord"}, 8)
	archivedChild.ID = "t-archived-dep"
	archivedChild.DependsOn = []string{archivedDone.ID}
	if err := saveTask(root, archivedChild); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return cmdList([]string{"-root", root})
	})
	if err != nil {
		t.Fatal(err)
	}

	heldRow := listTextRow(t, out, blockedHeld.ID)
	if strings.Contains(heldRow, "就绪") {
		t.Fatalf("held-blocked card printed 就绪:\n%s\nfull:\n%s", heldRow, out)
	}
	if !strings.Contains(heldRow, heldPred.ID) || !strings.Contains(heldRow, "held") {
		t.Fatalf("held-blocked note missing wait reason:\n%s", heldRow)
	}

	missingRow := listTextRow(t, out, blockedMissing.ID)
	if strings.Contains(missingRow, "就绪") {
		t.Fatalf("missing-blocked card printed 就绪:\n%s\nfull:\n%s", missingRow, out)
	}
	if !strings.Contains(missingRow, "missing") {
		t.Fatalf("missing-blocked note missing wait reason:\n%s", missingRow)
	}

	recoveredRow := listTextRow(t, out, recovered.ID)
	if !strings.Contains(recoveredRow, "就绪") {
		t.Fatalf("recovered card lost 就绪:\n%s", recoveredRow)
	}

	readyRow := listTextRow(t, out, ready.ID)
	if !strings.Contains(readyRow, "就绪") {
		t.Fatalf("no-dependency queued card lost 就绪:\n%s", readyRow)
	}

	delayedRow := listTextRow(t, out, delayed.ID)
	if !strings.Contains(delayedRow, "重试") {
		t.Fatalf("delayed-retry note lost 重试:\n%s", delayedRow)
	}

	archivedRow := listTextRow(t, out, archivedChild.ID)
	if strings.Contains(archivedRow, "missing") || strings.Contains(archivedRow, "等待") {
		t.Fatalf("archived-done predecessor reported as a wait:\n%s", archivedRow)
	}
	if !strings.Contains(archivedRow, "就绪") {
		t.Fatalf("archived-done dependent lost 就绪:\n%s", archivedRow)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), archivedDone.ID) {
			t.Fatalf("archive card leaked into list rows:\n%s", out)
		}
	}

	if strings.Contains(out, "下一个将派发: "+blockedHeld.ID) || strings.Contains(out, "下一个将派发: "+blockedMissing.ID) {
		t.Fatalf("footer named a blocked card:\n%s", out)
	}
	if !strings.Contains(out, "下一个将派发: "+archivedChild.ID) {
		t.Fatalf("footer did not name the actually-ready archived-done dependent:\n%s", out)
	}
}
