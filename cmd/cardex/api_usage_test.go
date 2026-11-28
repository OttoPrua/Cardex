package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/apiusage"
)

func cliMeterInt(n int64) *int64 { return &n }

func TestAPI3OfflineQuoteIsOptInAndDoesNotTouchHome(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "must-not-exist")
	meterPath := filepath.Join(dir, "fixture.json")
	pricePath := filepath.Join(dir, "price.json")
	binding := strings.Repeat("a", 64)
	raw := []byte(`{"type":"message","model":"fixture-model","usage":{"input_tokens":12,"output_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,
	"service_tier":"standard","speed":"standard","server_tool_use":{"web_search_requests":0,"web_fetch_requests":0}},"content":"do not retain synthetic answer"}`)
	if err := os.WriteFile(meterPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	p := apiusage.PriceSnapshot{Source: apiusage.SyntheticPrice, Binding: binding, Channel: "fixture-channel", Model: "fixture-model",
		Version: "fixture-v1", Currency: "USD", ServiceTier: "standard", Speed: "standard", EffectiveFrom: at, EffectiveUntil: at.Add(time.Hour),
		Rates: apiusage.Rates{Input: cliMeterInt(1_000_000), Output: cliMeterInt(2_000_000), CacheRead: cliMeterInt(0),
			CacheWrite: cliMeterInt(0), WebSearch: cliMeterInt(0), WebFetch: cliMeterInt(0)}}
	price, _, err := p.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pricePath, price, 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--home", home, "--json", "channel", "quote-fixture", "--usage", meterPath, "--price", pricePath,
		"--channel", "fixture-channel", "--model", "fixture-model", "--binding", binding, "--at", at.Format(time.RFC3339)}
	type result struct {
		Metering apiusage.View
		Estimate apiusage.Estimate
	}
	run := func(extra ...string) result {
		t.Helper()
		code, out, stderr := callCLI(t, append(append([]string{}, base...), extra...))
		if code != 0 {
			t.Fatalf("offline quote did not run: %d %s", code, stderr)
		}
		for _, forbidden := range []string{"synthetic answer", "0.57689", "costUSD"} {
			if strings.Contains(out, forbidden) || strings.Contains(stderr, forbidden) {
				t.Fatal("quote leaked raw result or CLI monetary estimate")
			}
		}
		var r result
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := run(); r.Metering.Total == nil || *r.Metering.Total != 15 || r.Estimate.Status != "not_requested" || r.Estimate.Amount != nil {
		t.Fatal("default output displayed an amount or lost usage")
	}
	if r := run("--show-synthetic-estimate"); r.Estimate.Status != "synthetic_estimate" || r.Estimate.Amount == nil || *r.Estimate.Amount != 18 {
		t.Fatal("explicit synthetic quote didn't match exact approved inputs")
	}
	cli := []byte(`{"type":"result","usage":{"input_tokens":2413,"output_tokens":22593,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},
	"modelUsage":{"fixture-model":{"inputTokens":2413,"outputTokens":22593,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.57689}}}`)
	if err := os.WriteFile(meterPath, cli, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := run("--show-synthetic-estimate", "--format", "recorded-claude-cli"); r.Metering.Total == nil || *r.Metering.Total != 25006 ||
		r.Metering.Metering.Source != apiusage.RecordedCLI || r.Metering.ModelAttribution != "scheduled_route_only" || r.Estimate.Status != "unknown" || r.Estimate.Amount != nil {
		t.Fatal("recorded CLI summary leaked cost, asserted provider trust, or double counted")
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only fixture invocation opened or created a CardeX home")
	}
}

// The child enters main(), including executable-only setup hooks which
// helpers that call Main() directly cannot cover.
func TestAPI3QuoteExecutableChild(t *testing.T) {
	if os.Getenv("CARDEX_API3_TEST_EXEC") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing executable args")
}

func TestAPI3QuoteExecutableIgnoresPoisonedRuntimeConfig(t *testing.T) {
	home, files := t.TempDir(), t.TempDir()
	poison := filepath.Join(home, "config.json")
	if err := os.WriteFile(poison, []byte(`{"runtime_path":["relative-poison"],"unrelated":"do-not-read"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(files, "meter.json")
	if err := os.WriteFile(fixture, []byte(`{"type":"message","model":"fake-model-fixture","usage":{"input_tokens":5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), os.Args[0], append([]string{"-test.run=^TestAPI3QuoteExecutableChild$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "CARDEX_API3_TEST_EXEC=1")
		return cmd.CombinedOutput()
	}
	// Positive control: running an ordinary command is blocked by this
	// exact poisoned config, proving the fixture reaches the stock prehook.
	if got, err := run("--home", home, "channel", "list"); err == nil || !strings.Contains(string(got), "runtime_path entry") {
		t.Fatalf("invalid control: %v %s", err, got)
	}
	got, err := run("--home", home, "--json", "channel", "quote-fixture", "--usage", fixture,
		"--channel", "fixture-channel", "--model", "fake-model-fixture", "--binding", strings.Repeat("a", 64), "--at", "2026-10-18T10:00:00Z")
	if err != nil || !strings.Contains(string(got), `"synthetic_only"`) || strings.Contains(string(got), "unrelated") {
		t.Fatalf("quote went through runtime configuration: %v %s", err, got)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("quote created runtime artifacts: %v %v", entries, err)
	}
}
