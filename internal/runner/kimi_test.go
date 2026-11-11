package runner_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
)

func TestKimiBuildSnapshot(t *testing.T) {
	home := t.TempDir()
	path := writePromptFile(t, snapshotPrompt)
	cmd, env, err := (runner.Kimi{ConfigHome: home}).Build(runner.LaunchSpec{
		Model:      "kimi-k2",
		PromptPath: path,
		WorkDir:    "/work/tree",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"kimi",
		"--model", "kimi-k2",
		"--prompt", snapshotPrompt,
		"--output-format", "stream-json",
	}
	if !slices.Equal(cmd, want) {
		t.Fatalf("argv\n got %q\nwant %q", cmd, want)
	}
	if !slices.Equal(env, []string{"KIMI_CODE_NO_AUTO_UPDATE=1", "KIMI_CODE_HOME=" + home}) {
		t.Fatalf("env %q", env)
	}
	assertNoArg(t, cmd, "--auto", "--yolo", "--plan", "--session")
	for _, item := range env {
		if strings.HasPrefix(item, "KIMI_MODEL_THINKING_EFFORT=") {
			t.Fatalf("env %q", env)
		}
	}
	if (runner.Kimi{}).NeedsPTY() || (runner.Kimi{}).Name() != "kimi" || len((runner.Kimi{}).Capabilities()) != 0 {
		t.Fatalf("meta pty=%v name=%s caps=%q", (runner.Kimi{}).NeedsPTY(), (runner.Kimi{}).Name(), (runner.Kimi{}).Capabilities())
	}
	if _, _, err := (runner.Kimi{}).Build(runner.LaunchSpec{}); err != nil {
		t.Fatal(err)
	}
	if err := (runner.Kimi{}).Drive(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestKimiClassifyAndUsage(t *testing.T) {
	k := runner.Kimi{}
	cases := []struct {
		file string
		want model.ErrorClass
	}{
		{"kimi_quota.jsonl", model.ErrorQuotaHit},
		{"kimi_auth.txt", model.ErrorAuthFailed},
		{"kimi_permission.txt", model.ErrorPermissionDenied},
		{"kimi_timeout.txt", model.ErrorTimeout},
		{"grok_limit_like.txt", ""},
		{"input_too_long.txt", ""},
	}
	for _, tc := range cases {
		got := k.Classify(0, readFixture(t, tc.file))
		if got != tc.want {
			t.Errorf("%s: class %q want %q", tc.file, got, tc.want)
		}
	}
	if u := k.ParseUsage(readFixture(t, "grok_usage_end.json")); u != (model.Usage{}) {
		t.Fatalf("usage %+v", u)
	}
	if u := k.ParseUsage(readFixture(t, "kimi_quota.jsonl")); u != (model.Usage{}) {
		t.Fatalf("quota usage %+v", u)
	}
}

func TestKimiWritablePathsAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := (runner.Kimi{}).WritablePaths()
	want := filepath.Join(home, ".kimi-code")
	if !slices.Equal(got, []string{want}) {
		t.Fatalf("paths %q", got)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", want, err)
	}
}
