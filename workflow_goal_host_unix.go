//go:build !windows

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const syscallOpenNonblock = syscall.O_NONBLOCK

func ensureHostControlFIFO(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

func injectPTYMaster(master *os.File, line string) error {
	if master == nil {
		return fmt.Errorf("%s: nil master", goalFailPTYMissing)
	}
	payload := strings.TrimRight(line, "\r\n") + "\r"
	_, err := master.Write([]byte(payload))
	return err
}

// injectHostedControl focuses the TUI prompt before slash commands. A live
// Executing turn swallows /goal pause|resume|clear; Esc cancels/returns to
// the prompt. Injecting the slash line alone is not pause proof.
func injectHostedControl(master *os.File, action, line string) error {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case goalControlPause, goalControlResume, goalControlStop:
		if _, err := master.Write([]byte{0x1b}); err != nil {
			return err
		}
		time.Sleep(300 * time.Millisecond)
		if _, err := master.Write([]byte{0x1b}); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return injectPTYMaster(master, line)
}

func runHostedGrokGoal(root string, cfg *Config, wf *WorkflowRecord, t *Task, ctx context.Context, args []string, grokHome, contractPath, digest string) error {
	if cfg == nil || strings.TrimSpace(cfg.GrokBuildBin) == "" {
		return fmt.Errorf("%w: grok_build_bin", errGoalCapability)
	}
	master, slave, err := openGoalPTY()
	if err != nil {
		return fmt.Errorf("%s: %w", goalFailPTYMissing, err)
	}
	defer master.Close()
	defer slave.Close()

	cmd := exec.CommandContext(ctx, cfg.GrokBuildBin, args...)
	cmd.Dir = t.Dir
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0
	if cmd.SysProcAttr.Foreground {
		return fmt.Errorf("hosted goal: Foreground and Setctty cannot both be set")
	}
	restore := func() error { return nil }
	if cmd.Cancel == nil {
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			return killProcGroup(cmd.Process.Pid)
		}
	}
	userHome, _ := os.UserHomeDir()
	cmd.Env = providerChildEnv(userHome, map[string]string{
		"GROK_HOME":                grokHome,
		"GROK_WORKFLOWS":           "1",
		"GROK_DISABLE_AUTOUPDATER": "1",
	})

	attemptID := firstNonBlank(t.ActiveAttemptID, t.Goal.BoundAttemptID)
	fifo := goalHostControlPath(root, t.ID, attemptID)
	if err := ensureHostControlFIFO(fifo); err != nil {
		return err
	}
	ctl, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer ctl.Close()
	t.Goal.Hosted = true
	t.Goal.ControlOwner = goalControlOwnerHosted
	t.Goal.ObservationNote = "cardex-hosted PTY; /goal injected on master, not slave"
	t.touch()
	_ = saveTask(root, t)
	_ = writeGoalHostStatus(root, t.ID, attemptID, goalHostStatus{SessionID: t.SessionID, SupervisorAlive: true})

	inject := copyableNativeGoalCommand(contractPath, digest, 0)
	if t.Goal != nil {
		inject = copyableNativeGoalCommand(contractPath, digest, t.Goal.BudgetTokens)
	}
	stopCtl := make(chan struct{})
	output := os.Stdout
	outputDone := make(chan error, 1)
	prevHook := afterCmdStart
	afterCmdStart = func(started *exec.Cmd) {
		if prevHook != nil {
			prevHook(started)
		}
		// A TUI can fill the PTY before accepting /goal. Relay through the
		// caller's output stream; do not create a private-payload log or hide prompts.
		go func() {
			_, err := io.Copy(output, master)
			outputDone <- err
		}()
		_ = slave.Close()
		readyDeadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(readyDeadline) {
			if _, err := os.Stat(grokGoalSessionDir(grokHome, t.Dir, t.SessionID)); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := injectPTYMaster(master, inject); err != nil {
			t.Goal.FailureClass = classifyGoalLaunchError(err)
			_ = saveTask(root, t)
			return
		}
		_ = writeGoalHostStatus(root, t.ID, attemptID, goalHostStatus{
			SessionID:       t.SessionID,
			LastInject:      inject,
			LastInjectAt:    time.Now().UTC().Format(time.RFC3339Nano),
			SupervisorAlive: true,
		})
		go hostedControlLoop(root, t, grokHome, master, ctl, stopCtl)
	}
	defer func() {
		afterCmdStart = prevHook
		close(stopCtl)
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_, _ = w.Write([]byte("\n"))
			_ = w.Close()
		}
	}()

	taskExecRoot.Store(t.ID, root)
	defer taskExecRoot.Delete(t.ID)

	runErr := runCmdRegisteredForTaskWorkspace(cmd, t.ID, t.Dir)
	if cmd.Process != nil {
		// Drain the final TUI bytes on normal exit. A descendant retaining the
		// slave or a stalled caller must not extend the execution deadline.
		timer := time.NewTimer(time.Second)
		select {
		case err := <-outputDone:
			// Unix PTY readers commonly report EIO when the slave closes.
			if err != nil && !errors.Is(err, syscall.EIO) {
				fmt.Fprintf(os.Stderr, "warning: hosted PTY output may be incomplete: %v\n", err)
			}
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "warning: hosted PTY output may be incomplete: execution deadline reached before output drain")
		case <-timer.C:
			fmt.Fprintln(os.Stderr, "warning: hosted PTY output may be incomplete: output drain timed out")
		}
		timer.Stop()
	}
	if runErr != nil && t.Goal != nil && t.Goal.FailureClass == "" && cmd.ProcessState == nil {
		t.Goal.FailureClass = classifyGoalLaunchError(runErr)
		_ = saveTask(root, t)
		if t.Goal.FailureClass == goalFailPTYIoctl || t.Goal.FailureClass == goalFailPTYMissing {
			_ = withWorkflowSchedulerLock(root, cfg, func() error {
				return abandonUnstartedGoalAttempt(root, t)
			})
			return fmt.Errorf("%s: %w", t.Goal.FailureClass, runErr)
		}
	}
	restoreErr := restore()
	finalErr := withWorkflowSchedulerLock(root, cfg, func() error {
		return finalizeManualGoalLaunch(root, wf, t, ctx, runErr)
	})
	if finalErr != nil {
		return finalErr
	}
	return restoreErr
}

func hostedControlLoop(root string, t *Task, grokHome string, master, ctl *os.File, stop <-chan struct{}) {
	if ctl == nil {
		return
	}
	sc := bufio.NewScanner(ctl)
	lineCh := make(chan string, 4)
	go func() {
		for sc.Scan() {
			lineCh <- strings.ToLower(strings.TrimSpace(sc.Text()))
		}
		close(lineCh)
	}()
	for {
		select {
		case <-stop:
			return
		case action, ok := <-lineCh:
			if !ok {
				return
			}
			if action == "" {
				continue
			}
			line := copyableControlCommand(action)
			if line == "" {
				continue
			}
			if err := injectHostedControl(master, action, line); err != nil {
				continue
			}
			fresh, err := loadTask(root, t.ID)
			if err == nil && fresh != nil {
				*t = *fresh
			}
			confirmHostedControl(root, t, grokHome, action, line)
			if action == goalControlStop {
				_ = injectPTYMaster(master, "/quit")
				_ = master.Close()
			}
		}
	}
}
