package runner_test

import (
	"strings"
	"testing"

	"cardex/internal/model"
	"cardex/internal/runner"
)

func TestCodexBuildMatchesTurnCompletedUsage(t *testing.T) {
	argv, _, err := runner.Codex{}.Build(runner.LaunchSpec{WorkDir: "/work", Model: "gpt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(argv, " "), "--json") {
		t.Fatalf("argv %q", argv)
	}
	doc := []byte("tokens=1\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":4,\"output_tokens\":6}}\n")
	got := runner.Codex{}.ParseUsage(doc)
	if got.Tokens != 10 || got.Source != "codex" || got.Confidence != 1 {
		t.Fatalf("codex usage %+v", got)
	}
	fake := runner.Fake{}.ParseUsage(doc)
	if fake.Tokens != 1 || fake.Source != "fake" {
		t.Fatalf("fake usage %+v", fake)
	}
}

func TestRealUsageIgnoresFakeMarker(t *testing.T) {
	cases := []struct {
		name   string
		parse  func([]byte) model.Usage
		output string
		tokens int64
		source string
	}{
		{
			name:   "claude",
			parse:  runner.Claude{}.ParseUsage,
			output: "the review mentions tokens=1\n{\"type\":\"result\",\"result\":\"done\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2},\"is_error\":false}\n",
			tokens: 7,
			source: "claude_result",
		},
		{
			name:   "codex",
			parse:  runner.Codex{}.ParseUsage,
			output: "tokens=1\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}\n",
			tokens: 3,
			source: "codex",
		},
		{
			name:   "opencode",
			parse:  runner.OpenCode{}.ParseUsage,
			output: "tokens=1 appears in the body\n{\"type\":\"step_finish\",\"part\":{\"tokens\":{\"input\":7,\"output\":5}}}\n",
			tokens: 12,
			source: "opencode_step_finish",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.parse([]byte(tc.output))
			if got.Tokens != tc.tokens || got.Source != tc.source || got.Confidence != 1 {
				t.Fatalf("%s %+v", tc.name, got)
			}
		})
	}
	fake := runner.Fake{}.ParseUsage([]byte("tokens=1\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":9,\"output_tokens\":9}}\n"))
	if fake.Tokens != 1 || fake.Source != "fake" {
		t.Fatalf("fake %+v", fake)
	}
}

func TestEngineProfileValidity(t *testing.T) {
	bad := []string{
		`{}`,
		`{"not_a_real_field":"x"}`,
		`{"base_url":" ","auth_env":" ","auth_value":"","auth_file":" ","models":{"opus":" "}}`,
	}
	for _, raw := range bad {
		if _, ok := runner.ParseEngineProfile([]byte(raw)); ok {
			t.Fatalf("parsed %s", raw)
		}
		argv, env, err := runner.Claude{}.Build(runner.LaunchSpec{Script: []byte(raw), Model: "sonnet"})
		if err == nil {
			t.Fatalf("fell through %s argv %v env %v", raw, argv, env)
		}
	}
	raw := []byte(`{"base_url":"https://example.test/v1","auth_value":"token-value"}`)
	if _, ok := runner.ParseEngineProfile(raw); !ok {
		t.Fatal("usable profile rejected")
	}
	_, env, err := runner.Claude{}.Build(runner.LaunchSpec{Script: raw, Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(env, "\n"), "ANTHROPIC_BASE_URL=https://example.test/v1") {
		t.Fatalf("env %v", env)
	}
	argv, env, err := runner.Claude{}.Build(runner.LaunchSpec{Model: "sonnet"})
	if err != nil || len(env) != 0 || !strings.Contains(strings.Join(argv, " "), "claude") {
		t.Fatalf("no profile argv %v env %v err %v", argv, env, err)
	}
}

func TestClassifyBodyLineAndLaterTerminal(t *testing.T) {
	body := "review passed\nerror: 401 Unauthorized\nall checks green\n"
	adapters := []runner.Runner{runner.Fake{}, runner.Claude{}, runner.Codex{}, runner.OpenCode{}}
	cases := []struct {
		name   string
		output string
		want   model.ErrorClass
	}{
		{name: "body-line", output: body, want: ""},
		{
			name:   "turn-failed-after-noise",
			output: "{\"type\":\"error\",\"error\":{\"message\":\"tool blew up\"}}\n" + body + "{\"type\":\"turn.failed\",\"error\":{\"message\":\"quota exhausted\"}}\n",
			want:   model.ErrorQuotaHit,
		},
		{
			name:   "budget-after-noise",
			output: "{\"type\":\"error\",\"error\":{\"message\":\"not a classified failure\"}}\n" + body + "{\"type\":\"budget_limited\"}\n",
			want:   model.ErrorBudgetLimited,
		},
		{
			name:   "success-result",
			output: "{\"type\":\"result\",\"is_error\":false,\"result\":\"error: 401 Unauthorized quota exhausted budget_limited\"}\n",
			want:   "",
		},
	}
	for _, tc := range cases {
		for _, r := range adapters {
			for _, exit := range []int{0, 7} {
				if got := r.Classify(exit, []byte(tc.output)); got != tc.want {
					t.Fatalf("%s %s exit %d => %q want %q", r.Name(), tc.name, exit, got, tc.want)
				}
			}
		}
	}
}

func TestClassifyIgnoresTaskBodyQuotes(t *testing.T) {
	body := "code review\nThe handler returns 401 Unauthorized when the token is missing.\nThe docs mention quota exhausted and a refusal example.\n"
	adapters := []runner.Runner{runner.Fake{}, runner.Claude{}, runner.Codex{}, runner.OpenCode{}}
	for _, r := range adapters {
		if got := r.Classify(0, []byte(body)); got != "" {
			t.Fatalf("%s exit 0 => %q", r.Name(), got)
		}
		if got := r.Classify(2, []byte(body)); got != "" {
			t.Fatalf("%s exit 2 => %q", r.Name(), got)
		}
	}
	cases := []struct {
		name   string
		output string
		want   model.ErrorClass
	}{
		{
			name:   "claude-exit-0",
			output: body + `{"type":"result","is_error":true,"subtype":"api_error","result":"401 Unauthorized"}` + "\n",
			want:   model.ErrorAuthFailed,
		},
		{
			name:   "codex-exit-0",
			output: body + `{"type":"turn.failed","error":{"message":"quota exhausted"}}` + "\n",
			want:   model.ErrorQuotaHit,
		},
		{
			name:   "opencode-exit-0",
			output: `{"type":"error","error":{"message":"refusal: the model refused the request"}}` + "\n" + body,
			want:   model.ErrorRefusal,
		},
		{
			name:   "body-error-line",
			output: body + "error: 401 Unauthorized\n",
			want:   "",
		},
		{
			name:   "budget-terminal",
			output: body + `{"type":"budget_limited"}` + "\n",
			want:   model.ErrorBudgetLimited,
		},
		{
			name:   "success-result-is-not-an-error",
			output: `{"type":"result","is_error":false,"result":"401 Unauthorized quota exhausted refusal"}` + "\n",
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, r := range adapters {
				if got := r.Classify(0, []byte(tc.output)); got != tc.want {
					t.Fatalf("%s %s => %q want %q", r.Name(), tc.name, got, tc.want)
				}
			}
		})
	}
}
