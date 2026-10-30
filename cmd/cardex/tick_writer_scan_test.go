package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func installLiveWriterScanHook(t *testing.T, root string) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	hook := func(got string) {
		if got == root {
			n.Add(1)
		}
	}
	liveWriterClaimScanHook.Store(&hook)
	t.Cleanup(func() { liveWriterClaimScanHook.Store(nil) })
	return &n
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitTickErr(t *testing.T, done <-chan error) error {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		t.Fatal("tick did not return")
		return nil
	}
}

func waitScanCount(t *testing.T, scans *atomic.Int64, want int64, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for scans.Load() < want {
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out waiting for %s (scans=%d want>=%d)", what, scans.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func plantLiveWorkspaceWriter(t *testing.T, root, title, dir string) *Task {
	t.Helper()
	tk := newTask(root, testCfg(), typeSequence, title, dir, []string{"p"}, 5)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	lease, err := prepareTaskProcessLease(cmd, tk.ID, dir)
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
		t.Cleanup(lease.cleanup)
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
	for !workspaceLeaseHeld(dir) {
		if !time.Now().Before(deadline) {
			t.Fatal("planted writer did not retain workspace lease")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !taskHasLiveWriterProof(root, tk) {
		t.Fatal("planted writer must present live writer proof")
	}
	return tk
}

func TestLoadLiveWriterPassViewUnreadableRootFailsClosed(t *testing.T) {
	view := loadLiveWriterPassView(filepath.Join(t.TempDir(), "missing-cardex-root"), nil)
	cand := &Task{ID: "healthy", Type: typeSequence, Dir: t.TempDir()}
	if !view.conflicts(cand) {
		t.Fatal("unreadable task root must fail closed")
	}
}

func TestLoadLiveWriterPassViewMergesInMemoryDispatch(t *testing.T) {
	dir := t.TempDir()
	running := &Task{ID: "running", Dir: dir, Type: typeSequence}
	queued := &Task{ID: "queued", Dir: dir, Type: typeSequence}
	view := loadLiveWriterPassView(testRoot(t), []*Task{running})
	if !view.conflicts(queued) {
		t.Fatal("in-memory dispatched writer must serialize the same directory")
	}
}

func TestTickLiveWriterScanNotMultipliedByCandidateCount(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })

	const n = 16
	root := testRoot(t)
	scans := installLiveWriterScanHook(t, root)
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 1
	cfg.DrainRescanSec = 60
	cfg.ClaudeBin = "/usr/bin/true"
	for i := 0; i < n; i++ {
		tk := newTask(root, cfg, typeSequence, fmt.Sprintf("cand-%d", i), t.TempDir(), []string{"p"}, 5)
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
	}

	var firstScans atomic.Int64
	var firstOnce sync.Once
	var launched atomic.Int32
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		firstOnce.Do(func() { firstScans.Store(scans.Load()) })
		launched.Add(1)
		finishTickTask(root, tk)
		return nil
	}
	if err := tick(root, cfg, true, true); err != nil {
		t.Fatal(err)
	}
	if launched.Load() != n {
		t.Fatalf("launched=%d want %d", launched.Load(), n)
	}
	first := firstScans.Load()
	total := scans.Load()
	if first == 0 {
		t.Fatal("expected a reconstruct before the first dispatch")
	}
	if first >= int64(n) {
		t.Fatalf("first-pass reconstructs=%d multiplied by candidate count %d", first, n)
	}
	if first > 2 {
		t.Fatalf("first-pass reconstructs=%d want at most 2", first)
	}
	if total > 2*int64(n)+2 {
		t.Fatalf("total reconstructs=%d want <= %d (two per dispatch plus empty drain)", total, 2*n+2)
	}
}

func TestTickDispatchUpdatesLiveWriterPassView(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })

	root := testRoot(t)
	scans := installLiveWriterScanHook(t, root)
	dir := t.TempDir()
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 2
	cfg.DrainRescanSec = 60
	cfg.ClaudeBin = "/usr/bin/true"
	first := newTask(root, cfg, typeSequence, "writer-a", dir, []string{"p"}, 9)
	second := newTask(root, cfg, typeSequence, "writer-b", dir, []string{"p"}, 1)
	if err := saveTask(root, first); err != nil {
		t.Fatal(err)
	}
	if err := saveTask(root, second); err != nil {
		t.Fatal(err)
	}

	startedA := make(chan struct{})
	releaseA := make(chan struct{})
	var ranB atomic.Bool
	var concurrent, maxC atomic.Int32
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		n := concurrent.Add(1)
		for {
			old := maxC.Load()
			if n <= old || maxC.CompareAndSwap(old, n) {
				break
			}
		}
		defer concurrent.Add(-1)
		switch tk.ID {
		case first.ID:
			close(startedA)
			<-releaseA
			finishTickTask(root, tk)
		case second.ID:
			ranB.Store(true)
			finishTickTask(root, tk)
		default:
			finishTickTask(root, tk)
		}
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- tick(root, cfg, true, true) }()
	waitSignal(t, startedA, "first writer")
	waitScanCount(t, scans, 2, "admission pass after dispatch")
	if ranB.Load() {
		t.Fatal("second same-dir writer dispatched while the first was in-flight")
	}
	if maxC.Load() != 1 {
		t.Fatalf("single-writer admission concurrent=%d want 1", maxC.Load())
	}
	close(releaseA)
	if err := waitTickErr(t, done); err != nil {
		t.Fatal(err)
	}
	if !ranB.Load() {
		t.Fatal("second writer must run after harvest of the first")
	}
	if maxC.Load() != 1 {
		t.Fatalf("same-dir writers overlapped after harvest update, concurrent=%d", maxC.Load())
	}
}

