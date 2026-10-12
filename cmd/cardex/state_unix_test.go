//go:build !windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestClassifySignalZeroErrorSeparatesLiveUnknownDead(t *testing.T) {
	t.Parallel()
	if got := classifySignalZeroError(nil); got != processObservationLive {
		t.Fatalf("nil = %s, want live", got)
	}
	if got := classifySignalZeroError(os.ErrProcessDone); got != processObservationDead {
		t.Fatalf("ErrProcessDone = %s, want dead", got)
	}
	if got := classifySignalZeroError(fmt.Errorf("gone: %w", os.ErrProcessDone)); got != processObservationDead {
		t.Fatalf("wrapped ErrProcessDone = %s, want dead", got)
	}
	if got := classifySignalZeroError(syscall.ESRCH); got != processObservationDead {
		t.Fatalf("ESRCH = %s, want dead", got)
	}
	if got := classifySignalZeroError(syscall.EPERM); got != processObservationUnknown {
		t.Fatalf("EPERM = %s, want unknown", got)
	}
	if got := classifySignalZeroError(syscall.EACCES); got != processObservationUnknown {
		t.Fatalf("EACCES = %s, want unknown", got)
	}
	if got := classifySignalZeroError(fmt.Errorf("kill: %w", syscall.EPERM)); got != processObservationUnknown {
		t.Fatalf("wrapped EPERM = %s, want unknown", got)
	}
	if got := classifySignalZeroError(&os.SyscallError{Syscall: "kill", Err: syscall.EPERM}); got != processObservationUnknown {
		t.Fatalf("SyscallError EPERM = %s, want unknown", got)
	}
	if processObservationAllowsLockSteal(processObservationLive) || processObservationAllowsLockSteal(processObservationUnknown) {
		t.Fatal("live/unknown observation must not authorize steal")
	}
	if !processObservationAllowsLockSteal(processObservationDead) {
		t.Fatal("dead observation must remain stealable")
	}
	if !errors.Is(syscall.EPERM, syscall.EPERM) {
		t.Fatal("fixture errno must remain comparable")
	}
}

func TestPermissionUnknownLockOwnerIsNotStolen(t *testing.T) {
	t.Parallel()
	if processObservationAllowsLockSteal(classifySignalZeroError(syscall.EPERM)) {
		t.Fatal("old signal(0)==nil steal treated EPERM as dead")
	}
	obs := classifyProcessObservation(1)
	if obs == processObservationDead {
		t.Skip("PID 1 is dead on this host; fixture errno still covers EPERM")
	}
	root := testRoot(t)
	path := lockPath(root)
	info, err := json.Marshal(lockInfo{PID: 1, At: time.Now().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, info, 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	if staleLock(path, time.Second) {
		t.Fatalf("permission-unknown/live PID 1 must not be stale, observation=%s", obs)
	}
	if acquireLock(root, time.Second) {
		t.Fatal("must not steal a permission-unknown or live scheduler owner")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var li lockInfo
	if err := json.Unmarshal(data, &li); err != nil || li.PID != 1 {
		t.Fatalf("owner mutated: %+v err=%v", li, err)
	}
}

func TestWriterDeferralReportsObservedRunningStatus(t *testing.T) {
	root := testRoot(t)
	cfg := defaultConfig("claude")
	cfg.MaxParallel = 2
	cfg.DrainRescanSec = 1
	cfg.ClaudeBin = fakeClaudeBin(t, mkOKResultJSON("sess-defer"), "", 0)
	if err := saveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	live := newTask(root, cfg, typeSequence, "live running writer", t.TempDir(), []string{"p"}, 9)
	live.Status = statusRunning
	if err := saveTask(root, live); err != nil {
		t.Fatal(err)
	}
	cmd := bindLiveAttempt(t, root, live)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	freshLive, err := loadTask(root, live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if freshLive.Status != statusRunning {
		t.Fatalf("live fixture status=%s", freshLive.Status)
	}
	healthy := newTask(root, cfg, typeSequence, "other workspace", t.TempDir(), []string{"p"}, 4)
	if err := saveTask(root, healthy); err != nil {
		t.Fatal(err)
	}
	if err := tick(root, cfg, true, true); err != nil {
		t.Fatal(err)
	}
	ev, ok := lastTaskEvent(root, live.ID)
	if !ok || ev.Type != evDeferred || ev.Detail["reason"] != "live_writer_proof" {
		t.Fatalf("live_writer_proof event: ok=%v ev=%+v", ok, ev)
	}
	if ev.Status != statusRunning {
		t.Fatalf("deferral status=%q want observed running", ev.Status)
	}
	if ev.Status == statusQueued {
		t.Fatal("deferral projected fake queued")
	}
	gotLive, err := loadTask(root, live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotLive.Status == statusQueued {
		t.Fatal("live writer disk status mutated to queued")
	}
	gotHealthy, err := loadTask(root, healthy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotHealthy.Status != statusDone {
		t.Fatalf("unrelated healthy card status=%s want done", gotHealthy.Status)
	}
}
