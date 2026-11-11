package runner_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
)

func TestGrokHomeRespectsExisting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_LOG_FILE", "")
	t.Setenv("GROK_LOG_DIR", "")
	custom := filepath.Join(home, "custom-grok")
	t.Setenv("GROK_HOME", custom)

	got := (runner.Grok{}).WritablePaths()
	if len(got) != 1 || got[0] != custom {
		t.Fatalf("paths %q", got)
	}
	if _, err := os.Stat(custom); !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", custom, err)
	}
	def := filepath.Join(home, ".grok")
	if got[0] == def {
		t.Fatal("set GROK_HOME was replaced with ~/.grok")
	}

	g := runner.Grok{GoalMode: true, Prompt: "finish"}
	_, env, err := g.Build(runner.LaunchSpec{Model: "grok-4.6", WorkDir: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	want := "GROK_HOME=" + custom
	if len(env) == 0 || env[0] != want {
		t.Fatalf("env %q", env)
	}
	for _, item := range env {
		if strings.Contains(item, def) {
			t.Fatalf("env replaced GROK_HOME with the default: %q", env)
		}
	}

	t.Setenv("GROK_HOME", "")
	unset := (runner.Grok{}).WritablePaths()
	if len(unset) != 1 || unset[0] != def {
		t.Fatalf("unset paths %q", unset)
	}
	_, env, err = g.Build(runner.LaunchSpec{Model: "grok-4.6"})
	if err != nil {
		t.Fatal(err)
	}
	if len(env) == 0 || env[0] != "GROK_HOME="+def {
		t.Fatalf("unset env %q", env)
	}

	_, headless, err := (runner.Grok{}).Build(runner.LaunchSpec{Model: "--yolo"})
	if err != nil {
		t.Fatal(err)
	}
	if headless != nil {
		t.Fatalf("headless env %q", headless)
	}
}

func TestPrivilegeValuesAreNotFlags(t *testing.T) {
	forcePath := writePromptFile(t, "--force")
	yoloPath := writePromptFile(t, "--yolo")
	cases := []struct {
		name  string
		build func() ([]string, error)
		flag  string
		value string
		last  bool
	}{
		{
			name: "grok_model_yolo",
			build: func() ([]string, error) {
				cmd, _, err := (runner.Grok{}).Build(runner.LaunchSpec{Model: "--yolo", PromptPath: forcePath})
				return cmd, err
			},
			flag: "--model", value: "--yolo",
		},
		{
			name: "grok_prompt_file_yolo",
			build: func() ([]string, error) {
				cmd, _, err := (runner.Grok{}).Build(runner.LaunchSpec{Model: "m", PromptPath: "--yolo"})
				return cmd, err
			},
			flag: "--prompt-file", value: "--yolo",
		},
		{
			name: "grok_cwd_force",
			build: func() ([]string, error) {
				cmd, _, err := (runner.Grok{GoalMode: true}).Build(runner.LaunchSpec{Model: "m", WorkDir: "--force"})
				return cmd, err
			},
			flag: "--cwd", value: "--force",
		},
		{
			name: "grok_goal_model_yolo",
			build: func() ([]string, error) {
				cmd, _, err := (runner.Grok{GoalMode: true}).Build(runner.LaunchSpec{Model: "--yolo", WorkDir: "/work"})
				return cmd, err
			},
			flag: "--model", value: "--yolo",
		},
		{
			name: "cursor_body_force",
			build: func() ([]string, error) {
				cmd, _, err := (runner.Cursor{}).Build(runner.LaunchSpec{Model: "composer-2", PromptPath: forcePath, WorkDir: "/work"})
				return cmd, err
			},
			value: "--force", last: true,
		},
		{
			name: "cursor_model_yolo",
			build: func() ([]string, error) {
				cmd, _, err := (runner.Cursor{}).Build(runner.LaunchSpec{Model: "--yolo", PromptPath: forcePath, WorkDir: "/work"})
				return cmd, err
			},
			flag: "--model", value: "--yolo",
		},
		{
			name: "cursor_workspace_yolo",
			build: func() ([]string, error) {
				cmd, _, err := (runner.Cursor{}).Build(runner.LaunchSpec{Model: "m", PromptPath: yoloPath, WorkDir: "--yolo"})
				return cmd, err
			},
			flag: "--workspace", value: "--yolo",
		},
		{
			name: "kimi_model_yolo",
			build: func() ([]string, error) {
				cmd, _, err := (runner.Kimi{ConfigHome: t.TempDir()}).Build(runner.LaunchSpec{Model: "--yolo", PromptPath: forcePath})
				return cmd, err
			},
			flag: "--model", value: "--yolo",
		},
		{
			name: "kimi_prompt_force",
			build: func() ([]string, error) {
				cmd, _, err := (runner.Kimi{ConfigHome: t.TempDir()}).Build(runner.LaunchSpec{Model: "kimi-k2", PromptPath: forcePath})
				return cmd, err
			},
			flag: "--prompt", value: "--force",
		},
		{
			name: "agy_model_yolo",
			build: func() ([]string, error) {
				cmd, _, err := (runner.AGY{}).Build(runner.LaunchSpec{Model: "--yolo", PromptPath: forcePath})
				return cmd, err
			},
			flag: "--model", value: "--yolo",
		},
		{
			name: "agy_print_force",
			build: func() ([]string, error) {
				cmd, _, err := (runner.AGY{}).Build(runner.LaunchSpec{Model: "claude-opus-4-6", PromptPath: forcePath})
				return cmd, err
			},
			flag: "--print", value: "--force",
		},
		{
			name: "agy_print_yolo",
			build: func() ([]string, error) {
				cmd, _, err := (runner.AGY{}).Build(runner.LaunchSpec{Model: "claude-opus-4-6", PromptPath: yoloPath})
				return cmd, err
			},
			flag: "--print", value: "--yolo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := tc.build()
			if err != nil {
				t.Fatalf("build rejected a parameter value: %v", err)
			}
			if tc.last {
				if cmd[len(cmd)-1] != tc.value {
					t.Fatalf("body %q argv %q", cmd[len(cmd)-1], cmd)
				}
				return
			}
			found := false
			for i, arg := range cmd {
				if arg == tc.flag && i+1 < len(cmd) && cmd[i+1] == tc.value {
					found = true
				}
			}
			if !found {
				t.Fatalf("argv missing %s %s: %q", tc.flag, tc.value, cmd)
			}
		})
	}
}

