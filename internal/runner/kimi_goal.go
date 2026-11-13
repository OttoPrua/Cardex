package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Kimi goal mode drives the interactive kimi-code TUI over a PTY.
//
// kimi-code 2.5+ has a native /goal command. It records the goal in the
// session's agents/main/wire.jsonl as goal.create, then goal.update records;
// the update that carries status is the terminal (complete, blocked, paused).
// A budget stop is blocked with a reason that starts "Blocked after goal budget
// reached". The session directory is KIMI_CODE_HOME/sessions/wd_<name>_<hash>/
// session_<uuid>, and its state.json names the cwd, so Drive finds the
// session by the attempt worktree (each attempt has its own worktree).
//
// A worktree kimi has not seen opens a "Trust this folder?" dialog first.
// Drive answers it with the default "Trust and continue" only for the folder
// it launched in; tool approvals still follow the configured permission mode.

var _ GoalRunner = Kimi{}

// kimiGoalObserveEvery is the session poll interval while Read is blocked.
const kimiGoalObserveEvery = 250 * time.Millisecond

// kimiReadyWait bounds how long Drive waits for the input box before it
// writes the goal anyway.
var kimiReadyWait = 60 * time.Second

var (
	errKimiNoTerminal = errors.New("kimi goal: no terminal")
	kimiANSI          = regexp.MustCompile("\x1b\\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-_]")
)

const kimiBudgetBlockPrefix = "Blocked after goal budget"

// ForGoal turns on the PTY /goal driver for one launch.
func (k Kimi) ForGoal(l GoalLaunch) Runner {
	k.GoalMode = true
	k.Prompt = l.Prompt
	k.WorkDir = l.WorkDir
	return k
}

// GoalFacts is the goal text and the session the launch wrote, if found.
func (k Kimi) GoalFacts() GoalFacts {
	return GoalFacts{Prompt: k.Prompt, SessionDir: KimiGoalSessionDir(k.home(), k.WorkDir)}
}

// home is the KIMI_CODE_HOME the CLI uses: ConfigHome, the inherited env, or
// ~/.kimi-code.
func (k Kimi) home() string {
	if h := strings.TrimSpace(k.ConfigHome); h != "" {
		return h
	}
	if h := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME")); h != "" {
		return h
	}
	if u, err := os.UserHomeDir(); err == nil {
		return filepath.Join(u, ".kimi-code")
	}
	return ""
}

