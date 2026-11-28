package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/proxy"
	"github.com/OttoPrua/Cardex/internal/pty"
	"github.com/OttoPrua/Cardex/internal/roots"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/sandbox"
	"github.com/OttoPrua/Cardex/internal/store"
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
	promptPath := roots.ResolveStored(home, attempt.PromptPath)
	card, err := db.GetCard(ctx, attempt.Card)
	if err != nil {
		return fmt.Errorf("host: load card: %w", err)
	}
	if card.UsesStrictReviewObjects(attempt) {
		ctx = proc.WithExactGitObjects(ctx)
	}
	if card.StrictDeps {
		allowed, err := strictLaunchGate(ctx, db, &attempt, strictProbe)
		if err != nil || !allowed {
			return err
		}
	}
	channel, err := db.GetChannel(ctx, attempt.Route.Channel)
	if err != nil {
		return fmt.Errorf("host: load channel: %w", err)
	}
	approvedBindingID := ""
	if attempt.RouteReason.Dispatch != nil {
		approvedBindingID = attempt.RouteReason.Dispatch.CompatibleBindingID
	}
	if syntheticCompatibleRelay == nil && (approvedBindingID != "" || channel.CompatibleRuntimeHeld()) {
		return holdCompatibleHostAttempt(ctx, db, card, attempt)
	}
	if approvedBindingID != "" && approvedBindingID != runner.CompatibleBindingID(channel.EngineProfile) {
		return handoffReview(ctx, db, &attempt, &card, "api channel dispatch binding changed; select an approved route again")
	}
	r, ok := runner.Lookup(string(channel.Runner))
	if !ok {
		return fmt.Errorf("host: runner %q is not registered", channel.Runner)
	}
	if attempt.Worktree == "" {
		return errors.New("host: attempt has no worktree")
	}
	var goalMode bool
	var goalRunner runner.Runner
	if gr, isGoal := r.(runner.GoalRunner); isGoal && requiresGoal(card) {
		launch := runner.GoalLaunch{WorkDir: attempt.Worktree, AttemptID: attempt.ID}
		if promptPath != "" {
			body, err := os.ReadFile(promptPath)
			if err != nil {
				return fmt.Errorf("host: goal prompt: %w", err)
			}
			launch.Prompt = string(body)
		}
		r = gr.ForGoal(launch)
		goalMode = true
		goalRunner = r
	}

	cfg := loadConfig(home)
	credentialNames := runner.CompatibleConfigCredentialNames(cfg.Engines)
	ctx = proc.WithCompatibleCredentialNames(ctx, credentialNames)
	spec := runner.LaunchSpec{
		AttemptID:    attempt.ID,
		AttemptIndex: attempt.Index,
		WorkDir:      attempt.Worktree,
		PromptPath:   promptPath,
		Model:        attempt.Route.Model,
		DropEnv:      proc.CompatibleCredentialNames(ctx),
	}
	if goalMode {
		spec.SessionID = attempt.ID
	}
	attemptDir := roots.AttemptDir(home, attempt.ID, attempt.PromptPath)
	scriptPath := filepath.Join(attemptDir, "script.json")
	if st, err := os.Stat(scriptPath); err == nil && !st.IsDir() {
		spec.ScriptPath = scriptPath
	}
	var relay *runner.CompatibleRelay
	apiIsolated := false
	if channel.Runner != model.RunnerClaude && runner.IsCompatibleBinding(channel.EngineProfile) {
		return handoffReview(ctx, db, &attempt, &card, "api channel requires the claude runner")
	}
	if channel.Runner == model.RunnerClaude && strings.TrimSpace(channel.EngineProfile) != "" {
		raw, reason := resolveEngineProfile(cfg, channel.EngineProfile)
		if reason != "" {
			return handoffReview(ctx, db, &attempt, &card, reason)
		}
		spec.Script = raw
		if runner.IsCompatibleEngine(raw) {
			if syntheticCompatibleRelay == nil {
				sum := sha256.Sum256(raw)
				proof := store.CompatibleRefusal{
					ProfileDigest: fmt.Sprintf("%x", sum),
					Verify: func(current model.Channel) bool {
						if !reflect.DeepEqual(current, channel) {
							return false
						}
						currentRaw, reason := resolveEngineProfile(loadConfig(home), current.EngineProfile)
						return reason == "" && runner.IsCompatibleEngine(currentRaw) && sha256.Sum256(currentRaw) == sum
					},
				}
				return db.HoldCompatibleRefusal(ctx, card, attempt, observeCompatibleObjective(ctx, db, card), time.Now(), proof)
			}
			apiIsolated = true
			spec.CardKind = card.Kind
			if !runner.IsCompatibleBinding(channel.EngineProfile) {
				return handoffReview(ctx, db, &attempt, &card, "api channel requires an explicit channel set binding")
			}
			if requiresBrowser(card) || requiresGoal(card) {
				return handoffReview(ctx, db, &attempt, &card, "api channel cannot provide requested browser or goal isolation")
			}
			var fileRoots []string
			if runner.CompatibleFileRef(raw) {
				obj, err := db.GetObjective(ctx, card.Objective)
				if err != nil {
					return handoffReview(ctx, db, &attempt, &card, "api channel cannot verify credential outside the repository")
				}
				project, err := db.GetProject(ctx, obj.Project)
				if err != nil || project.RepoPath == "" {
					return handoffReview(ctx, db, &attempt, &card, "api channel cannot verify credential outside the repository")
				}
				fileRoots = []string{home, project.RepoPath, attempt.Worktree}
				if project.WorktreeRoot != "" {
					fileRoots = append(fileRoots, project.WorktreeRoot)
				}
			}
			verified, err := runner.ValidateCompatibleLaunch(spec, channel, approvedBindingID, fileRoots...)
			if err != nil {
				return handoffReview(ctx, db, &attempt, &card, err.Error())
			}
			spec.Script = verified
			if _, reason := attemptDeadline(attemptDir); reason != "" {
				return handoffReview(ctx, db, &attempt, &card, reason)
			}
			spec.DropEnv = runner.CompatibleChildDropNames(home)
			configDir, err := isolateClaudeConfig(attemptDir)
			if err != nil {
				return handoffReview(ctx, db, &attempt, &card, err.Error())
			}
			spec.ConfigDir = configDir
			defer cleanupClaudeConfig(db, attempt.ID, configDir)
			relay, err = syntheticCompatibleRelay(ctx, verified, fileRoots...)
			if err != nil {
				return handoffReview(ctx, db, &attempt, &card, err.Error())
			}
			defer relay.Close()
			spec.RelayURL, spec.RelayKey = relay.LocalURL, relay.LocalKey
		}
	} else if cfg.invalid {
		appendEvent(ctx, db, attempt.ID, "host_config", map[string]string{"message": "config.json ignored"})
	}
	argv, env, err := r.Build(spec)
	if err != nil {
		if apiIsolated {
			return handoffReview(ctx, db, &attempt, &card, err.Error())
		}
		return err
	}
	if len(argv) == 0 {
		return errors.New("host: runner returned an empty command")
	}
	needPTY := r.NeedsPTY()
	if chooser, ok := r.(ptyChooser); ok {
		needPTY = chooser.NeedsPTYFor(spec)
	}

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

	if card.StrictDeps {
		allowed, err := strictLaunchGate(ctx, db, &attempt, strictClaim)
		if err != nil || !allowed {
			return err
		}
	} else {
		attempt.PID = os.Getpid()
		if attempt.StartedAt.IsZero() {
			attempt.StartedAt = time.Now().UTC()
		}
		if err := db.UpdateAttempt(ctx, &attempt); err != nil {
			return err
		}
	}

	// Legacy launch keeps its existing proxy behavior. API1 uses one
	// attempt-scoped local transport and no configured/inherited proxy.
	if !apiIsolated {
		if pe, err := proxy.ChildEnv(home, channel.ID); err != nil {
			appendEvent(ctx, db, attempt.ID, "host_config", map[string]string{"message": err.Error()})
		} else if pe != nil {
			env = append(env, pe...)
		}
	}
	// A browser card gets its own Chrome, started here outside the seatbelt
	// so Chrome keeps its own sandbox. It is stopped when this run returns.
	// A browser_start event with an error is terminal: no browser_stop follows.
	// Card cancel is watched only once the executor starts; Chrome startup is
	// bounded by browserStartTimeout.
	if card.StrictDeps && requiresBrowser(card) {
		reviewScope, err := proveStrictHostReviewScope(ctx, db, card, attempt)
		if err != nil {
			return err
		}
		allowed, err := strictLaunchGateWithReviewScope(ctx, db, &attempt, strictExec, reviewScope)
		if err != nil || !allowed {
			return err
		}
	}
	if requiresBrowser(card) {
		b, err := launchBrowser(ctx, cfg, attemptDir)
		if err != nil {
			appendEvent(ctx, db, attempt.ID, "browser_start", map[string]any{"error": err.Error()})
			return failStart(ctx, db, &attempt, err)
		}
		defer func() {
			b.Stop()
			appendEvent(context.Background(), db, attempt.ID, "browser_stop", map[string]any{"pgid": b.pgid})
		}()
		appendEvent(ctx, db, attempt.ID, "browser_start", map[string]any{"pgid": b.pgid, "cdp": b.httpURL})
		env = append(env, b.Env(env)...)
	}
	scratchDir, cleanupScratch, serr := prepareRunScratch(ctx, db, home, card, attempt.ID, attemptDir)
	if serr != nil {
		return failStart(ctx, db, &attempt, serr)
	}
	defer cleanupScratch()
	if apiIsolated {
		if err := runner.ProveAPIIsolation(spec, argv, env); err != nil {
			return handoffReview(ctx, db, &attempt, &card, err.Error())
		}
	}
	cmd, err := commandForEnv(argv, env, apiIsolated)
	if err == nil && !apiIsolated {
		cmd.Env = proc.CompatibleChildEnv(ctx, cmd.Env)
	}
	if err != nil {
		return failStart(ctx, db, &attempt, err)
	}
	cmd.Env = roots.ApplyTempEnv(cmd.Env, scratchDir)
	// The shim is first on PATH. The executor keeps tool homes and GIT_*;
	// only `go test` is started with AcceptanceEnv.
	if !apiIsolated {
		wrapGo := proc.PrependGoTestWrap
		if card.UsesStrictReviewObjects(attempt) {
			wrapGo = proc.PrependExactGoTestWrap
		}
		cmd.Env, err = wrapGo(cmd.Env, filepath.Join(attemptDir, "go-wrap"))
		if err != nil {
			return failStart(ctx, db, &attempt, err)
		}
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
		if promptPath != "" {
			prompt, err := openPrompt(ctx, promptPath)
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
			if relay != nil {
				relay.CopyOutput(capture, logRead)
			} else {
				_, _ = io.Copy(capture, logRead)
			}
		}()
	}

	writable := []string{attempt.Worktree}
	if apiIsolated {
		writable = append(writable, spec.ConfigDir)
	} else {
		writable = append(writable, r.WritablePaths()...)
	}
	if scratchDir != "" {
		writable = append(writable, scratchDir)
	}
	wrap := sandbox.Wrap
	if sandboxOff(ctx, db, card) {
		wrap = func(c *exec.Cmd, _ []string) (*exec.Cmd, error) { return sandbox.Off(c) }
	}
	wrapped, err := wrap(cmd, writable)
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
		proc.NewGroup(wrapped)
	}

	// This is the last atomic database decision before executor spawn.
	// Do not hold a database transaction across operating-system process
	// launch: a dependency can change after this check, and an already
	// admitted process is not revoked by later dependency changes.
	if card.StrictDeps {
		reviewScope, scopeErr := proveStrictHostReviewScope(ctx, db, card, attempt)
		if card.UsesStrictReviewObjects(attempt) {
			// Scope the actual post-wrapper child, not just CardeX evidence
			// commands. Plain Git used by the reviewer must see the same objects.
			wrapped.Env = proc.GitObjectScopeEnv(wrapped.Env)
		}
		allowed := false
		err := scopeErr
		if err == nil {
			allowed, err = strictLaunchGateWithReviewScope(ctx, db, &attempt, strictExec, reviewScope)
		}
		if err != nil || !allowed {
			closePipe(logWrite, logRead)
			if copied != nil {
				<-copied
			}
			return err
		}
	}

	appendEvent(ctx, db, attempt.ID, "host_start", map[string]any{
		"runner":      r.Name(),
		"pty":         needPTY,
		"drive":       needPTY,
		"prompt_path": promptPath,
		"argv":        argv,
	})

	var (
		waitErr    error
		driveErr   error
		pid        int
		goalStatus string
		fired      interface{ Load() bool }
	)
	// Canceling the card stops this executor the same way the deadline does,
	// so a canceled card does not keep holding files and its worktree.
	execCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go watchCardCanceled(execCtx, db, card.ID, attempt.ID, stopWatch)
	if needPTY {
		session, err := pty.Start(wrapped)
		if err != nil {
			return failStart(ctx, db, &attempt, err)
		}
		pid = session.Cmd.Process.Pid
		if err := persistExecutorGroup(attemptDir, pid); err != nil {
			killGroup(pid)
			_ = session.Wait()
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
		stop, killed, fired = armGroup(execCtx, pid, limit, release)
		driveErr = r.Drive(ctx, struct {
			io.Reader
			io.Writer
		}{pr, session.Master})
		// Drive has returned. Unblock the capture writer and keep draining.
		closeRead()
		if goalMode {
			goalStatus = goalTerminalStatus(driveErr)
		}
		if goalStatus != "" || (goalMode && errors.Is(driveErr, runner.ErrGoalStartFailed)) {
			// A delivered SIGTERM is not the group exiting. The deadline stays
			// armed and killGroup is what reaps a group that ignores it.
			if pid > 1 {
				_ = SignalGroup(pid, syscall.SIGTERM)
			}
		}
		waitErr = session.Wait()
		waitCopy(copied, stop, killed, pid)
		_ = session.Master.Close()
	} else {
		if err := wrapped.Start(); err != nil {
			closePipe(logWrite, logRead)
			// Cmd.Start failed without handing us a process. The subsequent
			// process-group persistence failures are NOT no-spawn paths.
			// Exclude preparatory browser/relay children from this proof.
			if card.StrictDeps && wrapped.Process == nil && !requiresBrowser(card) && relay == nil {
				if copied != nil {
					<-copied
				}
				return failStrictNoExecutor(ctx, db, &attempt, err)
			}
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
		stop, killed, fired = armGroup(execCtx, pid, limit, release)
		waitErr = wrapped.Wait()
		waitCopy(copied, stop, killed, pid)
		if logRead != nil {
			_ = logRead.Close()
		}
	}
	if goalMode {
		driveErr = noteGoalExit(ctx, db, attempt.ID, goalRunner, waitErr, driveErr, goalStatus != "")
		if errors.Is(driveErr, runner.ErrGrokCrash) {
			goalStatus = ""
		}
	}
	timedOut := fired != nil && fired.Load()
	if driveErr != nil && timedOut && (errors.Is(driveErr, context.Canceled) || errors.Is(driveErr, io.ErrClosedPipe) || errors.Is(driveErr, os.ErrClosed)) {
		driveErr = nil
	}
	output := capture.Bytes()
	if err := capture.Close(); err != nil {
		_ = finish(ctx, db, &attempt, card, output, exitCode(waitErr), r, err, driveErr, timedOut, goalStatus)
		return err
	}
	return finish(ctx, db, &attempt, card, output, exitCode(waitErr), r, nil, driveErr, timedOut, goalStatus)
}

func projectScratch(ctx context.Context, db *store.Store, card model.Card) string {
	obj, err := db.GetObjective(ctx, card.Objective)
	if err != nil {
		return ""
	}
	p, err := db.GetProject(ctx, obj.Project)
	if err != nil {
		return ""
	}
	return p.ScratchDir
}

func commandFor(argv, extra []string) (*exec.Cmd, error) {
	return commandForEnv(argv, extra, false)
}

func commandForEnv(argv, extra []string, isolated bool) (*exec.Cmd, error) {
	// ApplyEngineEnv already removed another profile's managed keys. Merging
	// that list back onto os.Environ() would put those keys back.
	base := os.Environ()
	overlay := extra
	if isolated || completeEngineEnv(extra) {
		base = extra
		overlay = nil
	}
	// Do not pass host bootstrap state back into the executable. This marker is
	// a CardeX workflow guard, not an OS permission boundary.
	env := mergeEnv(proc.ExecutorEnv(base), proc.ExecutorEnv(overlay))
	env = mergeEnv(env, []string{proc.ManagedExecutorEnv + "=1"})
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
		if isolated && envLookup(env, "PATH") == "" {
			return nil, errors.New("api channel requires a trusted executor search path")
		}
		if !filepath.IsAbs(path) {
			if found, err := proc.LookPath(path, env); err == nil {
				path = found
			}
		}
		if isolated && !filepath.IsAbs(path) {
			return nil, errors.New("api channel executor could not be resolved to an absolute path")
		}
		cmd.Path = path
		cmd.Args = append([]string{}, argv...)
		proc.Script(cmd)
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
		switch {
		case strings.EqualFold(key, "PATH"): // Windows spells it Path
			path = true
		case key == "ANTHROPIC_BASE_URL":
			base = true
		}
	}
	return path && base
}

