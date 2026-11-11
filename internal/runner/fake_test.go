package runner_test

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
)

func TestFakeScriptBehaviors(t *testing.T) {
	script := map[string]any{
		"attempts": map[string]any{
			"0": map[string]any{
				"write_files": []map[string]string{{
					"path":    "calc.go",
					"content": "package fixture\n\nfunc Answer() int {\n\treturn 1\n}\n\ntype Box struct{}\n\nfunc (Box) N() int {\n\treturn 1\n}\n",
				}},
				"change_funcs": []map[string]string{
					{"path": "calc.go", "name": "Answer", "body": "\treturn 2"},
					{"path": "calc.go", "name": "Box.N", "body": "\treturn 3"},
				},
				"result":    "结论\n",
				"exit_code": 9,
			},
			"1": map[string]any{"truncated_events": true, "exit_code": 0},
			"2": map[string]any{"quota_hit": map[string]string{"reset": "2026-12-01T00:00:00Z"}},
			"3": map[string]any{"auth_failure": true},
			"4": map[string]any{"permission_denied": true},
			"5": map[string]any{"refusal": true},
			"6": map[string]any{"sleep": "300ms"},
			"7": map[string]any{"silent": true},
			"8": map[string]any{
				"write_files": []map[string]string{{"path": "other.txt", "content": "second"}},
				"exit_code":   4,
			},
		},
	}
	raw, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	fake := runner.Fake{}
	dir := t.TempDir()

	res, err := fake.Run(context.Background(), runner.LaunchSpec{AttemptID: "a0", AttemptIndex: 0, WorkDir: dir, Script: raw})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 9 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	body, err := os.ReadFile(filepath.Join(dir, "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "return 2") || !strings.Contains(string(body), "return 3") {
		t.Fatalf("function not changed:\n%s", body)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "calc.go", body, 0); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(filepath.Join(dir, ".cardex-result.md"))
	if err != nil || string(result) != "结论\n" {
		t.Fatalf("result %q err %v", result, err)
	}
	cmd, env, err := fake.Build(runner.LaunchSpec{AttemptID: "a0", AttemptIndex: 0, WorkDir: dir, ScriptPath: "script.json"})
	if err != nil || !strings.Contains(strings.Join(cmd, " "), "a0") || len(env) == 0 {
		t.Fatalf("build %v %v %v", cmd, env, err)
	}

	res, err = fake.Run(context.Background(), runner.LaunchSpec{AttemptIndex: 1, WorkDir: dir, Script: raw})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Output)
	if !strings.Contains(out, `"type":"attempt_started"`) {
		t.Fatalf("events %q", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := lines[len(lines)-1]
	if json.Valid([]byte(last)) {
		t.Fatalf("last event not truncated: %s", last)
	}

	res, err = fake.Run(context.Background(), runner.LaunchSpec{AttemptIndex: 2, WorkDir: dir, Script: raw})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), "quota hit") || !strings.Contains(string(res.Output), "2026-12-01T00:00:00Z") {
		t.Fatalf("quota %q", res.Output)
	}
	if fake.Classify(res.ExitCode, res.Output) != model.ErrorQuotaHit {
		t.Fatal("classify quota")
	}

	res, err = fake.Run(context.Background(), runner.LaunchSpec{AttemptIndex: 3, WorkDir: dir, Script: raw})
	if err != nil || !strings.Contains(string(res.Output), "authentication failed") || fake.Classify(0, res.Output) != model.ErrorAuthFailed {
		t.Fatalf("auth %q %v", res.Output, err)
	}
	res, err = fake.Run(context.Background(), runner.LaunchSpec{AttemptIndex: 4, WorkDir: dir, Script: raw})
	if err != nil || !strings.Contains(string(res.Output), "permission denied") || fake.Classify(0, res.Output) != model.ErrorPermissionDenied {
		t.Fatalf("permission %q %v", res.Output, err)
	}
	res, err = fake.Run(context.Background(), runner.LaunchSpec{AttemptIndex: 5, WorkDir: dir, Script: raw})
	if err != nil || !strings.Contains(string(res.Output), "refusal") || fake.Classify(0, res.Output) != model.ErrorRefusal {
		t.Fatalf("refusal %q %v", res.Output, err)
	}

	start := time.Now()
	res, err = fake.Run(context.Background(), runner.LaunchSpec{AttemptIndex: 6, WorkDir: dir, Script: raw})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatalf("sleep returned in %s", time.Since(start))
	}

	silentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(silentDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = fake.Run(context.Background(), runner.LaunchSpec{AttemptIndex: 7, WorkDir: silentDir, Script: raw})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || len(res.Output) != 0 {
		t.Fatalf("silent %+v", res)
	}
	entries, err := os.ReadDir(silentDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Fatalf("silent dir changed: %v %v", entries, err)
	}

	other := t.TempDir()
	res, err = fake.Run(context.Background(), runner.LaunchSpec{AttemptIndex: 8, WorkDir: other, Script: raw})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 4 {
		t.Fatalf("index 8 exit %d", res.ExitCode)
	}
	b, err := os.ReadFile(filepath.Join(other, "other.txt"))
	if err != nil || string(b) != "second" {
		t.Fatalf("other %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(other, ".cardex-result.md")); !os.IsNotExist(err) {
		t.Fatal("index 8 wrote a result file")
	}
	usage := fake.ParseUsage([]byte("tokens=12\n"))
	if usage.Tokens != 12 || usage.Source != "fake" {
		t.Fatalf("usage %+v", usage)
	}
}
