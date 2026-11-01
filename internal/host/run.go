package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"cardex/internal/model"
	"cardex/internal/pty"
	"cardex/internal/runner"
	"cardex/internal/sandbox"
	"cardex/internal/store"
)

type ptyChooser interface {
	NeedsPTYFor(runner.LaunchSpec) bool
}

func run(ctx context.Context, home, attemptID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	attempt, err := db.GetAttempt(ctx, attemptID)
	if err != nil {
		return fmt.Errorf("host: load attempt: %w", err)
	}
	card, err := db.GetCard(ctx, attempt.Card)
	if err != nil {
		return fmt.Errorf("host: load card: %w", err)
	}
	channel, err := db.GetChannel(ctx, attempt.Route.Channel)
	if err != nil {
		return fmt.Errorf("host: load channel: %w", err)
	}
	r, ok := runner.Lookup(string(channel.Runner))
	if !ok {
		return fmt.Errorf("host: runner %q is not registered", channel.Runner)
	}
	if attempt.Worktree == "" {
		return errors.New("host: attempt has no worktree")
	}

	cfg := loadConfig(home)
	spec := runner.LaunchSpec{
		AttemptID:    attempt.ID,
		AttemptIndex: attempt.Index,
		WorkDir:      attempt.Worktree,
		PromptPath:   attempt.PromptPath,
		Model:        attempt.Route.Model,
	}
	scriptPath := filepath.Join(home, "attempts", attempt.ID, "script.json")
	if st, err := os.Stat(scriptPath); err == nil && !st.IsDir() {
		spec.ScriptPath = scriptPath
	}
	if channel.Runner == model.RunnerClaude && strings.TrimSpace(channel.EngineProfile) != "" {
		raw, reason := resolveEngineProfile(cfg, channel.EngineProfile)
		if reason != "" {
			return handoffReview(ctx, db, &attempt, &card, reason)
		}
		spec.Script = raw
	} else if cfg.invalid {
		appendEvent(ctx, db, attempt.ID, "host_config", map[string]string{"message": "config.json ignored"})
	}
	argv, env, err := r.Build(spec)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		return errors.New("host: runner returned an empty command")
	}
	needPTY := r.NeedsPTY()
	if chooser, ok := r.(ptyChooser); ok {
		needPTY = chooser.NeedsPTYFor(spec)
	}

	attemptDir := filepath.Join(home, "attempts", attempt.ID)
	if err := os.MkdirAll(attempt.Worktree, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(attemptDir, "output.log")
	limit, durationReason := attemptDeadline(attemptDir)
	if durationReason != "" {
		// A missing, empty, or non-positive limit must not start the executor.
		return handoffReview(ctx, db, &attempt, &card, durationReason)
	}

	attempt.PID = os.Getpid()
	if attempt.StartedAt.IsZero() {
		attempt.StartedAt = time.Now().UTC()
	}
	if err := db.UpdateAttempt(ctx, &attempt); err != nil {
		return err
	}

	cmd, err := commandFor(argv, env)
	if err != nil {
		return failStart(ctx, db, &attempt, err)
	}
	cmd.Dir = attempt.Worktree
	// The host appends each chunk to output.log. The attempt directory stays
	// outside the sandbox write list, and bytes already seen survive a crash.
	capture, err := openAttemptLog(logPath)
	if err != nil {
		return failStart(ctx, db, &attempt, err)
	}
	defer capture.Close()
	var logRead, logWrite *os.File
	var copied chan struct{}
	if !needPTY {
		if attempt.PromptPath != "" {
			prompt, err := openPrompt(ctx, attempt.PromptPath)
			if err != nil {
				return failStart(ctx, db, &attempt, err)
			}
			defer prompt.Close()
			cmd.Stdin = prompt
		}
		logRead, logWrite, err = os.Pipe()
		if err != nil {
			return failStart(ctx, db, &attempt, err)
		}
		cmd.Stdout = logWrite
		cmd.Stderr = logWrite
		copied = make(chan struct{})
		go func() {
			defer close(copied)
			_, _ = io.Copy(capture, logRead)
		}()
	}

	writable := append([]string{attempt.Worktree}, r.WritablePaths()...)
	wrapped, err := sandbox.Wrap(cmd, writable)
	if err != nil {
		closePipe(logWrite, logRead)
		if copied != nil {
			<-copied
		}
		return failStart(ctx, db, &attempt, err)
	}
	for _, ev := range sandbox.Consume(wrapped) {
		var payload any = map[string]string{"message": ev.Data}
		if json.Valid([]byte(ev.Data)) {
			payload = json.RawMessage(ev.Data)
		}
		appendEvent(ctx, db, attempt.ID, ev.Type, payload)
	}
	if !needPTY {
		if wrapped.SysProcAttr == nil {
			wrapped.SysProcAttr = &syscall.SysProcAttr{}
		}
		wrapped.SysProcAttr.Setpgid = true
	}

	appendEvent(ctx, db, attempt.ID, "host_start", map[string]any{
		"runner":      r.Name(),
		"pty":         needPTY,
		"drive":       needPTY,
		"prompt_path": attempt.PromptPath,
		"argv":        argv,
	})

	var (
		waitErr  error
		driveErr error
		pid      int
		fired    interface{ Load() bool }
	)
	if needPTY {
		session, err := pty.Start(wrapped)
		if err != nil {
			return failStart(ctx, db, &attempt, err)
		}
		pid = session.Cmd.Process.Pid
		if err := persistExecutorGroup(attemptDir, pid); err != nil {
			killGroup(pid)
			_ = session.Cmd.Wait()
			_ = session.Master.Close()
			return failStart(ctx, db, &attempt, err)
		}
		appendEvent(ctx, db, attempt.ID, "executor_pgid", map[string]any{"pgid": pid})
		pr, pw := io.Pipe()
		copied = make(chan struct{})
		var closeConsumer sync.Once
		closeRead := func() {
			closeConsumer.Do(func() { _ = pr.Close() })
		}
		go func() {
			defer close(copied)
			defer pw.Close()
			tmp := make([]byte, 32*1024)
			forward := true
			for {
				n, rerr := session.Master.Read(tmp)
				if n > 0 {
					_, _ = capture.Write(tmp[:n])
					if forward {
						if _, werr := pw.Write(tmp[:n]); werr != nil {
							// Drive stopped reading. Keep draining the master
							// into the attempt log.
							forward = false
						}
					}
				}
				if rerr != nil {
					return
				}
			}
		}()
		release := func() {
			_ = session.Master.Close()
			closeRead()
		}
		var stop func()
		var killed <-chan struct{}
		stop, killed, fired = armGroup(ctx, pid, limit, release)
		driveErr = r.Drive(ctx, struct {
			io.Reader
			io.Writer
		}{pr, session.Master})
		// Drive has returned. Unblock the capture writer and keep draining.
		closeRead()
		waitErr = session.Cmd.Wait()
		waitCopy(copied, stop, killed, pid)
		_ = session.Master.Close()
	} else {
		if err := wrapped.Start(); err != nil {
			closePipe(logWrite, logRead)
			return failStart(ctx, db, &attempt, err)
		}
		pid = wrapped.Process.Pid
		if err := persistExecutorGroup(attemptDir, pid); err != nil {
			killGroup(pid)
			_ = wrapped.Wait()
			closePipe(logWrite, logRead)
			return failStart(ctx, db, &attempt, err)
		}
		appendEvent(ctx, db, attempt.ID, "executor_pgid", map[string]any{"pgid": pid})
		if logWrite != nil {
			_ = logWrite.Close()
			logWrite = nil
		}
		release := func() {
			if logRead != nil {
				_ = logRead.Close()
			}
		}
		var stop func()
		var killed <-chan struct{}
		stop, killed, fired = armGroup(ctx, pid, limit, release)
		waitErr = wrapped.Wait()
		waitCopy(copied, stop, killed, pid)
		if logRead != nil {
			_ = logRead.Close()
		}
	}
	timedOut := fired != nil && fired.Load()
	if driveErr != nil && timedOut && (errors.Is(driveErr, context.Canceled) || errors.Is(driveErr, io.ErrClosedPipe) || errors.Is(driveErr, os.ErrClosed)) {
		driveErr = nil
	}
	output := capture.Bytes()
	if err := capture.Close(); err != nil {
		_ = finish(ctx, db, &attempt, card, output, exitCode(waitErr), r, err, driveErr, timedOut)
		return err
	}
	return finish(ctx, db, &attempt, card, output, exitCode(waitErr), r, nil, driveErr, timedOut)
}