func TestKimiPermissionKeyWhitespace(t *testing.T) {
	path := writePromptFile(t, "hello\n")
	cases := []struct {
		name string
		base string
		want string
	}{
		{
			name: "tab_before_equals",
			base: "default_permission_mode\t= \"manual\"\n" +
				"default_plan_mode\t= true\n" +
				"default_model = \"kimi-code/k3\"\n",
			want: "default_permission_mode = \"auto\"\n" +
				"default_plan_mode = false\n" +
				"\n" +
				"default_model = \"kimi-code/k3\"\n",
		},
		{
			name: "tab_around_equals_keeps_table",
			base: "default_permission_mode\t=\t\"manual\"\n" +
				"\n" +
				"[providers.demo]\n" +
				"default_permission_mode = \"manual\"\n",
			want: "default_permission_mode = \"auto\"\n" +
				"default_plan_mode = false\n" +
				"\n" +
				"\n" +
				"[providers.demo]\n" +
				"default_permission_mode = \"manual\"\n",
		},
		{
			name: "prefix_key_stays",
			base: "default_permission_mode_extra = 1\n" +
				"default_permission_mode\t= \"manual\"\n",
			want: "default_permission_mode = \"auto\"\n" +
				"default_plan_mode = false\n" +
				"\n" +
				"default_permission_mode_extra = 1\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(tc.base), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := (runner.Kimi{ConfigHome: home}).Build(runner.LaunchSpec{
				Model: "kimi-k2", PromptPath: path,
			}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(home, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("config\n got %q\nwant %q", got, tc.want)
			}
			if strings.Count(string(got), "default_permission_mode = \"manual\"") > 0 && tc.name == "tab_before_equals" {
				t.Fatal("old key kept beside the new key")
			}
		})
	}
}

func TestNestedErrorAndMultilineUsage(t *testing.T) {
	const nested = `{"error":{"message":"quota exceeded"}}`
	pretty := "{\n  \"error\": {\n    \"message\": \"quota exceeded\"\n  }\n}\n"
	const business = `{"type":"tool_use","input":{"message":"quota exceeded"}}`
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
		want model.ErrorClass
	}{
		{name: "nested_error_message", out: nested + "\n", want: model.ErrorQuotaHit},
		{name: "nested_error_message_document", out: pretty, want: model.ErrorQuotaHit},
		{name: "nested_error_msg", out: "{\"error\":{\"msg\":\"quota exceeded\"}}\n", want: model.ErrorQuotaHit},
		{name: "jsonl_error_string", out: "{\"type\":\"error\",\"message\":\"quota exceeded\"}\n", want: model.ErrorQuotaHit},
		{name: "nested_business_message", out: business + "\n", want: ""},
	}
	for _, ad := range adapters {
		for _, row := range rows {
			t.Run(ad.name+"/"+row.name, func(t *testing.T) {
				if got := ad.cl.Classify(0, []byte(row.out)); got != row.want {
					t.Fatalf("class %q want %q", got, row.want)
				}
			})
		}
	}

	type usageRow struct {
		name   string
		parse  func([]byte) model.Usage
		jsonl  string
		pretty string
		tokens int64
		source string
	}
	usage := []usageRow{
		{
			name:   "grok",
			parse:  runner.Grok{}.ParseUsage,
			jsonl:  `{"type":"end","usage":{"input_tokens":3,"output_tokens":2}}`,
			pretty: "{\n  \"type\": \"end\",\n  \"usage\": {\n    \"input_tokens\": 3,\n    \"output_tokens\": 2\n  }\n}\n",
			tokens: 5,
			source: "grok_build_end",
		},
		{
			name:   "cursor",
			parse:  runner.Cursor{}.ParseUsage,
			jsonl:  `{"type":"result","usage":{"inputTokens":3,"outputTokens":2}}`,
			pretty: "{\n  \"type\": \"result\",\n  \"usage\": {\n    \"inputTokens\": 3,\n    \"outputTokens\": 2\n  }\n}\n",
			tokens: 5,
			source: "cursor_result",
		},
		{
			name:   "agy",
			parse:  runner.AGY{}.ParseUsage,
			jsonl:  `{"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":9}}`,
			pretty: "{\n  \"usage\": {\n    \"input_tokens\": 3,\n    \"output_tokens\": 2,\n    \"total_tokens\": 9\n  }\n}\n",
			tokens: 9,
			source: "antigravity_result",
		},
	}
	for _, row := range usage {
		t.Run("usage/"+row.name, func(t *testing.T) {
			line := row.parse([]byte(row.jsonl + "\n"))
			doc := row.parse([]byte(row.pretty))
			if line.Tokens != row.tokens || doc.Tokens != line.Tokens {
				t.Fatalf("jsonl %+v document %+v", line, doc)
			}
			if line.Source != row.source || doc.Source != row.source || line.Confidence != 1 || doc.Confidence != 1 {
				t.Fatalf("jsonl %+v document %+v", line, doc)
			}
		})
	}
	if u := (runner.Kimi{}).ParseUsage([]byte(pretty)); u != (model.Usage{}) {
		t.Fatalf("kimi usage %+v", u)
	}
	if u := (runner.Kimi{}).ParseUsage([]byte(usage[0].jsonl + "\n")); u != (model.Usage{}) {
		t.Fatalf("kimi jsonl usage %+v", u)
	}
}