func (k Kimi) buildGoal(spec LaunchSpec) ([]string, []string, error) {
	env := []string{"KIMI_CODE_NO_AUTO_UPDATE=1"}
	if strings.TrimSpace(k.ConfigHome) != "" {
		if err := writeKimiAutoConfig(k.ConfigHome); err != nil {
			return nil, nil, err
		}
		env = append(env, "KIMI_CODE_HOME="+k.ConfigHome)
	}
	args := []string{"kimi"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	return args, env, nil
}

// Drive answers the trust dialog for the launch folder, waits for the input
// box, pastes "/goal <prompt>" and submits it, then watches the session.
func (k Kimi) Drive(ctx context.Context, rw io.ReadWriter) error {
	if !k.GoalMode {
		return nil
	}
	home, work := k.home(), k.WorkDir
	var screen []byte
	tmp := make([]byte, 32*1024)
	trusted, ready := false, false
	readyBy := time.Now().Add(kimiReadyWait)
	for !ready {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err, _, _ := readKimiChunk(ctx, rw, tmp, "", "")
		if n > 0 {
			screen = append(screen, tmp[:n]...)
			if len(screen) > 256*1024 {
				screen = screen[len(screen)-128*1024:]
			}
			text := kimiPlain(screen)
			switch {
			case !trusted && kimiTrustDialog(text, work):
				if _, werr := io.WriteString(rw, "\r"); werr != nil {
					return werr
				}
				trusted = true
				screen = screen[:0]
				continue
			case kimiInputReady(text):
				ready = true
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errKimiNoTerminal
			}
			return err
		}
		if time.Now().After(readyBy) {
			ready = true
		}
	}
	prompt := strings.TrimRight(k.Prompt, "\r\n")
	line := "/goal"
	if prompt != "" {
		// Bracketed paste keeps a multi-line prompt in one submission.
		line = "/goal \x1b[200~" + prompt + "\x1b[201~"
	}
	if _, err := io.WriteString(rw, line); err != nil {
		return err
	}
	// The TUI applies the paste before Enter submits it.
	if !sleepCtx(ctx, 400*time.Millisecond) {
		return ctx.Err()
	}
	if _, err := io.WriteString(rw, "\r"); err != nil {
		return err
	}
	screen = screen[:0]
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if term, ok := ReadKimiGoalSession(home, work); ok {
			return term
		}
		n, err, term, termOK := readKimiChunk(ctx, rw, tmp, home, work)
		if termOK {
			return term
		}
		if n > 0 {
			screen = append(screen, tmp[:n]...)
			if len(screen) > 256*1024 {
				screen = screen[len(screen)-128*1024:]
			}
			if msg, failed := kimiStartFailure(kimiPlain(screen)); failed {
				return fmt.Errorf("kimi %w: %s", ErrGoalStartFailed, msg)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if term, ok := ReadKimiGoalSession(home, work); ok {
		return term
	}
	return errKimiNoTerminal
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// readKimiChunk waits for one Read. With a home it polls the session while
// Read is blocked and returns termOK on a terminal.
func readKimiChunk(ctx context.Context, r io.Reader, buf []byte, home, work string) (int, error, error, bool) {
	type got struct {
		n   int
		err error
	}
	ch := make(chan got, 1)
	go func() {
		n, err := r.Read(buf)
		ch <- got{n, err}
	}()
	tick := time.NewTicker(kimiGoalObserveEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err(), nil, false
		case res := <-ch:
			return res.n, res.err, nil, false
		case <-tick.C:
			if home == "" {
				continue
			}
			if term, ok := ReadKimiGoalSession(home, work); ok {
				return 0, nil, term, true
			}
		}
	}
}

func kimiPlain(b []byte) string {
	return kimiANSI.ReplaceAllString(string(b), "")
}

// kimiTrustDialog is the first-run folder trust prompt for the launch folder.
// The folder line can be cut by the terminal width, so the base name is what
// must be on screen.
func kimiTrustDialog(text, work string) bool {
	if !strings.Contains(text, "Trust this folder?") || !strings.Contains(text, "Trust and continue") {
		return false
	}
	base := filepath.Base(filepath.Clean(work))
	return work != "" && base != "." && strings.Contains(text, base)
}

// kimiInputReady is the idle input box with its status line.
func kimiInputReady(text string) bool {
	return strings.Contains(text, "context:") && !strings.Contains(text[max(0, strings.LastIndex(text, "context:")-2048):], "Trust this folder?")
}

// KimiGoalSessionDir is the newest session directory under home whose
// state.json cwd is work. Empty when there is none.
func KimiGoalSessionDir(home, work string) string {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(work) == "" {
		return ""
	}
	want := kimiCleanPath(work)
	states, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "session_*", "state.json"))
	best, bestAt := "", int64(-1)
	for _, p := range states {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var st struct {
			Cwd       string `json:"cwd"`
			CreatedAt int64  `json:"createdAt"`
		}
		if json.Unmarshal(raw, &st) != nil || kimiCleanPath(st.Cwd) != want {
			continue
		}
		if st.CreatedAt > bestAt {
			best, bestAt = filepath.Dir(p), st.CreatedAt
		}
	}
	return best
}

func kimiCleanPath(p string) string {
	p = filepath.Clean(strings.TrimSpace(p))
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

// ReadKimiGoalSession reads the goal terminal of the session for work.
// complete is a nil error; a budget block is ErrGoalBudgetLimited; any other
// block is ErrGoalBlocked and a pause is ErrGoalPaused, both with the reason.
// ok is false while the goal is active or there is no goal yet.
func ReadKimiGoalSession(home, work string) (error, bool) {
	dir := KimiGoalSessionDir(home, work)
	if dir == "" {
		return nil, false
	}
	return readKimiWire(filepath.Join(dir, "agents", "main", "wire.jsonl"))
}

func readKimiWire(path string) (error, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	type rec struct {
		Type   string  `json:"type"`
		GoalID string  `json:"goalId"`
		Status *string `json:"status"`
		Reason string  `json:"reason"`
	}
	goal, status, reason := "", "", ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if !bytes.HasPrefix(line, []byte(`{"type":"goal.`)) && !bytes.HasPrefix(line, []byte(`{"type": "goal.`)) {
			continue
		}
		var r rec
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		switch r.Type {
		case "goal.create":
			goal, status, reason = r.GoalID, "active", ""
		case "goal.update":
			if goal != "" && r.Status != nil {
				status, reason = strings.ToLower(strings.TrimSpace(*r.Status)), r.Reason
			}
		}
	}
	if goal == "" {
		return nil, false
	}
	switch status {
	case "complete":
		return nil, true
	case "blocked":
		if strings.HasPrefix(reason, kimiBudgetBlockPrefix) {
			return fmt.Errorf("kimi %w: %s", ErrGoalBudgetLimited, reason), true
		}
		return kimiReasonErr(ErrGoalBlocked, reason), true
	case "paused":
		return kimiReasonErr(ErrGoalPaused, reason), true
	}
	return nil, false
}

func kimiReasonErr(base error, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("kimi %w", base)
	}
	return fmt.Errorf("kimi %w: %s", base, reason)
}

// kimiStartFailure finds the TUI's "Failed to start a session" error, shown
// when the first message cannot open a session (unknown model, logged out).
func kimiStartFailure(text string) (string, bool) {
	i := strings.Index(text, "Failed to start a session")
	if i < 0 {
		return "", false
	}
	msg := text[i:]
	if j := strings.Index(msg, "\n\n"); j >= 0 {
		msg = msg[:j]
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg, true
}
