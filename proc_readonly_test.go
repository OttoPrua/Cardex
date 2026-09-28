package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestExecutionLeaseChild(t *testing.T) {
	if os.Getenv("CARDEX_LEASE_CHILD") != "1" {
		return
	}
	if err := os.WriteFile(os.Getenv("CARDEX_LEASE_STARTED"), []byte("started"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(os.Getenv("CARDEX_LEASE_GATE")); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("execution gate timeout")
}

func TestExecutionLeaseReadOnlyParallel(t *testing.T) {
	for _, types := range [][2]string{{typeReview, typeSequence}, {typeSequence, typeReview}, {typeSequence, typeSequence}} {
		t.Run(types[0]+"_then_"+types[1], func(t *testing.T) {
			root, workspace := testRoot(t), t.TempDir()
			withSchedulerLock(t, root)
			tasks := make([]*Task, 2)
			for i := range tasks {
				tasks[i] = queuedSequence(t, root, testCfg(), types[i], workspace)
				tasks[i].Type = types[i]
				if err := saveTask(root, tasks[i]); err != nil {
					t.Fatal(err)
				}
				if err := reserveDispatchAttempt(root, tasks[i]); err != nil {
					t.Fatal(err)
				}
				taskExecRoot.Store(tasks[i].ID, root)
				defer taskExecRoot.Delete(tasks[i].ID)
			}
			launch := func(i int) (<-chan error, string, string) {
				dir := t.TempDir()
				gate, started := filepath.Join(dir, "gate"), filepath.Join(dir, "started")
				cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestExecutionLeaseChild$")
				cmd.Dir = workspace
				cmd.Env = append(os.Environ(), "CARDEX_LEASE_CHILD=1", "CARDEX_LEASE_GATE="+gate, "CARDEX_LEASE_STARTED="+started)
				setupProcGroup(cmd)
				done := make(chan error, 1)
				go func() { done <- runCmdRegisteredForTask(cmd, tasks[i].ID) }()
				t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0600) })
				return done, gate, started
			}
			waitStarted := func(path string, done <-chan error) {
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(path); err == nil {
						return
					}
					select {
					case err := <-done:
						t.Fatalf("child exited before start: %v", err)
					default:
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("second child never overlapped the first")
			}
			first, gate1, started1 := launch(0)
			waitStarted(started1, first)
			second, gate2, started2 := launch(1)
			if types[0] == typeSequence && types[1] == typeSequence {
				select {
				case err := <-second:
					if !errors.Is(err, errWorkspaceExecutionLeaseBusy) {
						t.Fatalf("second writer not blocked: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("second writer did not reject held lease")
				}
				if _, err := os.Stat(started2); !os.IsNotExist(err) {
					t.Fatal("second writer executed")
				}
			} else {
				waitStarted(started2, second)
				for _, task := range tasks {
					rec, err := loadAttempt(root, task.ID, task.ActiveAttemptID)
					if err != nil || !verifyAttemptProcess(rec) {
						t.Fatalf("live identity lost: %+v %v", rec, err)
					}
				}
				_ = os.WriteFile(gate2, nil, 0600)
				if err := <-second; err != nil {
					t.Fatal(err)
				}
				rec, err := loadAttempt(root, tasks[1].ID, tasks[1].ActiveAttemptID)
				if err != nil || !producerGone(tasks[1], rec) {
					t.Fatalf("completed producer confused with other task: %+v %v", rec, err)
				}
			}
			_ = os.WriteFile(gate1, nil, 0600)
			if err := <-first; err != nil {
				t.Fatal(err)
			}
			if workspaceProcessResidue(workspace) {
				t.Fatal("workspace writer lease remained")
			}
		})
	}
}