func envLookup(env []string, key string) string {
	var got string
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, key) {
			got = v
		}
	}
	return got
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

// Populated exclusively by a host test helper. Production cannot activate
// compatible provider traffic by configuring an engine, changing health, or
// invoking a prepared attempt directly.
var syntheticCompatibleRelay func(context.Context, []byte, ...string) (*runner.CompatibleRelay, error)

func isolateClaudeConfig(attemptDir string) (string, error) {
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		return "", errors.New("api channel isolation parent directory cannot be created")
	}
	dir, err := os.MkdirTemp(attemptDir, "claude-config-")
	if err != nil {
		return "", errors.New("api channel isolation directory cannot be created")
	}
	return dir, nil
}

func cleanupClaudeConfig(db *store.Store, attemptID, dir string) {
	err := os.RemoveAll(dir)
	appendEvent(context.Background(), db, attemptID, "api_channel_cleanup", map[string]any{
		"removed": err == nil,
	})
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

func finish(ctx context.Context, db *store.Store, attempt *model.Attempt, card model.Card, output []byte, code int, r runner.Runner, prior, driveErr error, timedOut bool, goalStatus string) error {
	commitCtx, commitCancel := persistContext(ctx)
	defer commitCancel()
	attempt.Exit = &code
	attempt.EndedAt = time.Now().UTC()
	attempt.Usage = r.ParseUsage(output)
	attempt.ErrorClass = r.Classify(code, output)
	attempt.ErrorExcerpt = excerpt(output, attempt.ErrorClass)
	// A goal terminal already observed is host cleanup when the reaper follows
	// up. That signal must not be stored as the hard-limit timeout.
	if timedOut && goalStatus == "" {
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
	switch goalStatus {
	case "budget_limited":
		timedOut = false
		attempt.ErrorClass = model.ErrorBudgetLimited
	case "complete":
		timedOut = false
		if attempt.ErrorClass == model.ErrorTimeout {
			attempt.ErrorClass = ""
		}
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
		"exit":          code,
		"tokens":        attempt.Usage.Tokens,
		"source":        attempt.Usage.Source,
		"usage_receipt": runner.DescribeUsage(r, output, attempt.Usage),
		"error_class":   attempt.ErrorClass,
	}
	if timedOut {
		finishEvent["stop"] = "hard_limit_timeout"
	}
	if goalStatus != "" {
		finishEvent["goal_status"] = goalStatus
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

// goalTerminalStatus names the goal terminal Drive observed, or "" when Drive
// ended without one. Blocked and paused are terminals for an unattended run:
// nobody is there to resume the goal, so the TUI is closed like on completion.
func goalTerminalStatus(driveErr error) string {
	switch {
	case driveErr == nil:
		return "complete"
	case errors.Is(driveErr, runner.ErrGoalBudgetLimited):
		return "budget_limited"
	case errors.Is(driveErr, runner.ErrGoalBlocked):
		return "blocked"
	case errors.Is(driveErr, runner.ErrGoalPaused):
		return "paused"
	}
	return ""
}

func requiresGoal(card model.Card) bool {
	for _, item := range card.Requires {
		if item == "goal_mode" {
			return true
		}
	}
	return false
}

// noteGoalExit records the WaitStatus fact and calls ConsumeGrokProcessExit.
// A crash replaces driveErr. A PTY line is not this fact.
// keepGoal means a goal terminal was already observed. The host reaper's
// follow-up signal is cleanup, so it does not replace that terminal with a crash.
// Grok keeps its grok_process_exit event; other goal runners record
// goal_process_exit with the runner name.
func noteGoalExit(ctx context.Context, db *store.Store, attemptID string, r runner.Runner, waitErr, driveErr error, keepGoal bool) error {
	exit := runner.GrokProcessExit{}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			exit.Signaled = true
			exit.Signal = int(ws.Signal())
		}
	}
	crash, crashed := runner.ConsumeGrokProcessExit(exit)
	if keepGoal {
		crashed = false
		crash = nil
	}
	var facts runner.GoalFacts
	if rep, ok := r.(runner.GoalReporter); ok {
		facts = rep.GoalFacts()
	}
	event := "goal_process_exit"
	if r.Name() == string(model.RunnerGrok) {
		event = "grok_process_exit"
	}
	appendEvent(ctx, db, attemptID, event, map[string]any{
		"runner":      r.Name(),
		"signaled":    exit.Signaled,
		"signal":      exit.Signal,
		"crash":       crashed,
		"goal_mode":   true,
		"prompt":      facts.Prompt,
		"session_dir": facts.SessionDir,
	})
	if crashed {
		return crash
	}
	return driveErr
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

// cancelPoll is how often a running host rereads its card's status.
var cancelPoll = 3 * time.Second

func watchCardCanceled(ctx context.Context, db *store.Store, cardID, attemptID string, stop context.CancelFunc) {
	t := time.NewTicker(cancelPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, err := db.GetCard(ctx, cardID)
		if err != nil || c.Status != model.CardCanceled {
			continue
		}
		appendEvent(context.Background(), db, attemptID, "card_canceled", map[string]string{"message": "card canceled; executor stopped"})
		stop()
		return
	}
}

// sandboxOff reports whether the card runs outside the Cardex sandbox. An
// explicit card setting wins; an empty one inherits its project's default.
// When the project cannot be read the card stays sandboxed.
func sandboxOff(ctx context.Context, db *store.Store, card model.Card) bool {
	switch card.Sandbox {
	case "off":
		return true
	case "on":
		return false
	}
	obj, err := db.GetObjective(ctx, card.Objective)
	if err != nil {
		return false
	}
	p, err := db.GetProject(ctx, obj.Project)
	if err != nil {
		return false
	}
	return p.Sandbox == "off"
}

// A host refusal records the attempt even when objective state cannot be
// proved. Changing a live card also requires a matching objective snapshot.
func holdCompatibleHostAttempt(ctx context.Context, db *store.Store, card model.Card, attempt model.Attempt) error {
	return db.HoldCompatibleAttempt(ctx, card, attempt, observeCompatibleObjective(ctx, db, card), time.Now())
}

func observeCompatibleObjective(ctx context.Context, db *store.Store, card model.Card) *model.Objective {
	if objective, err := db.GetObjective(ctx, card.Objective); err == nil {
		return &objective
	}
	return nil
}
