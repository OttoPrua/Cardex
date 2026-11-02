package runner_test

import (
	"slices"
	"testing"

	"cardex/internal/model"
	"cardex/internal/runner"
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
	if (runner.AGY{}).NeedsPTY() || (runner.AGY{}).Name() != "agy" || len((runner.AGY{}).Capabilities()) != 0 {
		t.Fatalf("meta pty=%v name=%s caps=%q", (runner.AGY{}).NeedsPTY(), (runner.AGY{}).Name(), (runner.AGY{}).Capabilities())
	}
	if (runner.AGY{}).WritablePaths() != nil {
		t.Fatalf("paths %q", (runner.AGY{}).WritablePaths())
	}
	if _, _, err := (runner.AGY{}).Build(runner.LaunchSpec{}); err != nil {
		t.Fatal(err)
	}
	if err := (runner.AGY{}).Drive(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
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
