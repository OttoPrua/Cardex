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
	if got[0].Req != "" || got[1].Req != "" {
		t.Fatalf("empty req leaked into json=%s", raw)
	}
	if strings.Contains(raw, `"req"`) {
		t.Fatalf("omitempty req present: %s", raw)
	}
	if strings.Contains(raw, "not-json") || strings.Contains(out, "not-json") {
		t.Fatalf("malformed line leaked")
	}
}

func TestDigestJSONReqFromTaskAndArchive(t *testing.T) {
	root := testRoot(t)
	liveReq := "req_0123456789abcdef"
	archReq := "req_abcdef0123456789"
	live := &Task{ID: "task-req-live", Title: "live with req", Project: "Perlica", Req: liveReq}
	arch := &Task{ID: "task-req-arch", Title: "archived with req", Project: "Perlica", Req: archReq}
	conflict := &Task{ID: "task-req-conflict", Title: "req_ffffffffffffffff lookalike title", Project: "Cardex"}
	if err := os.MkdirAll(tasksDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archiveDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTaskJSON(t, filepath.Join(tasksDir(root), live.ID+".json"), live)
	writeTaskJSON(t, filepath.Join(archiveDir(root), arch.ID+".json"), arch)
	writeTaskJSON(t, filepath.Join(tasksDir(root), conflict.ID+".json"), conflict)

	var b strings.Builder
	for i, id := range []string{live.ID, arch.ID, conflict.ID, "task-missing"} {
		row := managerWakeOutboxRow{
			Schema: outboxSchemaV1, Seq: int64(i + 1), WakeEventID: "w", TaskID: id,
			TaskEventSeq: 1, TransitionID: "t", EventType: "failed", Status: "failed",
			ReasonClass: "cli", TS: "2026-10-09T00:00:00Z",
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

	raw, err := captureStdout(t, func() error {
		return cmdDigest([]string{"-root", root, "-since", "1", "-json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []digestEntry
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("entries=%d json=%s", len(got), raw)
	}
	if got[0].Req != liveReq {
		t.Fatalf("live req=%q json=%s", got[0].Req, raw)
	}
	if got[1].Req != archReq {
		t.Fatalf("archived req=%q json=%s", got[1].Req, raw)
	}
	if got[2].Req != "" {
		t.Fatalf("title lookalike invented req=%q", got[2].Req)
	}
	if got[3].Req != "" || got[3].TaskID != "task-missing" {
		t.Fatalf("missing task invented req: %+v", got[3])
	}
	if strings.Count(raw, `"req"`) != 2 {
		t.Fatalf("req omitempty failed: %s", raw)
	}
}

func TestDigestCLIAddListJSONReq(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	reqID := "req_fedcba9876543210"
	if err := cmdAdd([]string{"-root", root, "-dir", work, "-title", "cli req card", "-req", reqID, "bounded prompt"}); err != nil {
		t.Fatal(err)
	}
	listRaw, err := captureStdout(t, func() error {
		return cmdList([]string{"-root", root, "-json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var listed []Task
	if err := json.Unmarshal([]byte(listRaw), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Req != reqID {
		t.Fatalf("list req: %s", listRaw)
	}

	emptyTitle := t.TempDir()
	if err := cmdAdd([]string{"-root", root, "-dir", emptyTitle, "-title", "req_aaaaaaaaaaaaaaaa", "no req flag"}); err != nil {
		t.Fatal(err)
	}
	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	var withReq, noReq *Task
	for _, tk := range tasks {
		switch tk.Req {
		case reqID:
			withReq = tk
		case "":
			noReq = tk
		}
	}
	if withReq == nil || noReq == nil {
		t.Fatalf("expected one req card and one empty, got %+v", tasks)
	}

	writeDigestOutbox(t, root, withReq.ID, noReq.ID)
	raw, err := captureStdout(t, func() error {
		return cmdDigest([]string{"-root", root, "-since", "1", "-json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []digestEntry
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Req != reqID || got[1].Req != "" {
		t.Fatalf("digest cli json=%s", raw)
	}
	if strings.Contains(got[1].Title, "req_") && got[1].Req != "" {
		t.Fatalf("title supplied req: %+v", got[1])
	}
}

func writeDigestOutbox(t *testing.T, root string, ids ...string) {
	t.Helper()
	var b strings.Builder
	for i, id := range ids {
		row := managerWakeOutboxRow{
			Schema: outboxSchemaV1, Seq: int64(i + 1), WakeEventID: "w", TaskID: id,
			TaskEventSeq: 1, TransitionID: "t", EventType: "failed", Status: "failed",
			ReasonClass: "cli", TS: "2026-10-09T00:00:00Z",
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