func TestTickNewlyAppearingConflictingWriterBlocksAfterHarvest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })

	root := testRoot(t)
	dirA := t.TempDir()
	dirB := t.TempDir()
	dirC := t.TempDir()
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 1
	cfg.DrainRescanSec = 60
	cfg.ClaudeBin = "/usr/bin/true"
	a := newTask(root, cfg, typeSequence, "lane-a", dirA, []string{"p"}, 9)
	b := newTask(root, cfg, typeSequence, "lane-b", dirB, []string{"p"}, 5)
	c := newTask(root, cfg, typeSequence, "lane-c", dirC, []string{"p"}, 1)
	for _, tk := range []*Task{a, b, c} {
		if err := saveTask(root, tk); err != nil {
			t.Fatal(err)
		}
	}

	startedA := make(chan struct{})
	releaseA := make(chan struct{})
	var ranB, ranC atomic.Bool
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		switch tk.ID {
		case a.ID:
			close(startedA)
			<-releaseA
			finishTickTask(root, tk)
		case b.ID:
			ranB.Store(true)
			finishTickTask(root, tk)
		case c.ID:
			ranC.Store(true)
			finishTickTask(root, tk)
		default:
			finishTickTask(root, tk)
		}
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- tick(root, cfg, true, true) }()
	waitSignal(t, startedA, "first writer")
	plantLiveWorkspaceWriter(t, root, "appeared-writer", dirB)
	if !writerConflictsWithActive(b, reconstructLiveWriterClaims(root)) {
		t.Fatal("planted writer must conflict with the waiting same-dir card")
	}
	close(releaseA)
	if err := waitTickErr(t, done); err != nil {
		t.Fatal(err)
	}
	if ranB.Load() {
		t.Fatal("card sharing the new live writer's directory must stay blocked after harvest")
	}
	if !ranC.Load() {
		t.Fatal("disjoint card must dispatch after harvest once the slot is free")
	}
}

func TestTickUnreadableTaskMetadataFailsClosed(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })

	root := testRoot(t)
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 2
	cfg.DrainRescanSec = 60
	cfg.ClaudeBin = "/usr/bin/true"
	healthy := newTask(root, cfg, typeSequence, "healthy", t.TempDir(), []string{"p"}, 5)
	if err := saveTask(root, healthy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath(root, "broken-unknown-writer"), []byte(`{"status":"running",`), 0o600); err != nil {
		t.Fatal(err)
	}

	view := loadLiveWriterPassView(root, nil)
	if !view.conflicts(healthy) {
		t.Fatal("unreadable sibling metadata must fail closed on the pass view")
	}

	var launched []string
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		launched = append(launched, tk.ID)
		finishTickTask(root, tk)
		return nil
	}
	if err := tick(root, cfg, true, true); err != nil {
		t.Fatal(err)
	}
	for _, id := range launched {
		if id == healthy.ID {
			t.Fatal("tick launched a writer while a sibling record was unreadable")
		}
	}
}

func TestTickUnreadableGitIdentityFailsClosed(t *testing.T) {
	orig := tickRunTask
	t.Cleanup(func() { tickRunTask = orig })

	broken := t.TempDir()
	mustWriteFile(t, filepath.Join(broken, ".git"), "gitdir: /nope\n")
	if err := os.Chmod(filepath.Join(broken, ".git"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(broken, ".git"), 0o644) })

	root := testRoot(t)
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 2
	cfg.DrainRescanSec = 60
	cfg.ClaudeBin = "/usr/bin/true"
	tk := newTask(root, cfg, typeSequence, "unknown-git", broken, []string{"p"}, 5)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	view := loadLiveWriterPassView(root, nil)
	if !view.conflicts(tk) {
		t.Fatal("unreadable git identity must fail closed even with an empty reconstructed set")
	}

	var launched atomic.Int32
	tickRunTask = func(ctx context.Context, root string, cfg *Config, tk *Task, via string) error {
		launched.Add(1)
		finishTickTask(root, tk)
		return nil
	}
	if err := tick(root, cfg, true, true); err != nil {
		t.Fatal(err)
	}
	if launched.Load() != 0 {
		t.Fatalf("unreadable git identity dispatched %d writers", launched.Load())
	}
}
