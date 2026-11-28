package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/runner"
)

func TestAPI2ChannelCheckRequiresOneExplicitCostApproval(t *testing.T) {
	home := t.TempDir()
	writeEngineConfig(t, home, `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test","default_model":"fixture-model","credential":{"source":"env","name":"CARDEX_FIXTURE_PROBE"}}}}`)
	call := func(args ...string) (int, string, string) {
		return callCLI(t, append([]string{"--home", home}, args...))
	}
	if code, _, detail := call("channel", "set", "--id", "synthetic", "--runner", "claude", "--engine-profile", "vendor-a", "--family", "vendor-a"); code != 0 {
		t.Fatal(detail)
	}
	original := runCompatibleProbe
	defer func() { runCompatibleProbe = original }()
	calls := 0
	runCompatibleProbe = func(_ context.Context, _ string, ch model.Channel, requested string, _ []string) runner.CompatibleProbeReceipt {
		calls++
		if ch.ID != "synthetic" || ch.Health != model.HealthDegraded || requested != "fixture-model" {
			t.Fatal("probe targeted wrong channel or model")
		}
		return runner.CompatibleProbeReceipt{Channel: ch.ID, Endpoint: "https://api.example.test", RequestedModel: requested,
			ReturnedModel: requested, Status: "ok", ModelSource: model.ModelSourceProvider, Attempted: true}
	}
	if code, _, detail := call("channel", "check"); code != 0 || calls != 0 {
		t.Fatalf("normal check changed probe behavior: %d %s", code, detail)
	}
	for _, args := range [][]string{
		{"--probe", "--channel", "synthetic", "--model", "fixture-model"},
		{"--probe", "--channel", "synthetic", "--confirm-possible-charge"},
		{"--model", "fixture-model", "--confirm-possible-charge"},
		{"--probe", "--channel", "synthetic", "--model", "fixture-model", "--confirm-possible-charge=false"},
		{"--probe", "--channel", "synthetic", "--model", "fixture-model", "--confirm-possible-charge", "second"},
	} {
		code, _, _ := call(append([]string{"channel", "check"}, args...)...)
		if code == 0 || calls != 0 {
			t.Fatal("an incomplete probe authorization started a request")
		}
	}
	t.Setenv(proc.ManagedExecutorEnv, "1")
	if code, _, _ := call("channel", "check", "--probe", "--channel", "synthetic", "--model", "fixture-model", "--confirm-possible-charge"); code == 0 || calls != 0 {
		t.Fatal("managed executor authorized a probe with flags")
	}
	t.Setenv(proc.ManagedExecutorEnv, "")
	code, out, detail := call("--json", "channel", "check", "--probe", "--channel", "synthetic", "--model", "fixture-model", "--confirm-possible-charge")
	var got runner.CompatibleProbeReceipt
	if code != 0 || calls != 1 || json.Unmarshal([]byte(out), &got) != nil || got.Status != "ok" || got.ReturnedModel != "fixture-model" {
		t.Fatalf("invalid receipt or request count: %d %q %s", code, out, detail)
	}
	if ch := channelOf(t, home, "synthetic"); ch.Health != model.HealthDegraded {
		t.Fatal("probe changed channel health")
	}
	runCompatibleProbe = original
	code, out, _ = call("--json", "channel", "check", "--probe", "--channel", "synthetic", "--model", "fixture-model", "--confirm-possible-charge")
	if code != 1 || json.Unmarshal([]byte(out), &got) != nil || got.Status != "approval_unavailable" || got.Attempted {
		t.Fatal("production approval gate was bypassed by same-UID flags")
	}

}

func TestAPI2StrictComparisonCLIRequiresExplicitPins(t *testing.T) {
	home := t.TempDir()
	for _, flags := range [][]string{
		{"--strict-comparison", "--channel", "synthetic"},
		{"--strict-comparison", "--channel", "synthetic", "--model", "fixture-model", "--node", "n"},
		{"--strict-comparison", "--channel", "synthetic", "--model", "fixture-model", "--require", "web"},
		{"--channel", "synthetic", "--model", "fixture-model"},
		{"--require", model.RequireStrictComparison},
	} {
		code, _, detail := callCLI(t, append([]string{"--home", home, "card", "add", "--objective", "o", "--title", "synthetic"}, flags...))
		if code != 2 || !strings.Contains(detail, "strict") {
			t.Fatalf("unstated comparison was accepted: %d %s", code, detail)
		}
	}
}
