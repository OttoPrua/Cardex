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
	src := `package main

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
	main := `package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	if os.Getenv("GIT_CONFIG_GLOBAL") == "" || os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		fmt.Fprintln(os.Stderr, "go run lost Git or tool configuration")
		os.Exit(1)
	}
	cmd := exec.Command("go", "test", "-count=1", "-mod=mod", ".")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`
	if err := os.WriteFile(filepath.Join(mod, "main.go"), []byte(main), 0o644); err != nil {
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

	wrapDir := t.TempDir()
	env, err := proc.PrependGoTestWrap(os.Environ(), wrapDir)
	if err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "config", "--global", "--get", "cardex.probe")
	git.Env = env
	out, err := git.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "marker-ok" {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"test", []string{"test", "-count=1", "-mod=mod", "."}},
		{"run", []string{"-C", mod, "run", "."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(filepath.Join(wrapDir, "go"), tc.args...)
			cmd.Dir = mod
			cmd.Env = env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go %s: %v\n%s", tc.name, err, out)
			}
		})
	}
}

func TestGoWrapUsesUpdatedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("wrapper check uses a POSIX go shim")
	}
	mod := t.TempDir()
	wrapDir := filepath.Join(mod, "go-wrap")
	t.Setenv("PATH", t.TempDir())
	env, err := proc.PrependGoTestWrap(os.Environ(), wrapDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "go"), []byte("#!/bin/sh\nprintf 'local go %s\\n' \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(wrapDir, "go"), "version")
	cmd.Dir = mod
	cmd.Env = append(env, "PATH=./go-wrap"+string(os.PathListSeparator)+".")
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "local go version\n" {
		t.Fatalf("go version: %v\n%s", err, out)
	}
}
