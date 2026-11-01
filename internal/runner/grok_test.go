package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/runner"
)

const snapshotPrompt = "ship the card"

var (
	_ runner.Runner = runner.Grok{}
	_ runner.Runner = runner.Cursor{}
	_ runner.Runner = runner.Kimi{}
	_ runner.Runner = runner.AGY{}
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writePromptFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertNoArg(t *testing.T, args []string, banned ...string) {
	t.Helper()
	for _, flag := range banned {
		if slices.Contains(args, flag) {
			t.Fatalf("argv contains %s: %q", flag, args)
		}
	}
}

// scriptRW records writes and replays a fixture. It is not a PTY.
type scriptRW struct {
	wrote bytes.Buffer
	src   *bytes.Reader
}

func (s *scriptRW) Write(p []byte) (int, error) { return s.wrote.Write(p) }
func (s *scriptRW) Read(p []byte) (int, error)  { return s.src.Read(p) }

func driveGrok(t *testing.T, prompt, fixture string) (error, []byte) {
	t.Helper()
	rw := &scriptRW{src: bytes.NewReader(readFixture(t, fixture))}
	g := runner.Grok{GoalMode: true, Prompt: prompt}
	err := g.Drive(context.Background(), rw)
	return err, rw.wrote.Bytes()
}

func TestGrokBuildSnapshot(t *testing.T) {
	spec := runner.LaunchSpec{
		Model:      "grok-4.6",
		PromptPath: "/work/prompt.md",
		WorkDir:    "/work/tree",
	}
	cmd, env, err := (runner.Grok{}).Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"grok",
		"--no-auto-update",
		"--model", "grok-4.6",
		"--output-format", "streaming-json",
		"--sandbox", "workspace",
		"--permission-mode", "auto",
		"--no-memory", "--no-subagents", "--disable-web-search", "--verbatim",
		"--no-plan",
		"--prompt-file", "/work/prompt.md",
	}
	if !slices.Equal(cmd, want) {
		t.Fatalf("argv\n got %q\nwant %q", cmd, want)
	}
	if env != nil {
		t.Fatalf("env %q", env)
	}
	assertNoArg(t, cmd,
		"--always-approve", "--yolo", "--skip-permissions", "--dangerously-skip-permissions",
		"--reasoning-effort", "--resume", "--cwd",
		"dontAsk", "bypassPermissions", "always-approve",
	)
	if (runner.Grok{}).NeedsPTY() {
		t.Fatal("ordinary grok NeedsPTY")
	}
	if !slices.Equal((runner.Grok{}).Capabilities(), []string{"goal_mode"}) {
		t.Fatalf("capabilities %q", (runner.Grok{}).Capabilities())
	}
	if slices.Contains((runner.Grok{}).Capabilities(), "web") {
		t.Fatal("web capability")
	}
	if (runner.Grok{}).Name() != "grok" {
		t.Fatalf("name %s", (runner.Grok{}).Name())
	}
	// Empty model and prompt still build. LaunchSpec has no hard gate for them.
	if _, _, err := (runner.Grok{}).Build(runner.LaunchSpec{}); err != nil {
		t.Fatal(err)
	}
}

func TestGrokGoalBuildAndNeedsPTY(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	g := runner.Grok{GoalMode: true, Prompt: "finish the card\n"}
	if !g.NeedsPTY() {
		t.Fatal("goal grok NeedsPTY")
	}
	if !slices.Equal(g.Capabilities(), []string{"goal_mode"}) {
		t.Fatalf("capabilities %q", g.Capabilities())
	}
	cmd, env, err := g.Build(runner.LaunchSpec{Model: "grok-4.6", WorkDir: "/work/tree", PromptPath: "/work/prompt.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"grok",
		"--no-auto-update",
		"--model", "grok-4.6",
		"--sandbox", "workspace",
		"--permission-mode", "auto",
		"--no-memory", "--no-subagents", "--disable-web-search",
		"--no-plan",
		"--cwd", "/work/tree",
	}
	if !slices.Equal(cmd, want) {
		t.Fatalf("argv\n got %q\nwant %q", cmd, want)
	}
	assertNoArg(t, cmd,
		"--always-approve", "--yolo", "--skip-permissions", "--dangerously-skip-permissions",
		"--output-format", "--prompt-file", "--verbatim", "--reasoning-effort", "--resume", "-s",
		"dontAsk", "bypassPermissions", "always-approve",
	)
	wantEnv := []string{
		"GROK_HOME=" + filepath.Join(home, ".grok"),
		"GROK_WORKFLOWS=1",
		"GROK_DISABLE_AUTOUPDATER=1",
	}
	if !slices.Equal(env, wantEnv) {
		t.Fatalf("env\n got %q\nwant %q", env, wantEnv)
	}
}

func TestGrokWritablePathsAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := (runner.Grok{}).WritablePaths()
	want := filepath.Join(home, ".grok")
	if !slices.Equal(got, []string{want}) {
		t.Fatalf("paths %q", got)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", want, err)
	}
}

func TestGrokClassifyAndUsage(t *testing.T) {
	g := runner.Grok{}
	cases := []struct {
		file string
		want model.ErrorClass
	}{
		{"grok_goal_budget_limited.jsonl", model.ErrorBudgetLimited},
		{"grok_goal_quota.jsonl", model.ErrorQuotaHit},
		{"grok_auth.txt", model.ErrorAuthFailed},
		{"grok_permission.txt", model.ErrorPermissionDenied},
		{"grok_transport.txt", model.ErrorTransport},
		{"grok_timeout.txt", model.ErrorTimeout},
		{"grok_refusal.txt", model.ErrorRefusal},
		{"grok_goal_crash.txt", ""},
		{"grok_goal_complete.jsonl", ""},
		{"grok_goal_not_achieved.jsonl", ""},
		{"grok_limit_like.txt", ""},
		{"input_too_long.txt", ""},
	}
	for _, tc := range cases {
		got := g.Classify(0, readFixture(t, tc.file))
		if got != tc.want {
			t.Errorf("%s: class %q want %q", tc.file, got, tc.want)
		}
	}
	if g.Classify(0, readFixture(t, "grok_goal_budget_limited.jsonl")) == g.Classify(0, readFixture(t, "grok_goal_quota.jsonl")) {
		t.Fatal("budget_limited and quota_hit collapsed")
	}
	usage := g.ParseUsage(readFixture(t, "grok_usage_end.json"))
	if usage.Tokens != 19 || usage.Source != "grok_build_end" || usage.Confidence != 1 {
		t.Fatalf("usage %+v", usage)
	}
	if u := g.ParseUsage(readFixture(t, "grok_limit_like.txt")); u != (model.Usage{}) {
		t.Fatalf("soft usage %+v", u)
	}
}

