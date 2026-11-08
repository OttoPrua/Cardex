package notify

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReceiverRoundTripPreservesUnknownKeys(t *testing.T) {
	home := t.TempDir()
	original := []byte(`{
  "schedules": [{"name": "daily"}],
  "custom": {"keep": true},
  "notify": {
    "fallback": "codex:thread-0001",
    "extra": "stay",
    "receivers": [
      {"id": "kept", "adapter": "file", "target": "box", "enabled": true, "note": "unknown-field"}
    ]
  }
}
`)
	if err := os.WriteFile(filepath.Join(home, "config.json"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	err := AddReceiver(home, Receiver{
		ID:      "example-manager",
		Adapter: "codex",
		Target:  "thread-0001",
		Enabled: true,
		Scope:   Scope{Projects: []string{"p"}},
		Events:  []string{"freeze"},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, warnings, err := LoadReceivers(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings %+v", warnings)
	}
	if len(loaded) != 2 || loaded[0].ID != "kept" || loaded[1].ID != "example-manager" {
		t.Fatalf("loaded %+v", loaded)
	}
	if !loaded[1].Enabled || loaded[1].Adapter != "codex" || loaded[1].Target != "thread-0001" {
		t.Fatalf("added %+v", loaded[1])
	}
	if len(loaded[1].Scope.Projects) != 1 || loaded[1].Scope.Projects[0] != "p" || len(loaded[1].Events) != 1 {
		t.Fatalf("scope %+v", loaded[1])
	}
	body, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["schedules"]; !ok {
		t.Fatal("schedules dropped")
	}
	if _, ok := doc["custom"]; !ok {
		t.Fatal("custom dropped")
	}
	notifyBody, _ := doc["notify"].(map[string]any)
	if notifyBody["fallback"] != "codex:thread-0001" || notifyBody["extra"] != "stay" {
		t.Fatalf("notify %#v", notifyBody)
	}
	rawReceivers, _ := notifyBody["receivers"].([]any)
	first, _ := rawReceivers[0].(map[string]any)
	if first["note"] != "unknown-field" {
		t.Fatalf("unknown field %#v", first)
	}
	matches, err := filepath.Glob(filepath.Join(home, ".config-*.json"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temp config left %v %v", matches, err)
	}

	before, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	dup := AddReceiver(home, Receiver{ID: "example-manager", Adapter: "lark", Target: "chat-1", Enabled: true})
	if dup == nil || !strings.Contains(dup.Error(), "already exists") {
		t.Fatalf("duplicate %v", dup)
	}
	after, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("duplicate id wrote the file")
	}
}

func TestInvalidReceiverWarnsAndKeepsTheRest(t *testing.T) {
	home := t.TempDir()
	body := []byte(`{
  "notify": {
    "fallback": "file:box",
    "receivers": [
      {"id": "bad-events", "adapter": "codex", "target": "thread-0001", "events": "nope", "enabled": true},
      {"id": "bad-kind", "adapter": "codex", "target": "thread-0001", "events": ["nope"], "enabled": true},
      {"id": "bad-scope", "adapter": "file", "target": "box", "scope": "all", "enabled": true},
      {"id": "bad-flag", "adapter": "file", "target": "box", "enabled": "yes"},
      {"id": "bad id", "adapter": "file", "target": "box", "enabled": true},
      {"id": "slack-one", "adapter": "slack", "target": "room", "enabled": true},
      {"id": "no-target", "adapter": "codex", "enabled": true},
      {"adapter": "codex", "target": "thread-0001"},
      "not-object",
      {"id": "example-manager", "adapter": "hermes", "target": "profile-a/example-manager", "enabled": false, "root": "/tmp/hermes-root"}
    ]
  }
}
`)
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, warnings, err := LoadReceivers(home)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("load rewrote config")
	}
	if len(loaded) != 1 || loaded[0].ID != "example-manager" || loaded[0].Enabled || loaded[0].Adapter != "hermes" {
		t.Fatalf("loaded %+v", loaded)
	}
	got := map[string]string{}
	for _, warning := range warnings {
		got[warning.Entry] = warning.Reason
	}
	want := map[string]string{
		"bad-events":   "events unparsable",
		"bad-kind":     "event nope",
		"bad-scope":    "scope unparsable",
		"bad-flag":     "enabled unparsable",
		"bad id":       "invalid id",
		"slack-one":    "adapter slack",
		"no-target":    "missing target",
		"receivers[7]": "missing id",
		"receivers[8]": "not an object",
	}
	for entry, reason := range want {
		if got[entry] != reason {
			t.Errorf("warning %s = %q, want %q; all %+v", entry, got[entry], reason, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("warnings %+v", got)
	}
}

func TestReceiverFromOrigin(t *testing.T) {
	codex, err := ReceiverFromOrigin("codex:thread-0001")
	if err != nil || codex.Adapter != "codex" || codex.Target != "thread-0001" || !codex.Enabled {
		t.Fatalf("%+v %v", codex, err)
	}
	hermes, err := ReceiverFromOrigin("hermes:profile-a/example-manager")
	if err != nil || hermes.Adapter != "hermes" || hermes.Target != "profile-a/example-manager" || !hermes.Enabled {
		t.Fatalf("%+v %v", hermes, err)
	}
	if _, err := ReceiverFromOrigin("slack:room"); err == nil {
		t.Fatal("unknown origin accepted")
	}
}

func TestRemoveReceiverLeavesOtherEntries(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"notify":{"receivers":[{"id":"bad-kind","adapter":"codex","events":["nope"]},{"id":"example-manager","adapter":"codex","target":"thread-0001","enabled":true}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveReceiver(home, "example-manager"); err != nil {
		t.Fatal(err)
	}
	loaded, warnings, err := LoadReceivers(home)
	if err != nil || len(loaded) != 0 || len(warnings) != 1 || warnings[0].Entry != "bad-kind" {
		t.Fatalf("loaded %+v warnings %+v err %v", loaded, warnings, err)
	}
	body, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil || !strings.Contains(string(body), "bad-kind") || strings.Contains(string(body), "example-manager") {
		t.Fatalf("file %s %v", body, err)
	}
}
