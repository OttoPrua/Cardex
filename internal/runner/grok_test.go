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
	"testing"

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
	// observeNativeGrokGoal / writeGrokGoalFixture), not PTY bytes.
	// Quota is v1 process stdout: grok_test.go fake grok-build error event.
	// Crash is v1 process error text: failure_class_test.go "signal: killed".
	completeDir := installSessionFixture(t, "grok_goal_complete.jsonl")
	budgetDir := installSessionFixture(t, "grok_goal_budget_limited.jsonl")
	notDir := installSessionFixture(t, "grok_goal_not_achieved.jsonl")
	complete, w1 := driveSession(t, prompt, completeDir, nil)
	budget, w2 := driveSession(t, prompt, budgetDir, nil)
	quota, w3 := driveGrok(t, prompt, "grok_goal_quota.jsonl")
	crash, w4 := driveGrok(t, prompt, "grok_goal_crash.txt")
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
	if !errors.Is(quota, runner.ErrGrokQuotaHit) {
		t.Fatalf("quota %v", quota)
	}
	if !errors.Is(crash, runner.ErrGrokCrash) {
		t.Fatalf("crash %v", crash)
	}
	if errors.Is(budget, runner.ErrGrokQuotaHit) || errors.Is(quota, runner.ErrGrokBudgetLimited) || errors.Is(crash, runner.ErrGrokBudgetLimited) {
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
	dir := t.TempDir()
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
	return dir
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
}

func TestClassifyIgnoresBodyEchoAndPartial(t *testing.T) {
	adapters := []struct {
		name string
		cl   runner.Runner
	}{
		{"grok", runner.Grok{}},
		{"cursor", runner.Cursor{}},
		{"kimi", runner.Kimi{}},
		{"agy", runner.AGY{}},
	}
	rows := []struct {
		name string
		out  string
	}{
		{"text", "{\"type\":\"text\",\"data\":\"解释 budget_limited\"}"},
		{"echo", "解释 budget_limited\n"},
		{"partial", "{\"type\":\"text\",\"data\":\"rate limit"},
		{"text-rate", "{\"type\":\"text\",\"data\":\"rate limit\"}"},
	}
	for _, ad := range adapters {
		for _, row := range rows {
			got := ad.cl.Classify(0, []byte(row.out))
			if got == model.ErrorBudgetLimited || got == model.ErrorQuotaHit {
				t.Fatalf("%s %s class %q", ad.name, row.name, got)
			}
		}
	}
	prompt := "finish the card\n"
	text := &chunkRW{chunks: [][]byte{
		[]byte(`{"type":"text","data":"rate limit`),
		[]byte(`"}`),
	}}
	g := runner.Grok{GoalMode: true, Prompt: prompt}
	if err := g.Drive(context.Background(), text); err == nil || errors.Is(err, runner.ErrGrokBudgetLimited) || errors.Is(err, runner.ErrGrokQuotaHit) {
		t.Fatalf("chunked text %v", err)
	}
	if text.reads < 2 {
		t.Fatalf("chunked text reads %d", text.reads)
	}
	echo := &scriptRW{src: bytes.NewReader([]byte("解释 budget_limited\n"))}
	if err := g.Drive(context.Background(), echo); err == nil || errors.Is(err, runner.ErrGrokBudgetLimited) || errors.Is(err, runner.ErrGrokQuotaHit) {
		t.Fatalf("echo drive %v", err)
	}
	body := &scriptRW{src: bytes.NewReader([]byte("{\"type\":\"text\",\"data\":\"解释 budget_limited\"}\n"))}
	if err := g.Drive(context.Background(), body); err == nil || errors.Is(err, runner.ErrGrokBudgetLimited) || errors.Is(err, runner.ErrGrokQuotaHit) {
		t.Fatalf("body drive %v", err)
	}
	quota := &chunkRW{chunks: [][]byte{
		[]byte(`{"type":"error","message":"HTTP 429: usage limit`),
		[]byte(` reached"}`),
	}}
	if err := g.Drive(context.Background(), quota); !errors.Is(err, runner.ErrGrokQuotaHit) {
		t.Fatalf("chunked error record %v", err)
	}
	if quota.reads < 2 {
		t.Fatalf("quota fired on the partial record after %d reads", quota.reads)
	}
	if g.Classify(0, []byte("{\"type\":\"error\",\"message\":\"HTTP 429: usage limit reached\"}")) != model.ErrorQuotaHit {
		t.Fatal("explicit error record")
	}
	if g.Classify(0, readFixture(t, "grok_goal_budget_limited.jsonl")) != model.ErrorBudgetLimited {
		t.Fatal("explicit budget record")
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
