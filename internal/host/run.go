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
	if cfg.invalid {
		appendEvent(ctx, db, attempt.ID, "host_config", map[string]string{"message": "config.json ignored"})
	} else if strings.TrimSpace(cfg.Host.HardDuration) != "" && hardLimit(cfg) == 0 {
		appendEvent(ctx, db, attempt.ID, "host_config", map[string]string{"message": "host.hard_duration ignored"})
	}

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
	if channel.Runner == model.RunnerClaude && channel.EngineProfile != "" {
		if raw, ok := profileJSON(cfg, channel.EngineProfile); ok {
			spec.Script = raw
		}
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
	var logFile *os.File
	defer func() {
		if logFile != nil {
			_ = logFile.Close()
		}
	}()
	if !needPTY {
		if attempt.PromptPath != "" {
			prompt, err := os.Open(attempt.PromptPath)
			if err != nil {
				return failStart(ctx, db, &attempt, err)
			}
			defer prompt.Close()
			cmd.Stdin = prompt
		}
		logFile, err = os.Create(logPath)
		if err != nil {
			return failStart(ctx, db, &attempt, err)
		}
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	writable := append([]string{attempt.Worktree, attemptDir}, r.WritablePaths()...)
	wrapped, err := sandbox.Wrap(cmd, writable)
	if err != nil {
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
		waitErr error
		output  []byte
		pid     int
	)
	limit := hardLimit(cfg)
	if needPTY {
		session, err := pty.Start(wrapped)
		if err != nil {
			return failStart(ctx, db, &attempt, err)
		}
		pid = session.Cmd.Process.Pid
		var buf bytes.Buffer
		copied := make(chan struct{})
		go func() {
			defer close(copied)
			_, _ = io.Copy(&buf, session.Master)
		}()
		_ = r.Drive(ctx, session.Master)
		stop := watchKill(ctx, pid, limit)
		waitErr = session.Cmd.Wait()
		stop()
		select {
		case <-copied:
		case <-time.After(3 * time.Second):
			_ = session.Master.Close()
			<-copied
		}
		_ = session.Master.Close()
		output = buf.Bytes()
		if err := os.WriteFile(logPath, output, 0o644); err != nil {
			_ = finish(ctx, db, &attempt, card, output, exitCode(waitErr), r, err)
			return err
		}
	} else {
		if err := wrapped.Start(); err != nil {
			return failStart(ctx, db, &attempt, err)
		}
		pid = wrapped.Process.Pid
		stop := watchKill(ctx, pid, limit)
		waitErr = wrapped.Wait()
		stop()
		if logFile != nil {
			_ = logFile.Sync()
			_ = logFile.Close()
			logFile = nil
		}
		var readErr error
		output, readErr = os.ReadFile(logPath)
		if readErr != nil {
			_ = finish(ctx, db, &attempt, card, nil, exitCode(waitErr), r, readErr)
			return readErr
		}
	}
	return finish(ctx, db, &attempt, card, output, exitCode(waitErr), r, nil)
}

func commandFor(argv, extra []string) (*exec.Cmd, error) {
	env := mergeEnv(os.Environ(), extra)
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

func finish(ctx context.Context, db *store.Store, attempt *model.Attempt, card model.Card, output []byte, code int, r runner.Runner, prior error) error {
	attempt.Exit = &code
	attempt.EndedAt = time.Now().UTC()
	attempt.Usage = r.ParseUsage(output)
	attempt.ErrorClass = r.Classify(code, output)
	attempt.ErrorExcerpt = excerpt(output, attempt.ErrorClass)
	if attempt.ErrorExcerpt == "" && prior != nil {
		attempt.ErrorExcerpt = trimExcerpt(prior.Error())
	}
	head, commitErr := workspaceFrom(ctx).Commit(ctx, attempt.Worktree, "cardex "+card.ID+" "+attempt.ID)
	if commitErr == nil && head != "" {
		attempt.HeadCommit = head
	}
	if err := db.UpdateAttempt(ctx, attempt); err != nil {
		return err
	}
	appendEvent(ctx, db, attempt.ID, "host_finish", map[string]any{
		"exit":        code,
		"tokens":      attempt.Usage.Tokens,
		"source":      attempt.Usage.Source,
		"error_class": attempt.ErrorClass,
	})
	if commitErr != nil {
		appendEvent(ctx, db, attempt.ID, "workspace_commit", map[string]string{"error": commitErr.Error()})
	} else {
		appendEvent(ctx, db, attempt.ID, "workspace_commit", map[string]string{"head": head})
	}
	return nil
}

func failStart(ctx context.Context, db *store.Store, attempt *model.Attempt, cause error) error {
	code := 127
	attempt.Exit = &code
	attempt.EndedAt = time.Now().UTC()
	attempt.ErrorExcerpt = trimExcerpt(cause.Error())
	if err := db.UpdateAttempt(ctx, attempt); err != nil {
		return err
	}
	appendEvent(ctx, db, attempt.ID, "host_finish", map[string]any{
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

func watchKill(ctx context.Context, pid int, limit time.Duration) func() {
	if pid <= 1 {
		return func() {}
	}
	cancellable := ctx != nil && ctx.Done() != nil
	if limit <= 0 && !cancellable {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	go func() {
		var timer <-chan time.Time
		if limit > 0 {
			t := time.NewTimer(limit)
			defer t.Stop()
			timer = t.C
		}
		select {
		case <-done:
		case <-timer:
			killGroup(pid)
		case <-ctx.Done():
			killGroup(pid)
		}
	}()
	return stop
}

func killGroup(pid int) {
	if pid <= 1 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
