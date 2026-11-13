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

func TestImportProjectOnlyScopeSelectsStoredProject(t *testing.T) {
	const thread = "11111111-1111-4111-8111-111111111111"
	cases := []struct {
		name    string
		project string
	}{
		{name: "preserved project name", project: "lumen"},
		{name: "project name that is not a preserved token", project: "lu men"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v1 := t.TempDir()
			writeV1(t, v1, "tasks/queued.json", map[string]any{
				"id": "q-lamp", "title": "Queue the lamp", "status": "queued", "project": tc.project,
			})
			rawCfg, err := json.Marshal(map[string]any{
				"manager_wake": map[string]any{
					"subscriptions": []map[string]any{{
						"id": "example-codex", "provider": "codex", "thread_id": thread,
						"projects": []string{tc.project}, "enabled": true,
					}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(v1, "config.json"), rawCfg, 0o644); err != nil {
				t.Fatal(err)
			}
			v2, _ := importReadOnly(t, v1)
			loaded, warnings, err := notify.LoadReceivers(v2)
			if err != nil || len(warnings) != 0 || len(loaded) != 1 {
				t.Fatalf("load %+v warnings %v err %v", loaded, warnings, err)
			}
			rec := loaded[0]
			if !rec.Enabled || rec.Adapter != "codex" || rec.Target != thread {
				t.Fatalf("receiver %+v", rec)
			}
			if len(rec.Scope.Projects) != 1 || rec.Scope.Projects[0] != tc.project {
				t.Fatalf("projects %+v", rec.Scope.Projects)
			}
			if len(rec.Scope.Objectives) != 0 || len(rec.Scope.DirPrefixes) != 0 || rec.Scope.All {
				t.Fatalf("scope %+v", rec.Scope)
			}
			db := openDB(t, v2)
			objs := objectivesOf(t, db)
			if len(objs) != 1 || objs[0].Project == "" || objs[0].Project == tc.project {
				t.Fatalf("stored project %+v", objs)
			}
			stored := objs[0].Project
			deliveries := []struct {
				name    string
				project string
				want    bool
			}{
				{name: "imported project id", project: stored, want: true},
				{name: "original project name", project: tc.project, want: true},
				{name: "different project", project: stored + "-other", want: false},
			}
			for _, row := range deliveries {
				t.Run(row.name, func(t *testing.T) {
					var calls [][]string
					svc := notify.New(func(_ context.Context, argv []string) error {
						calls = append(calls, append([]string(nil), argv...))
						return nil
					}, nil)
					msg := notify.Message{
						QuestionID: "q-notice",
						Objective:  "not-in-scope",
						Text:       "project notice",
						Home:       t.TempDir(),
						Project:    row.project,
						Receivers:  loaded,
					}
					if err := svc.Deliver(context.Background(), msg); err != nil {
						t.Fatal(err)
					}
					got := argvHas(calls, thread) && argvHas(calls, msg.Text)
					if got != row.want {
						t.Fatalf("selected %v calls %v", got, calls)
					}
				})
			}
		})
	}
}

func TestImportProviderDefaultAndCase(t *testing.T) {
	const thread = "11111111-1111-4111-8111-111111111111"
	codex := "codex"
	hermes := "hermes"
	lark := "lark"
	file := "file"
	cases := []struct {
		name       string
		id         string
		provider   *string
		profile    string
		thread     string
		wantAdpt   string
		wantTarget string
	}{
		{name: "omitted provider", id: "example-codex", thread: thread, wantAdpt: codex, wantTarget: thread},
		{name: "CODEX", id: "example-codex", provider: &codexUpper, thread: thread, wantAdpt: codex, wantTarget: thread},
		{name: "padded CODEX", id: "example-codex", provider: &codexPadded, thread: thread, wantAdpt: codex, wantTarget: thread},
		{name: "empty provider", id: "example-codex", provider: &emptyProvider, thread: thread, wantAdpt: codex, wantTarget: thread},
		{name: "whitespace provider", id: "example-codex", provider: &blankProvider, thread: thread, wantAdpt: codex, wantTarget: thread},
		{name: "HERMES", id: "example-hermes", provider: &hermesUpper, profile: "profile-a", thread: "example-manager", wantAdpt: hermes, wantTarget: "profile-a/example-manager"},
		{name: "padded Hermes", id: "example-hermes", provider: &hermesPadded, profile: "profile-a", thread: "example-manager", wantAdpt: hermes, wantTarget: "profile-a/example-manager"},
		{name: "LARK", id: "example-lark", provider: &larkUpper, thread: "chat-0001", wantAdpt: lark, wantTarget: "chat-0001"},
		{name: "FILE", id: "example-file", provider: &fileUpper, thread: "box", wantAdpt: file, wantTarget: "box"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v1 := t.TempDir()
			sub := map[string]any{
				"id": tc.id, "thread_id": tc.thread, "projects": []string{"lumen"}, "enabled": true,
			}
			if tc.provider != nil {
				sub["provider"] = *tc.provider
			}
			if tc.profile != "" {
				sub["profile"] = tc.profile
			}
			rawCfg, err := json.Marshal(map[string]any{
				"manager_wake": map[string]any{"subscriptions": []map[string]any{sub}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(v1, "config.json"), rawCfg, 0o644); err != nil {
				t.Fatal(err)
			}
			v2, reportNotes := importReadOnly(t, v1)
			for _, note := range reportNotes {
				if strings.Contains(note, "missing id, adapter, or target") || strings.Contains(note, "adapter ") {
					t.Fatalf("skipped %s", note)
				}
			}
			loaded, warnings, err := notify.LoadReceivers(v2)
			if err != nil || len(warnings) != 0 || len(loaded) != 1 {
				t.Fatalf("load %+v warnings %v err %v notes %v", loaded, warnings, err, reportNotes)
			}
			rec := loaded[0]
			if rec.ID != tc.id || rec.Adapter != tc.wantAdpt || rec.Target != tc.wantTarget || !rec.Enabled {
				t.Fatalf("receiver %+v", rec)
			}
			if len(rec.Scope.Projects) != 1 || rec.Scope.Projects[0] != "lumen" {
				t.Fatalf("projects %+v", rec.Scope.Projects)
			}
		})
	}
}

var (
	codexUpper    = "CODEX"
	codexPadded   = "  CODEX  "
	emptyProvider = ""
	blankProvider = "   "
	hermesUpper   = "HERMES"
	hermesPadded  = " Hermes "
	larkUpper     = "LARK"
	fileUpper     = "FILE"
)

func argvHas(calls [][]string, needle string) bool {
	for _, argv := range calls {
		for _, arg := range argv {
			if arg == needle {
				return true
			}
		}
	}
	return false
}

func importReadOnly(t *testing.T, v1 string) (string, []string) {
	t.Helper()
	before := snapshotTree(t, v1)
	if runtime.GOOS == "windows" {
		t.Skip("read-only dirs via chmod are not enforced on Windows")
	}
	if out, err := exec.Command("chmod", "-R", "a-w", v1).CombinedOutput(); err != nil {
		t.Fatalf("chmod: %v %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("chmod", "-R", "u+w", v1).CombinedOutput(); err != nil {
			t.Errorf("restore: %v %s", err, out)
		}
	})
	v2 := t.TempDir()
	report, err := Import(context.Background(), v2, v1, false)
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshotTree(t, v1); after != before {
		t.Fatal("v1 source changed")
	}
	return v2, report.Notes
}
