package runner_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cardex/internal/model"
	"cardex/internal/runner"
)

func TestRegistryAndCapabilities(t *testing.T) {
	for _, name := range []string{"fake", "claude", "codex", "opencode"} {
		got, ok := runner.Lookup(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if got.Name() != name {
			t.Fatalf("name %q", got.Name())
		}
		for _, cap := range got.Capabilities() {
			if cap == "web" || cap == "goal_mode" {
				t.Fatalf("%s invented capability %s", name, cap)
			}
		}
		if name == "fake" {
			if strings.Join(got.Capabilities(), ",") != "fake" {
				t.Fatalf("fake capabilities %v", got.Capabilities())
			}
			continue
		}
		if len(got.Capabilities()) != 0 {
			t.Fatalf("%s capabilities %v", name, got.Capabilities())
		}
	}
	if _, ok := runner.Lookup("grok"); ok {
		t.Fatal("grok is registered in this lane")
	}
}

func TestBuildSnapshots(t *testing.T) {
	claude, _, err := runner.Claude{}.Build(runner.LaunchSpec{Model: "claude-opus"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(claude, " ")
	for _, needle := range []string{"claude -p", "--output-format json", "--dangerously-skip-permissions", "--model claude-opus"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("claude argv %q missing %s", joined, needle)
		}
	}

	codex, _, err := runner.Codex{}.Build(runner.LaunchSpec{WorkDir: "/work", Model: "gpt"})
	if err != nil {
		t.Fatal(err)
	}
	codexLine := strings.Join(codex, " ")
	for _, needle := range []string{"codex exec", "--sandbox workspace-write", "-C /work", "-m gpt"} {
		if !strings.Contains(codexLine, needle) {
			t.Fatalf("codex argv %q missing %s", codexLine, needle)
		}
	}

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	promptText := "ship the card"
	if err := os.WriteFile(promptPath, []byte(promptText), 0o644); err != nil {
		t.Fatal(err)
	}
	opencode, _, err := runner.OpenCode{}.Build(runner.LaunchSpec{WorkDir: "/work", Model: "m1", PromptPath: promptPath})
	if err != nil {
		t.Fatal(err)
	}
	openLine := strings.Join(opencode, " ")
	for _, needle := range []string{"opencode run", "--pure", "--format json", "--auto", "--model m1", "--dir /work"} {
		if !strings.Contains(openLine, needle) {
			t.Fatalf("opencode argv %q missing %s", openLine, needle)
		}
	}
	if opencode[len(opencode)-1] != promptText {
		t.Fatalf("prompt text not positional: %q", opencode)
	}
	if _, _, err := (runner.OpenCode{}).Build(runner.LaunchSpec{PromptPath: filepath.Join(t.TempDir(), "missing.md")}); err == nil {
		t.Fatal("missing prompt file was ignored")
	}
}

func TestEngineEnvStripsOtherProfile(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "secret-kimi-value")
	t.Setenv("ANTHROPIC_BASE_URL", "https://leftover.example")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "leftover-token")
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "leftover-opus")
	profile, ok := runner.EnginePreset("kimi")
	if !ok {
		t.Fatal("missing kimi preset")
	}
	env, err := runner.ApplyEngineEnv([]string{
		"ANTHROPIC_BASE_URL=https://leftover.example",
		"ANTHROPIC_AUTH_TOKEN=leftover-token",
		"ANTHROPIC_DEFAULT_OPUS_MODEL=leftover-opus",
		"KEEP=yes",
	}, profile)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, dup := got[k]; dup {
			t.Fatalf("duplicate %s", k)
		}
		got[k] = v
	}
	if got["ANTHROPIC_BASE_URL"] != "https://api.kimi.com/coding/" {
		t.Fatalf("base url %q", got["ANTHROPIC_BASE_URL"])
	}
	if got["ANTHROPIC_API_KEY"] != "secret-kimi-value" {
		t.Fatalf("auth var %#v", got["ANTHROPIC_API_KEY"])
	}
	if got["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "k3" {
		t.Fatalf("opus model %q", got["ANTHROPIC_DEFAULT_OPUS_MODEL"])
	}
	if strings.Contains(strings.Join(env, "\n"), "leftover") {
		t.Fatalf("leftover profile env survived: %s", strings.Join(env, "\n"))
	}
	if got["KEEP"] != "yes" {
		t.Fatal("unrelated env dropped")
	}

	rawProfile := []byte(`{"base_url":"https://api.kimi.com/coding/","auth_env":"KIMI_API_KEY","auth_var":"ANTHROPIC_API_KEY","models":{"opus":"k3"}}`)
	argv, built, err := runner.Claude{}.Build(runner.LaunchSpec{Model: "opus", Script: rawProfile})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(argv, " "), "--model k3") {
		t.Fatalf("model was not mapped: %v", argv)
	}
	if !strings.Contains(strings.Join(built, "\n"), "ANTHROPIC_BASE_URL=https://api.kimi.com/coding/") {
		t.Fatalf("build env missing base url: %v", built)
	}
	if strings.Contains(strings.Join(built, "\n"), "leftover-token") || strings.Contains(strings.Join(built, "\n"), "leftover-opus") {
		t.Fatalf("build kept another profile: %v", built)
	}

	_, err = runner.ApplyEngineEnv(nil, runner.EngineProfile{AuthEnv: "CARDEX_MISSING_AUTH_FOR_TEST", AuthValue: "super-secret-value"})
	if err == nil || strings.Contains(err.Error(), "super-secret-value") {
		t.Fatalf("auth error leaked or succeeded: %v", err)
	}
}

