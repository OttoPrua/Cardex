//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestTaskProcessExecWaitsForDurableBind(t *testing.T) {
	parallelAdmissionHooks(t)
	for _, failBind := range []bool{false, true} {
		t.Run(strconv.FormatBool(failBind), func(t *testing.T) {
			ws := t.TempDir()
			_, taskID := reservedTaskExec(t, ws)
			marker := filepath.Join(ws, "executed")
			binding := make(chan int, 1)
			release := make(chan struct{})
			attemptWriteHook = func(rec *AttemptRecord) error {
				if rec.State == attemptBound {
					binding <- rec.PID
					<-release
					if failBind {
						return errors.New("injected bind failure")
					}
				}
				return nil
			}
			defer func() { attemptWriteHook = nil }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// The target exits immediately and records its PID and literal argv. The
			// gate must preserve both, without evaluating user arguments as shell code.
			literal := "spaces ' $HOME $(touch forbidden)"
			cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s\n%s\n' "$$" "$2" > "$1"`, "target", marker, literal)
			cmd.Dir = ws
			setupProcGroup(cmd)
			done := make(chan error, 1)
			go func() { done <- runCmdRegisteredForTask(cmd, taskID) }()
			var pid int
			select {
			case pid = <-binding:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("did not reach durable bind")
			}
			// Hold the bind long enough to expose execution before durable admission.
			time.Sleep(50 * time.Millisecond)
			_, beforeErr := os.Stat(marker)
			close(release)
			if !os.IsNotExist(beforeErr) {
				t.Errorf("target executed before durable bind: %v", beforeErr)
			}
			select {
			case err := <-done:
				if (err != nil) != failBind {
					t.Fatalf("run error=%v, failBind=%v", err, failBind)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("target did not finish")
			}
			data, err := os.ReadFile(marker)
			if failBind {
				if !os.IsNotExist(err) {
					t.Fatalf("target executed despite failed bind: %q, %v", data, err)
				}
			} else if err != nil || string(data) != strconv.Itoa(pid)+"\n"+literal+"\n" {
				t.Fatalf("PID/argv changed across exec: %q, %v", data, err)
			}
			if cmd.ProcessState == nil || processAlive(pid) || workspaceProcessResidue(ws) {
				t.Fatal("process or execution lease survived completion")
			}
			if _, err := os.Stat(filepath.Join(ws, "forbidden")); !os.IsNotExist(err) {
				t.Fatal("literal argument was evaluated")
			}
		})
	}
}

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
