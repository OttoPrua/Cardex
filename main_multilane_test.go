package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
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