func TestGoalSessionSkipsCorruptUpdates(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		want    error
	}{
		{name: "complete", fixture: "grok_goal_complete.jsonl", want: nil},
		{name: "budget", fixture: "grok_goal_budget_limited.jsonl", want: runner.ErrGrokBudgetLimited},
	}
	junk := []string{
		"not json\n",
		"{\"params\":\n",
		"[]\n",
		"{}\n",
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := installSessionFixture(t, tc.fixture)
			path := filepath.Join(dir, "updates.jsonl")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			for _, line := range junk {
				b.WriteString(line)
			}
			b.Write(raw)
			for _, line := range junk {
				b.WriteString(line)
			}
			if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
				t.Fatal(err)
			}
			term, ok := runner.ReadGrokGoalSession(dir)
			if !ok || !errors.Is(term, tc.want) {
				t.Fatalf("reader ok=%v err=%v", ok, term)
			}
			got, _ := driveSession(t, "finish the card\n", dir, nil)
			if !errors.Is(got, tc.want) || (tc.want == nil && got != nil) {
				t.Fatalf("drive %v", got)
			}
		})
	}

	dir := installSessionFixture(t, "grok_goal_complete.jsonl")
	if err := os.WriteFile(filepath.Join(dir, "updates.jsonl"), []byte("not json\n{\"params\":\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.ReadGrokGoalSession(dir); ok {
		t.Fatal("corrupt file with no goal update became a terminal")
	}
}

func TestLaneNotesStayWithinLimit(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "v2", "lane-notes")
	for _, name := range []string{"R2-B.md", "R2-B-F1.md"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		n := bytes.Count(b, []byte("\n"))
		if n > 40 {
			t.Fatalf("%s has %d lines", name, n)
		}
	}
	note, err := os.ReadFile(filepath.Join(dir, "R2-B-F1.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(note)
	for _, heading := range []string{"## 做了什么", "## 没做什么，以及原因", "## 接口请求", "## 已知问题", "## 修复记录"} {
		if !strings.Contains(text, heading) {
			t.Fatalf("missing %s", heading)
		}
	}
	for _, id := range []string{"N1 ", "N2 ", "N3 ", "N4 ", "N5 ", "N7 ", "N8 ", "参数值不是权限旗标"} {
		if !strings.Contains(text, id) {
			t.Fatalf("missing %s", id)
		}
	}
}

func TestDriveEOFChecksBufferedQuota(t *testing.T) {
	const card = "You've hit the rate limit for your plan."
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "quota_sentence", text: card, want: true},
		{name: "headless_stderr", text: "quota exceeded", want: false},
		{name: "prefix", text: "You've hit the rate", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rw := &nilThenEOF{payload: []byte(tc.text)}
			err := runner.Grok{GoalMode: true, Prompt: "finish the card"}.Drive(context.Background(), rw)
			hit := errors.Is(err, runner.ErrGrokQuotaHit)
			if hit != tc.want {
				t.Fatalf("quota hit %v err %v", hit, err)
			}
			if rw.reads != 2 {
				t.Fatalf("reads %d", rw.reads)
			}
		})
	}
}

// nilThenEOF returns its payload with a nil error, then 0, io.EOF.
type nilThenEOF struct {
	payload []byte
	reads   int
	wrote   bytes.Buffer
}

func (r *nilThenEOF) Write(p []byte) (int, error) { return r.wrote.Write(p) }

func (r *nilThenEOF) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		return copy(p, r.payload), nil
	}
	return 0, io.EOF
}
