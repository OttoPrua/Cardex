package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeKimiSession(t *testing.T, home, cwd, name string, created int64, wire string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "wd_x_"+name, "session_"+name)
	if err := os.MkdirAll(filepath.Join(dir, "agents", "main"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwdJSON, _ := json.Marshal(cwd)
	state := `{"id":"session_` + name + `","cwd":` + string(cwdJSON) + `,"createdAt":` + strconv.FormatInt(created, 10) + `}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "main", "wire.jsonl"), []byte(wire), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const kimiCreate = `{"type":"goal.create","agentId":"main","goalId":"g1","objective":"x","time":1}` + "\n" +
	`{"type":"goal.update","agentId":"main","turnsUsed":1,"time":2}` + "\n"

func TestReadKimiGoalSessionTerminals(t *testing.T) {
	cases := []struct {
		name   string
		wire   string
		ok     bool
		want   error
		reason string
	}{
		{name: "none", wire: `{"type":"turn.prompt"}` + "\n", ok: false},
		{name: "active", wire: kimiCreate, ok: false},
		{name: "complete", wire: kimiCreate + `{"type":"goal.update","agentId":"main","status":"complete","actor":"model","time":3}` + "\n" + `{"type":"goal.clear","time":4}` + "\n", ok: true},
		{name: "spaced", wire: `{"type": "goal.create", "goalId": "g1"}` + "\n" + `{"type": "goal.update", "status": "complete"}` + "\n", ok: true},
		{name: "budget", wire: kimiCreate + `{"type":"goal.update","status":"blocked","reason":"Blocked after goal budget reached: 3 turns"}` + "\n", ok: true, want: ErrGoalBudgetLimited},
		{name: "blocked", wire: kimiCreate + `{"type":"goal.update","status":"blocked","reason":"needs a key"}` + "\n", ok: true, want: ErrGoalBlocked, reason: "needs a key"},
		{name: "paused", wire: kimiCreate + `{"type":"goal.update","status":"paused","reason":"Paused after runtime error: 429"}` + "\n", ok: true, want: ErrGoalPaused, reason: "429"},
		{name: "resumed", wire: kimiCreate + `{"type":"goal.update","status":"paused"}` + "\n" + `{"type":"goal.update","status":"active"}` + "\n", ok: false},
		{name: "new goal resets", wire: kimiCreate + `{"type":"goal.update","status":"complete"}` + "\n" + `{"type":"goal.create","goalId":"g2"}` + "\n", ok: false},
		{name: "torn tail", wire: kimiCreate + `{"type":"goal.update","status":"compl`, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, work := t.TempDir(), t.TempDir()
			writeKimiSession(t, home, work, "a", 10, tc.wire)
			term, ok := ReadKimiGoalSession(home, work)
			if ok != tc.ok {
				t.Fatalf("ok %v want %v (%v)", ok, tc.ok, term)
			}
			if !ok {
				return
			}
			if tc.want == nil && term != nil {
				t.Fatalf("term %v want completion", term)
			}
			if tc.want != nil && !errors.Is(term, tc.want) {
				t.Fatalf("term %v want %v", term, tc.want)
			}
			if tc.reason != "" && !strings.Contains(term.Error(), tc.reason) {
				t.Fatalf("term %v lacks reason %q", term, tc.reason)
			}
			if tc.want == ErrGoalBudgetLimited && (errors.Is(term, ErrGoalBlocked) || !errors.Is(term, ErrGrokBudgetLimited)) {
				t.Fatalf("budget %v must be the shared budget terminal only", term)
			}
		})
	}
}

func TestKimiGoalSessionDirPicksWorktree(t *testing.T) {
	home, work, other := t.TempDir(), t.TempDir(), t.TempDir()
	writeKimiSession(t, home, other, "other", 99, kimiCreate)
	old := writeKimiSession(t, home, work, "old", 5, kimiCreate)
	newer := writeKimiSession(t, home, work, "new", 7, kimiCreate)
	if got := KimiGoalSessionDir(home, work); got != newer {
		t.Fatalf("dir %s want newest %s (old %s)", got, newer, old)
	}
	if got := KimiGoalSessionDir(home, filepath.Join(work, "nope")); got != "" {
		t.Fatalf("unrelated cwd matched %s", got)
	}
	if got := KimiGoalSessionDir("", work); got != "" {
		t.Fatalf("empty home matched %s", got)
	}
}