func TestGrokDriveTerminals(t *testing.T) {
	const prompt = "finish the card\n"
	const written = "/goal finish the card\r"
	// Completion and budget_limited are v1 session files (workflow_goal.go
	// observeNativeGrokGoal), not PTY bytes. The headless quota JSON and the
	// classifier string "signal: killed" are not the interactive channel.
	completeDir := installSessionFixture(t, "grok_goal_complete.jsonl")
	budgetDir := installSessionFixture(t, "grok_goal_budget_limited.jsonl")
	notDir := installSessionFixture(t, "grok_goal_not_achieved.jsonl")
	complete, w1 := driveSession(t, prompt, completeDir, nil)
	budget, w2 := driveSession(t, prompt, budgetDir, nil)
	headless, w3 := driveGrok(t, prompt, "grok_goal_quota.jsonl")
	killed, w4 := driveGrok(t, prompt, "grok_goal_crash.txt")
	for _, w := range [][]byte{w1, w2, w3, w4} {
		if string(w) != written {
			t.Fatalf("wrote %q", w)
		}
	}
	if complete != nil {
		t.Fatalf("completion %v", complete)
	}
	if !errors.Is(budget, runner.ErrGrokBudgetLimited) {
		t.Fatalf("budget %v", budget)
	}
	if errors.Is(headless, runner.ErrGrokQuotaHit) || errors.Is(headless, runner.ErrGrokBudgetLimited) {
		t.Fatalf("headless quota JSON on the PTY is not the interactive quota channel: %v", headless)
	}
	if errors.Is(killed, runner.ErrGrokCrash) || errors.Is(killed, runner.ErrGrokBudgetLimited) || errors.Is(killed, runner.ErrGrokQuotaHit) {
		t.Fatalf("signal: killed on the PTY is not a crash: %v", killed)
	}
	if errors.Is(budget, runner.ErrGrokQuotaHit) || errors.Is(budget, runner.ErrGrokCrash) {
		t.Fatal("terminals collapsed")
	}
	g := runner.Grok{GoalMode: true, Prompt: prompt}
	if g.Classify(0, readFixture(t, "grok_goal_budget_limited.jsonl")) != model.ErrorBudgetLimited {
		t.Fatal("classify budget")
	}
	if g.Classify(0, readFixture(t, "grok_goal_quota.jsonl")) != model.ErrorQuotaHit {
		t.Fatal("classify quota")
	}
	if g.Classify(0, readFixture(t, "grok_goal_crash.txt")) != "" {
		t.Fatal("classify crash")
	}

	if term, ok := runner.ReadGrokGoalSession(completeDir); !ok || term != nil {
		t.Fatalf("reader complete ok=%v err=%v", ok, term)
	}
	if term, ok := runner.ReadGrokGoalSession(budgetDir); !ok || !errors.Is(term, runner.ErrGrokBudgetLimited) {
		t.Fatalf("reader budget ok=%v err=%v", ok, term)
	}
	if _, ok := runner.ReadGrokGoalSession(notDir); ok {
		t.Fatal("reader not_achieved")
	}

	notAchieved, wn := driveSession(t, prompt, notDir, nil)
	if string(wn) != written {
		t.Fatalf("wrote %q", wn)
	}
	if notAchieved == nil || errors.Is(notAchieved, runner.ErrGrokBudgetLimited) || errors.Is(notAchieved, runner.ErrGrokQuotaHit) || errors.Is(notAchieved, runner.ErrGrokCrash) {
		t.Fatalf("not_achieved %v", notAchieved)
	}
	if g.Classify(0, readFixture(t, "grok_goal_not_achieved.jsonl")) != "" {
		t.Fatal("classify not_achieved")
	}

	for _, name := range []string{"grok_goal_complete.jsonl", "grok_goal_budget_limited.jsonl"} {
		err, wrote := driveGrok(t, prompt, name)
		if string(wrote) != written {
			t.Fatalf("pty %s wrote %q", name, wrote)
		}
		if err == nil || errors.Is(err, runner.ErrGrokBudgetLimited) {
			t.Fatalf("session json on pty %s counted as completion or budget_limited: %v", name, err)
		}
	}

	empty := &scriptRW{src: bytes.NewReader(nil)}
	if err := g.Drive(context.Background(), empty); err == nil || errors.Is(err, runner.ErrGrokBudgetLimited) || errors.Is(err, runner.ErrGrokQuotaHit) || errors.Is(err, runner.ErrGrokCrash) {
		t.Fatalf("empty %v", err)
	}
	if string(empty.wrote.Bytes()) != written {
		t.Fatalf("empty wrote %q", empty.wrote.Bytes())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := &scriptRW{src: bytes.NewReader(readFixture(t, "grok_goal_complete.jsonl"))}
	if err := g.Drive(ctx, canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled %v", err)
	}
	if canceled.wrote.Len() != 0 {
		t.Fatalf("canceled wrote %q", canceled.wrote.Bytes())
	}

	ordinary := &scriptRW{src: bytes.NewReader(readFixture(t, "grok_goal_crash.txt"))}
	if err := (runner.Grok{}).Drive(context.Background(), ordinary); err != nil || ordinary.wrote.Len() != 0 {
		t.Fatalf("ordinary drive %v wrote %q", err, ordinary.wrote.Bytes())
	}

	note, err := os.ReadFile(filepath.Join("..", "..", "docs", "v2", "lane-notes", "R2-B.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sentence := range []string{"Grok.SessionDir", "updates.jsonl", "不读取 ~/.kimi-code"} {
		if !strings.Contains(string(note), sentence) {
			t.Fatalf("\u63a5\u53e3\u8bf7\u6c42 missing %s", sentence)
		}
	}
}

func TestAdapterSourcesDoNotExec(t *testing.T) {
	for _, name := range []string{"grok.go", "cursor.go", "kimi.go", "agy.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "os/exec" {
				t.Fatalf("%s imports os/exec", name)
			}
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "exec.Command") {
			t.Fatalf("%s calls exec.Command", name)
		}
	}
}

func driveSession(t *testing.T, prompt, dir string, pty []byte) (error, []byte) {
	t.Helper()
	rw := &scriptRW{src: bytes.NewReader(pty)}
	g := runner.Grok{GoalMode: true, Prompt: prompt, SessionDir: dir}
	err := g.Drive(context.Background(), rw)
	return err, rw.wrote.Bytes()
}

func installSessionFixture(t *testing.T, fixture string) string {
	t.Helper()
	dir := t.TempDir()
	writeSessionFixture(t, dir, fixture)
	return dir
}

