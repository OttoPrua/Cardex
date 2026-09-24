package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDigestFiltersStableTextAndJSON(t *testing.T) {
	root := testRoot(t)
	writeDigestFixture(t, root)

	out, err := captureStdout(t, func() error {
		return cmdDigest([]string{"-root", root})
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 20 {
		t.Fatalf("default last 20, got %d\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "#3 ") || !strings.HasPrefix(lines[len(lines)-1], "#22 ") {
		t.Fatalf("default window=%s .. %s", lines[0], lines[len(lines)-1])
	}
	again, err := captureStdout(t, func() error {
		return cmdDigest([]string{"-root", root})
	})
	if err != nil || again != out {
		t.Fatalf("unstable digest err=%v", err)
	}

	since, err := captureStdout(t, func() error {
		return cmdDigest([]string{"-root", root, "-since", "20", "-project", "Perlica"})
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArchTitle := strings.Repeat("题", 50)
	if since != "#20 task-live done verdict=done 「live title」 files=2 +3/-1 commits=1 verify=0\n#22 task-arch held verdict=- 「"+wantArchTitle+"」 held in archive\n" {
		t.Fatalf("since/project=%q", since)
	}

	last, err := captureStdout(t, func() error {
		return cmdDigest([]string{"-root", root, "-last", "1", "-manager", "Yvonne"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if last != "#21 task-mgr failed verdict=- 「manager card」 boom second\n" {
		t.Fatalf("last/manager=%q", last)
	}

	events, err := captureStdout(t, func() error {
		return cmdDigest([]string{"-root", root, "-since", "1", "-events", "canceled", "-project", "cardex"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if events != "#4 task-live canceled verdict=done 「live title」 files=2 +3/-1 commits=1 verify=0\n" {
		t.Fatalf("events filter=%q", events)
	}

	raw, err := captureStdout(t, func() error {
		return cmdDigest([]string{"-root", root, "-since", "21", "-json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []digestEntry
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TaskID != "task-mgr" || got[1].TaskID != "task-arch" || got[1].Evidence != "held in archive" || got[1].Title != strings.Repeat("题", 50) {
		t.Fatalf("json=%s", raw)
	}
	if strings.Contains(raw, "not-json") || strings.Contains(out, "not-json") {
		t.Fatalf("malformed line leaked")
	}
}

func writeDigestFixture(t *testing.T, root string) {
	t.Helper()
	exit0 := 0
	live := &Task{
		ID: "task-live", Title: "live title", Project: "Perlica", Status: "done", Verdict: "done",
		Harvest: &HarvestEvidence{ChangedFiles: []string{"a.go", "b.go"}, Insertions: 3, Deletions: 1, NewCommits: []string{"abc"}, VerifyExit: &exit0},
	}
	mgr := &Task{ID: "task-mgr", Title: "manager card", Project: "Cardex", Manager: "Yvonne", LastError: "boom\nsecond"}
	arch := &Task{ID: "task-arch", Title: strings.Repeat("题", 60), Project: "Perlica", LastSummary: "held in archive"}
	if err := os.MkdirAll(tasksDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archiveDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaskJSON(t, filepath.Join(tasksDir(root), "task-live.json"), live)
	writeTaskJSON(t, filepath.Join(tasksDir(root), "task-mgr.json"), mgr)
	writeTaskJSON(t, filepath.Join(archiveDir(root), "task-arch.json"), arch)

	var b strings.Builder
	b.WriteString("{not-json\n")
	b.WriteString("{\"schema\":\"other\",\"seq\":99,\"task_id\":\"task-live\",\"event_type\":\"done\"}\n")
	for seq := int64(1); seq <= 22; seq++ {
		taskID := "task-live"
		event := "done"
		project := "Perlica"
		switch seq {
		case 4:
			event = "canceled"
			project = "Cardex"
		case 21:
			taskID = "task-mgr"
			event = "failed"
			project = "Cardex"
		case 22:
			taskID = "task-arch"
			event = "held"
		}
		row := managerWakeOutboxRow{
			Schema: outboxSchemaV1, Seq: seq, WakeEventID: "w", TaskID: taskID,
			TaskEventSeq: 1, TransitionID: "t", Project: project, EventType: event,
			Status: event, ReasonClass: "cli", TS: "2026-09-01T00:00:00Z",
		}
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	path := managerWakeOutboxPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTaskJSON(t *testing.T, path string, task *Task) {
	t.Helper()
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