func commandFor(argv, extra []string) (*exec.Cmd, error) {
	// ApplyEngineEnv already removed another profile's managed keys. Merging
	// that list back onto os.Environ() would put those keys back.
	env := mergeEnv(os.Environ(), extra)
	if completeEngineEnv(extra) {
		env = mergeEnv(extra, nil)
	}
	cmd := &exec.Cmd{}
	if argv[0] == "fake" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		cmd.Path = exe
		cmd.Args = append([]string{exe}, argv...)
		env = mergeEnv(env, []string{"CARDEX_FAKE_CHILD=1"})
	} else {
		path := argv[0]
		if !filepath.IsAbs(path) {
			if found, err := exec.LookPath(path); err == nil {
				path = found
			}
		}
		cmd.Path = path
		cmd.Args = append([]string{}, argv...)
	}
	cmd.Env = env
	return cmd, nil
}

// completeEngineEnv reports that extra is a full engines environment, not a
// handful of extra variables. It carries PATH and the injected base URL.
func completeEngineEnv(env []string) bool {
	path, base := false, false
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch key {
		case "PATH":
			path = true
		case "ANTHROPIC_BASE_URL":
			base = true
		}
	}
	return path && base
}

func closePipe(write, read *os.File) {
	if write != nil {
		_ = write.Close()
	}
	if read != nil {
		_ = read.Close()
	}
}

