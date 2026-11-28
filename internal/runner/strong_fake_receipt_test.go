package runner

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
)

func TestStrongFakeReceiptRejectsAmbiguousEvents(t *testing.T) {
	for _, raw := range []string{
		"tokens=2\ntokens=2\n", "tokens=2\ntokens=3\n", "tokens=2\ntokens=x\n", "tokens=02\n",
		" tokens=2\n", "tokens=2 \n", "tokens=2.0\n", "TOKENS=2\n", "tokens=0\n", "tokens=2\nusage: tokens unknown\n",
	} {
		if got := strongFakeExactUsage([]byte(raw)); got.Source != "" {
			t.Fatalf("accepted ambiguous usage %q: %+v", raw, got)
		}
	}
	if got := strongFakeExactUsage([]byte("trace\ntokens=2\n")); got.Source != "strong_fake_child" || got.Tokens != 2 || got.Confidence != 1 {
		t.Fatalf("valid receipt: %+v", got)
	}
	if strictStrongTransport([]byte("strong_fake_input_sha=approved\nstrong_fake_input_sha=approved\n"), "approved") {
		t.Fatal("duplicated child proof accepted")
	}
}

func TestStrongFakeLaunchRejectsOverridesAndDiagnosticPaths(t *testing.T) {
	if !strongcustody.Supported() {
		t.Skip("unsupported local custody")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "input.json")
	script := filepath.Join(parent, "script.json")
	work := filepath.Join(parent, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"task":"bounded local fixture"}`)
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	digest := model.StrongArtifactDigest(input)
	challenge := strings.Repeat("a", 64)
	good := []byte(`{"attempts":{"0":{"result":"## 结论\noriginal fixture","output":"tokens=7\n"}}}`)
	writeScript := func(v []byte) {
		if err := os.WriteFile(script, v, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeScript(good)
	base := LaunchSpec{AttemptID: "synthetic-attempt", WorkDir: work, PromptPath: path, ScriptPath: script}
	for name, mutate := range map[string]func(*LaunchSpec){
		"inline bytes": func(s *LaunchSpec) { s.Script = good },
		"inline env override": func(s *LaunchSpec) {
			s.Script = []byte(`{"attempts":{"0":{"env":{"CARDEX_FAKE_WORKDIR":"/tmp/redirected"}}}}`)
		},
		"workdir newline":        func(s *LaunchSpec) { s.WorkDir += "-\ntokens=999" },
		"prompt newline":         func(s *LaunchSpec) { s.PromptPath += "-\ntokens=999" },
		"script carriage return": func(s *LaunchSpec) { s.ScriptPath += "-\rtokens=999" },
	} {
		t.Run(name, func(t *testing.T) {
			got := base
			mutate(&got)
			called := false
			allow := func() error { called = true; return nil }
			if r, _, err := RunStrongFakeChild(t.Context(), got, digest, challenge, allow, allow); r != nil || !errors.Is(err, model.ErrStrongArtifact) || called {
				t.Fatalf("override reached authorization: %v %v %t", r, err, called)
			}
		})
	}
	for _, field := range []string{"CARDEX_FAKE_WORKDIR", "CARDEX_FAKE_SCRIPT", "CARDEX_FAKE_PROMPT"} {
		v := []byte(fmt.Sprintf(`{"attempts":{"0":{"result":"## 结论\naffected","env":{"%s":"/tmp/redirected"}}}}`, field))
		writeScript(v)
		called := false
		allow := func() error { called = true; return nil }
		if r, _, err := RunStrongFakeChild(t.Context(), base, digest, challenge, allow, allow); r != nil || !errors.Is(err, model.ErrStrongArtifact) || called {
			t.Fatalf("disk env override %s reached authorization: %v %v", field, r, err)
		}
	}
	writeScript(good)
	out, stdout, err := RunStrongFakeChild(t.Context(), base, digest, challenge, func() error {
		// A disk mutation after validation cannot replace the frozen stdin.
		return os.WriteFile(script, []byte(`{"attempts":{"0":{"result":"## 结论\nchanged fixture","output":"tokens=999\n"}}}`), 0600)
	}, func() error { return nil })
	if err != nil || out.Record().Usage.Tokens != 7 || bytes.Contains(stdout, []byte("prompt=")) {
		t.Fatalf("changed bytes or path diagnostic reached receipt: %v %q %v", out, stdout, err)
	}
	bytesRead, err := os.ReadFile(filepath.Join(work, ".cardex-result.md"))
	if err != nil || !strings.Contains(string(bytesRead), "original fixture") || strings.Contains(string(bytesRead), "changed fixture") {
		t.Fatalf("worker reopened script: %s %v", bytesRead, err)
	}
}

func TestStrongFakeAmbientEnvironmentDoesNotDivertOrdinaryChild(t *testing.T) {
	d := t.TempDir()
	input := filepath.Join(d, "ordinary-prompt.txt")
	script := filepath.Join(d, "ordinary-script.json")
	if err := os.WriteFile(input, []byte("ordinary fixture prompt"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"attempts":{"0":{"result":"## 结论\nordinary fixture","output":"tokens=4\n"}}}`)
	if err := os.WriteFile(script, raw, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	arg, env, err := (Fake{}).Build(LaunchSpec{AttemptID: "ordinary-synthetic", WorkDir: d, PromptPath: input, ScriptPath: script})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), exe, arg[1:]...)
	cmd.Env = append([]string{"CARDEX_FAKE_CHILD=1", strongFakeBundleEnv + "=" + strings.Repeat("1", 64),
		strongFakeChallengeEnv + "=" + strings.Repeat("2", 64), strongFakeScriptSHAEnv + "=" + strings.Repeat("3", 64)}, env...)
	for _, key := range []string{"SystemRoot", "WINDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("tokens=4")) || bytes.Contains(out, []byte(strongFakeBundlePrefix)) {
		t.Fatalf("ordinary fake behavior changed: %q %v", out, err)
	}
	result, err := os.ReadFile(filepath.Join(d, ".cardex-result.md"))
	if err != nil || !bytes.Contains(result, []byte("ordinary fixture")) {
		t.Fatalf("ordinary result: %q %v", result, err)
	}
}
