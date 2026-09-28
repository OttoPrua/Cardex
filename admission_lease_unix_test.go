//go:build !windows

package main

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"testing"
)

func TestBindWriteFailureReapsProcessAndReleasesLease(t *testing.T) {
	parallelAdmissionHooks(t)
	if runtime.GOOS == "windows" {
		t.Skip("workspace flock is POSIX")
	}
	root := testRoot(t)
	withSchedulerLock(t, root)
	cfg := testCfg()
	ws := t.TempDir()
	tk := queuedSequence(t, root, cfg, "bind write failure", ws)
	if err := reserveDispatchAttempt(root, tk); err != nil {
		t.Fatal(err)
	}
	taskExecRoot.Store(tk.ID, root)
	t.Cleanup(func() { taskExecRoot.Delete(tk.ID) })
	attemptWriteHook = func(rec *AttemptRecord) error {
		if rec != nil && rec.State == attemptBound {
			return errors.New("injected bind write failure")
		}
		return nil
	}
	t.Cleanup(func() { attemptWriteHook = nil })

	cmd := exec.CommandContext(context.Background(), "sleep", "60")
	cmd.Dir = ws
	setupProcGroup(cmd)
	err := runCmdRegisteredForTask(cmd, tk.ID)
	if err == nil {
		t.Fatal("injected bind write failure returned nil")
	}
	if cmd.Process == nil {
		t.Fatal("bind path must Start before durable bind")
	}
	if cmd.ProcessState == nil {
		t.Fatal("bind failure must Wait/reap the new process")
	}
	if processAlive(cmd.Process.Pid) {
		t.Fatalf("new process pid=%d still alive after bind failure", cmd.Process.Pid)
	}
	lease, lerr := acquireWorkspaceExecutionLease(ws)
	if lerr != nil {
		t.Fatalf("workspace lease not acquirable after bind failure: %v", lerr)
	}
	if lease != nil {
		_ = lease.Close()
	}
	if workspaceProcessResidue(ws) {
		t.Fatal("workspace execution lease still held after bind failure cleanup")
	}
}
