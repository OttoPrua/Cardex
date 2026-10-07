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
	"sync/atomic"
	"syscall"
	"time"
)

const (
	hostedNaturalObserveEvery = 100 * time.Millisecond
	hostedNaturalOutputQuiet  = 150 * time.Millisecond
)

// ptyOutputWatch records the last PTY drain byte so natural /quit waits for
// the final-output boundary. Sending /quit is not exit, custody, or done.
type ptyOutputWatch struct {
	lastUnixNano atomic.Int64
}

func (w *ptyOutputWatch) note(n int) {
	if w == nil || n <= 0 {
		return
	}
	w.lastUnixNano.Store(time.Now().UnixNano())
}

func (w *ptyOutputWatch) Quiet(d time.Duration) bool {
	if w == nil || d <= 0 {
		return false
	}
	last := w.lastUnixNano.Load()
	if last == 0 {
		return false
	}
	return time.Since(time.Unix(0, last)) >= d
}

type ptyTouchReader struct {
	r io.Reader
	w *ptyOutputWatch
}

func (r *ptyTouchReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if r.w != nil {
		r.w.note(n)
	}
	return n, err
}

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
// Executing turn swallows /goal pause|resume|clear. Esc does not cancel a
// turn. Enter/CR on a permission prompt chooses the focused option.
func injectHostedControl(master *os.File, action, line string) error {
	return injectHostedControlGuarded(master, action, line, nil)
}

func injectHostedControlGuarded(master *os.File, action, line string, beforePayload func() error) error {
	if beforePayload != nil {
		if err := beforePayload(); err != nil {
			return err
		}
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case goalControlPause, goalControlResume, goalControlStop:
		if _, err := master.Write([]byte{0x1b}); err != nil {
			return err
		}
		time.Sleep(300 * time.Millisecond)
		if beforePayload != nil {
			if err := beforePayload(); err != nil {
				return err
			}
		}
		if _, err := master.Write([]byte{0x1b}); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
		if beforePayload != nil {
			if err := beforePayload(); err != nil {
				return err
			}
		}
	}
	return injectPTYMaster(master, line)
}

// injectHostedControlForSession is the hosted-loop inject site. A pending
// native permission prompt must not receive Esc/slash/CR.
func injectHostedControlForSession(master *os.File, action, line, grokHome, cwd, sessionID string) error {
	check := func() error {
		return hostedControlRefuseReason(grokHome, cwd, sessionID)
	}
	if err := check(); err != nil {
		return err
	}
	return injectHostedControlGuarded(master, action, line, check)
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
	// CommandContext installs a direct-child-only Cancel by default. Override it
	// for the hosted process group, retaining a direct-child fallback as well.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		groupErr := killProcGroup(cmd.Process.Pid)
		if directErr := cmd.Process.Kill(); directErr != nil && groupErr != nil {
			return directErr
		}
		return nil
	}
	cmd.WaitDelay = time.Second
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
	if err := persistHostedGoalSupervisorStart(root, t); err != nil {
		return err
	}

	inject := copyableNativeGoalCommand(contractPath, digest, 0)
	if t.Goal != nil {
		inject = copyableNativeGoalCommand(contractPath, digest, t.Goal.BudgetTokens)
	}
	stopCtl := make(chan struct{})
	output := os.Stdout
	// Tee the existing bounded exact complete-line matcher into the single
	// master io.Copy. Child stdout/stderr stay on the slave (isatty(2)).
	// This is not a second PTY reader and not a private output log.
	captured := &bootstrapRefusalCapture{sink: output}
	type outputResult struct {
		err      error
		reported bool
	}
	outputDone := make(chan outputResult, 1)
	outputWatch := &ptyOutputWatch{}
	var hostedPersistErr error
	prevHook := afterCmdResume
	afterCmdResume = func(started *exec.Cmd) {
		if prevHook != nil {
			prevHook(started)
		}
		// A TUI can fill the PTY before accepting /goal. Relay through the
		// caller's output stream; do not create a private-payload log or hide prompts.
		go func() {
			_, err := io.Copy(captured, &ptyTouchReader{r: master, w: outputWatch})
			reported := false
			if err != nil && !errors.Is(err, syscall.EIO) {
				// A failed caller sink must not stop draining the PTY and leave
				// its producer blocked forever on terminal output. Report the
				// loss immediately and drain remaining bytes until child exit.
				// Sequential Discard continues this same reader.
				fmt.Fprintf(os.Stderr, "warning: hosted PTY output may be incomplete: %v\n", err)
				reported = true
				_, _ = io.Copy(io.Discard, &ptyTouchReader{r: master, w: outputWatch})
			}
			outputDone <- outputResult{err: err, reported: reported}
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
			hostedPersistErr = persistHostedGoalOwned(root, cfg, t)
			return
		}
		_ = writeGoalHostStatus(root, t.ID, attemptID, goalHostStatus{
			SessionID:       t.SessionID,
			LastInject:      inject,
			LastInjectAt:    time.Now().UTC().Format(time.RFC3339Nano),
			SupervisorAlive: true,
		})
		go hostedControlLoop(root, t, grokHome, master, ctl, stopCtl, outputWatch)
	}
	defer func() {
		afterCmdResume = prevHook
		close(stopCtl)
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_, _ = w.Write([]byte("\n"))
			_ = w.Close()
		}
	}()

	taskExecRoot.Store(t.ID, root)
	defer taskExecRoot.Delete(t.ID)

	runErr := runCmdRegisteredForTaskWorkspace(cmd, t.ID, t.Dir)
	matchedRefusal := ""
	if cmd.Process != nil {
		// Drain the final TUI bytes on normal exit. A descendant retaining the
		// slave or a stalled caller must not extend the execution deadline.
		timer := time.NewTimer(time.Second)
		select {
		case result := <-outputDone:
			// Unix PTY readers commonly report EIO when the slave closes.
			if result.err != nil && !errors.Is(result.err, syscall.EIO) && !result.reported {
				fmt.Fprintf(os.Stderr, "warning: hosted PTY output may be incomplete: %v\n", result.err)
			}
			matchedRefusal = captured.capturedOutput()
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "warning: hosted PTY output may be incomplete: execution deadline reached before output drain")
		case <-timer.C:
			fmt.Fprintln(os.Stderr, "warning: hosted PTY output may be incomplete: output drain timed out")
		}
		timer.Stop()
	}
	if rec, rerr := loadRequiredGoalAttempt(root, t); rerr == nil && rec != nil {
		recordRetainedBootstrapBeforeProviderEvidence(root, t, rec, runErr, matchedRefusal)
	}
	restoreErr := restore()
	return completeHostedGrokGoalAfterCmd(root, cfg, wf, t, ctx, runErr, restoreErr, hostedPersistErr, cmd.ProcessState != nil)
}