func writeSessionFixture(t *testing.T, dir, fixture string) {
	t.Helper()
	raw := readFixture(t, fixture)
	var rec struct {
		Params struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				GoalID                string `json:"goal_id"`
				Status                string `json:"status"`
				LastClassifierVerdict string `json:"last_classifier_verdict"`
			} `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &rec); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "goal"), 0o755); err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(map[string]string{
		"goal_id":                 rec.Params.Update.GoalID,
		"status":                  rec.Params.Update.Status,
		"phase":                   "idle",
		"last_classifier_verdict": rec.Params.Update.LastClassifierVerdict,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "goal", "state.json"), append(state, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := json.Marshal(map[string]any{
		"info": map[string]string{"id": rec.Params.SessionID, "cwd": "/work/tree"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), append(summary, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	updates := raw
	if len(updates) == 0 || updates[len(updates)-1] != '\n' {
		updates = append(updates, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "updates.jsonl"), updates, 0o644); err != nil {
		t.Fatal(err)
	}
}

// holdRW signals when Read is entered, then blocks until release is closed.
// Closing release makes Read return EOF. Leaving it open keeps Read blocked.
type holdRW struct {
	started      chan struct{}
	release      chan struct{}
	readReturned chan struct{}
	once         sync.Once
	wrote        bytes.Buffer
}

func (h *holdRW) Write(p []byte) (int, error) { return h.wrote.Write(p) }

func (h *holdRW) Read([]byte) (int, error) {
	h.once.Do(func() { close(h.started) })
	<-h.release
	close(h.readReturned)
	return 0, io.EOF
}

func TestGrokDriveSessionAfterReadStarts(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		want    error
	}{
		{name: "complete", fixture: "grok_goal_complete.jsonl"},
		{name: "budget_limited", fixture: "grok_goal_budget_limited.jsonl", want: runner.ErrGrokBudgetLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/while_read_blocked", func(t *testing.T) {
			driveSessionAfterRead(t, tc.fixture, tc.want, false)
		})
		t.Run(tc.name+"/recheck_after_eof", func(t *testing.T) {
			driveSessionAfterRead(t, tc.fixture, tc.want, true)
		})
	}
}

func driveSessionAfterRead(t *testing.T, fixture string, want error, eof bool) {
	t.Helper()
	dir := t.TempDir()
	h := &holdRW{
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		readReturned: make(chan struct{}),
	}
	errc := make(chan error, 1)
	go func() {
		errc <- runner.Grok{
			GoalMode:   true,
			Prompt:     "finish the card",
			SessionDir: dir,
		}.Drive(context.Background(), h)
	}()
	select {
	case <-h.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not start")
	}
	writeSessionFixture(t, dir, fixture)
	if eof {
		close(h.release)
	}
	select {
	case err := <-errc:
		if want == nil {
			if err != nil {
				t.Fatalf("Drive err %v", err)
			}
		} else if !errors.Is(err, want) {
			t.Fatalf("Drive err %v want %v", err, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Drive did not observe the session")
	}
	if h.wrote.String() != "/goal finish the card\r" {
		t.Fatalf("wrote %q", h.wrote.String())
	}
	if eof {
		select {
		case <-h.readReturned:
		default:
			t.Fatal("Drive returned before Read EOF")
		}
		return
	}
	select {
	case <-h.readReturned:
		t.Fatal("Read returned; session was not observed while Read was blocked")
	default:
	}
	close(h.release)
	select {
	case <-h.readReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not finish after release")
	}
}

func TestPromptFileBody(t *testing.T) {
	body := "hello world\n第二行 中文\n"
	path := writePromptFile(t, body)
	spec := runner.LaunchSpec{Model: "m", PromptPath: path, WorkDir: t.TempDir()}
	type slot struct {
		name string
		cmd  []string
		flag string
	}
	cursor, _, err := (runner.Cursor{}).Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	kimiHome := t.TempDir()
	kimi, _, err := (runner.Kimi{ConfigHome: kimiHome}).Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	agy, _, err := (runner.AGY{}).Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	got := []slot{
		{"cursor", cursor, ""},
		{"kimi", kimi, "--prompt"},
		{"agy", agy, "--print"},
	}
	for _, tc := range got {
		arg := tc.cmd[len(tc.cmd)-1]
		if tc.flag != "" {
			arg = ""
			for i, a := range tc.cmd {
				if a == tc.flag && i+1 < len(tc.cmd) {
					arg = tc.cmd[i+1]
				}
			}
		}
		if arg != body {
			t.Fatalf("%s argv slot %q want body %q", tc.name, arg, body)
		}
		if arg == path || slices.Contains(tc.cmd, path) {
			t.Fatalf("%s argv contains the prompt path %q", tc.name, path)
		}
	}
	grok, _, err := (runner.Grok{}).Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	fileArg := ""
	for i, a := range grok {
		if a == "--prompt-file" && i+1 < len(grok) {
			fileArg = grok[i+1]
		}
	}
	if fileArg != path {
		t.Fatalf("grok --prompt-file %q want path %q", fileArg, path)
	}
	if slices.Contains(grok, body) {
		t.Fatal("headless grok argv contains the prompt body")
	}
}

func TestGrokQuotaAndCrashChannels(t *testing.T) {
	const headless = `{"type":"error","message":"HTTP 429: usage limit reached"}`
	v1 := "/Volumes/Storage/Development/Cardex/repo/cmd/cardex"
	grokTest, err := os.ReadFile(filepath.Join(v1, "grok_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(grokTest), headless) {
		t.Fatal("v1 grok_test.go fakeGrokBuild quota line missing")
	}
	goalSrc, err := os.ReadFile(filepath.Join(v1, "workflow_goal.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goalSrc), "func observeNativeGrokGoal") {
		t.Fatal("v1 goal observer missing")
	}
	failSrc, err := os.ReadFile(filepath.Join(v1, "failure_class_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(failSrc), `"signal: killed"`) {
		t.Fatal("v1 classifier crash string missing")
	}
	procSrc, err := os.ReadFile(filepath.Join(v1, "grok.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(procSrc), "status.Signaled()") {
		t.Fatal("v1 WaitStatus signal fact missing")
	}

	t.Run("b2_unproven_quota_line", func(t *testing.T) {
		dir := t.TempDir()
		err, wrote := driveSession(t, "finish the card\n", dir, []byte("quota exceeded\n"))
		if string(wrote) != "/goal finish the card\r" {
			t.Fatalf("wrote %q", wrote)
		}
		if err == nil || errors.Is(err, runner.ErrGrokQuotaHit) || errors.Is(err, runner.ErrGrokBudgetLimited) || errors.Is(err, runner.ErrGrokCrash) {
			t.Fatalf("unproven quota line is not the interactive terminal: %v", err)
		}
		if _, ok := runner.ReadGrokGoalSession(dir); ok {
			t.Fatal("empty session is not the quota terminal")
		}
	})

	t.Run("b2_crash_host_exit", func(t *testing.T) {
		err, ok := runner.ConsumeGrokProcessExit(runner.GrokProcessExit{Signaled: true, Signal: 9})
		if !ok || !errors.Is(err, runner.ErrGrokCrash) {
			t.Fatalf("signaled exit %v ok=%v", err, ok)
		}
		err, ok = runner.ConsumeGrokProcessExit(runner.GrokProcessExit{Signaled: true, Signal: 11})
		if !ok || !errors.Is(err, runner.ErrGrokCrash) {
			t.Fatalf("SIGSEGV %v ok=%v", err, ok)
		}
		if _, ok := runner.ConsumeGrokProcessExit(runner.GrokProcessExit{Signaled: true, Signal: 15}); ok {
			t.Fatal("SIGTERM is not a v1 crash signal")
		}
		if _, ok := runner.ConsumeGrokProcessExit(runner.GrokProcessExit{}); ok {
			t.Fatal("clean exit")
		}
		if _, ok := runner.ConsumeGrokProcessExit(runner.GrokProcessExit{Signal: 9}); ok {
			t.Fatal("signal number without Signaled")
		}
	})

	t.Run("b2_signal_killed_pty_body_not_crash", func(t *testing.T) {
		err, _ := driveGrok(t, "finish the card\n", "grok_goal_crash.txt")
		if errors.Is(err, runner.ErrGrokCrash) {
			t.Fatal("classifier string on the PTY is not the host exit fact")
		}
		body := []byte("ordinary body containing signal: killed\n")
		rw := &scriptRW{src: bytes.NewReader(body)}
		err = runner.Grok{GoalMode: true, Prompt: "finish the card"}.Drive(context.Background(), rw)
		if errors.Is(err, runner.ErrGrokCrash) {
			t.Fatal("body signal: killed")
		}
		for _, ad := range []runner.Runner{runner.Grok{}, runner.Cursor{}, runner.Kimi{}, runner.AGY{}} {
			if got := ad.Classify(0, body); got != "" {
				t.Fatalf("%T class %q", ad, got)
			}
			if got := ad.Classify(0, readFixture(t, "grok_goal_crash.txt")); got != "" {
				t.Fatalf("%T fixture class %q", ad, got)
			}
		}
	})
}

// TestGrokGoalQuotaTerminal is the B2 table. The positive proof is
// testdata/grok_goal_quota_screen.bin replayed through Drive, whole and in
// chunks. Its provenance names the screen-log frame and the pager sentence
// embedded into that frame. The pager sentence in
// testdata/grok_goal_quota_pty.txt and the shell billing sentences are the
// string table. They are not testdata/grok_goal_quota_stderr.txt and not the
// headless {"type":"error"} record pasted on the PTY. ReadGrokGoalSession
// does not invent a quota status.
func TestGrokGoalQuotaTerminal(t *testing.T) {
	const prompt = "finish the card\n"
	const written = "/goal finish the card\r"
	card := string(readFixture(t, "grok_goal_quota_pty.txt"))
	if strings.TrimSpace(card) != "You've hit the rate limit for your plan." {
		t.Fatalf("fixture %q is not the pager card", card)
	}
	sameCard := []string{
		"You\u2019ve hit the rate limit for your plan. Upgrade your account or try again later.",
		"You\u2019ve hit your team\u2019s API rate limit. Ask a team admin to purchase more credits for higher limits, or try again later. See https://docs.x.ai/developers/rate-limits#rate-limit-tiers",
		"You\u2019ve reached your free Grok Build usage limit for now. Get SuperGrok for much higher limits, or try again later: https://grok.com/supergrok?referrer=grok-build",
		"You\u2019ve hit the credit limit for your plan.",
		"You\u2019ve hit your spending cap.",
		"You hit your free usage limit.",
		"You hit your weekly limit.",
		"out of credits or over your spending limit. Add credits and retry.",
	}

	t.Run("screen_log", func(t *testing.T) {
		raw := readFixture(t, "grok_goal_quota_screen.bin")
		note := string(readFixture(t, "grok_goal_quota_screen.provenance.txt"))
		pager := bytes.TrimSpace(readFixture(t, "grok_goal_quota_pty.txt"))
		if bytes.Equal(raw, pager) {
			t.Fatal("screen fixture is the pager sentence")
		}
		for _, frag := range [][]byte{
			[]byte("\x1b[?2026h\x1b[15;4H"),
			[]byte("\x1b[15;6HStarting"),
			[]byte("\x1b[15;15Hsession"),
			[]byte("\x1b[15;24H0.0s"),
			[]byte("\x1b[10;1H"),
			pager,
			[]byte("\x1b[?2026l"),
		} {
			if !bytes.Contains(raw, frag) {
				t.Fatalf("screen fixture missing %q", frag)
			}
		}
		if bytes.Contains(raw, []byte{'\n'}) || bytes.Contains(raw, []byte{'\r'}) {
			t.Fatal("screen fixture CR/LF was rewritten")
		}
		commit := bytes.LastIndex(raw, []byte("\x1b[?2026l"))
		if sentenceAt := bytes.Index(raw, pager); sentenceAt < 0 || sentenceAt > commit {
			t.Fatal("pager sentence is not inside the committed frame")
		}
		for _, want := range []string{
			"rounds/R2/R2-B.screen.log",
			"offset: 1643",
			"length: 142",
			"time: 2026-10-31T01:29:23+08:00",
			"grok_goal_quota_pty.txt:1",
		} {
			if !strings.Contains(note, want) {
				t.Fatalf("provenance missing %q", want)
			}
		}
		for _, banned := range []string{"token", "Bearer", "cookie", "sk-", "/Users/", "/home/", "@"} {
			if bytes.Contains(raw, []byte(banned)) || strings.Contains(note, banned) {
				t.Fatalf("scrub %q", banned)
			}
		}
		dir := t.TempDir()
		got, wrote := driveSession(t, prompt, dir, raw)
		if string(wrote) != written {
			t.Fatalf("wrote %q", wrote)
		}
		if !errors.Is(got, runner.ErrGrokQuotaHit) {
			t.Fatalf("screen log %v", got)
		}
		if _, ok := runner.ReadGrokGoalSession(dir); ok {
			t.Fatal("screen log invented a session status")
		}
	})

	t.Run("screen_log_chunks", func(t *testing.T) {
		raw := readFixture(t, "grok_goal_quota_screen.bin")
		at := bytes.LastIndex(raw, []byte("\x1b[?2026l"))
		if at <= 0 {
			t.Fatal("sync commit")
		}
		dir := t.TempDir()
		rw := &chunkRW{chunks: [][]byte{raw[:at], raw[at:]}}
		err := runner.Grok{GoalMode: true, Prompt: prompt, SessionDir: dir}.Drive(context.Background(), rw)
		if rw.reads != 2 {
			t.Fatalf("uncommitted frame fired early: reads %d", rw.reads)
		}
		if !errors.Is(err, runner.ErrGrokQuotaHit) {
			t.Fatalf("committed frame %v", err)
		}
		if _, ok := runner.ReadGrokGoalSession(dir); ok {
			t.Fatal("chunked replay invented a session status")
		}
		early, _ := driveSession(t, prompt, t.TempDir(), raw[:at])
		if early == nil || errors.Is(early, runner.ErrGrokQuotaHit) || errors.Is(early, runner.ErrGrokBudgetLimited) || errors.Is(early, runner.ErrGrokCrash) {
			t.Fatalf("uncommitted prefix %v", early)
		}
	})

	t.Run("pager_card", func(t *testing.T) {
		dir := t.TempDir()
		got, wrote := driveSession(t, prompt, dir, []byte(card))
		if string(wrote) != written {
			t.Fatalf("wrote %q", wrote)
		}
		if !errors.Is(got, runner.ErrGrokQuotaHit) {
			t.Fatalf("pager card %v", got)
		}
		if _, ok := runner.ReadGrokGoalSession(dir); ok {
			t.Fatal("pty card invented a session status")
		}
		g := runner.Grok{GoalMode: true, Prompt: prompt}
		if g.Classify(0, []byte(card)) != model.ErrorQuotaHit {
			t.Fatal("classify pager card")
		}
		if g.Classify(0, []byte(strings.TrimSpace(card))) != model.ErrorQuotaHit {
			t.Fatal("classify pager card at eof")
		}
	})

	for _, line := range sameCard {
		t.Run("shell_card/"+line[:12], func(t *testing.T) {
			dir := t.TempDir()
			got, _ := driveSession(t, prompt, dir, []byte(line+"\n"))
			if !errors.Is(got, runner.ErrGrokQuotaHit) {
				t.Fatalf("%v", got)
			}
			if got := (runner.Grok{GoalMode: true, Prompt: prompt}).Classify(0, []byte(line+"\n")); got != model.ErrorQuotaHit {
				t.Fatalf("class %q", got)
			}
		})
	}

	t.Run("card_after_echo", func(t *testing.T) {
		pty := "/goal finish the card\n" + card
		got, _ := driveSession(t, prompt, t.TempDir(), []byte(pty))
		if !errors.Is(got, runner.ErrGrokQuotaHit) {
			t.Fatalf("%v", got)
		}
	})

	t.Run("card_split_across_reads", func(t *testing.T) {
		line := strings.TrimSpace(card)
		cut := len(line) / 2
		rw := &chunkRW{chunks: [][]byte{[]byte(line[:cut]), []byte(line[cut:] + "\n")}}
		err := runner.Grok{GoalMode: true, Prompt: prompt, SessionDir: t.TempDir()}.Drive(context.Background(), rw)
		if rw.reads != 2 {
			t.Fatalf("reads %d", rw.reads)
		}
		if !errors.Is(err, runner.ErrGrokQuotaHit) {
			t.Fatalf("%v", err)
		}
	})

	type neg struct {
		name   string
		prompt string
		pty    string
		chunks []string
	}
	negative := []neg{
		{name: "headless_stderr", pty: "quota exceeded\n"},
		{name: "headless_json", pty: "{\"type\":\"error\",\"message\":\"HTTP 429: usage limit reached\"}\n"},
		{name: "prompt_echo", pty: "/goal 解释\nquota exceeded\n", prompt: "解释"},
		{name: "card_as_prompt_echo", prompt: strings.TrimSpace(card), pty: "/goal " + strings.TrimSpace(card) + "\n"},
		{name: "card_inside_prompt", prompt: "finish the card\n" + strings.TrimSpace(card), pty: "/goal finish the card\n" + strings.TrimSpace(card) + "\n"},
		{name: "body", pty: "You've hit the rate limit for your plan is only an example\n"},
		{name: "body_rate_retry", pty: "Rate limited | Retrying (attempt 1)... is only an example\n"},
		{name: "body_quota_exceeded", pty: "quota exceeded is only an example\n"},
		{name: "prefix", pty: "You've hit the rate"},
		{name: "prefix_newline", pty: "You've hit the rate\n"},
		{name: "split_body", chunks: []string{"quota exceeded", " is only an example\n"}},
		{name: "signal_killed", pty: "signal: killed\n"},
	}
	for _, row := range negative {
		t.Run(row.name, func(t *testing.T) {
			p := prompt
			if row.prompt != "" {
				p = row.prompt
			}
			var err error
			if row.chunks != nil {
				parts := make([][]byte, len(row.chunks))
				for i, c := range row.chunks {
					parts[i] = []byte(c)
				}
				rw := &chunkRW{chunks: parts}
				err = runner.Grok{GoalMode: true, Prompt: p, SessionDir: t.TempDir()}.Drive(context.Background(), rw)
				if row.name == "split_body" && rw.reads != 2 {
					t.Fatalf("split body fired early: reads %d", rw.reads)
				}
			} else {
				err, _ = driveSession(t, p, t.TempDir(), []byte(row.pty))
			}
			if err == nil || errors.Is(err, runner.ErrGrokQuotaHit) || errors.Is(err, runner.ErrGrokBudgetLimited) || errors.Is(err, runner.ErrGrokCrash) {
				t.Fatalf("%v", err)
			}
		})
	}
	// The card sentence inside the prompt echo, in body text, or as a prefix
	// is not the terminal. Headless "quota exceeded" stays a Classify fact and
	// is still not this terminal; that row is asserted above on Drive only.
	for _, row := range []neg{
		{name: "card_as_prompt_echo", prompt: strings.TrimSpace(card), pty: "/goal " + strings.TrimSpace(card) + "\n"},
		{name: "card_inside_prompt", prompt: "finish the card\n" + strings.TrimSpace(card), pty: "/goal finish the card\n" + strings.TrimSpace(card) + "\n"},
		{name: "body", pty: "You've hit the rate limit for your plan is only an example\n"},
		{name: "body_rate_retry", pty: "Rate limited | Retrying (attempt 1)... is only an example\n"},
		{name: "prefix", pty: "You've hit the rate"},
		{name: "prefix_newline", pty: "You've hit the rate\n"},
	} {
		t.Run("classify/"+row.name, func(t *testing.T) {
			p := prompt
			if row.prompt != "" {
				p = row.prompt
			}
			if got := (runner.Grok{GoalMode: true, Prompt: p}).Classify(0, []byte(row.pty)); got == model.ErrorQuotaHit || got == model.ErrorBudgetLimited {
				t.Fatalf("class %q", got)
			}
		})
	}

	t.Run("complete_session", func(t *testing.T) {
		dir := installSessionFixture(t, "grok_goal_complete.jsonl")
		got, _ := driveSession(t, prompt, dir, []byte(card))
		if got != nil {
			t.Fatalf("completion %v", got)
		}
		if term, ok := runner.ReadGrokGoalSession(dir); !ok || term != nil {
			t.Fatalf("reader complete ok=%v err=%v", ok, term)
		}
	})
	t.Run("budget_limited_session", func(t *testing.T) {
		dir := installSessionFixture(t, "grok_goal_budget_limited.jsonl")
		got, _ := driveSession(t, prompt, dir, []byte(card))
		if !errors.Is(got, runner.ErrGrokBudgetLimited) || errors.Is(got, runner.ErrGrokQuotaHit) {
			t.Fatalf("budget_limited %v", got)
		}
		if term, ok := runner.ReadGrokGoalSession(dir); !ok || !errors.Is(term, runner.ErrGrokBudgetLimited) || errors.Is(term, runner.ErrGrokQuotaHit) {
			t.Fatalf("reader budget ok=%v err=%v", ok, term)
		}
	})
	t.Run("crash_signals", func(t *testing.T) {
		for _, sig := range []int{3, 6, 9, 10, 11} {
			crash, ok := runner.ConsumeGrokProcessExit(runner.GrokProcessExit{Signaled: true, Signal: sig})
			if !ok || !errors.Is(crash, runner.ErrGrokCrash) || errors.Is(crash, runner.ErrGrokQuotaHit) {
				t.Fatalf("signal %d %v ok=%v", sig, crash, ok)
			}
		}
		for _, exit := range []runner.GrokProcessExit{{Signaled: true, Signal: 15}, {Signal: 9}, {}} {
			if _, ok := runner.ConsumeGrokProcessExit(exit); ok {
				t.Fatalf("not a crash fact %+v", exit)
			}
		}
		killed, _ := driveGrok(t, prompt, "grok_goal_crash.txt")
		if killed == nil || errors.Is(killed, runner.ErrGrokQuotaHit) || errors.Is(killed, runner.ErrGrokCrash) {
			t.Fatalf("signal: killed on the PTY %v", killed)
		}
	})
}

// TestGrokScreenEchoAndTruncation is the screen-channel echo and truncation
// table. Classify and goal-mode Drive both see the bytes. cursor, kimi, and
// agy have no screen frame; their Classify path is classifyScanned only.
// Echo origin has to survive scroll, and a row's truncation flag has to
// follow the current cells. Drive's copy includes the synchronized-update
// end marker.
func TestGrokScreenEchoAndTruncation(t *testing.T) {
	sentence := "You've hit the rate limit for your plan."
	if len(sentence) >= 80 {
		t.Fatalf("sentence len %d", len(sentence))
	}
	padded := sentence + strings.Repeat(" ", 80-len(sentence)) + "is only an example"
	sync := "\x1b[?2026l"
	scrolledPrompt := "explain\n" + strings.Repeat("context line\n", 24) + sentence
	rows := []struct {
		name      string
		prompt    string
		zero      bool
		pty       string
		wantQuota bool
	}{
		{
			name: "goal_echo_without_prompt",
			zero: true,
			pty:  "/goal explain\n" + sentence + "\n" + sync,
		},
		{
			name:   "truncated_multiline_prompt",
			prompt: "finish the card\n" + padded,
			pty:    "/goal finish the card\n" + padded + "\n" + sync,
		},
		{
			name:   "truncated_body",
			prompt: "finish the card",
			pty:    padded + "\n" + sync,
		},
		{
			name:   "scrolled_prompt_echo",
			prompt: scrolledPrompt,
			pty:    "/goal " + scrolledPrompt + "\n" + sync,
		},
		{
			name:      "indented_echo_then_real_quota",
			prompt:    "explain",
			pty:       "\x1b[1;4H/goal explain\x1b[2;4H" + sentence + sync,
			wantQuota: true,
		},
		{
			name: "scrolled_goal_echo_without_prompt",
			zero: true,
			pty:  "/goal explain\n" + strings.Repeat("context line\n", 24) + sentence + "\n" + sync,
		},
		{
			name:      "quota_after_full_row_repaint",
			prompt:    "explain",
			pty:       "\x1b[10;1H" + strings.Repeat("x", 81) + "\x1b[10;1H" + strings.Repeat(" ", 80) + "\x1b[10;1H" + sentence + sync,
			wantQuota: true,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			var classify runner.Grok
			if !row.zero {
				classify.Prompt = row.prompt
			}
			got := classify.Classify(0, []byte(row.pty))
			dir := t.TempDir()
			err, _ := driveSession(t, row.prompt, dir, []byte(row.pty))
			if _, ok := runner.ReadGrokGoalSession(dir); ok {
				t.Fatal("counterexample invented a session status")
			}
			if row.wantQuota {
				if got != model.ErrorQuotaHit {
					t.Fatalf("Classify=%q want %q", got, model.ErrorQuotaHit)
				}
				if err == nil || err.Error() != runner.ErrGrokQuotaHit.Error() {
					t.Fatalf("Drive=%v want %q", err, runner.ErrGrokQuotaHit)
				}
				return
			}
			if got != "" {
				t.Fatalf("Classify=%q want empty", got)
			}
			if err == nil || err.Error() != "grok goal: no terminal" {
				t.Fatalf("Drive=%v want %q", err, "grok goal: no terminal")
			}
		})
	}
}

// TestGrokInteractiveQuota keeps the headless stderr line and the headless
// error JSON off the interactive quota terminal. Completion, budget_limited,
// and the host exit crash stay on their own channels. The positive card is
// TestGrokGoalQuotaTerminal.
func TestGrokInteractiveQuota(t *testing.T) {
	const prompt = "finish the card\n"
	const written = "/goal finish the card\r"
	const headless = `{"type":"error","message":"HTTP 429: usage limit reached"}`
	sample := []byte("quota exceeded\n")

	dir := t.TempDir()
	if _, ok := runner.ReadGrokGoalSession(dir); ok {
		t.Fatal("empty session before drive")
	}
	got, wrote := driveSession(t, prompt, dir, sample)
	if string(wrote) != written {
		t.Fatalf("wrote %q", wrote)
	}
	if got == nil || errors.Is(got, runner.ErrGrokQuotaHit) || errors.Is(got, runner.ErrGrokBudgetLimited) || errors.Is(got, runner.ErrGrokCrash) {
		t.Fatalf("unproven quota line is not the interactive terminal: %v", got)
	}
	if _, ok := runner.ReadGrokGoalSession(dir); ok {
		t.Fatal("empty session after drive invented a quota status")
	}

	split := &chunkRW{chunks: [][]byte{[]byte("quota exceeded"), []byte(" is only an example\n")}}
	err := runner.Grok{GoalMode: true, Prompt: prompt, SessionDir: t.TempDir()}.Drive(context.Background(), split)
	if split.reads != 2 {
		t.Fatalf("split body fired on the first read: reads %d", split.reads)
	}
	if err == nil || errors.Is(err, runner.ErrGrokQuotaHit) || errors.Is(err, runner.ErrGrokBudgetLimited) || errors.Is(err, runner.ErrGrokCrash) {
		t.Fatalf("split body terminal %v", err)
	}
	prefix := &scriptRW{src: bytes.NewReader([]byte("quota exc"))}
	err = runner.Grok{GoalMode: true, Prompt: prompt, SessionDir: t.TempDir()}.Drive(context.Background(), prefix)
	if err == nil || errors.Is(err, runner.ErrGrokQuotaHit) || errors.Is(err, runner.ErrGrokBudgetLimited) || errors.Is(err, runner.ErrGrokCrash) {
		t.Fatalf("quota prefix terminal %v", err)
	}

	completeDir := installSessionFixture(t, "grok_goal_complete.jsonl")
	complete, wroteComplete := driveSession(t, prompt, completeDir, sample)
	if string(wroteComplete) != written {
		t.Fatalf("complete wrote %q", wroteComplete)
	}
	if complete != nil || errors.Is(complete, runner.ErrGrokQuotaHit) || errors.Is(complete, runner.ErrGrokBudgetLimited) || errors.Is(complete, runner.ErrGrokCrash) {
		t.Fatalf("completion %v", complete)
	}
	if term, ok := runner.ReadGrokGoalSession(completeDir); !ok || term != nil {
		t.Fatalf("reader complete ok=%v err=%v", ok, term)
	}

	budgetDir := installSessionFixture(t, "grok_goal_budget_limited.jsonl")
	budget, wroteBudget := driveSession(t, prompt, budgetDir, sample)
	if string(wroteBudget) != written {
		t.Fatalf("budget wrote %q", wroteBudget)
	}
	if !errors.Is(budget, runner.ErrGrokBudgetLimited) || budget == nil || errors.Is(budget, runner.ErrGrokQuotaHit) || errors.Is(budget, runner.ErrGrokCrash) {
		t.Fatalf("budget_limited %v", budget)
	}
	if term, ok := runner.ReadGrokGoalSession(budgetDir); !ok || !errors.Is(term, runner.ErrGrokBudgetLimited) || errors.Is(term, runner.ErrGrokQuotaHit) {
		t.Fatalf("reader budget ok=%v err=%v", ok, term)
	}

	for _, sig := range []int{3, 6, 9, 10, 11} {
		crash, ok := runner.ConsumeGrokProcessExit(runner.GrokProcessExit{Signaled: true, Signal: sig})
		if !ok || !errors.Is(crash, runner.ErrGrokCrash) || errors.Is(crash, runner.ErrGrokQuotaHit) || errors.Is(crash, runner.ErrGrokBudgetLimited) {
			t.Fatalf("signal %d %v ok=%v", sig, crash, ok)
		}
	}
	for _, exit := range []runner.GrokProcessExit{
		{Signaled: true, Signal: 15},
		{Signaled: true, Signal: 1},
		{Signal: 9},
		{},
	} {
		if _, ok := runner.ConsumeGrokProcessExit(exit); ok {
			t.Fatalf("not a crash fact %+v", exit)
		}
	}

	headlessPty, _ := driveGrok(t, prompt, "grok_goal_quota.jsonl")
	if headlessPty == nil || errors.Is(headlessPty, runner.ErrGrokQuotaHit) || errors.Is(headlessPty, runner.ErrGrokBudgetLimited) || errors.Is(headlessPty, runner.ErrGrokCrash) {
		t.Fatalf("headless JSON on the PTY %v", headlessPty)
	}
	killedPty, _ := driveGrok(t, prompt, "grok_goal_crash.txt")
	if killedPty == nil || errors.Is(killedPty, runner.ErrGrokQuotaHit) || errors.Is(killedPty, runner.ErrGrokBudgetLimited) || errors.Is(killedPty, runner.ErrGrokCrash) {
		t.Fatalf("signal: killed on the PTY %v", killedPty)
	}
	g := runner.Grok{GoalMode: true}
	if g.Classify(0, readFixture(t, "grok_goal_quota.jsonl")) != model.ErrorQuotaHit {
		t.Fatal("headless record is still the classify quota channel")
	}
	if g.Classify(0, []byte(headless)) != model.ErrorQuotaHit {
		t.Fatal("headless record without newline")
	}
	if got := g.Classify(0, readFixture(t, "grok_goal_crash.txt")); got != "" {
		t.Fatalf("classify signal: killed %q", got)
	}
}

// TestRecordBoundaryDiagnostics is the B3/B6 table. Echo, body, and a split
// fragment are not error classes. A finished whole-line diagnostic is.
func TestRecordBoundaryDiagnostics(t *testing.T) {
	adapters := []struct {
		name string
		cl   runner.Runner
	}{
		{"grok", runner.Grok{GoalMode: true}},
		{"cursor", runner.Cursor{}},
		{"kimi", runner.Kimi{}},
		{"agy", runner.AGY{}},
	}
	type row struct {
		name string
		out  string
		want model.ErrorClass
	}
	rows := []row{
		{name: "echo_quota", out: "/goal 解释\nquota exceeded\n"},
		{name: "echo_http_429", out: "/goal 解释\nHTTP 429: usage limit reached\n"},
		{name: "echo_dial", out: "/goal 解释\ndial tcp: lookup api.example: no such host\n"},
		{name: "echo_io_timeout", out: "/goal 解释\ni/o timeout\n"},
		{name: "echo_send", out: "/goal 解释\nerror sending request\n"},
		{name: "echo_relogin", out: "/goal 解释\n认证错误: 请重新登录\n"},
		{name: "body_quota", out: "quota exceeded is only an example\n"},
		{name: "body_http_429", out: "HTTP 429: usage limit reached is only an example\n"},
		{name: "body_dial", out: "这里只解释 dial tcp: lookup api.example: no such host\n"},
		{name: "body_io_timeout", out: "这里只解释 i/o timeout\n"},
		{name: "body_send", out: "error sending request is only an example\n"},
		{name: "body_relogin", out: "这里只解释 认证错误: 请重新登录\n"},
		{name: "body_relogin_suffix", out: "请重新登录 is only an example\n"},
		{name: "body_perm", out: "这里只解释 permission denied\n"},
		{name: "body_agy_phrase", out: "这里只解释 Antigravity 需要交互：动作被拒绝且没有可验收产物\n"},
		{name: "echo_agy_phrase", out: "/goal 解释\nAntigravity 需要交互：动作被拒绝且没有可验收产物\n"},
		{name: "split_body", out: "quota exceeded is only an example\n"},
		{name: "quota_line", out: "quota exceeded\n", want: model.ErrorQuotaHit},
		{name: "quota_line_eof", out: "quota exceeded", want: model.ErrorQuotaHit},
		{name: "usage_limit_reached", out: "usage limit reached\n", want: model.ErrorQuotaHit},
		{name: "rate_limited", out: "rate limited\n", want: model.ErrorQuotaHit},
		{name: "http_429", out: "HTTP 429: usage limit reached\n", want: model.ErrorQuotaHit},
		{name: "dial", out: "dial tcp: lookup api.example: no such host\n", want: model.ErrorTransport},
		{name: "dial_eof", out: "dial tcp: lookup api.example: no such host", want: model.ErrorTransport},
		{name: "io_timeout", out: "i/o timeout\n", want: model.ErrorTransport},
		{name: "send", out: "error sending request\n", want: model.ErrorTransport},
		{name: "conn_error", out: "connection error\n", want: model.ErrorTransport},
		{name: "conn_reset", out: "connection reset\n", want: model.ErrorTransport},
		{name: "conn_reset_peer", out: "connection reset by peer\n", want: model.ErrorTransport},
		{name: "stream", out: "stream disconnect\n", want: model.ErrorTransport},
		{name: "unreachable", out: "network is unreachable\n", want: model.ErrorTransport},
		{name: "dns", out: "temporary failure in name resolution\n", want: model.ErrorTransport},
		{name: "disconnected", out: "disconnected before\n", want: model.ErrorTransport},
		{name: "no_such_host", out: "no such host\n", want: model.ErrorTransport},
		{name: "read_tcp", out: "read tcp 10.0.0.1:443: i/o timeout\n", want: model.ErrorTransport},
		{name: "relogin", out: "认证错误: 请重新登录\n", want: model.ErrorAuthFailed},
		{name: "relogin_bare", out: "请重新登录\n", want: model.ErrorAuthFailed},
		{name: "timeout_labeled", out: "codex_error: 步骤超时(60 分钟)\n", want: model.ErrorTimeout},
		{name: "timeout_error_prefix", out: "error: context deadline exceeded\n", want: model.ErrorTimeout},
		{name: "fragment_dial", out: "dial tcp: lookup api.ex"},
		{name: "agy_phrase", out: "Antigravity 需要交互：动作被拒绝且没有可验收产物\n"},
	}
	for _, ad := range adapters {
		for _, row := range rows {
			t.Run(ad.name+"/"+row.name, func(t *testing.T) {
				want := row.want
				if row.name == "agy_phrase" && ad.name == "agy" {
					want = model.ErrorPermissionDenied
				}
				if row.name == "rate_limited" && ad.name == "agy" {
					want = ""
				}
				if got := ad.cl.Classify(0, []byte(row.out)); got != want {
					t.Fatalf("class %q want %q", got, want)
				}
			})
		}
	}

	g := runner.Grok{GoalMode: true, Prompt: "解释"}
	splits := [][]string{
		{"quota exceeded", " is only an example\n"},
		{"/goal 解释\n", "quota exceeded\n"},
		{"/goal 解释\n", "HTTP 429: usage limit reached\n"},
		{"/goal 解释\n", "dial tcp: lookup api.example: no such host\n"},
		{"/goal 解释\n", "i/o timeout\n"},
		{"/goal 解释\n", "error sending request\n"},
		{"/goal 解释\n", "认证错误: 请重新登录\n"},
	}
	for _, chunks := range splits {
		t.Run("drive/"+chunks[0], func(t *testing.T) {
			parts := make([][]byte, len(chunks))
			for i, c := range chunks {
				parts[i] = []byte(c)
			}
			rw := &chunkRW{chunks: parts}
			err := g.Drive(context.Background(), rw)
			if rw.reads != len(chunks) {
				t.Fatalf("reads %d want %d", rw.reads, len(chunks))
			}
			if err == nil || errors.Is(err, runner.ErrGrokQuotaHit) || errors.Is(err, runner.ErrGrokBudgetLimited) || errors.Is(err, runner.ErrGrokCrash) {
				t.Fatalf("pty terminal %v", err)
			}
			var full strings.Builder
			for _, c := range chunks {
				full.WriteString(c)
			}
			for _, ad := range adapters {
				if got := ad.cl.Classify(0, []byte(full.String())); got == model.ErrorQuotaHit || got == model.ErrorBudgetLimited || got == model.ErrorTransport || got == model.ErrorAuthFailed {
					t.Fatalf("%s class %q", ad.name, got)
				}
			}
		})
	}
}

func TestKimiAutoPermissionConfig(t *testing.T) {
	home := t.TempDir()
	path := writePromptFile(t, "hello world\n第二行 中文\n")
	_, env, err := (runner.Kimi{ConfigHome: home}).Build(runner.LaunchSpec{
		Model: "kimi-k2", PromptPath: path, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(env, []string{"KIMI_CODE_NO_AUTO_UPDATE=1", "KIMI_CODE_HOME=" + home}) {
		t.Fatalf("env %q", env)
	}
	cfg, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(cfg)
	if !strings.Contains(text, "default_permission_mode = \"auto\"") || !strings.Contains(text, "default_plan_mode = false") {
		t.Fatalf("config %q", text)
	}
	real, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{filepath.Join(real, ".kimi-code"), filepath.Join(real, ".cardex")} {
		if home == forbidden || strings.HasPrefix(home, forbidden+string(os.PathSeparator)) {
			t.Fatalf("config home is the real CLI home %s", forbidden)
		}
	}
	if _, err := os.ReadFile(filepath.Join(home, "config.toml")); err != nil {
		t.Fatal(err)
	}

	t.Run("b5_existing_config_toml", func(t *testing.T) {
		seeded := t.TempDir()
		const base = "default_model = \"kimi-code/k3\"\n" +
			"default_permission_mode = \"manual\"\n" +
			"default_plan_mode = true\n" +
			"\n" +
			"[providers.\"managed:kimi-code\"]\n" +
			"type = \"kimi\"\n"
		if err := os.WriteFile(filepath.Join(seeded, "config.toml"), []byte(base), 0o600); err != nil {
			t.Fatal(err)
		}
		_, env, err := (runner.Kimi{ConfigHome: seeded}).Build(runner.LaunchSpec{
			Model: "kimi-k2", PromptPath: path, WorkDir: t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(env, []string{"KIMI_CODE_NO_AUTO_UPDATE=1", "KIMI_CODE_HOME=" + seeded}) {
			t.Fatalf("env %q", env)
		}
		cfg, err := os.ReadFile(filepath.Join(seeded, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		const want = "default_permission_mode = \"auto\"\n" +
			"default_plan_mode = false\n" +
			"\n" +
			"default_model = \"kimi-code/k3\"\n" +
			"\n" +
			"[providers.\"managed:kimi-code\"]\n" +
			"type = \"kimi\"\n"
		if string(cfg) != want {
			t.Fatalf("config\n got %q\nwant %q", cfg, want)
		}
	})
}

// TestBodyEchoNestedAreNotFacts is the B3 table. Body text, the /goal echo,
// and nested business fields are not quota, crash, or budget_limited for grok
// Drive or for Classify of grok, cursor, kimi, and agy. Those four Classify
// methods share errorScan. Goal status is the goal_updated update only.
// An explicit error record and a real goal_updated event still classify.
func TestBodyEchoNestedAreNotFacts(t *testing.T) {
	const headlessQuota = `{"type":"error","message":"HTTP 429: usage limit reached"}`
	const goalBudget = `{"method":"_x.ai/session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"goal_updated","status":"budget_limited","goal_id":"g1"}}}`
	adapters := []struct {
		name string
		cl   runner.Runner
		grok bool
	}{
		{"grok-goal", runner.Grok{GoalMode: true}, true},
		{"grok", runner.Grok{}, true},
		{"cursor", runner.Cursor{}, false},
		{"kimi", runner.Kimi{}, false},
		{"agy", runner.AGY{}, false},
	}
	type row struct {
		name   string
		out    string
		chunks []string
		grok   model.ErrorClass
		others model.ErrorClass
	}
	rows := []row{
		{name: "b3_body_explain_usage_limit", out: "这里只解释 usage limit\n"},
		{name: "b3_body_explain_usage_limit_chunked", chunks: []string{"这里只解释 usage lim", "it\n"}},
		{name: "b3_echo_explain_usage_limit", out: "/goal 解释\nusage limit\n"},
		{name: "b3_echo_explain_usage_limit_chunked", chunks: []string{"/goal 解释\n", "usage limit\n"}},
		{name: "b3_echo_goal_rate_limit", out: "/goal 解释 rate limit\n"},
		{name: "b3_echo_goal_rate_limit_chunked", chunks: []string{"/goal 解释 rate lim", "it\n"}},
		{name: "b3_echo_quota_exceeded", out: "/goal 解释\nquota exceeded\n"},
		{name: "b3_echo_quota_exceeded_chunked", chunks: []string{"/goal 解释\n", "quota exceeded\n"}},
		{name: "b3_echo_http_429", out: "/goal 解释\nHTTP 429: usage limit reached\n"},
		{name: "b3_echo_http_429_chunked", chunks: []string{"/goal 解释\n", "HTTP 429: usage limit reached\n"}},
		{name: "b3_body_quota_example", out: "quota exceeded is only an example\n"},
		{name: "b3_body_http_429_example", out: "HTTP 429: usage limit reached is only an example\n"},
		{name: "b3_body_signal_killed", out: "ordinary body containing signal: killed\n"},
		{name: "b3_body_signal_killed_chunked", chunks: []string{"ordinary body containing signal: kil", "led\n"}},
		{name: "b3_body_json_signal_killed", out: "{\"type\":\"text\",\"data\":\"signal: killed\"}\n"},
		{name: "b3_body_json_rate_limit", out: "{\"type\":\"text\",\"data\":\"rate limit\"}\n"},
		{name: "b3_body_json_budget_limited", out: "{\"type\":\"text\",\"data\":\"解释 budget_limited\"}\n"},
		{name: "b3_echo_plain_budget_limited", out: "解释 budget_limited\n"},
		{name: "b3_nested_status_budget_limited", out: "{\"type\":\"tool_use\",\"input\":{\"status\":\"budget_limited\"}}\n"},
		{name: "b3_nested_status_budget_limited_chunked", chunks: []string{`{"type":"tool_use","input":{"status":"budget_lim`, "ited\"}}\n"}},
		{name: "b3_partial_text_rate_limit", out: `{"type":"text","data":"rate limit`},
		{name: "b3_explicit_error_record", out: headlessQuota + "\n", grok: model.ErrorQuotaHit, others: model.ErrorQuotaHit},
		{name: "b3_explicit_error_record_chunked", chunks: []string{`{"type":"error","message":"HTTP 429: usage limit`, ` reached"}` + "\n"}, grok: model.ErrorQuotaHit, others: model.ErrorQuotaHit},
		{name: "b3_goal_updated_budget_limited", out: goalBudget + "\n", grok: model.ErrorBudgetLimited},
		{name: "b3_goal_updated_budget_limited_chunked", chunks: []string{goalBudget[:len(goalBudget)-8], goalBudget[len(goalBudget)-8:] + "\n"}, grok: model.ErrorBudgetLimited},
	}
	for _, ad := range adapters {
		for _, row := range rows {
			t.Run(ad.name+"/"+row.name, func(t *testing.T) {
				want := row.others
				if ad.grok {
					want = row.grok
				}
				// AGY already classifies one truncated JSON document as
				// result_unparsable. That is not quota, crash, or budget_limited.
				if ad.name == "agy" && row.name == "b3_partial_text_rate_limit" {
					want = model.ErrorResultUnparsable
				}
				full := row.out
				if len(row.chunks) > 0 {
					var b strings.Builder
					for i, chunk := range row.chunks {
						b.WriteString(chunk)
						if i == len(row.chunks)-1 {
							break
						}
						got := ad.cl.Classify(0, []byte(b.String()))
						if got == model.ErrorQuotaHit || got == model.ErrorBudgetLimited {
							t.Fatalf("partial class %q after %d chunks", got, i+1)
						}
					}
					full = b.String()
				}
				if got := ad.cl.Classify(0, []byte(full)); got != want {
					t.Fatalf("class %q want %q", got, want)
				}
			})
		}
	}
	g := runner.Grok{GoalMode: true, Prompt: "解释 rate limit"}
	for _, row := range rows {
		t.Run("drive/"+row.name, func(t *testing.T) {
			var err error
			if len(row.chunks) > 0 {
				parts := make([][]byte, len(row.chunks))
				for i, c := range row.chunks {
					parts[i] = []byte(c)
				}
				rw := &chunkRW{chunks: parts}
				err = g.Drive(context.Background(), rw)
				if rw.reads != len(row.chunks) {
					t.Fatalf("reads %d want %d", rw.reads, len(row.chunks))
				}
			} else {
				rw := &scriptRW{src: bytes.NewReader([]byte(row.out))}
				err = g.Drive(context.Background(), rw)
			}
			if err == nil || errors.Is(err, runner.ErrGrokQuotaHit) || errors.Is(err, runner.ErrGrokCrash) || errors.Is(err, runner.ErrGrokBudgetLimited) {
				t.Fatalf("pty terminal %v", err)
			}
		})
	}
	findings := []string{"这里只解释 usage limit\n", "/goal 解释\nusage limit\n"}
	for _, ad := range adapters {
		for _, full := range findings {
			t.Run(ad.name+"/prefixes/"+full, func(t *testing.T) {
				for i := range len(full) {
					got := ad.cl.Classify(0, []byte(full[:i]))
					if got == model.ErrorQuotaHit || got == model.ErrorBudgetLimited {
						t.Fatalf("prefix %d class %q", i, got)
					}
				}
				if got := ad.cl.Classify(0, []byte(full)); got == model.ErrorQuotaHit || got == model.ErrorBudgetLimited {
					t.Fatalf("full class %q", got)
				}
			})
		}
	}
	for _, ad := range adapters {
		for _, file := range []string{"cursor_quota.txt", "kimi_quota.jsonl", "agy_quota.json", "grok_goal_quota.jsonl"} {
			t.Run(ad.name+"/keep/"+file, func(t *testing.T) {
				if got := ad.cl.Classify(0, readFixture(t, file)); got != model.ErrorQuotaHit {
					t.Fatalf("class %q", got)
				}
			})
		}
		t.Run(ad.name+"/keep/grok_auth", func(t *testing.T) {
			if got := ad.cl.Classify(0, readFixture(t, "grok_auth.txt")); got != model.ErrorAuthFailed {
				t.Fatalf("class %q", got)
			}
		})
		t.Run(ad.name+"/keep/grok_limit_like", func(t *testing.T) {
			if got := ad.cl.Classify(0, readFixture(t, "grok_limit_like.txt")); got == model.ErrorQuotaHit {
				t.Fatal("limit-like is quota")
			}
		})
		t.Run(ad.name+"/keep/echo_not_quota", func(t *testing.T) {
			for _, line := range []string{"/goal 解释\nquota exceeded\n", "/goal 解释\nHTTP 429: usage limit reached\n"} {
				if got := ad.cl.Classify(0, []byte(line)); got == model.ErrorQuotaHit || got == model.ErrorBudgetLimited {
					t.Fatalf("%q class %q", line, got)
				}
			}
		})
	}
	goal := runner.Grok{GoalMode: true}
	if got := goal.Classify(0, readFixture(t, "grok_goal_budget_limited.jsonl")); got != model.ErrorBudgetLimited {
		t.Fatalf("budget class %q", got)
	}
	if got := goal.Classify(0, []byte(goalBudget+"\n")); got == model.ErrorQuotaHit {
		t.Fatal("budget_limited classified as quota")
	}
	for _, ad := range adapters {
		if ad.grok {
			continue
		}
		if got := ad.cl.Classify(0, []byte(goalBudget+"\n")); got == model.ErrorQuotaHit || got == model.ErrorBudgetLimited {
			t.Fatalf("%s goal status %q", ad.name, got)
		}
	}
}

// TestSourceEchoAndLabels locks the B3 source rule. The echo is not a fixed
// number of lines after /goal and does not end on a blank line. An arbitrary
// colon label is not a diagnostic. A complete JSON error record still is.
func TestSourceEchoAndLabels(t *testing.T) {
	const phrase = "Antigravity 需要交互：动作被拒绝且没有可验收产物"
	const quotaJSON = `{"type":"error","message":"HTTP 429: usage limit reached"}`
	const permJSON = `{"type":"error","message":"permission denied"}`
	const authJSON = `{"type":"error","message":"authentication failed"}`
	const goalBudget = `{"method":"_x.ai/session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"goal_updated","status":"budget_limited","goal_id":"g1"}}}`
	adapters := []struct {
		name string
		cl   runner.Runner
	}{
		{"grok", runner.Grok{}},
		{"cursor", runner.Cursor{}},
		{"kimi", runner.Kimi{}},
		{"agy", runner.AGY{}},
	}
	type row struct {
		name string
		out  string
		want model.ErrorClass
		agy  model.ErrorClass
	}
	rows := []row{
		{name: "echo_multiline_quota", out: "/goal 解释\n第一项\nquota exceeded\n"},
		{name: "echo_blank_quota", out: "/goal 解释\n\nquota exceeded\n"},
		{name: "label_example_permission", out: "示例: permission denied\n"},
		{name: "label_example_relogin", out: "示例: 请重新登录\n"},
		{name: "json_after_goal", out: "/goal\n" + quotaJSON + "\n", want: model.ErrorQuotaHit},
		{name: "perm_json_after_goal", out: "/goal\n" + permJSON + "\n", want: model.ErrorPermissionDenied},
		{name: "auth_json_after_goal", out: "/goal\n" + authJSON + "\n", want: model.ErrorAuthFailed},
		{name: "json_after_multiline_blank_echo", out: "/goal 解释\n第一项\n\n" + quotaJSON + "\n", want: model.ErrorQuotaHit},
		{name: "json_after_blank_echo", out: "/goal 解释\n\n" + quotaJSON + "\n", want: model.ErrorQuotaHit},
		{name: "quota_line", out: "quota exceeded\n", want: model.ErrorQuotaHit},
		{name: "relogin", out: "认证错误: 请重新登录\n", want: model.ErrorAuthFailed},
		{name: "timeout_codex_error", out: "codex_error: 步骤超时(60 分钟)\n", want: model.ErrorTimeout},
		{name: "timeout_error_prefix", out: "error: context deadline exceeded\n", want: model.ErrorTimeout},
		{name: "agy_phrase", out: phrase + "\n", agy: model.ErrorPermissionDenied},
		{name: "agy_phrase_in_multiline_echo", out: "/goal 解释\n第一项\n" + phrase + "\n"},
		{name: "agy_phrase_in_blank_echo", out: "/goal 解释\n\n" + phrase + "\n"},
	}
	for _, ad := range adapters {
		for _, row := range rows {
			t.Run(ad.name+"/"+row.name, func(t *testing.T) {
				want := row.want
				if ad.name == "agy" && row.agy != "" {
					want = row.agy
				}
				if got := ad.cl.Classify(0, []byte(row.out)); got != want {
					t.Fatalf("class %q want %q", got, want)
				}
			})
		}
	}

	// Provenance: the echo span is Grok.Prompt, the text Drive writes after /goal.
	prompted := []struct {
		name   string
		prompt string
		out    string
		want   model.ErrorClass
	}{
		{name: "prompt_holds_multiline_quota", prompt: "解释\n第一项\nquota exceeded", out: "/goal 解释\n第一项\nquota exceeded\n"},
		{name: "prompt_holds_blank_quota", prompt: "解释\n\nquota exceeded", out: "/goal 解释\n\nquota exceeded\n"},
		{name: "prompt_holds_agy_phrase", prompt: "解释\n第一项\n" + phrase, out: "/goal 解释\n第一项\n" + phrase + "\n"},
		{name: "after_prompt_quota", prompt: "解释", out: "/goal 解释\nquota exceeded\n", want: model.ErrorQuotaHit},
		{name: "after_prompt_relogin", prompt: "解释", out: "/goal 解释\n认证错误: 请重新登录\n", want: model.ErrorAuthFailed},
		{name: "after_prompt_timeout", prompt: "解释", out: "/goal 解释\ncodex_error: 步骤超时(60 分钟)\n", want: model.ErrorTimeout},
		{name: "after_prompt_json", prompt: "解释\n\n第一项", out: "/goal 解释\n\n第一项\n" + quotaJSON + "\n", want: model.ErrorQuotaHit},
		// A known prompt line is not a diagnostic, even when that line is a
		// complete JSON error object or a goal_updated record. The same JSON
		// after the echo still classifies.
		{name: "prompt_json_quota", prompt: "解释\n" + quotaJSON, out: "/goal 解释\n" + quotaJSON + "\n"},
		{name: "prompt_json_permission", prompt: "解释\n" + permJSON, out: "/goal 解释\n" + permJSON + "\n"},
		{name: "prompt_json_auth", prompt: "解释\n" + authJSON, out: "/goal 解释\n" + authJSON + "\n"},
		{name: "prompt_json_goal_updated", prompt: "解释\n" + goalBudget, out: "/goal 解释\n" + goalBudget + "\n"},
		{name: "after_echo_json_quota", prompt: "解释", out: "/goal 解释\n" + quotaJSON + "\n", want: model.ErrorQuotaHit},
		{name: "after_echo_json_permission", prompt: "解释", out: "/goal 解释\n" + permJSON + "\n", want: model.ErrorPermissionDenied},
		{name: "after_echo_json_auth", prompt: "解释", out: "/goal 解释\n" + authJSON + "\n", want: model.ErrorAuthFailed},
		{name: "after_echo_json_goal_updated", prompt: "解释", out: "/goal 解释\n" + goalBudget + "\n", want: model.ErrorBudgetLimited},
	}
	for _, row := range prompted {
		t.Run("grok/"+row.name, func(t *testing.T) {
			got := (runner.Grok{Prompt: row.prompt}).Classify(0, []byte(row.out))
			if got != row.want {
				t.Fatalf("class %q want %q", got, row.want)
			}
		})
	}
}

type chunkRW struct {
	wrote  bytes.Buffer
	chunks [][]byte
	reads  int
}

func (c *chunkRW) Write(p []byte) (int, error) { return c.wrote.Write(p) }

func (c *chunkRW) Read(p []byte) (int, error) {
	if c.reads >= len(c.chunks) {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[c.reads])
	c.reads++
	return n, nil
}
