package proc_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/proc"
)

func TestGoWrapIsolatesGoTestKeepsGitConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("wrapper check uses a POSIX go shim")
	}
	mod := t.TempDir()
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module isolatedprobe\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := `package isolatedprobe

import (
	"os"
	"testing"
)

func TestNoServeEnv(t *testing.T) {
	for _, key := range []string{
		"CARDEX_SCHED_HOST", "CURSOR_CONFIG_DIR", "KIMI_CODE_HOME", "CLAUDE_CONFIG_DIR",
		"GIT_EXEC_PATH", "GIT_CONFIG_GLOBAL", "GIT_TEMPLATE_DIR",
	} {
		if v := os.Getenv(key); v != "" {
			t.Errorf("leaked %s=%s", key, v)
		}
	}
	if os.Getenv("PATH") == "" || os.Getenv("HOME") == "" {
		t.Error("PATH or HOME missing")
	}
}
`
	if err := os.WriteFile(filepath.Join(mod, "probe_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[cardex]\n\tprobe = marker-ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARDEX_SCHED_HOST", "1")
	t.Setenv("CURSOR_CONFIG_DIR", t.TempDir())
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("GIT_EXEC_PATH", t.TempDir())
	t.Setenv("GIT_TEMPLATE_DIR", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)

	env, err := proc.PrependGoTestWrap(os.Environ(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "config", "--global", "--get", "cardex.probe")
	git.Env = env
	out, err := git.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "marker-ok" {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	wrapGo := filepath.Join(procWrapDir(t, env), "go")
	cmd := exec.Command(wrapGo, "test", "-count=1", "-mod=mod", ".")
	cmd.Dir = mod
	cmd.Env = env
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test: %v\n%s", err, out)
	}
}

func procWrapDir(t *testing.T, env []string) string {
	t.Helper()
	for _, kv := range env {
		key, val, ok := strings.Cut(kv, "=")
		if ok && key == "CARDEX_GO_WRAP_DIR" {
			return val
		}
	}
	t.Fatal("CARDEX_GO_WRAP_DIR missing")
	return ""
}
