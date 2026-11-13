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

var _ GoalRunner = Cursor{}

// cursorReadyWait bounds the wait for the TUI input box before /goal is typed.
var cursorReadyWait = 45 * time.Second

var cursorGoalObserveEvery = 250 * time.Millisecond

var errCursorNoTerminal = errors.New("cursor goal: process ended without a goal terminal")

func (c Cursor) ForGoal(l GoalLaunch) Runner {
	c.GoalMode, c.Prompt, c.WorkDir = true, l.Prompt, l.WorkDir
	return c
}

func (c Cursor) GoalFacts() GoalFacts {
	return GoalFacts{Prompt: c.Prompt, SessionDir: CursorGoalTranscriptDir(c.dataDir(), c.WorkDir)}
}

func (c Cursor) dataDir() string {
	if c.DataDir != "" {
		return c.DataDir
	}
	if d := strings.TrimSpace(os.Getenv("CURSOR_DATA_DIR")); d != "" {
		return d
	}
	if p := userPath(".cursor"); len(p) > 0 {
		return p[0]
	}
	return ""
}

// buildGoal is the interactive TUI in the same sandbox as print mode. The
// objective is typed by Drive, never passed on argv.
func (c Cursor) buildGoal(spec LaunchSpec) ([]string, []string, error) {
	args := []string{"cursor-agent", "--trust", "--sandbox", "enabled", "--workspace", spec.WorkDir}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if cursorPrivilegeWidening(args) {
		return nil, nil, fmt.Errorf("cursor: privilege-widening flags")
	}
	return args, nil, nil
}

// Drive waits for the input box, pastes "/goal <prompt>", submits it, then
// watches the workspace transcript for the goal's terminal UpdateGoal.
func (c Cursor) Drive(ctx context.Context, rw io.ReadWriter) error {
	if !c.GoalMode {
		return nil
	}
	data, work := c.dataDir(), c.WorkDir
	since := time.Now().Add(-2 * time.Second)
	observe := func() (error, bool) { return ReadCursorGoalTranscript(data, work, since) }
	var screen []byte
	tmp := make([]byte, 32*1024)
	readyBy := time.Now().Add(cursorReadyWait)
	for ready := false; !ready; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err, _, _ := readObservedChunk(ctx, rw, tmp, nil, cursorGoalObserveEvery)
		if n > 0 {
			screen = appendScreen(screen, tmp[:n])
			ready = cursorInputReady(kimiPlain(screen))
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errCursorNoTerminal
			}
			return err
		}
		if time.Now().After(readyBy) {
			ready = true
		}
	}
	prompt := strings.TrimRight(c.Prompt, "\r\n")
	line := "/goal"
	if prompt != "" {
		line = "/goal \x1b[200~" + prompt + "\x1b[201~"
	}
	if _, err := io.WriteString(rw, line); err != nil {
		return err
	}
	// Let the paste land and the slash menu settle before Enter.
	if !sleepCtx(ctx, time.Second) {
		return ctx.Err()
	}
	if _, err := io.WriteString(rw, "\r"); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if term, ok := observe(); ok {
			return term
		}
		_, err, term, termOK := readObservedChunk(ctx, rw, tmp, observe, cursorGoalObserveEvery)
		if termOK {
			return term
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if term, ok := observe(); ok {
		return term
	}
	return errCursorNoTerminal
}

func appendScreen(screen, b []byte) []byte {
	screen = append(screen, b...)
	if len(screen) > 256*1024 {
		screen = screen[len(screen)-128*1024:]
	}
	return screen
}

// readObservedChunk waits for one Read, polling observe while Read is blocked.
func readObservedChunk(ctx context.Context, r io.Reader, buf []byte, observe func() (error, bool), every time.Duration) (int, error, error, bool) {
	type got struct {
		n   int
		err error
	}
	ch := make(chan got, 1)
	go func() {
		n, err := r.Read(buf)
		ch <- got{n, err}
	}()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err(), nil, false
		case res := <-ch:
			return res.n, res.err, nil, false
		case <-tick.C:
			if observe == nil {
				continue
			}
			if term, ok := observe(); ok {
				return 0, nil, term, true
			}
		}
	}
}

// cursorInputReady is the idle composer of a fresh TUI.
func cursorInputReady(text string) bool {
	return strings.Contains(text, "Plan, search, build anything") || strings.Contains(text, "Add a follow-up")
}

var cursorSlugRun = regexp.MustCompile(`[^A-Za-z0-9]+`)

// CursorProjectSlug is the folder name cursor-agent uses for a workspace
// under <data>/projects: every run of non-alphanumerics becomes one dash.
func CursorProjectSlug(work string) string {
	return strings.Trim(cursorSlugRun.ReplaceAllString(filepath.Clean(work), "-"), "-")
}

// CursorGoalTranscriptDir is the agent-transcripts folder for work.
func CursorGoalTranscriptDir(data, work string) string {
	if strings.TrimSpace(data) == "" || strings.TrimSpace(work) == "" {
		return ""
	}
	return filepath.Join(data, "projects", CursorProjectSlug(work), "agent-transcripts")
}

// ReadCursorGoalTranscript reads the newest transcript for work written
// after since and reports a terminal UpdateGoal. Cursor goals have no budget;
// complete is the only status the goal skill sets, others are mapped anyway.
func ReadCursorGoalTranscript(data, work string, since time.Time) (error, bool) {
	dir := CursorGoalTranscriptDir(data, work)
	if dir == "" {
		return nil, false
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*.jsonl"))
	best, bestAt := "", time.Time{}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil || st.ModTime().Before(since) {
			continue
		}
		if best == "" || st.ModTime().After(bestAt) {
			best, bestAt = f, st.ModTime()
		}
	}
	if best == "" {
		return nil, false
	}
	raw, err := os.ReadFile(best)
	if err != nil {
		return nil, false
	}
	return cursorGoalTerminal(raw)
}

func cursorGoalTerminal(raw []byte) (error, bool) {
	status, created := "", false
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte("Goal")) {
			continue
		}
		var rec struct {
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue // a torn tail line
		}
		for _, c := range rec.Message.Content {
			if c.Type != "tool_use" {
				continue
			}
			name, args := c.Name, c.Input
			if name == "CallDynamicTool" {
				var call struct {
					ToolName  string          `json:"toolName"`
					Arguments json.RawMessage `json:"arguments"`
				}
				if json.Unmarshal(c.Input, &call) != nil {
					continue
				}
				name, args = call.ToolName, call.Arguments
			}
			switch name {
			case "CreateGoal":
				created, status = true, "active"
			case "UpdateGoal":
				var u struct {
					Status string `json:"status"`
				}
				if json.Unmarshal(args, &u) == nil && u.Status != "" {
					status = u.Status
				}
			}
		}
	}
	if !created {
		return nil, false
	}
	switch status {
	case "complete":
		return nil, true
	case "blocked":
		return fmt.Errorf("cursor %w", ErrGoalBlocked), true
	case "paused":
		return fmt.Errorf("cursor %w", ErrGoalPaused), true
	}
	return nil, false
}
