package runner_test

import (
	"bytes"
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cardex/internal/model"
	"cardex/internal/runner"
)

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
	complete, w1 := driveGrok(t, prompt, "grok_goal_complete.jsonl")
	budget, w2 := driveGrok(t, prompt, "grok_goal_budget_limited.jsonl")
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

	notAchieved, wn := driveGrok(t, prompt, "grok_goal_not_achieved.jsonl")
	if string(wn) != written {
		t.Fatalf("wrote %q", wn)
	}
	if notAchieved == nil || errors.Is(notAchieved, runner.ErrGrokBudgetLimited) || errors.Is(notAchieved, runner.ErrGrokQuotaHit) || errors.Is(notAchieved, runner.ErrGrokCrash) {
		t.Fatalf("not_achieved %v", notAchieved)
	}
	if g.Classify(0, readFixture(t, "grok_goal_not_achieved.jsonl")) != "" {
		t.Fatal("classify not_achieved")
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
