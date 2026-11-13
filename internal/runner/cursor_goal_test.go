package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const cursorCreate = `{"role":"user","message":{"content":[{"type":"text","text":"/goal x"}]}}
{"role":"assistant","message":{"content":[{"type":"tool_use","name":"CallDynamicTool","input":{"namespace":"cursor","toolName":"CreateGoal","arguments":{"objective":"x"}}}]}}
`

const cursorComplete = `{"role":"assistant","message":{"content":[{"type":"tool_use","name":"CallDynamicTool","input":{"namespace":"cursor","toolName":"UpdateGoal","arguments":{"status":"complete"}}}]}}
`

func writeCursorTranscript(t *testing.T, data, work, id, body string) string {
	t.Helper()
	dir := filepath.Join(CursorGoalTranscriptDir(data, work), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCursorProjectSlug(t *testing.T) {
	for in, want := range map[string]string{
		"/Volumes/Storage/Development/Cardex/work/goal-probe/cursor/ws2":      "Volumes-Storage-Development-Cardex-work-goal-probe-cursor-ws2",
		"/Volumes/Storage/Development/Cardex/work/goal-probe/cursor/ws_a.b Z": "Volumes-Storage-Development-Cardex-work-goal-probe-cursor-ws-a-b-Z",
	} {
		if got := CursorProjectSlug(in); got != want {
			t.Fatalf("%s: %s", in, got)
		}
	}
}

func TestCursorGoalTerminal(t *testing.T) {
	cases := []struct {
		name, body string
		ok         bool
		want       error
	}{
		{"none", `{"role":"user","message":{"content":[]}}` + "\n", false, nil},
		{"active", cursorCreate, false, nil},
		{"direct tool", `{"role":"assistant","message":{"content":[{"type":"tool_use","name":"CreateGoal","input":{"objective":"x"}}]}}` + "\n" + `{"role":"assistant","message":{"content":[{"type":"tool_use","name":"UpdateGoal","input":{"status":"complete"}}]}}` + "\n", true, nil},
		{"complete", cursorCreate + cursorComplete, true, nil},
		{"complete before create", cursorComplete, false, nil},
		{"new goal resets", cursorCreate + cursorComplete + cursorCreate, false, nil},
		{"paused", cursorCreate + strings.Replace(cursorComplete, "complete", "paused", 1), true, ErrGoalPaused},
		{"torn tail", cursorCreate + cursorComplete[:40], false, nil},
	}
	for _, c := range cases {
		term, ok := cursorGoalTerminal([]byte(c.body))
		if ok != c.ok || (c.want == nil && term != nil) || (c.want != nil && !errors.Is(term, c.want)) {
			t.Fatalf("%s: %v %v", c.name, term, ok)
		}
	}
}

func TestCursorGoalTranscriptNewest(t *testing.T) {
	data, work := t.TempDir(), t.TempDir()
	old := writeCursorTranscript(t, data, work, "old", cursorCreate+cursorComplete)
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(old, past, past)
	since := time.Now().Add(-time.Minute)
	if _, ok := ReadCursorGoalTranscript(data, work, since); ok {
		t.Fatal("a transcript from before launch counted")
	}
	writeCursorTranscript(t, data, work, "new", cursorCreate)
	if _, ok := ReadCursorGoalTranscript(data, work, since); ok {
		t.Fatal("active goal counted")
	}
	writeCursorTranscript(t, data, work, "new", cursorCreate+cursorComplete)
	if term, ok := ReadCursorGoalTranscript(data, work, since); !ok || term != nil {
		t.Fatalf("complete %v %v", term, ok)
	}
}

func TestCursorGoalBuild(t *testing.T) {
	c := Cursor{}.ForGoal(GoalLaunch{Prompt: "p", WorkDir: "/w"}).(Cursor)
	if !c.NeedsPTY() {
		t.Fatal("goal mode needs a PTY")
	}
	args, _, err := c.Build(LaunchSpec{Model: "grok-4.7-low", WorkDir: "/w", PromptPath: "/nonexistent"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	if got != "cursor-agent --trust --sandbox enabled --workspace /w --model grok-4.7-low" {
		t.Fatalf("argv %s", got)
	}
}

func TestCursorDriveGoalComplete(t *testing.T) {
	data, work := t.TempDir(), t.TempDir()
	tty := &fakeKimiTTY{out: make(chan []byte, 8)}
	tty.onWrite = func(all string) {
		if strings.HasSuffix(all, "\r") {
			writeCursorTranscript(t, data, work, "s", cursorCreate+cursorComplete)
		}
	}
	tty.out <- []byte("\x1b[2m → Plan, search, build anything\x1b[0m")
	c := Cursor{DataDir: data}.ForGoal(GoalLaunch{Prompt: "line one\nline two\n", WorkDir: work}).(Cursor)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Drive(ctx, tty); err != nil {
		t.Fatalf("drive %v", err)
	}
	if got := tty.text(); got != "/goal \x1b[200~line one\nline two\x1b[201~\r" {
		t.Fatalf("wrote %q", got)
	}
	if f := c.GoalFacts(); f.SessionDir != CursorGoalTranscriptDir(data, work) || f.Prompt == "" {
		t.Fatalf("facts %+v", f)
	}
}

func TestCursorDriveEOFIsNotCompletion(t *testing.T) {
	data, work := t.TempDir(), t.TempDir()
	tty := &fakeKimiTTY{out: make(chan []byte, 8)}
	tty.out <- []byte("Add a follow-up")
	tty.onWrite = func(all string) {
		if strings.HasSuffix(all, "\r") {
			defer func() { _ = recover() }()
			close(tty.out)
		}
	}
	c := Cursor{DataDir: data}.ForGoal(GoalLaunch{Prompt: "p", WorkDir: work}).(Cursor)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Drive(ctx, tty); !errors.Is(err, errCursorNoTerminal) {
		t.Fatalf("drive %v", err)
	}
}

func (f *fakeKimiTTY) text() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.String()
}
