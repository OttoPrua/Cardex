package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSetupPlanAndApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	bin := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("codex", "echo Logged in using ChatGPT; exit 0")
	write("grok", "exit 0") // no ~/.grok/auth.json in HOME: not signed in
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()

	code, out, errb := callCLI(t, []string{"--home", home, "setup"})
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	for _, want := range []string{"codex     ready", "grok      not_signed_in", "grok login", "claude    not_installed", "cardex setup --yes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan missing %q\n%s", want, out)
		}
	}
	if code, list, _ := callCLI(t, []string{"--home", home, "channel", "list"}); code != 0 || strings.Contains(list, "codex") {
		t.Fatalf("plan without --yes changed channels\n%s", list)
	}

	code, out, errb = callCLI(t, []string{"--home", home, "setup", "--yes"})
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	if !strings.Contains(out, "created channels: codex") || !strings.Contains(out, "cardex positioning approve ") {
		t.Fatalf("apply\n%s", out)
	}
	_, show, _ := callCLI(t, []string{"--home", home, "--json", "channel", "show", "codex"})
	if !strings.Contains(show, "gpt-6.2-astra") {
		t.Fatalf("channel\n%s", show)
	}
	_, props, _ := callCLI(t, []string{"--home", home, "--json", "positioning", "proposals"})
	if !strings.Contains(props, "gpt-6.2-astra") {
		t.Fatalf("proposal\n%s", props)
	}

	code, out, _ = callCLI(t, []string{"--home", home, "setup", "--yes"})
	if code != 0 || !strings.Contains(out, "codex     configured") || strings.Contains(out, "created channels") {
		t.Fatalf("rerun is not idempotent\n%s", out)
	}
}

func TestVersionFlag(t *testing.T) {
	for _, a := range []string{"version", "--version", "-v"} {
		code, out, _ := callCLI(t, []string{a})
		if code != 0 || !strings.HasPrefix(out, "cardex ") {
			t.Fatalf("%s: %d %q", a, code, out)
		}
	}
}