func injectHostedNaturalQuit(master *os.File, grokHome, cwd, sessionID string) error {
	if err := hostedControlRefuseReason(grokHome, cwd, sessionID); err != nil {
		return err
	}
	return injectPTYMaster(master, "/quit")
}

func hostedControlLoop(root string, t *Task, grokHome string, master, ctl *os.File, stop <-chan struct{}, watch *ptyOutputWatch) {
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
	ticker := time.NewTicker(hostedNaturalObserveEvery)
	defer ticker.Stop()
	quitSent := false
	lastFP := ""
	tryNaturalQuit := func() {
		if quitSent || t == nil {
			return
		}
		fresh, err := loadTask(root, t.ID)
		if err == nil && fresh != nil {
			*t = *fresh
		}
		expectedGoalID := ""
		if t.Goal != nil {
			expectedGoalID = t.Goal.NativeGoalID
		}
		fp := nativeGoalEvidenceFingerprint(grokHome, t.Dir, t.SessionID)
		stable := nativeGoalEvidenceStable(lastFP, fp)
		lastFP = fp
		if !stable || !hostedNaturalQuitReady(grokHome, t.Dir, t.SessionID, expectedGoalID, watch.Quiet(hostedNaturalOutputQuiet)) {
			// Missing or out-of-order current-session turn evidence keeps waiting.
			return
		}
		if err := injectHostedNaturalQuit(master, grokHome, t.Dir, t.SessionID); err != nil {
			return
		}
		quitSent = true
	}
	injectTerminalQuit := func() {
		if quitSent {
			return
		}
		_ = injectPTYMaster(master, "/quit")
		quitSent = true
	}
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			tryNaturalQuit()
		case action, ok := <-lineCh:
			if !ok {
				return
			}
			if action == "" {
				continue
			}
			fresh, err := loadTask(root, t.ID)
			if err == nil && fresh != nil {
				*t = *fresh
			}
			expectedGoalID := ""
			if t.Goal != nil {
				expectedGoalID = t.Goal.NativeGoalID
			}
			if action == goalControlStop {
				obs, oerr := observeNativeGrokGoal(grokHome, t.Dir, t.SessionID, expectedGoalID)
				if oerr == nil && nativeStopConfirmed(obs, expectedGoalID) {
					// Already a stop-confirmable native terminal (including budget_limited/Idle).
					// Bytes are not confirmation; native post-state is. Then quit for Wait/drain.
					confirmHostedControl(root, t, grokHome, action, "")
					injectTerminalQuit()
					continue
				}
			}
			if err := hostedControlRefuseReason(grokHome, t.Dir, t.SessionID); err != nil {
				persistHostedControlUnsupported(root, t, action)
				continue
			}
			line := copyableControlCommand(action)
			if line == "" {
				continue
			}
			if err := injectHostedControlForSession(master, action, line, grokHome, t.Dir, t.SessionID); err != nil {
				if hostedControlRefuseReason(grokHome, t.Dir, t.SessionID) != nil {
					persistHostedControlUnsupported(root, t, action)
				}
				continue
			}
			confirmed := waitHostedControlConfirmed(root, t, grokHome, action, line, 15*time.Second)
			if action == goalControlStop && confirmed {
				injectTerminalQuit()
				// Native completion is not process exit. Sending /quit bytes is
				// not release; Wait/output drain/custody reclaim still own the child.
			}
		}
	}
}
