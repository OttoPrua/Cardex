package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestAPIChannelSetBindsAndRejects(t *testing.T) {
	const canary = "cdx-canary-a-7f3e9c"
	home := t.TempDir()
	writeEngineConfig(t, home, `{
	  "engines": {
	    "vendor-a": {
	      "protocol": "anthropic-compatible",
	      "base_url": "https://api.example.test/anthropic",
	      "default_model": "vendor-a-model-1",
	      "credential": {"source": "env", "name": "CARDEX_FIXTURE_VENDOR_A"}
	    }
	  }
	}`)
	code, out, errb := callCLI(t, []string{"--home", home, "--json", "channel", "set", "--id", "api-a", "--runner", "claude", "--engine-profile", "vendor-a", "--family", "vendor-a"})
	if code != 0 {
		t.Fatalf("create %d\n%s\n%s", code, out, errb)
	}
	if strings.Contains(out+errb, canary) {
		t.Fatal("create output contained a credential")
	}
	var created model.Channel
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	if created.Health != model.HealthDegraded || !strings.HasPrefix(created.EngineProfile, "anthropic-compatible:vendor-a@") || runner.CompatibleBindingID(created.EngineProfile) == "" || created.Family != "vendor-a" || len(created.Models) != 1 || created.Models[0] != "vendor-a-model-1" {
		t.Fatalf("%+v", created)
	}
	withDB(t, home, func(db *store.Store) {
		rows, err := db.ListPositioning(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("channel set created positioning %+v", rows)
		}
	})
	code, out, errb = callCLI(t, []string{"--home", home, "channel", "set", "--id", "api-a", "--redline", "0.4"})
	if code != 0 {
		t.Fatalf("restart update %d\n%s\n%s", code, out, errb)
	}
	if ch := channelOf(t, home, "api-a"); ch.Redline != 0.4 || ch.EngineProfile != created.EngineProfile || ch.Family != "vendor-a" || ch.Models[0] != "vendor-a-model-1" {
		t.Fatalf("preserved %+v", ch)
	}

	code, out, errb = callCLI(t, []string{"--home", home, "channel", "set", "--id", "api-a", "--redline", "0.9", "--model", "opus"})
	if code == 0 || !strings.Contains(errb, "tier alias") {
		t.Fatalf("alias %d\n%s\n%s", code, out, errb)
	}
	if ch := channelOf(t, home, "api-a"); ch.Redline != 0.4 || ch.Models[0] != "vendor-a-model-1" {
		t.Fatalf("failed update wrote %+v", ch)
	}

	writeEngineConfig(t, home, `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"http://api.example.test/anthropic","default_model":"vendor-a-model-1","credential":{"source":"env","name":"CARDEX_FIXTURE_VENDOR_A"}}}}`)
	code, _, errb = callCLI(t, []string{"--home", home, "channel", "set", "--id", "api-a", "--concurrency", "3"})
	if code == 0 || !strings.Contains(errb, "https") {
		t.Fatalf("bad target %d %s", code, errb)
	}
	if ch := channelOf(t, home, "api-a"); ch.Concurrency != 0 || ch.Redline != 0.4 {
		t.Fatalf("bad target wrote %+v", ch)
	}

	writeEngineConfig(t, home, `{"engines":{"vendor-a":{"base_url":"https://api.example.test/anthropic","auth_env":"SYNTHETIC_LEGACY_KEY"}}}`)
	code, _, errb = callCLI(t, []string{"--home", home, "channel", "set", "--id", "api-a", "--concurrency", "3"})
	if code == 0 || !strings.Contains(errb, "legacy authentication") {
		t.Fatalf("downgrade was accepted: %d, %s", code, errb)
	}
	if ch := channelOf(t, home, "api-a"); ch.Concurrency != 0 || ch.Health != model.HealthDegraded {
		t.Fatal("downgrade changed channel")
	}

	fresh := t.TempDir()
	writeEngineConfig(t, fresh, `{"engines":{"vendor-a":{"protocol":"openai-compatible","base_url":"https://api.example.test/v1","credential":{"source":"env","name":"KEY"}}}}`)
	code, _, errb = callCLI(t, []string{"--home", fresh, "channel", "set", "--id", "x", "--runner", "claude", "--engine-profile", "vendor-a", "--family", "vendor-a"})
	if code == 0 || !strings.Contains(errb, "not supported") {
		t.Fatalf("openai %d %s", code, errb)
	}
	if _, err := channelMaybe(t, fresh, "x"); err == nil {
		t.Fatal("rejected create was stored")
	}

	legacy := t.TempDir()
	code, _, errb = callCLI(t, []string{"--home", legacy, "channel", "set", "--id", "k", "--runner", "claude", "--engine-profile", "kimi", "--model", "opus"})
	if code != 0 {
		t.Fatalf("legacy preset %d %s", code, errb)
	}
	if ch := channelOf(t, legacy, "k"); ch.EngineProfile != "kimi" || len(ch.Models) != 1 || ch.Models[0] != "opus" {
		t.Fatalf("legacy %+v", ch)
	}
	code, _, errb = callCLI(t, []string{"--home", legacy, "channel", "set", "--id", "g", "--runner", "grok", "--engine-profile", "kimi"})
	if code == 0 || !strings.Contains(errb, "claude") {
		t.Fatalf("runner %d %s", code, errb)
	}
	if _, err := channelMaybe(t, legacy, "g"); err == nil {
		t.Fatal("incompatible runner was stored")
	}

	secretHome := t.TempDir()
	writeEngineConfig(t, secretHome, `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic","default_model":"vendor-a-model-1","credential":{"source":"env","name":"CARDEX_FIXTURE_VENDOR_A"},"auth_value":"`+canary+`"}}}`)
	code, out, errb = callCLI(t, []string{"--home", secretHome, "channel", "set", "--id", "api-a", "--runner", "claude", "--engine-profile", "vendor-a", "--family", "vendor-a"})
	if code == 0 || strings.Contains(out+errb, canary) {
		t.Fatalf("plaintext %d\n%s\n%s", code, out, errb)
	}
	dbRaw, err := os.ReadFile(filepath.Join(secretHome, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(dbRaw, []byte(canary)) {
		t.Fatal("credential was stored in the database")
	}

	missing := t.TempDir()
	writeEngineConfig(t, missing, `{"engines":{}}`)
	code, out, errb = callCLI(t, []string{"--home", missing, "channel", "set", "--id", "api-a", "--runner", "claude", "--engine-profile", "absent", "--family", "vendor-a"})
	if code == 0 || !strings.Contains(errb, "unknown") || strings.Contains(out+errb, canary) {
		t.Fatalf("missing profile %d\n%s\n%s", code, out, errb)
	}
	if _, err := channelMaybe(t, missing, "api-a"); err == nil {
		t.Fatal("unknown profile was stored")
	}
}

func TestAPIChannelKeychainAmbiguityIsRejected(t *testing.T) {
	home := t.TempDir()
	writeEngineConfig(t, home, `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic","default_model":"vendor-a-model-1","credential":{"source":"keychain","name":"synthetic-a-service"}}}}`)
	restore := runner.SetSecurityRunnerForTest(&cliKeychain{accounts: []string{"one", "two"}})
	defer restore()
	code, out, errb := callCLI(t, []string{"--home", home, "channel", "set", "--id", "api-a", "--runner", "claude", "--engine-profile", "vendor-a", "--family", "vendor-a"})
	if code == 0 || !strings.Contains(errb, "more than one") || strings.Contains(out+errb, "cdx-canary") {
		t.Fatalf("ambiguous %d\n%s\n%s", code, out, errb)
	}
	if _, err := channelMaybe(t, home, "api-a"); err == nil {
		t.Fatal("ambiguous keychain binding was stored")
	}
}

func writeEngineConfig(t *testing.T, home, doc string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func channelOf(t *testing.T, home, id string) model.Channel {
	t.Helper()
	ch, err := channelMaybe(t, home, id)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func channelMaybe(t *testing.T, home, id string) (model.Channel, error) {
	t.Helper()
	var ch model.Channel
	var err error
	withDB(t, home, func(db *store.Store) {
		ch, err = db.GetChannel(context.Background(), id)
	})
	return ch, err
}

type cliKeychain struct {
	accounts []string
	reads    int
}

func (c *cliKeychain) Accounts(string) ([]string, error) {
	return append([]string{}, c.accounts...), nil
}

func (c *cliKeychain) Password(string, string) (string, error) {
	c.reads++
	return "cdx-canary-a-7f3e9c", nil
}

func TestAPIChannelPositioningConflict(t *testing.T) {
	home := t.TempDir()
	writeEngineConfig(t, home, `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test/anthropic","default_model":"vendor-a-model-1","credential":{"source":"env","name":"CARDEX_FIXTURE_VENDOR_A"}}}}`)
	withDB(t, home, func(db *store.Store) {
		row := model.Positioning{ID: "pos-ds", Version: 1, Model: "vendor-a-model-1", Family: "elsewhere", Tiers: []model.Tier{model.TierT2}}
		if err := db.CreatePositioning(context.Background(), &row); err != nil {
			t.Fatal(err)
		}
	})
	code, _, errb := callCLI(t, []string{"--home", home, "channel", "set", "--id", "api-a", "--runner", "claude", "--engine-profile", "vendor-a", "--family", "vendor-a"})
	if code == 0 || !strings.Contains(errb, "routing conflict") {
		t.Fatalf("conflict %d %s", code, errb)
	}
	if _, err := channelMaybe(t, home, "api-a"); err == nil {
		t.Fatal("conflicting channel was stored")
	}
}

func TestAPIChannelEndpointEditRequiresSeparateActivation(t *testing.T) {
	home := t.TempDir()
	config := func(target string) {
		writeEngineConfig(t, home, `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"`+target+`","default_model":"vendor-a-model-1","credential":{"source":"env","name":"CARDEX_FIXTURE_VENDOR_A"}}}}`)
	}
	run := func(fields ...string) (int, string) {
		t.Helper()
		code, _, err := callCLI(t, append([]string{"--home", home, "channel", "set", "--id", "api-a"}, fields...))
		return code, err
	}
	config("https://first.example.test")
	if code, detail := run("--runner", "claude", "--engine-profile", "vendor-a", "--family", "vendor-a"); code != 0 {
		t.Fatalf("register: %d %s", code, detail)
	}
	if code, detail := run("--health", "ok"); code != 0 {
		t.Fatalf("first activation: %d %s", code, detail)
	}
	before := channelOf(t, home, "api-a")
	config("https://second.example.test")
	if code, _ := run("--health", "ok"); code == 0 {
		t.Fatal("an edited endpoint inherited the old activation")
	}
	if got := channelOf(t, home, "api-a"); got.EngineProfile != before.EngineProfile || got.Health != model.HealthOK {
		t.Fatal("rejected activation modified binding")
	}
	if code, detail := run("--engine-profile", "vendor-a"); code != 0 {
		t.Fatalf("stage new identity: %d %s", code, detail)
	}
	staged := channelOf(t, home, "api-a")
	if staged.EngineProfile == before.EngineProfile || staged.Health != model.HealthDegraded {
		t.Fatal("changed profile was not staged as a new, degraded identity")
	}
	if code, detail := run("--health", "ok"); code != 0 {
		t.Fatalf("activate staged identity: %d %s", code, detail)
	}
	if got := channelOf(t, home, "api-a"); got.Health != model.HealthOK || got.EngineProfile != staged.EngineProfile {
		t.Fatal("explicit activation did not apply to staged identity")
	}
}

func TestAPIChannelFileAliasInsideProjectIsRejectedBeforeWrite(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("credential file mode is unsupported")
	}
	home, repo := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "repo-alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "fixture-secret"), []byte("synthetic-fixture-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	withDB(t, home, func(db *store.Store) {
		if err := db.CreateProject(context.Background(), &model.Project{ID: "p", RepoPath: repo}); err != nil {
			t.Fatal(err)
		}
	})
	file := strconv.Quote(filepath.Join(alias, "fixture-secret"))
	writeEngineConfig(t, home, `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test","default_model":"fixture-model","credential":{"source":"file","path":`+file+`}}}}`)
	code, _, detail := callCLI(t, []string{"--home", home, "channel", "set", "--id", "api-a", "--runner", "claude", "--engine-profile", "vendor-a", "--family", "vendor-a"})
	if code == 0 || !strings.Contains(detail, "outside") {
		t.Fatalf("registered a repository credential alias: %d %s", code, detail)
	}
	if _, err := channelMaybe(t, home, "api-a"); err == nil {
		t.Fatal("rejected registration wrote a channel")
	}
}

func TestAPIChannelHealthOnlyDeactivationWorksWithInvalidConfig(t *testing.T) {
	home := t.TempDir()
	valid := `{"engines":{"vendor-a":{"protocol":"anthropic-compatible","base_url":"https://api.example.test","default_model":"fixture-model","credential":{"source":"env","name":"CARDEX_FIXTURE_VENDOR_A"}}}}`
	writeEngineConfig(t, home, valid)
	run := func(fields ...string) (int, string) {
		t.Helper()
		code, _, detail := callCLI(t, append([]string{"--home", home, "channel", "set", "--id", "api-a"}, fields...))
		return code, detail
	}
	if code, detail := run("--runner", "claude", "--engine-profile", "vendor-a", "--family", "vendor-a"); code != 0 {
		t.Fatalf("register: %d %s", code, detail)
	}
	if code, detail := run("--health", "ok"); code != 0 {
		t.Fatalf("activate: %d %s", code, detail)
	}
	prior := channelOf(t, home, "api-a")
	writeEngineConfig(t, home, "{invalid")
	if code, _ := run("--health", "degraded", "--redline", "0.9"); code == 0 {
		t.Fatal("mixed update skipped profile validation")
	}
	if code, detail := run("--health", "degraded"); code != 0 {
		t.Fatalf("deactivate with invalid config: %d %s", code, detail)
	}
	after := channelOf(t, home, "api-a")
	if after.Health != model.HealthDegraded || after.EngineProfile != prior.EngineProfile || after.Redline != prior.Redline {
		t.Fatal("deactivation did not preserve the staged binding and options")
	}
	if code, _ := run("--health", "ok"); code == 0 {
		t.Fatal("reactivation bypassed the profile check")
	}
	writeEngineConfig(t, home, valid)
	if got := channelOf(t, home, "api-a"); got.Health != model.HealthDegraded {
		t.Fatal("repairing invalid config silently reactivated the channel")
	}
}
