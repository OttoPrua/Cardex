package runner_test

import (
	"slices"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
)

func TestAGYBuildSnapshot(t *testing.T) {
	path := writePromptFile(t, snapshotPrompt)
	cmd, env, err := (runner.AGY{}).Build(runner.LaunchSpec{
		Model:      "claude-opus-4-6",
		PromptPath: path,
		WorkDir:    "/work/tree",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"agy",
		"--output-format", "json",
		"--mode", "accept-edits",
		"--model", "claude-opus-4-6",
		"--effort", "high",
		"--disable-slash-commands",
		"--dangerously-skip-permissions",
		"--print", snapshotPrompt,
	}
	if !slices.Equal(cmd, want) {
		t.Fatalf("argv\n got %q\nwant %q", cmd, want)
	}
	if !slices.Equal(env, []string{"NO_COLOR=1"}) {
		t.Fatalf("env %q", env)
	}
	assertNoArg(t, cmd, "--cwd")
	thinking, _, err := (runner.AGY{}).Build(runner.LaunchSpec{
		Model:      "claude-opus-4-6-thinking",
		PromptPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantThinking := []string{
		"agy",
		"--output-format", "json",
		"--mode", "accept-edits",
		"--model", "claude-opus-4-6-thinking",
		"--disable-slash-commands",
		"--dangerously-skip-permissions",
		"--print", snapshotPrompt,
	}
	if !slices.Equal(thinking, wantThinking) {
		t.Fatalf("thinking argv\n got %q\nwant %q", thinking, wantThinking)
	}
	assertNoArg(t, thinking, "--effort")
	if !argPair(cmd, "--effort", "high") {
		t.Fatalf("default argv missing --effort high: %q", cmd)
	}
	for _, modelName := range []string{"gemini-4-argon-medium", "gemini-4-argon-low", "gemini-4-argon-high"} {
		got, _, err := (runner.AGY{}).Build(runner.LaunchSpec{Model: modelName, PromptPath: path})
		if err != nil {
			t.Fatal(err)
		}
		if argPair(got, "--effort", "high") {
			t.Fatalf("%s argv contains --effort high: %q", modelName, got)
		}
	}
	if (runner.AGY{}).NeedsPTY() || (runner.AGY{}).Name() != "agy" || len((runner.AGY{}).Capabilities()) != 0 {
		t.Fatalf("meta pty=%v name=%s caps=%q", (runner.AGY{}).NeedsPTY(), (runner.AGY{}).Name(), (runner.AGY{}).Capabilities())
	}
	if _, _, err := (runner.AGY{}).Build(runner.LaunchSpec{}); err != nil {
		t.Fatal(err)
	}
	if err := (runner.AGY{}).Drive(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func argPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestAGYClassifyAndUsage(t *testing.T) {
	a := runner.AGY{}
	cases := []struct {
		file string
		want model.ErrorClass
	}{
		{"agy_quota.json", model.ErrorQuotaHit},
		{"agy_auth.txt", model.ErrorAuthFailed},
		{"agy_permission.txt", model.ErrorPermissionDenied},
		{"agy_usage.json", model.ErrorPermissionDenied},
		{"agy_truncated.json", model.ErrorResultUnparsable},
		// Real B1103 captures (2026-11-12): a retired model id and a region refusal.
		{"agy_model_unknown.txt", model.ErrorUnavailable},
		{"agy_region.txt", model.ErrorUnavailable},
		{"grok_limit_like.txt", ""},
		{"input_too_long.txt", ""},
	}
	for _, tc := range cases {
		got := a.Classify(0, readFixture(t, tc.file))
		if got != tc.want {
			t.Errorf("%s: class %q want %q", tc.file, got, tc.want)
		}
	}
	usage := a.ParseUsage(readFixture(t, "agy_usage.json"))
	if usage.Tokens != 7982 || usage.Source != "antigravity_result" || usage.Confidence != 1 {
		t.Fatalf("usage %+v", usage)
	}
	if u := a.ParseUsage(readFixture(t, "agy_quota.json")); u != (model.Usage{}) {
		t.Fatalf("quota usage %+v", u)
	}
}
