package main

import (
	"path/filepath"
	"testing"
	"time"
)

func saveWaitTask(t *testing.T, root string, id, title, status, project string, deps []string) *Task {
	t.Helper()
	cfg := testCfg()
	tk := newTask(root, cfg, typeSequence, title, filepath.Join(root, "work", project), []string{"do " + title}, 5)
	tk.ID = id
	tk.Status = status
	tk.Project = project
	tk.DependsOn = append([]string(nil), deps...)
	if err := saveTask(root, tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func findBrief(t *testing.T, snap *boardSnapshot, id string) TaskBrief {
	t.Helper()
	for _, p := range snap.Projects {
		for _, ph := range p.Phases {
			for _, tb := range ph.Tasks {
				if tb.ID == id {
					return tb
				}
			}
		}
	}
	t.Fatalf("task %s missing from snapshot", id)
	return TaskBrief{}
}

func TestBoardSnapshotShowsDependencyWaitWhoAndWhy(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	saveWaitTask(t, root, "t-pred-run", "running predecessor", statusRunning, "wait-alpha", nil)
	saveWaitTask(t, root, "t-pred-held", "held predecessor", statusHeld, "wait-alpha", nil)
	saveWaitTask(t, root, "t-pred-fail", "failed predecessor", statusFailed, "wait-alpha", nil)
	saveWaitTask(t, root, "t-pred-queue", "queued predecessor", statusQueued, "wait-alpha", nil)
	child := saveWaitTask(t, root, "t-child", "queued dependent", statusQueued, "wait-alpha",
		[]string{"t-pred-run", "t-pred-held", "t-pred-fail", "t-pred-queue", "t-ghost"})
	snap, err := buildSnapshot(root, time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	br := findBrief(t, snap, child.ID)
	if br.Status != statusQueued {
		t.Fatalf("dependent status=%s", br.Status)
	}
	why := map[string]string{}
	for _, w := range br.WaitingOn {
		why[w.ID] = w.Why
	}
	want := map[string]string{
		"t-pred-run":   depWaitRunning,
		"t-pred-held":  depWaitHeld,
		"t-pred-fail":  depWaitFailed,
		"t-pred-queue": depWaitUnknown,
		"t-ghost":      depWaitMissing,
	}
	for id, w := range want {
		if why[id] != w {
			t.Fatalf("wait[%s]=%q want %q in %+v", id, why[id], w, br.WaitingOn)
		}
	}
	if br.BlockedReason == "" {
		t.Fatal("queued dependent blocked_reason empty")
	}
}

func TestBoardWaitUpdatesAfterPredecessorRecoveryWithoutCancel(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	pred := saveWaitTask(t, root, "t-pred-held", "held predecessor", statusHeld, "wait-alpha", nil)
	child := saveWaitTask(t, root, "t-child", "queued dependent", statusQueued, "wait-alpha", []string{pred.ID})
	before, err := buildSnapshot(root, time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	br := findBrief(t, before, child.ID)
	if len(br.WaitingOn) != 1 || br.WaitingOn[0].ID != pred.ID || br.WaitingOn[0].Why != depWaitHeld {
		t.Fatalf("before wait=%+v", br.WaitingOn)
	}
	pred.Status = statusDone
	if err := writeTaskFile(root, pred); err != nil {
		t.Fatal(err)
	}
	after, err := buildSnapshot(root, time.Date(2026, 10, 8, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	br2 := findBrief(t, after, child.ID)
	if len(br2.WaitingOn) != 0 {
		t.Fatalf("stale wait after recovery: %+v", br2.WaitingOn)
	}
	if br2.BlockedReason != "" {
		t.Fatalf("blocked_reason stale after recovery: %q", br2.BlockedReason)
	}
	if br2.Status != statusQueued {
		t.Fatalf("dependent was %s after recovery", br2.Status)
	}
	fresh, err := loadTask(root, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != statusQueued {
		t.Fatalf("downstream canceled or skipped: %s", fresh.Status)
	}
}

func TestBoardWaitUnknownAndMissingBoundaries(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	saveWaitTask(t, root, "t-paused", "limit paused pred", statusLimitPaused, "wait-alpha", nil)
	child := saveWaitTask(t, root, "t-child", "child", statusQueued, "wait-alpha", []string{"t-paused", "t-missing"})
	snap, err := buildSnapshot(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	br := findBrief(t, snap, child.ID)
	got := map[string]string{}
	for _, w := range br.WaitingOn {
		got[w.ID] = w.Why
	}
	if got["t-paused"] != depWaitUnknown || got["t-missing"] != depWaitMissing {
		t.Fatalf("boundary waits=%+v", br.WaitingOn)
	}
}

func TestAttachDependencyWaitsDoesNotRewriteHeldReason(t *testing.T) {
	held := &Task{ID: "t-held", Status: statusHeld, Title: "超轮限 升级", DependsOn: []string{"t-x"}}
	br := toBrief(testCfg(), held, time.Now())
	attachDependencyWaits(&br, held, map[string]*Task{})
	if br.BlockedReason == "" {
		t.Fatal("held blocked_reason should stay the existing human reason")
	}
	if len(br.WaitingOn) != 1 || br.WaitingOn[0].Why != depWaitMissing {
		t.Fatalf("waiting_on=%+v", br.WaitingOn)
	}
}