func TestKimiGoalBuildAndCapabilities(t *testing.T) {
	k := Kimi{}
	if strings.Join(k.Capabilities(), ",") != "goal_mode" || k.NeedsPTY() {
		t.Fatalf("caps %v pty %v", k.Capabilities(), k.NeedsPTY())
	}
	g := k.ForGoal(GoalLaunch{Prompt: "P", WorkDir: "/w", AttemptID: "a1"}).(Kimi)
	if !g.GoalMode || !g.NeedsPTY() || g.Prompt != "P" || g.WorkDir != "/w" {
		t.Fatalf("goal copy %+v", g)
	}
	args, env, err := g.Build(LaunchSpec{Model: "kimi-code/kimi-for-coding", PromptPath: "/does/not/exist"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if joined != "kimi --model kimi-code/kimi-for-coding" {
		t.Fatalf("argv %q", joined)
	}
	for _, bad := range []string{"--prompt", "--yolo", "--auto", "-y", "--output-format"} {
		for _, a := range args {
			if a == bad {
				t.Fatalf("goal argv carries %s", bad)
			}
		}
	}
	if strings.Join(env, ",") != "KIMI_CODE_NO_AUTO_UPDATE=1" {
		t.Fatalf("env %v", env)
	}
	ch := t.TempDir()
	g.ConfigHome = ch
	_, env, err = g.Build(LaunchSpec{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(env, ","), "KIMI_CODE_HOME="+ch) {
		t.Fatalf("env %v", env)
	}
	if raw, _ := os.ReadFile(filepath.Join(ch, "config.toml")); !strings.Contains(string(raw), `default_permission_mode = "auto"`) {
		t.Fatalf("config %s", raw)
	}
}

// fakeKimiTTY plays the TUI side of Drive's PTY.
type fakeKimiTTY struct {
	mu      sync.Mutex
	out     chan []byte
	written strings.Builder
	onWrite func(all string)
}

func (f *fakeKimiTTY) Read(p []byte) (int, error) {
	b, ok := <-f.out
	if !ok {
		return 0, io.EOF
	}
	return copy(p, b), nil
}

func (f *fakeKimiTTY) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.written.Write(p)
	all := f.written.String()
	f.mu.Unlock()
	if f.onWrite != nil {
		f.onWrite(all)
	}
	return len(p), nil
}

func TestKimiDriveTrustThenGoalComplete(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	tty := &fakeKimiTTY{out: make(chan []byte, 8)}
	var once, done sync.Once
	tty.onWrite = func(all string) {
		if all == "\r" {
			once.Do(func() {
				tty.out <- []byte("\x1b[2J ╭─╮\r\n │ > │\r\n Never Ask  K2.8\x1b[0m  context: 0% (0/1M)")
			})
		}
		if strings.HasSuffix(all, "\x1b[201~\r") {
			done.Do(func() {
				writeKimiSession(t, home, work, "s", 1, kimiCreate+`{"type":"goal.update","status":"complete"}`+"\n")
			})
		}
	}
	tty.out <- []byte("\x1b[?2004h  Trust this folder?\r\n  " + work + "\r\n   \x1b[36m❯ Trust and continue\x1b[0m\r\n     Exit")
	k := Kimi{ConfigHome: home}.ForGoal(GoalLaunch{Prompt: "line one\nline two\n", WorkDir: work}).(Kimi)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := k.Drive(ctx, tty); err != nil {
		t.Fatalf("drive %v", err)
	}
	got := tty.written.String()
	want := "\r/goal \x1b[200~line one\nline two\x1b[201~\r"
	if got != want {
		t.Fatalf("written %q want %q", got, want)
	}
	if facts := k.GoalFacts(); facts.SessionDir == "" || facts.Prompt != "line one\nline two\n" {
		t.Fatalf("facts %+v", facts)
	}
}

func TestKimiDriveIgnoresTrustForOtherFolder(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	tty := &fakeKimiTTY{out: make(chan []byte, 8)}
	tty.out <- []byte("Trust this folder?\r\n  /somewhere/else\r\n ❯ Trust and continue")
	close(tty.out)
	k := Kimi{ConfigHome: home}.ForGoal(GoalLaunch{Prompt: "p", WorkDir: work}).(Kimi)
	err := k.Drive(context.Background(), tty)
	if err == nil || tty.written.Len() != 0 {
		t.Fatalf("err %v written %q", err, tty.written.String())
	}
}

func TestKimiDriveBudgetAndEOF(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	writeKimiSession(t, home, work, "s", 1, kimiCreate)
	tty := &fakeKimiTTY{out: make(chan []byte, 8)}
	tty.onWrite = func(all string) {
		if strings.HasSuffix(all, "\r") {
			writeKimiSession(t, home, work, "s", 1, kimiCreate+`{"type":"goal.update","status":"blocked","reason":"Blocked after goal budget reached"}`+"\n")
		}
	}
	tty.out <- []byte("context: 0%")
	k := Kimi{ConfigHome: home}.ForGoal(GoalLaunch{Prompt: "p", WorkDir: work}).(Kimi)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := k.Drive(ctx, tty); !errors.Is(err, ErrGoalBudgetLimited) {
		t.Fatalf("drive %v", err)
	}
	// EOF with no terminal is not completion.
	home2, work2 := t.TempDir(), t.TempDir()
	tty2 := &fakeKimiTTY{out: make(chan []byte, 8)}
	tty2.out <- []byte("context: 0%")
	tty2.onWrite = func(all string) {
		if strings.HasSuffix(all, "\r") {
			defer func() { _ = recover() }()
			close(tty2.out)
		}
	}
	k2 := Kimi{ConfigHome: home2}.ForGoal(GoalLaunch{Prompt: "p", WorkDir: work2}).(Kimi)
	if err := k2.Drive(ctx, tty2); err == nil {
		t.Fatal("EOF without a terminal was completion")
	}
}
