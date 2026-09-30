package main

import (
	"context"
	"errors"
	"flag"
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
