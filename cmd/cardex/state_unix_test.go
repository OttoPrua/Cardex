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
