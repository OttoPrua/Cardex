package runner_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
)

func TestCursorBuildSnapshot(t *testing.T) {
	path := writePromptFile(t, snapshotPrompt)
	cmd, env, err := (runner.Cursor{}).Build(runner.LaunchSpec{
		Model:      "composer-2",
		PromptPath: path,
		WorkDir:    "/work/tree",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cursor-agent",
		"--print", "--trust", "--output-format", "stream-json",
		"--model", "composer-2", "--workspace", "/work/tree", "--sandbox", "enabled",
		snapshotPrompt,
	}
	if !slices.Equal(cmd, want) {
		t.Fatalf("argv\n got %q\nwant %q", cmd, want)
	}
	if env != nil {
		t.Fatalf("env %q", env)
	}
	assertNoArg(t, cmd, "--auto-review", "--force", "--yolo", "--mode", "--resume")
	if (runner.Cursor{}).NeedsPTY() {
		t.Fatal("NeedsPTY")
	}
	if (runner.Cursor{}).Name() != "cursor" {
		t.Fatalf("name %s", (runner.Cursor{}).Name())
	}
	if caps := (runner.Cursor{}).Capabilities(); len(caps) != 1 || caps[0] != "goal_mode" {
		t.Fatalf("capabilities %q", (runner.Cursor{}).Capabilities())
	}
	if _, _, err := (runner.Cursor{}).Build(runner.LaunchSpec{}); err != nil {
		t.Fatal(err)
	}
	if err := (runner.Cursor{}).Drive(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestCursorClassifyAndUsage(t *testing.T) {
	c := runner.Cursor{}
	cases := []struct {
		file string
		want model.ErrorClass
	}{
		{"cursor_quota.txt", model.ErrorQuotaHit},
		{"cursor_policy.txt", model.ErrorPermissionDenied},
		{"cursor_transport.jsonl", model.ErrorTransport},
		{"cursor_auth.txt", model.ErrorAuthFailed},
		{"cursor_semantic_timeout.txt", ""},
		{"cursor_usage_result.json", ""},
		{"grok_limit_like.txt", ""},
		{"input_too_long.txt", ""},
	}
	for _, tc := range cases {
		got := c.Classify(0, readFixture(t, tc.file))
		if got != tc.want {
			t.Errorf("%s: class %q want %q", tc.file, got, tc.want)
		}
	}
	usage := c.ParseUsage(readFixture(t, "cursor_usage_result.json"))
	if usage.Tokens != 19 || usage.Source != "cursor_result" || usage.Confidence != 1 {
		t.Fatalf("usage %+v", usage)
	}
	if u := c.ParseUsage(readFixture(t, "cursor_quota.txt")); u != (model.Usage{}) {
		t.Fatalf("soft usage %+v", u)
	}
}

func TestCursorWritablePathsAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := (runner.Cursor{}).WritablePaths()
	want := filepath.Join(home, ".cursor")
	if !slices.Equal(got, []string{want}) {
		t.Fatalf("paths %q", got)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", want, err)
	}
}
