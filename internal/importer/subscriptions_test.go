package importer

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/notify"
)

func TestImportSubscriptionsReadOnlySource(t *testing.T) {
	v1 := t.TempDir()
	writeV1(t, v1, "tasks/queued.json", map[string]any{
		"id": "q-lumen", "title": "Queue the lamp", "status": "queued", "project": "lumen",
	})
	rawCfg, err := json.Marshal(map[string]any{
		"manager_wake": map[string]any{
			"subscriptions": []map[string]any{
				{
					"id": "example-codex", "provider": "codex", "thread_id": "thread-0001",
					"projects": []string{"lumen"}, "dir_prefixes": []string{"/work/lumen"},
					"task_ids":    []string{"q-lumen", "missing-task"},
					"event_types": []string{"done", "canceled", "needs_owner"},
					"enabled":     true,
				},
				{
					"id": "example-hermes", "provider": "hermes", "profile": "profile-a",
					"thread_id": "example-manager", "event_types": []string{"held"}, "enabled": false,
				},
				{
					"id": "kept-id", "provider": "file", "thread_id": "box", "enabled": true,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v1, "config.json"), rawCfg, 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, v1)
	if runtime.GOOS == "windows" {
		t.Skip("read-only dirs via chmod are not enforced on Windows")
	}
	if out, err := exec.Command("chmod", "-R", "a-w", v1).CombinedOutput(); err != nil {
		t.Fatalf("chmod: %v %s", err, out)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		if out, err := exec.Command("chmod", "-R", "u+w", v1).CombinedOutput(); err != nil {
			t.Errorf("restore: %v %s", err, out)
		}
	}
	defer restore()

	v2 := t.TempDir()
	if err := notify.AddReceiver(v2, notify.Receiver{
		ID: "kept-id", Adapter: "file", Target: "original", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	report, err := Import(context.Background(), v2, v1, false)
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshotTree(t, v1); after != before {
		t.Fatal("v1 source changed")
	}
	for _, note := range report.Notes {
		for _, unparsed := range report.Unparsed {
			if note == unparsed {
				t.Fatalf("note stored as unparsed %s", note)
			}
		}
	}
	joined := strings.Join(report.Notes, "\n")
	for _, part := range []string{
		"subscription example-codex: task missing-task: not found",
		"subscription example-codex: event canceled: no v2 notice",
		"receiver kept-id: already exists",
	} {
		if !strings.Contains(joined, part) {
			t.Fatalf("notes missing %s\n%s", part, joined)
		}
	}
	loaded, warnings, err := notify.LoadReceivers(v2)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("load %+v %v", warnings, err)
	}
	byID := map[string]notify.Receiver{}
	for _, rec := range loaded {
		byID[rec.ID] = rec
	}
	codex := byID["example-codex"]
	if codex.Adapter != "codex" || codex.Target != "thread-0001" || !codex.Enabled {
		t.Fatalf("codex %+v", codex)
	}
	if len(codex.Scope.Projects) != 1 || codex.Scope.Projects[0] != "lumen" {
		t.Fatalf("projects %+v", codex.Scope)
	}
	if len(codex.Scope.DirPrefixes) != 1 || codex.Scope.DirPrefixes[0] != "/work/lumen" {
		t.Fatalf("dirs %+v", codex.Scope)
	}
	if len(codex.Scope.Objectives) != 1 || codex.Scope.Objectives[0] != "v1-import-lumen" {
		t.Fatalf("objectives %+v", codex.Scope.Objectives)
	}
	if !containsAll(codex.Events, "done", "freeze", "reopen", "breaker", "budget", "refusal", "custom") {
		t.Fatalf("events %+v", codex.Events)
	}
	hermes := byID["example-hermes"]
	if hermes.Adapter != "hermes" || hermes.Target != "profile-a/example-manager" || hermes.Enabled {
		t.Fatalf("hermes %+v", hermes)
	}
	if !containsAll(hermes.Events, "freeze", "custom") || containsAll(hermes.Events, "done") {
		t.Fatalf("hermes events %+v", hermes.Events)
	}
	if byID["kept-id"].Target != "original" {
		t.Fatalf("conflict overwritten %+v", byID["kept-id"])
	}
}

func containsAll(list []string, wants ...string) bool {
	have := map[string]bool{}
	for _, item := range list {
		have[item] = true
	}
	for _, want := range wants {
		if !have[want] {
			return false
		}
	}
	return true
}
