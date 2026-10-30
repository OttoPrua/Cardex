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
			name:   "explicit-line-exit-0",
			output: body + "error: authentication failed\n",
			want:   model.ErrorAuthFailed,
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