func mergeEnv(base, extra []string) []string {
	vals := map[string]string{}
	order := make([]string, 0, len(base)+len(extra))
	add := func(list []string) {
		for _, kv := range list {
			i := strings.IndexByte(kv, '=')
			if i <= 0 {
				continue
			}
			key := kv[:i]
			if _, ok := vals[key]; !ok {
				order = append(order, key)
			}
			vals[key] = kv[i+1:]
		}
	}
	add(base)
	add(extra)
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+vals[key])
	}
	return out
}

func handoffReview(ctx context.Context, db *store.Store, attempt *model.Attempt, card *model.Card, reason string) error {
	attempt.EndedAt = time.Now().UTC()
	attempt.ErrorExcerpt = trimExcerpt(reason)
	card.Status = model.CardNeedsInput
	card.WaitReason = trimExcerpt(reason)
	if err := db.UpdateCard(ctx, card); err != nil {
		return err
	}
	if err := db.UpdateAttempt(ctx, attempt); err != nil {
		return err
	}
	appendEvent(ctx, db, attempt.ID, "needs_review", map[string]string{"reason": reason})
	return nil
}

func finish(ctx context.Context, db *store.Store, attempt *model.Attempt, card model.Card, output []byte, code int, r runner.Runner, prior, driveErr error, timedOut bool) error {
	commitCtx, commitCancel := persistContext(ctx)
	defer commitCancel()
	attempt.Exit = &code
	attempt.EndedAt = time.Now().UTC()
	attempt.Usage = r.ParseUsage(output)
	attempt.ErrorClass = r.Classify(code, output)
	attempt.ErrorExcerpt = excerpt(output, attempt.ErrorClass)
	if timedOut {
		attempt.ErrorClass = model.ErrorTimeout
		if attempt.ErrorExcerpt == "" {
			attempt.ErrorExcerpt = "hard-limit timeout"
		} else if !strings.Contains(attempt.ErrorExcerpt, "hard-limit timeout") {
			attempt.ErrorExcerpt = trimExcerpt("hard-limit timeout: " + attempt.ErrorExcerpt)
		}
	}
	if driveErr != nil && !errors.Is(driveErr, context.Canceled) {
		msg := trimExcerpt(driveErr.Error())
		if msg != "" && !strings.Contains(attempt.ErrorExcerpt, msg) {
			if attempt.ErrorExcerpt == "" {
				attempt.ErrorExcerpt = msg
			} else {
				attempt.ErrorExcerpt = trimExcerpt(attempt.ErrorExcerpt + "\n" + msg)
			}
		}
		if attempt.ErrorClass == "" {
			raw, merr := json.Marshal(map[string]any{
				"type":  "error",
				"error": map[string]string{"message": driveErr.Error()},
			})
			if merr == nil {
				attempt.ErrorClass = r.Classify(code, raw)
			}
		}
	}
	if attempt.ErrorExcerpt == "" && prior != nil {
		attempt.ErrorExcerpt = trimExcerpt(prior.Error())
	}
	// Commit keeps this bound. Write-back below opens another one: a child of
	// an expired commit context is already expired, so the result would be lost.
	head, commitErr := workspaceFrom(commitCtx).Commit(commitCtx, attempt.Worktree, "cardex "+card.ID+" "+attempt.ID)
	commitCancel()
	if commitErr == nil && head != "" {
		attempt.HeadCommit = head
	}
	writeCtx, writeCancel := persistContext(ctx)
	defer writeCancel()
	if err := db.UpdateAttempt(writeCtx, attempt); err != nil {
		return err
	}
	finishEvent := map[string]any{
		"exit":        code,
		"tokens":      attempt.Usage.Tokens,
		"source":      attempt.Usage.Source,
		"error_class": attempt.ErrorClass,
	}
	if timedOut {
		finishEvent["stop"] = "hard_limit_timeout"
	}
	if driveErr != nil && !errors.Is(driveErr, context.Canceled) {
		finishEvent["drive_error"] = driveErr.Error()
	}
	appendEvent(writeCtx, db, attempt.ID, "host_finish", finishEvent)
	if commitErr != nil {
		appendEvent(writeCtx, db, attempt.ID, "workspace_commit", map[string]string{"error": commitErr.Error()})
	} else {
		appendEvent(writeCtx, db, attempt.ID, "workspace_commit", map[string]string{"head": head})
	}
	return nil
}

