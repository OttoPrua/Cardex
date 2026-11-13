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
	if (runner.Kimi{}).NeedsPTY() || (runner.Kimi{}).Name() != "kimi" || strings.Join((runner.Kimi{}).Capabilities(), ",") != "goal_mode" {
		t.Fatalf("meta pty=%v name=%s caps=%q", (runner.Kimi{}).NeedsPTY(), (runner.Kimi{}).Name(), (runner.Kimi{}).Capabilities())
	}
	_, env, err = (runner.Kimi{}).Build(runner.LaunchSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(env, []string{"KIMI_CODE_NO_AUTO_UPDATE=1"}) {
		t.Fatalf("default env %q", env)
	}
	if err := (runner.Kimi{}).Drive(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestKimiBuildInheritsConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	configPath := filepath.Join(home, "config.toml")
	const config = "default_permission_mode = \"manual\"\n" +
		"default_plan_mode = true\n\n" +
		"[models.\"kimi-code/k3.1-pro\"]\n" +
		"provider = \"kimi-code\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, configHome := range []string{"", " \t "} {
		_, env, err := (runner.Kimi{ConfigHome: configHome}).Build(runner.LaunchSpec{
			Model: "kimi-code/k3.1-pro",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(env, []string{"KIMI_CODE_NO_AUTO_UPDATE=1"}) {
			t.Fatalf("ConfigHome %q: env %q", configHome, env)
		}
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != config {
		t.Fatalf("inherited config changed: %q", got)
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
