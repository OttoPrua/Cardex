package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestCLIFindingsLogin(t *testing.T) {
	bin, _ := sentinelBin(t, "codex", "claude")
	t.Setenv("PATH", bin)
	saved := loginProbe
	defer func() { loginProbe = saved }()
	var ran []bool
	loginProbe = func(_ context.Context, r model.RunnerName, _ string, run bool) loginState {
		ran = append(ran, run)
		if r == model.RunnerCodex {
			return loginMissing
		}
		return loginOK
	}
	chs := []model.Channel{
		{ID: "a", Runner: model.RunnerCodex},
		{ID: "b", Runner: model.RunnerCodex},
		{ID: "c", Runner: model.RunnerClaude, Health: model.HealthAuthFailed},
	}
	got := cliFindings(context.Background(), chs, true)
	text := ""
	for _, f := range got {
		text += f.problem + " | " + f.suggestion + "\n"
	}
	if strings.Count(text, "codex is not signed in") != 1 || !strings.Contains(text, "run codex login") {
		t.Fatalf("codex sign-in finding once:\n%s", text)
	}
	if strings.Contains(text, "claude is not signed in") {
		t.Fatalf("signed-in CLI reported:\n%s", text)
	}
	if !strings.Contains(text, "channel c is marked auth_failed") || !strings.Contains(text, "channel set -id c -health ok") {
		t.Fatalf("auth_failed finding:\n%s", text)
	}
	if len(ran) != 2 || !ran[0] {
		t.Fatalf("probes %v", ran)
	}
}

func TestCLIFindingsNoChannel(t *testing.T) {
	bin, _ := sentinelBin(t, "grok")
	t.Setenv("PATH", bin)
	got := cliFindings(context.Background(), nil, false)
	if len(got) != 1 || !strings.Contains(got[0].problem, "no channel is configured") || !strings.Contains(got[0].suggestion, "found grok") {
		t.Fatalf("%+v", got)
	}
}

func TestLoginProbeNoExecInDoctor(t *testing.T) {
	bin, marker := sentinelBin(t, "codex")
	t.Setenv("PATH", bin)
	if st := loginProbe(context.Background(), model.RunnerCodex, "codex", false); st != loginUnknown {
		t.Fatalf("state %v", st)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("probe ran a binary in doctor mode: %v", err)
	}
}

func TestLoginProbeStatusExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctx := context.Background()
	if st := loginProbe(ctx, model.RunnerCodex, write("codex-out", "echo Not logged in; exit 1"), true); st != loginMissing {
		t.Fatalf("codex logged out: %v", st)
	}
	if st := loginProbe(ctx, model.RunnerCodex, write("codex-in", "echo Logged in; exit 0"), true); st != loginOK {
		t.Fatalf("codex logged in: %v", st)
	}
	if st := loginProbe(ctx, model.RunnerClaude, write("claude-out", `echo '{"loggedIn": false}'`), true); st != loginMissing {
		t.Fatalf("claude logged out: %v", st)
	}
	if st := loginProbe(ctx, model.RunnerClaude, write("claude-in", `echo '{"loggedIn": true}'`), true); st != loginOK {
		t.Fatalf("claude logged in: %v", st)
	}
	if st := loginProbe(ctx, model.RunnerKimi, write("kimi", "exit 1"), true); st != loginUnknown {
		t.Fatalf("kimi has no probe: %v", st)
	}
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	if st := loginProbe(ctx, model.RunnerGrok, "grok", false); st != loginMissing {
		t.Fatalf("grok without auth.json: %v", st)
	}
}