func failStart(ctx context.Context, db *store.Store, attempt *model.Attempt, cause error) error {
	pctx, cancel := persistContext(ctx)
	defer cancel()
	code := 127
	attempt.Exit = &code
	attempt.EndedAt = time.Now().UTC()
	attempt.ErrorExcerpt = trimExcerpt(cause.Error())
	if err := db.UpdateAttempt(pctx, attempt); err != nil {
		return err
	}
	appendEvent(pctx, db, attempt.ID, "host_finish", map[string]any{
		"exit":    code,
		"error":   attempt.ErrorExcerpt,
		"started": false,
	})
	return cause
}

func trimExcerpt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[:400]
	}
	return s
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			if ws.Exited() {
				return ws.ExitStatus()
			}
		}
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
	}
	return 1
}

// resultPersistTimeout bounds one post-execution step. The workspace commit
// and the attempt write-back that follows it each take a fresh bound, so a
// commit deadline does not expire the write-back. It is not the run deadline.
var resultPersistTimeout = 30 * time.Second

// persistContext outlives cancellation of the run context, keeps values stored
// on it (the workspace manager), and has its own deadline. Call it again for
// the next step; do not derive that step from the context this call returned.
func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), resultPersistTimeout)
}

// attemptLog appends executor output to output.log as it arrives and keeps
// the same bytes for usage parsing and error classification.
type attemptLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
	f   *os.File
	err error
}

func openAttemptLog(path string) (*attemptLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &attemptLog{f: f}, nil
}

func (l *attemptLog) Write(p []byte) (int, error) {
	if len(p) == 0 || l == nil {
		return 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	if l.f != nil {
		if _, err := l.f.Write(p); err != nil {
			if l.err == nil {
				l.err = err
			}
		} else if err := l.f.Sync(); err != nil && l.err == nil {
			l.err = err
		}
	}
	return len(p), nil
}

func (l *attemptLog) Bytes() []byte {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.buf.Bytes()...)
}

func (l *attemptLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		if err := l.f.Close(); err != nil && l.err == nil {
			l.err = err
		}
		l.f = nil
	}
	return l.err
}

// openPrompt opens path. When the run context is cancelled first, the error is
// the context error so failStart still records the attempt.
func openPrompt(ctx context.Context, path string) (*os.File, error) {
	if ctx == nil || ctx.Done() == nil {
		return os.Open(path)
	}
	type result struct {
		f   *os.File
		err error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := os.Open(path)
		ch <- result{f, err}
	}()
	select {
	case <-ctx.Done():
		go func() {
			r := <-ch
			if r.f != nil {
				_ = r.f.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-ch:
		return r.f, r.err
	}
}