func TestClassifyV1Samples(t *testing.T) {
	cases := []struct {
		text string
		want model.ErrorClass
	}{
		{"codex_error: 401 Unauthorized from api", model.ErrorAuthFailed},
		{"authentication failed: invalid api key", model.ErrorAuthFailed},
		{"oauth token has expired, please re-login", model.ErrorAuthFailed},
		{"认证错误: 请重新登录", model.ErrorAuthFailed},
		{"remote_codex_error: 403 Forbidden", model.ErrorPermissionDenied},
		{"access denied by policy: cannot write to /etc", model.ErrorPermissionDenied},
		{"operation blocked by organization admin", model.ErrorPermissionDenied},
		{"HTTP 429: usage limit reached", model.ErrorQuotaHit},
		{"quota exhausted", model.ErrorQuotaHit},
		{"用量限额", model.ErrorQuotaHit},
		{"401 unauthorized; quota exhausted", model.ErrorAuthFailed},
		{"permission denied; quota exhausted", model.ErrorPermissionDenied},
		{"budget_limited", model.ErrorBudgetLimited},
		{"步骤超时(60 分钟)", model.ErrorTimeout},
		{"远程步骤超时(60 分钟)", model.ErrorTimeout},
		{"context deadline exceeded", model.ErrorTimeout},
		{"read tcp 10.0.0.1:443: i/o timeout", model.ErrorTransport},
		{"dial tcp: lookup api.example: no such host", model.ErrorTransport},
		{"proxyconnect tcp: connection refused 127.0.0.1", model.ErrorTransport},
		{"the model refusal", model.ErrorRefusal},
		{"quota nearly consumed with 5% remaining", ""},
		{"context length exceeded: 200000 tokens", ""},
		{"prompt is too long for this model", ""},
		{"signal: killed", ""},
		{"signal: segmentation fault", ""},
		{`exec: "codex": executable file not found in $PATH`, ""},
		{"context canceled", ""},
	}
	adapters := []runner.Runner{runner.Fake{}, runner.Claude{}, runner.Codex{}, runner.OpenCode{}}
	for _, tc := range cases {
		if got := runner.ClassifyOutput(tc.text); got != tc.want {
			t.Fatalf("ClassifyOutput %q => %q want %q", tc.text, got, tc.want)
		}
		for _, r := range adapters {
			if got := r.Classify(1, []byte(tc.text)); got != tc.want {
				t.Fatalf("%s %q => %q want %q", r.Name(), tc.text, got, tc.want)
			}
		}
	}
}

func TestParseUsageShapes(t *testing.T) {
	fake := runner.ParseOutputUsage([]byte("noise\ntokens=17\n"))
	if fake.Tokens != 17 || fake.Source != "fake" || fake.Confidence != 1 {
		t.Fatalf("fake usage %+v", fake)
	}
	claude := `{"type":"result","result":"done","usage":{"input_tokens":5,"output_tokens":2},"is_error":false}`
	got := runner.Claude{}.ParseUsage([]byte(claude))
	if got.Tokens != 7 || got.Source != "claude_result" || got.Confidence != 1 {
		t.Fatalf("claude usage %+v", got)
	}
	total := `{"type":"result","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":99}}`
	got = runner.ParseOutputUsage([]byte(total))
	if got.Tokens != 99 || got.Source != "claude_result" {
		t.Fatalf("total_tokens %+v", got)
	}
	codex := `{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":1}}`
	got = runner.Codex{}.ParseUsage([]byte(codex))
	if got.Tokens != 3 || got.Source != "codex" || got.Confidence != 1 {
		t.Fatalf("codex usage %+v", got)
	}
	open := `{"type":"step_finish","sessionID":"ses-1","part":{"reason":"stop","tokens":{"input":7,"output":5},"cost":0.25}}`
	got = runner.OpenCode{}.ParseUsage([]byte(open))
	if got.Tokens != 12 || got.Source != "opencode_step_finish" || got.Confidence != 1 {
		t.Fatalf("opencode usage %+v", got)
	}
	if got := runner.ParseOutputUsage([]byte("not json and no tokens")); got != (model.Usage{}) {
		t.Fatalf("unparseable %+v", got)
	}
	if got := runner.ParseOutputUsage(nil); got != (model.Usage{}) {
		t.Fatalf("empty %+v", got)
	}
}

func TestWritablePathsDoNotTouchDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sets := [][]string{
		runner.Claude{}.WritablePaths(),
		runner.Codex{}.WritablePaths(),
		runner.OpenCode{}.WritablePaths(),
	}
	for _, paths := range sets {
		if len(paths) < 2 {
			t.Fatalf("paths %v", paths)
		}
		for _, p := range paths {
			if !strings.HasPrefix(p, home+string(filepath.Separator)) {
				t.Fatalf("path %s is outside HOME %s", p, home)
			}
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("path %s exists or was created: %v", p, err)
			}
		}
	}
	if runtime.GOOS == "darwin" {
		if !strings.Contains(strings.Join(runner.Claude{}.WritablePaths(), " "), filepath.Join(home, "Library", "Caches", "claude")) {
			t.Fatal("darwin claude cache path missing")
		}
	}
	fake := runner.Fake{}
	if fake.WritablePaths() != nil {
		t.Fatalf("fake paths %v", fake.WritablePaths())
	}
}
