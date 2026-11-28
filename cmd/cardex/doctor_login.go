package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	engineRunner "github.com/OttoPrua/Cardex/internal/runner"
)

// runnerCLI maps a runner to the executable it starts.
var runnerCLI = map[model.RunnerName]struct {
	bin string
}{
	model.RunnerGrok:     {"grok"},
	model.RunnerCodex:    {"codex"},
	model.RunnerClaude:   {"claude"},
	model.RunnerCursor:   {"cursor-agent"},
	model.RunnerKimi:     {"kimi"},
	model.RunnerAGY:      {"agy"},
	model.RunnerOpenCode: {"opencode"},
}

// loginState is the result of a free, local sign-in check.
type loginState int

const (
	loginUnknown loginState = iota // no reliable check for this CLI
	loginOK
	loginMissing
)

// loginProbe checks sign-in without spending model quota. With run false it
// only reads local files (doctor never starts a PATH binary); with run true
// (channel check) it also runs the CLI's own status command. It is a variable
// so tests can replace it.
type loginProbeHomeKey struct{}

var loginProbe = func(ctx context.Context, runner model.RunnerName, bin string, run bool) loginState {
	status := func(args []string, ok func(string) bool) loginState {
		if !run {
			return loginUnknown
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		home, _ := ctx.Value(loginProbeHomeKey{}).(string)
		cmd.Env = engineRunner.CompatibleCredentialEnv(home, os.Environ())
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.Stdin = nil
		err := cmd.Run()
		if ctx.Err() != nil {
			return loginUnknown
		}
		if err == nil && ok(out.String()) {
			return loginOK
		}
		if _, exit := err.(*exec.ExitError); exit || err == nil {
			return loginMissing
		}
		return loginUnknown
	}
	always := func(string) bool { return true }
	switch runner {
	case model.RunnerCodex:
		return status([]string{"login", "status"}, always)
	case model.RunnerCursor:
		return status([]string{"status"}, func(s string) bool { return strings.Contains(strings.ToLower(s), "logged in as") })
	case model.RunnerClaude:
		return status([]string{"auth", "status"}, func(s string) bool {
			return strings.Contains(strings.ReplaceAll(s, " ", ""), `"loggedIn":true`)
		})
	case model.RunnerGrok:
		home, err := os.UserHomeDir()
		if err != nil {
			return loginUnknown
		}
		info, err := os.Stat(filepath.Join(home, ".grok", "auth.json"))
		if err != nil || info.Size() == 0 {
			return loginMissing
		}
		return loginOK
	}
	return loginUnknown
}

// sandboxFindings reports the OS sandbox tool only where Cardex uses it.
func sandboxFindings() []finding {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return lookupFinding("sandbox-exec", "it ships with macOS at /usr/bin/sandbox-exec; check that the system file is present")
}

// cliFindings checks the CLIs that configured channels use: installed and
// signed in. With no channel it lists the CLIs it found so setup can start.
// run allows status commands to be executed (channel check only).
func cliFindings(ctx context.Context, channels []model.Channel, run bool) []finding {
	var out []finding
	if len(channels) == 0 {
		var found []string
		for _, r := range sortedRunners() {
			if _, err := proc.LookPath(runnerCLI[r].bin, nil); err == nil {
				found = append(found, string(r))
			}
		}
		hint := "no supported CLI is on PATH: install one (grok, codex, claude, cursor-agent, kimi, agy, opencode), sign in, then cardex channel set -id <id> -runner <runner> -model <model>"
		if len(found) > 0 {
			hint = "found " + strings.Join(found, ", ") + " on PATH: add one with cardex channel set -id <id> -runner <runner> -model <model>"
		}
		return []finding{{"no channel is configured, so no card can run", hint}}
	}
	seen := map[model.RunnerName]bool{}
	for _, ch := range channels {
		if ch.CompatibleRuntimeHeld() {
			out = append(out, finding{
				"channel " + ch.ID + ": " + model.CompatibleRuntimeHold,
				"registration is configuration-only and does not require Claude CLI sign-in",
			})
			continue
		}
		r := ch.Runner
		cli, known := runnerCLI[r]
		if !known || seen[r] {
			continue
		}
		seen[r] = true
		if _, err := proc.LookPath(cli.bin, nil); err != nil {
			out = append(out, finding{
				fmt.Sprintf("channel %s uses %s, which is not on PATH", ch.ID, cli.bin),
				"install " + cli.bin + " or add it to the PATH serve runs with",
			})
			continue
		}
		if loginProbe(ctx, r, cli.bin, run) == loginMissing {
			out = append(out, finding{
				fmt.Sprintf("%s is not signed in, so channel %s will fail with an auth error", cli.bin, ch.ID),
				"sign in: run " + model.LoginCommand(r) + ", then rerun cardex channel check",
			})
		}
	}
	for _, ch := range channels {
		if !ch.CompatibleRuntimeHeld() && ch.Health == model.HealthAuthFailed {
			login := model.LoginCommand(ch.Runner)
			if login == "" {
				login = "its CLI"
			}
			out = append(out, finding{
				fmt.Sprintf("channel %s is marked auth_failed and gets no cards", ch.ID),
				"sign in (" + login + "), then cardex channel set -id " + ch.ID + " -health ok",
			})
		}
	}
	return out
}

func sortedRunners() []model.RunnerName {
	var out []model.RunnerName
	for r := range runnerCLI {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
