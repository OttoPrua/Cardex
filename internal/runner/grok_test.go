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

	t.Run("b2_quota_interactive_sample", func(t *testing.T) {
		// No checked-in interactive TUI quota transcript. The headless line
		// above is fakeGrokBuild stdout, not goal-mode PTY output.
		err, wrote := driveGrok(t, "finish the card\n", "grok_goal_quota.jsonl")
		if string(wrote) != "/goal finish the card\r" {
			t.Fatalf("wrote %q", wrote)
		}
		if errors.Is(err, runner.ErrGrokQuotaHit) {
			t.Fatal("headless error JSON on the PTY is not interactive quota")
		}
		if got := (runner.Grok{}).Classify(0, []byte(headless)); got != model.ErrorQuotaHit {
			t.Fatalf("headless error record class %q", got)
		}
		note, err := os.ReadFile(filepath.Join("..", "..", "docs", "v2", "lane-notes", "R2-B.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(note), "没有可核对的交互") {
			t.Fatal("lane note must say there is no checkable interactive quota sample")
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
	}{
		{"grok", runner.Grok{}},
		{"cursor", runner.Cursor{}},
		{"kimi", runner.Kimi{}},
		{"agy", runner.AGY{}},
	}
	type row struct {
		name   string
		out    string
		chunks []string
		grok   model.ErrorClass
		others model.ErrorClass
	}
	rows := []row{
		{name: "b3_echo_goal_rate_limit", out: "/goal 解释 rate limit\n"},
		{name: "b3_echo_goal_rate_limit_chunked", chunks: []string{"/goal 解释 rate lim", "it\n"}},
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
				if ad.name == "grok" {
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
