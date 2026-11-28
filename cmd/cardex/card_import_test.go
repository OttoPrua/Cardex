package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestCardImportHelpExample(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	code, out, errb := callCLI(t, []string{"--home", home, "card", "import", "--help"})
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	for _, needle := range []string{
		"purpose", "parameters", "--apply", "--file", "--objective", "--origin",
		`"version": 1`, "body_file", "claims", "deps", "acceptance", "c-parse", "c-doc",
		"preview",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("help missing %q\n%s", needle, out)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("help created a home: %v", err)
	}
	_, group, _ := callCLI(t, []string{"--home", home, "card", "--help"})
	for _, needle := range []string{"import", "--apply", "body_file", `"version": 1`, "c-parse"} {
		if !strings.Contains(group, needle) {
			t.Fatalf("card help missing %q\n%s", needle, group)
		}
	}
}

func TestCardImportMissingDB(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	manifest := writeManifest(t, t.TempDir(), map[string]any{
		"version": 1,
		"cards": []any{map[string]any{
			"id": "c-new", "kind": "dev", "title": "新", "body": "正文",
		}},
	})
	for _, apply := range []bool{false, true} {
		args := []string{"--home", home, "card", "import", "--objective", "o1", "--file", manifest}
		if apply {
			args = append(args, "--apply")
		}
		code, _, errb := callCLI(t, args)
		if code == 0 || !strings.Contains(errb, "cardex.db not found") {
			t.Fatalf("apply %v exit %d stderr %s", apply, code, errb)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatalf("apply %v created a home: %v", apply, err)
		}
	}
}

func TestCardImportPreviewRefusesLiveWALWithoutMutation(t *testing.T) {
	home, dbPath := seedImportHome(t)
	raw, err := sql.Open("sqlite", importFixtureURI(dbPath, "_pragma=busy_timeout(5000)"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec("PRAGMA wal_autocheckpoint = 0"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO objectives(id, project_id, title, acceptance, soft_tokens, hard_tokens, priority, mode, freeze_mode, origin, status, integration_branch, channel, model, review) VALUES ('o-wal','p1','WAL objective','{"commands":[],"checklist":[]}',0,0,0,'manual','safe','','active','','','','')`); err != nil {
		t.Fatal(err)
	}
	before := importFiles(t, home)
	if len(before["cardex.db-wal"]) == 0 {
		t.Fatal("expected an uncheckpointed WAL")
	}
	file := writeManifest(t, t.TempDir(), map[string]any{
		"version": 1,
		"cards":   []any{map[string]any{"id": "c-wal", "kind": "dev", "title": "card", "body": ""}},
	})
	args := []string{"--home", home, "--json", "card", "import", "--objective", "o-wal", "--file", file}
	code, out, errb := callCLI(t, args)
	if code != 1 || out != "" || !strings.Contains(errb, "preview unavailable") {
		t.Fatalf("expected an explicit unavailable result, got %d %s %s", code, out, errb)
	}
	assertImportFilesUnchanged(t, home, before)

	// Apply reads the actual WAL transactionally, not a stale copy of the
	// main database. The objective above was created only in the live WAL.
	code, out, errb = callCLI(t, append(args, "--apply"))
	if code != 0 || !strings.Contains(out, "applied") {
		t.Fatalf("apply to live WAL: %d %s %s", code, out, errb)
	}
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM cards WHERE objective_id='o-wal' AND id='c-wal'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("WAL apply count=%d err=%v", count, err)
	}
}

func TestCardImportCleanPreviewLeavesHomeUnchanged(t *testing.T) {
	home, _ := seedImportHome(t)
	file := writeManifest(t, t.TempDir(), map[string]any{
		"version": 1,
		"cards":   []any{map[string]any{"id": "c-preview", "kind": "dev", "title": "card", "body": "", "deps": []string{"c-keep"}}},
	})
	before := importFiles(t, home)
	args := []string{"--home", home, "--json", "card", "import", "--objective", "o1", "--file", file}
	code, out, errb := callCLI(t, args)
	if code != 0 || !strings.Contains(out, "preview") || !strings.Contains(out, "c-keep") {
		t.Fatalf("preview: %d %s %s", code, out, errb)
	}
	assertImportFilesUnchanged(t, home, before)
}

func importFiles(t *testing.T, home string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string][]byte)
	for _, entry := range entries {
		if !entry.IsDir() {
			out[entry.Name()] = readBytes(t, filepath.Join(home, entry.Name()))
		}
	}
	return out
}

func assertImportFilesUnchanged(t *testing.T, home string, before map[string][]byte) {
	t.Helper()
	after := importFiles(t, home)
	if len(before) != len(after) {
		t.Fatalf("file count changed: %d to %d", len(before), len(after))
	}
	for name, data := range before {
		got, exists := after[name]
		if !exists || !bytes.Equal(data, got) {
			t.Fatalf("%s changed during preview", name)
		}
	}
}

func TestCardImportApplyBatchAndRetry(t *testing.T) {
	home, _ := seedImportHome(t)
	sentinel := filepath.Join(t.TempDir(), "should-not-run")
	body := "第一行：不要执行\n第二行：echo 你好\n$(touch " + sentinel + ")\n`touch " + sentinel + "`\n; echo literal-only\n"
	acceptance := "touch " + sentinel + " && echo 你好 && echo '单引号'"
	fileBody := "文件正文\n第二行仍然是数据\nsh -c 'touch " + sentinel + "'\n"
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "bodies"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bodies", "note.md"), []byte(fileBody), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards": []any{
			map[string]any{
				"id": "c-a", "kind": "dev", "title": "解析清单", "body": body,
				"claims": []any{map[string]string{"path": "internal/parse.go", "symbol": "Parse"}},
				"deps":   []string{"c-doc"}, "acceptance": []string{acceptance},
			},
			map[string]any{
				"id": "c-doc", "kind": "dev", "title": "说明", "body_file": "bodies/note.md",
				"claims": []any{map[string]string{"path": "docs/note.md"}},
				"deps":   []string{"c-keep"}, "acceptance": []string{"go test ./docs"},
			},
		},
	})
	code, out, errb := callCLI(t, []string{"--home", home, "--json", "card", "import", "--objective", "o1", "--file", manifest})
	if code != 0 {
		t.Fatalf("preview exit %d\n%s\n%s", code, out, errb)
	}
	if strings.Contains(errb, "touch") {
		t.Fatalf("stderr ran or echoed a command unexpectedly: %s", errb)
	}
	var preview importReportJSON
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Mode != "preview" || len(preview.Conflicts) != 0 || len(preview.Cards) != 2 {
		t.Fatalf("preview %+v", preview)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("preview executed a body or acceptance command")
	}
	assertCardMissing(t, home, "c-a")

	t.Setenv("CARDEX_ORIGIN", "should-not-win:env")
	code, out, errb = callCLI(t, []string{"--home", home, "--json", "card", "import", "--objective", "o1", "--file", manifest, "--apply", "--origin", "hermes:profile/session"})
	if code != 0 {
		t.Fatalf("apply exit %d\n%s\n%s", code, out, errb)
	}
	var applied importReportJSON
	if err := json.Unmarshal([]byte(out), &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Mode != "applied" || len(applied.Conflicts) != 0 {
		t.Fatalf("applied %+v", applied)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("apply executed a body or acceptance command")
	}
	gotA := importGetCard(t, home, "c-a")
	if gotA.Body != body || len(gotA.Acceptance) != 1 || gotA.Acceptance[0] != acceptance || gotA.Objective != "o1" || gotA.Kind != model.KindDev || gotA.Title != "解析清单" || gotA.Status != model.CardReady || gotA.Tier != "" {
		t.Fatalf("c-a %+v", gotA)
	}
	if strings.Join(gotA.Deps, ",") != "c-doc" {
		t.Fatalf("deps %+v", gotA.Deps)
	}
	if len(gotA.Claims) != 1 || gotA.Claims[0].Path != "internal/parse.go" || gotA.Claims[0].Symbol != "Parse" {
		t.Fatalf("claims %+v", gotA.Claims)
	}
	gotDoc := importGetCard(t, home, "c-doc")
	if gotDoc.Body != fileBody || strings.Join(gotDoc.Deps, ",") != "c-keep" || len(gotDoc.Claims) != 1 || gotDoc.Claims[0].Path != "docs/note.md" || gotDoc.Claims[0].Symbol != "" {
		t.Fatalf("c-doc %+v", gotDoc)
	}
	if origin := mustOrigin(t, home, "c-a"); origin != "hermes:profile/session" {
		t.Fatalf("origin %q", origin)
	}
	if origin := mustOrigin(t, home, "c-doc"); origin != "hermes:profile/session" {
		t.Fatalf("doc origin %q", origin)
	}
	if n := originCount(t, home, "c-a"); n != 1 {
		t.Fatalf("origin events %d", n)
	}
	keep := importGetCard(t, home, "c-keep")
	if keep.Body != "keep-body" || keep.Title != "保留" {
		t.Fatalf("keep changed %+v", keep)
	}

	// A changed manifest must not overwrite the stored batch.
	rewrite := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards": []any{
			map[string]any{"id": "c-a", "kind": "dev", "title": "改写", "body": "新正文"},
			map[string]any{"id": "c-doc", "kind": "dev", "title": "改写说明", "body": "新说明"},
		},
	})
	code, _, errb = callCLI(t, []string{"--home", home, "card", "import", "--objective", "o1", "--file", rewrite, "--apply", "--origin", "hermes:profile/session"})
	if code == 0 || !strings.Contains(errb, "already exists") {
		t.Fatalf("retry exit %d stderr %s", code, errb)
	}
	gotA = importGetCard(t, home, "c-a")
	if gotA.Title != "解析清单" || gotA.Body != body {
		t.Fatalf("retry overwrote c-a %+v", gotA)
	}
	if n := originCount(t, home, "c-a"); n != 1 {
		t.Fatalf("retry origin events %d", n)
	}
	if n := originCount(t, home, "c-doc"); n != 1 {
		t.Fatalf("retry doc origin events %d", n)
	}
	ids := listCardIDs(t, home)
	if strings.Join(ids, ",") != "c-a,c-doc,c-keep" {
		t.Fatalf("ids %v", ids)
	}
}

func TestCardImportRejectsLeaveZeroRows(t *testing.T) {
	cases := []struct {
		name string
		file func(t *testing.T, dir string) string
		want string
	}{
		{name: "batch cycle", want: "cycle", file: func(t *testing.T, dir string) string {
			return writeManifest(t, dir, map[string]any{"version": 1, "cards": []any{
				map[string]any{"id": "c-a", "kind": "dev", "title": "甲", "body": "a", "deps": []string{"c-b"}},
				map[string]any{"id": "c-b", "kind": "dev", "title": "乙", "body": "b", "deps": []string{"c-c"}},
				map[string]any{"id": "c-c", "kind": "dev", "title": "丙", "body": "c", "deps": []string{"c-a"}},
			}})
		}},
		{name: "existing cycle", want: "cycle", file: func(t *testing.T, dir string) string {
			return writeManifest(t, dir, map[string]any{"version": 1, "cards": []any{
				map[string]any{"id": "c-new", "kind": "dev", "title": "新", "body": "n", "deps": []string{"c-old"}},
			}})
		}},
		{name: "existing id", want: "already exists", file: func(t *testing.T, dir string) string {
			return writeManifest(t, dir, map[string]any{"version": 1, "cards": []any{
				map[string]any{"id": "c-fresh", "kind": "dev", "title": "新", "body": "ok"},
				map[string]any{"id": "c-keep", "kind": "dev", "title": "覆盖", "body": "nope"},
			}})
		}},
		{name: "invalid second", want: "invalid card kind", file: func(t *testing.T, dir string) string {
			return writeManifest(t, dir, map[string]any{"version": 1, "cards": []any{
				map[string]any{"id": "c-ok", "kind": "dev", "title": "好", "body": "ok", "acceptance": []string{"touch " + filepath.Join(dir, "ran")}},
				map[string]any{"id": "c-bad", "kind": "nope", "title": "坏", "body": "bad"},
			}})
		}},
		{name: "unsupported field", want: "unsupported field", file: func(t *testing.T, dir string) string {
			return writeManifest(t, dir, map[string]any{"version": 1, "cards": []any{
				map[string]any{"id": "c-ok", "kind": "dev", "title": "好", "body": "ok"},
				map[string]any{"id": "c-bad", "kind": "dev", "title": "坏", "body": "bad", "tier": "T2"},
			}})
		}},
		{name: "unsupported version", want: "unsupported version", file: func(t *testing.T, dir string) string {
			return writeManifest(t, dir, map[string]any{"version": 2, "cards": []any{
				map[string]any{"id": "c-ok", "kind": "dev", "title": "好", "body": "ok"},
			}})
		}},
		{name: "duplicate id", want: "duplicate card id", file: func(t *testing.T, dir string) string {
			return writeManifest(t, dir, map[string]any{"version": 1, "cards": []any{
				map[string]any{"id": "c-dup", "kind": "dev", "title": "一", "body": "a"},
				map[string]any{"id": "c-dup", "kind": "dev", "title": "二", "body": "b"},
			}})
		}},
		{name: "missing dep", want: "not found", file: func(t *testing.T, dir string) string {
			return writeManifest(t, dir, map[string]any{"version": 1, "cards": []any{
				map[string]any{"id": "c-ok", "kind": "dev", "title": "好", "body": "ok", "deps": []string{"c-missing"}},
			}})
		}},
		{name: "body and body_file", want: "body", file: func(t *testing.T, dir string) string {
			if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("file"), 0o644); err != nil {
				t.Fatal(err)
			}
			return writeManifest(t, dir, map[string]any{"version": 1, "cards": []any{
				map[string]any{"id": "c-both", "kind": "dev", "title": "两边", "body": "inline", "body_file": "a.md"},
			}})
		}},
		{name: "body_file escape", want: "body_file", file: func(t *testing.T, dir string) string {
			return writeManifest(t, dir, map[string]any{"version": 1, "cards": []any{
				map[string]any{"id": "c-esc", "kind": "dev", "title": "逃", "body_file": "../secret.md"},
			}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := seedImportHome(t)
			if tc.name == "existing cycle" {
				mustHome(t, home, "card", "add", "--id", "c-old", "--objective", "o1", "--title", "旧", "--kind", "dev", "--dep", "c-new")
			}
			before := listCardIDs(t, home)
			configBefore := readBytes(t, filepath.Join(home, "config.json"))
			dir := t.TempDir()
			manifest := tc.file(t, dir)
			code, _, errb := callCLI(t, []string{"--home", home, "card", "import", "--objective", "o1", "--file", manifest, "--apply"})
			if code == 0 || !strings.Contains(errb, tc.want) {
				t.Fatalf("exit %d stderr %s", code, errb)
			}
			if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
				t.Fatal("acceptance command ran")
			}
			after := listCardIDs(t, home)
			if strings.Join(before, ",") != strings.Join(after, ",") {
				t.Fatalf("cards %v -> %v", before, after)
			}
			keep := importGetCard(t, home, "c-keep")
			if keep.Body != "keep-body" || keep.Title != "保留" {
				t.Fatalf("keep %+v", keep)
			}
			if !bytes.Equal(configBefore, readBytes(t, filepath.Join(home, "config.json"))) {
				t.Fatal("config changed")
			}
			ch := mustGetChannel(t, home, "ch-deploy")
			if strings.Join(ch.Models, ",") != "deploy-model" {
				t.Fatalf("channel %+v", ch)
			}
		})
	}
}

func TestCardImportRejectsUnusableBranchID(t *testing.T) {
	home, _ := seedImportHome(t)
	dir := t.TempDir()
	manifest := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards": []any{
			map[string]any{"id": "c-ok.v2", "kind": "dev", "title": "合法", "body": "原值"},
			map[string]any{"id": "draft..one", "kind": "dev", "title": "两点", "body": "不行"},
		},
	})
	for _, apply := range []bool{false, true} {
		args := []string{"--home", home, "card", "import", "--objective", "o1", "--file", manifest}
		if apply {
			args = append(args, "--apply")
		}
		before := listCardIDs(t, home)
		code, _, errb := callCLI(t, args)
		if code == 0 || !strings.Contains(errb, "draft..one") || !strings.Contains(errb, "cannot name a branch") {
			t.Fatalf("apply %v exit %d stderr %s", apply, code, errb)
		}
		if strings.Join(before, ",") != strings.Join(listCardIDs(t, home), ",") {
			t.Fatalf("apply %v wrote cards", apply)
		}
	}

	legal := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards": []any{
			map[string]any{"id": "c-ok.v2", "kind": "dev", "title": "合法", "body": "原值"},
			map[string]any{"id": "foo.Lock", "kind": "dev", "title": "大小写", "body": "保留"},
		},
	})
	code, _, errb := callCLI(t, []string{"--home", home, "card", "import", "--objective", "o1", "--file", legal, "--apply"})
	if code != 0 {
		t.Fatalf("legal exit %d stderr %s", code, errb)
	}
	if got := importGetCard(t, home, "c-ok.v2"); got.ID != "c-ok.v2" || got.Body != "原值" {
		t.Fatalf("id rewritten %+v", got)
	}
	if got := importGetCard(t, home, "foo.Lock"); got.ID != "foo.Lock" {
		t.Fatalf("id rewritten %+v", got)
	}

	mustHome(t, home, "objective", "add", "--id", "bad..id", "--project", "p1", "--title", "坏目标")
	badObj := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards":   []any{map[string]any{"id": "c-under-bad", "kind": "dev", "title": "目标坏", "body": "不行"}},
	})
	before := listCardIDs(t, home)
	code, _, errb = callCLI(t, []string{"--home", home, "card", "import", "--objective", "bad..id", "--file", badObj, "--apply"})
	if code == 0 || !strings.Contains(errb, "cannot name a branch") {
		t.Fatalf("bad objective exit %d stderr %s", code, errb)
	}
	if strings.Join(before, ",") != strings.Join(listCardIDs(t, home), ",") {
		t.Fatal("bad objective wrote a card")
	}
}

func TestCardIDMatchesGitCheckRefFormat(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip(err)
	}
	ids := []string{
		"c-parse", "c-cdx-b1-card-import", "draft..one", "draft.one", "draft.", ".draft",
		"foo.lock", "foo.Lock", "a.lock.b", "a/b", "feature/name", "a\\b", "a b",
		"a@{b", "a@b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a]b", "卡片",
		"c-ok.v2", "v1.2.3", "@", "HEAD", "ends.lockx",
	}
	for _, id := range ids {
		cmd := exec.Command("git", "check-ref-format", "refs/heads/cardex/o1/c/"+id)
		gitOK := cmd.Run() == nil
		want := gitOK && !strings.ContainsAny(id, "/\\")
		if stableCardID(id) != want {
			t.Fatalf("id %q gitOK %v stable %v", id, gitOK, stableCardID(id))
		}
	}
}

func TestCardImportBodyFileSymlinkStaysInManifest(t *testing.T) {
	home, _ := seedImportHome(t)
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	dir := filepath.Join(base, "manifest")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const secret = "SECRET-OUTSIDE\n"
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip(err.Error())
		}
		t.Fatal(err)
	}
	manifest := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards": []any{map[string]any{
			"id": "c-link", "kind": "dev", "title": "链接", "body_file": "link/secret.md",
		}},
	})
	before := listCardIDs(t, home)
	code, out, errb := callCLI(t, []string{"--home", home, "card", "import", "--objective", "o1", "--file", manifest, "--apply"})
	if code == 0 || !strings.Contains(errb, "body_file") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	if strings.Contains(out, secret) || strings.Contains(errb, secret) {
		t.Fatal("escaped body was echoed")
	}
	if strings.Join(before, ",") != strings.Join(listCardIDs(t, home), ",") {
		t.Fatal("symlink escape wrote a card")
	}

	if err := os.Mkdir(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	const inside = "目录内正文\n"
	if err := os.WriteFile(filepath.Join(dir, "real", "note.md"), []byte(inside), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	kept := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards": []any{map[string]any{
			"id": "c-inside", "kind": "dev", "title": "内部链接", "body_file": "link/note.md",
		}},
	})
	code, _, errb = callCLI(t, []string{"--home", home, "card", "import", "--objective", "o1", "--file", kept, "--apply"})
	if code != 0 {
		t.Fatalf("inside exit %d stderr %s", code, errb)
	}
	if got := importGetCard(t, home, "c-inside"); got.Body != inside || got.ID != "c-inside" {
		t.Fatalf("inside body %+v", got)
	}
}

func TestCardImportBodyFileDotSegment(t *testing.T) {
	home, _ := seedImportHome(t)
	base := t.TempDir()
	dir := filepath.Join(base, "manifest")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	const dotBody = "点路径正文\n$(echo 不要执行)\n"
	const nested = "子目录点路径\n"
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte(dotBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "note.md"), []byte(nested), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "secret.md"), []byte("SECRET-OUTSIDE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards": []any{
			map[string]any{"id": "c-dot", "kind": "dev", "title": "点", "body_file": "./note.md"},
			map[string]any{"id": "c-dot-sub", "kind": "dev", "title": "点子", "body_file": "./sub/./note.md"},
		},
	})
	code, out, errb := callCLI(t, []string{"--home", home, "--json", "card", "import", "--objective", "o1", "--file", manifest})
	if code != 0 {
		t.Fatalf("preview exit %d\n%s\n%s", code, out, errb)
	}
	code, _, errb = callCLI(t, []string{"--home", home, "card", "import", "--objective", "o1", "--file", manifest, "--apply"})
	if code != 0 {
		t.Fatalf("apply exit %d stderr %s", code, errb)
	}
	if got := importGetCard(t, home, "c-dot"); got.Body != dotBody {
		t.Fatalf("dot body %q", got.Body)
	}
	if got := importGetCard(t, home, "c-dot-sub"); got.Body != nested {
		t.Fatalf("nested body %q", got.Body)
	}
	escaped := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards": []any{map[string]any{
			"id": "c-dot-out", "kind": "dev", "title": "外出", "body_file": "./../secret.md",
		}},
	})
	before := listCardIDs(t, home)
	code, out, errb = callCLI(t, []string{"--home", home, "card", "import", "--objective", "o1", "--file", escaped, "--apply"})
	if code == 0 || !strings.Contains(errb, "body_file") {
		t.Fatalf("escape exit %d\n%s\n%s", code, out, errb)
	}
	if strings.Contains(out, "SECRET-OUTSIDE") || strings.Contains(errb, "SECRET-OUTSIDE") {
		t.Fatal("escaped body was echoed")
	}
	if strings.Join(before, ",") != strings.Join(listCardIDs(t, home), ",") {
		t.Fatal("dot escape wrote a card")
	}
}

func TestCardImportApplyWritesLiteralHome(t *testing.T) {
	parent := t.TempDir()
	const literalName = "target%41notes"
	const aliasName = "targetAnotes"
	literalHome := filepath.Join(parent, literalName)
	aliasHome := filepath.Join(parent, aliasName)
	if err := os.Mkdir(literalHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(aliasHome, 0o755); err != nil {
		t.Fatal(err)
	}
	seedNamedHome(t, literalHome, "c-keep-literal")
	seedNamedHome(t, aliasHome, "c-keep-alias")
	if ids := literalCardIDs(t, literalHome); strings.Join(ids, ",") != "c-keep-literal" {
		t.Fatalf("literal seed ids %v", ids)
	}
	if ids := literalCardIDs(t, aliasHome); strings.Join(ids, ",") != "c-keep-alias" {
		t.Fatalf("alias seed ids %v", ids)
	}
	dir := t.TempDir()
	const body = "指定库正文\n$(echo 不要执行)\n"
	manifest := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards": []any{map[string]any{
			"id": "c-new", "kind": "dev", "title": "新卡", "body": body,
		}},
	})
	code, out, errb := callCLI(t, []string{"--home", literalHome, "--json", "card", "import", "--objective", "o1", "--file", manifest})
	if code != 0 {
		t.Fatalf("preview exit %d\n%s\n%s", code, out, errb)
	}
	var preview importReportJSON
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Mode != "preview" || len(preview.Conflicts) != 0 {
		t.Fatalf("preview %+v", preview)
	}
	code, _, errb = callCLI(t, []string{"--home", literalHome, "card", "import", "--objective", "o1", "--file", manifest, "--apply"})
	if code != 0 {
		t.Fatalf("apply exit %d stderr %s", code, errb)
	}
	if ids := literalCardIDs(t, literalHome); strings.Join(ids, ",") != "c-keep-literal,c-new" {
		t.Fatalf("literal ids %v", ids)
	}
	if got := literalCardBody(t, literalHome, "c-new"); got != body {
		t.Fatalf("stored body %q", got)
	}
	if ids := literalCardIDs(t, aliasHome); strings.Join(ids, ",") != "c-keep-alias" {
		t.Fatalf("alias ids after apply %v", ids)
	}
	code, out, errb = callCLI(t, []string{"--home", literalHome, "--json", "card", "show", "c-new"})
	if code != 0 || !strings.Contains(out, "c-new") || !strings.Contains(out, "指定库正文") {
		t.Fatalf("show literal exit %d\n%s\n%s", code, out, errb)
	}
	code, out, errb = callCLI(t, []string{"--home", aliasHome, "--json", "card", "show", "c-new"})
	if code == 0 || strings.Contains(out, "指定库正文") {
		t.Fatalf("show alias exit %d\n%s\n%s", code, out, errb)
	}
	code, out, errb = callCLI(t, []string{"--home", literalHome, "--json", "card", "import", "--objective", "o1", "--file", manifest})
	if code == 0 || (!strings.Contains(out, "already exists") && !strings.Contains(errb, "already exists")) {
		t.Fatalf("second preview exit %d\n%s\n%s", code, out, errb)
	}
	if ids := literalCardIDs(t, literalHome); strings.Join(ids, ",") != "c-keep-literal,c-new" {
		t.Fatalf("literal ids after second preview %v", ids)
	}
	code, out, errb = callCLI(t, []string{"--home", aliasHome, "--json", "card", "import", "--objective", "o1", "--file", manifest})
	if code != 0 {
		t.Fatalf("alias preview exit %d\n%s\n%s", code, out, errb)
	}
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Mode != "preview" || len(preview.Conflicts) != 0 {
		t.Fatalf("alias still holds the new card: %+v", preview)
	}
	if ids := literalCardIDs(t, aliasHome); strings.Join(ids, ",") != "c-keep-alias" {
		t.Fatalf("alias preview wrote %v", ids)
	}
}

func TestCardImportRejectsTrailingJSON(t *testing.T) {
	home, _ := seedImportHome(t)
	dir := t.TempDir()
	manifest := writeManifest(t, dir, map[string]any{
		"version": 1,
		"cards": []any{map[string]any{
			"id": "c-trail", "kind": "dev", "title": "尾部", "body": "不要入库",
		}},
	})
	raw := readBytes(t, manifest)
	if err := os.WriteFile(manifest, append(raw, ']'), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, apply := range []bool{false, true} {
		args := []string{"--home", home, "card", "import", "--objective", "o1", "--file", manifest}
		if apply {
			args = append(args, "--apply")
		}
		before := listCardIDs(t, home)
		code, _, errb := callCLI(t, args)
		if code == 0 || !strings.Contains(errb, "trailing data") {
			t.Fatalf("apply %v exit %d stderr %s", apply, code, errb)
		}
		if strings.Join(before, ",") != strings.Join(listCardIDs(t, home), ",") {
			t.Fatalf("apply %v wrote cards", apply)
		}
	}
}

func seedNamedHome(t *testing.T, home, keepID string) {
	t.Helper()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "目标")
	mustHome(t, home, "card", "add", "--id", keepID, "--objective", "o1", "--title", "保留", "--body", keepID, "--kind", "dev")
}

func literalCardIDs(t *testing.T, home string) []string {
	t.Helper()
	db := openLiteralSQLite(t, home)
	defer db.Close()
	rows, err := db.Query(`SELECT id FROM cards ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func literalCardBody(t *testing.T, home, id string) string {
	t.Helper()
	db := openLiteralSQLite(t, home)
	defer db.Close()
	var body string
	if err := db.QueryRow(`SELECT body FROM cards WHERE id=?`, id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func importFixtureURI(path, query string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // file:///C:/... rather than file://C:/...
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: query}).String()
}

func openLiteralSQLite(t *testing.T, home string) *sql.DB {
	t.Helper()
	path := filepath.Join(home, "cardex.db")
	db, err := sql.Open("sqlite", importFixtureURI(path, "mode=ro&immutable=1"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	var opened string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&opened); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if !strings.Contains(opened, filepath.Base(home)) {
		_ = db.Close()
		t.Fatalf("opened %s, want a file inside %s", opened, home)
	}
	return db
}

func seedImportHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p1", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o1", "--project", "p1", "--title", "目标")
	mustHome(t, home, "card", "add", "--id", "c-keep", "--objective", "o1", "--title", "保留", "--body", "keep-body", "--kind", "dev", "--acceptance", "go test ./keep")
	withDB(t, home, func(db *store.Store) {
		ch := model.Channel{
			ID: "ch-deploy", Runner: model.RunnerClaude, Tier: model.TierT2, Health: model.HealthOK,
			Models: []string{"deploy-model"},
		}
		if err := db.CreateChannel(context.Background(), &ch); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{\"schedules\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, filepath.Join(home, "cardex.db")
}

func writeManifest(t *testing.T, dir string, v any) string {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type importReportJSON struct {
	Objective string `json:"objective"`
	Mode      string `json:"mode"`
	Cards     []struct {
		ID         string   `json:"id"`
		Kind       string   `json:"kind"`
		Title      string   `json:"title"`
		Deps       []string `json:"deps"`
		Claims     []string `json:"claims"`
		Acceptance []string `json:"acceptance"`
	} `json:"cards"`
	Conflicts []string `json:"conflicts"`
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func listCardIDs(t *testing.T, home string) []string {
	t.Helper()
	var ids []string
	withDB(t, home, func(db *store.Store) {
		cards, err := db.ListCards(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cards {
			ids = append(ids, c.ID)
		}
	})
	return ids
}

func importGetCard(t *testing.T, home, id string) model.Card {
	t.Helper()
	var got model.Card
	withDB(t, home, func(db *store.Store) {
		c, err := db.GetCard(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		got = c
	})
	return got
}

func assertCardMissing(t *testing.T, home, id string) {
	t.Helper()
	withDB(t, home, func(db *store.Store) {
		_, err := db.GetCard(context.Background(), id)
		if err == nil {
			t.Fatalf("card %s was written", id)
		}
	})
}

func mustGetChannel(t *testing.T, home, id string) model.Channel {
	t.Helper()
	var got model.Channel
	withDB(t, home, func(db *store.Store) {
		c, err := db.GetChannel(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		got = c
	})
	return got
}

func mustOrigin(t *testing.T, home, id string) string {
	t.Helper()
	var origin string
	withDB(t, home, func(db *store.Store) {
		got, err := loadCardOrigin(context.Background(), db, id)
		if err != nil {
			t.Fatal(err)
		}
		origin = got
	})
	return origin
}

func originCount(t *testing.T, home, id string) int {
	t.Helper()
	var n int
	withDB(t, home, func(db *store.Store) {
		evs, err := db.ListEvents(context.Background(), "card:"+id)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if ev.Type == "origin" {
				n++
			}
		}
	})
	return n
}

func TestCardImportRollbackAfterPartialWrite(t *testing.T) {
	home, dbPath := seedImportHome(t)
	raw, err := sql.Open("sqlite", importFixtureURI(dbPath, "mode=rw"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER reject_second_origin BEFORE INSERT ON events
		WHEN NEW.entity='card:c-second' AND NEW.event_type='origin'
		BEGIN SELECT RAISE(ABORT, 'fixture origin failure'); END;`); err != nil {
		t.Fatal(err)
	}
	file := writeManifest(t, t.TempDir(), map[string]any{
		"version": 1,
		"cards": []any{
			map[string]any{"id": "c-first", "kind": "dev", "title": "First", "body": "one", "claims": []any{map[string]any{"path": "first.go"}}},
			map[string]any{"id": "c-second", "kind": "dev", "title": "Second", "body": "two", "claims": []any{map[string]any{"path": "second.go"}}},
		},
	})
	code, out, errb := callCLI(t, []string{"--home", home, "card", "import", "--objective", "o1", "--file", file, "--apply", "--origin", "file:fixture"})
	if code == 0 || !strings.Contains(errb, "fixture origin failure") {
		t.Fatalf("expected failure after both inserts, got %d %s %s", code, out, errb)
	}
	for _, stmt := range []string{
		`SELECT COUNT(*) FROM cards WHERE id IN ('c-first', 'c-second')`,
		`SELECT COUNT(*) FROM claims WHERE card_id IN ('c-first', 'c-second')`,
		`SELECT COUNT(*) FROM events WHERE entity IN ('card:c-first', 'card:c-second')`,
	} {
		var count int
		if err := raw.QueryRow(stmt).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rollback query %s: count=%d err=%v", stmt, count, err)
		}
	}
}

func TestCardImportCompetingBatchesHaveOneWinner(t *testing.T) {
	home, _ := seedImportHome(t)
	type result struct {
		code int
		out  string
		err  string
		id   string
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, id := range []string{"c-red", "c-blue"} {
		file := writeManifest(t, t.TempDir(), map[string]any{
			"version": 1,
			"cards": []any{
				map[string]any{"id": id, "kind": "dev", "title": id, "body": "", "deps": []string{"c-common"}},
				map[string]any{"id": "c-common", "kind": "dev", "title": "Shared", "body": ""},
			},
		})
		go func() {
			<-start
			code, out, err := callCLI(t, []string{"--home", home, "--json", "card", "import", "--objective", "o1", "--file", file, "--apply"})
			results <- result{code, out, err, id}
		}()
	}
	close(start)
	success := 0
	for range 2 {
		r := <-results
		if r.code == 0 {
			success++
			if !strings.Contains(r.out, "applied") {
				t.Fatalf("winner has no commit report: %+v", r)
			}
			_ = importGetCard(t, home, r.id)
		} else {
			if !strings.Contains(r.err, "c-common already exists") {
				t.Fatalf("loser should report the shared ID conflict: %+v", r)
			}
			assertCardMissing(t, home, r.id)
		}
	}
	if success != 1 {
		t.Fatalf("committed batches: %d", success)
	}
	if ids := listCardIDs(t, home); len(ids) != 3 {
		t.Fatalf("expected existing card plus winning batch: %v", ids)
	}
}

func TestCardImportManagedExecutorCannotApply(t *testing.T) {
	t.Setenv("CARDEX_MANAGED_EXECUTOR", "1")
	home := filepath.Join(t.TempDir(), "absent")
	args := []string{"--home", home, "card", "import"}
	code, out, errb := callCLI(t, append(append([]string{}, args...), "--help"))
	if code != 0 || !strings.Contains(out, "preview") {
		t.Fatalf("managed help: %d %s %s", code, out, errb)
	}
	code, _, errb = callCLI(t, append(args, "--objective", "o1", "--file", "--help", "--apply"))
	if code != 2 || !strings.Contains(errb, "cannot write cards") {
		t.Fatalf("managed write incorrectly permitted: %d %s", code, errb)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("managed write created a home: %v", err)
	}
}

func TestCardImportRejectsAmbiguousOrOversizedInput(t *testing.T) {
	for _, c := range []struct{ name, manifest, want string }{
		{"duplicate", `{"version":1,"cards":[{"id":"c-first","id":"c-second","kind":"dev","title":"Title","body":""}]}`, "duplicate field"},
		{"case", `{"version":1,"cards":[{"ID":"c-first","kind":"dev","title":"Title","body":""}]}`, "unsupported field"},
		{"large-manifest", strings.Repeat(" ", maxImportManifest+1), "exceeds"},
		{"overflowing-number", `{"version":` + strings.Repeat("12345", 10000) + `,"cards":[]}`, "requires int"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(f, []byte(c.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadCardManifest(f); err == nil || !strings.Contains(err.Error(), c.want) || len(err.Error()) > 260 {
				t.Fatal("manifest did not fail with a bounded diagnostic")
			}
		})
	}
	t.Run("large-body-file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "body.md"), []byte(strings.Repeat("x", maxImportBody+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		file := writeManifest(t, dir, map[string]any{
			"version": 1,
			"cards":   []any{map[string]any{"id": "c-bodied", "kind": "dev", "title": "File story", "body_file": "body.md"}},
		})
		if _, err := loadCardManifest(file); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversize body accepted: %v", err)
		}
	})
}

func TestCardImportCapsDiagnosticVolume(t *testing.T) {
	home, _ := seedImportHome(t)
	before := importFiles(t, home)
	longID := strings.Repeat("长", 40) // 120 UTF-8 bytes.
	deps := make([]string, 150)
	for i := range deps {
		deps[i] = fmt.Sprintf("m%03d-%s", i, longID)
	}
	cards := []any{map[string]any{
		"id": "c-" + longID, "kind": "dev", "title": "Invalid batch", "body": "", "deps": deps,
	}}
	file := writeManifest(t, t.TempDir(), map[string]any{"version": 1, "cards": cards})
	args := []string{"--home", home, "--json", "card", "import", "--objective", "o1", "--file", file}
	for _, apply := range []bool{false, true} {
		cmd := append([]string{}, args...)
		if apply {
			cmd = append(cmd, "--apply")
		}
		code, out, errb := callCLI(t, cmd)
		if code == 0 || len(errb) > maxImportIssueSum+maxImportIssues || !strings.Contains(errb, "additional conflicts omitted") {
			t.Fatalf("apply=%v: diagnostic budget was not enforced: code=%d stderr-bytes=%d", apply, code, len(errb))
		}
		var report importReportJSON
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Conflicts) > maxImportIssues || !strings.Contains(report.Conflicts[0], "m000-") {
			t.Fatalf("expected a bounded/deterministic report, got %d conflicts", len(report.Conflicts))
		}
	}
	assertImportFilesUnchanged(t, home, before)
}

func TestCardImportDenseCycleReportsOneEdgeAndWritesNothing(t *testing.T) {
	home, _ := seedImportHome(t)
	before := importFiles(t, home)
	ids := make([]string, 64)
	for i := range ids {
		ids[i] = fmt.Sprintf("c%02d-%s", i, strings.Repeat("x", maxImportIDBytes-4))
	}
	cards := make([]any, 0, len(ids))
	for i, id := range ids {
		// A long first DFS path plus thousands of possible back-edges.
		deps := append([]string{ids[(i+1)%len(ids)]}, ids...)
		cards = append(cards, map[string]any{"id": id, "kind": "dev", "title": "Cycle fixture", "body": "", "deps": deps})
	}
	file := writeManifest(t, t.TempDir(), map[string]any{"version": 1, "cards": cards})
	code, out, errb := callCLI(t, []string{"--home", home, "--json", "card", "import", "--objective", "o1", "--file", file, "--apply"})
	var report importReportJSON
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("invalid report: %v (exit %d; stderr bytes %d)", err, code, len(errb))
	}
	if code == 0 || len(errb) > maxImportIssue+1 || len(report.Conflicts) != 1 ||
		!strings.Contains(report.Conflicts[0], "dependency cycle: edge from "+ids[len(ids)-1]+" to "+ids[0]) {
		t.Fatalf("expected deterministic, bounded cycle evidence: code=%d conflicts=%d stderr-bytes=%d", code, len(report.Conflicts), len(errb))
	}
	assertImportFilesUnchanged(t, home, before)
}

func TestCardImportRejectsOversizedGraphBeforeWriterLock(t *testing.T) {
	home, dbPath := seedImportHome(t)
	db, err := sql.Open("sqlite", importFixtureURI(dbPath, "mode=rw&_txlock=immediate"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	const sentinel = "synthetic-malformed-id-not-for-output"
	longID := strings.Repeat(sentinel, maxImportIDBytes)
	deps := make([]string, maxImportDeps)
	for i := range deps {
		deps[i] = fmt.Sprintf("c-dep-%03d", i)
	}
	many := []any{}
	for i := 0; i <= maxImportEdges/maxImportDeps; i++ {
		many = append(many, map[string]any{"id": fmt.Sprintf("c-card-%03d", i), "kind": "dev", "title": "Edges", "body": "", "deps": deps})
	}
	for name, v := range map[string][]any{
		"oversized-id":         {map[string]any{"id": longID, "kind": "dev", "title": "Invalid", "body": ""}},
		"oversized-dep":        {map[string]any{"id": "c-invalid", "kind": "dev", "title": "Invalid dep", "body": "", "deps": []string{longID}}},
		"oversized-edge-count": many,
	} {
		t.Run(name, func(t *testing.T) {
			file := writeManifest(t, t.TempDir(), map[string]any{"version": 1, "cards": v})
			type outcome struct {
				code   int
				out    string
				stderr string
			}
			done := make(chan outcome, 1)
			go func() {
				code, out, errb := callCLI(t, []string{"--home", home, "card", "import", "--objective", "o1", "--file", file, "--apply"})
				done <- outcome{code, out, errb}
			}()
			select {
			case got := <-done:
				if got.code == 0 || got.out != "" || len(got.stderr) > 160 || strings.Contains(got.stderr, sentinel) ||
					!(strings.Contains(got.stderr, "128 bytes") || strings.Contains(got.stderr, "unique dependency edges")) {
					t.Fatalf("expected a small parse failure before acquiring a writer lock: code=%d stderr-bytes=%d", got.code, len(got.stderr))
				}
			case <-time.After(3 * time.Second):
				t.Fatal("invalid manifest blocked on the database writer")
			}
		})
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM cards WHERE id<>'c-keep'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unexpected rows %d, err %v", n, err)
	}
}

func TestCardImportMissingTransitiveDependencyAndDedup(t *testing.T) {
	home, _ := seedImportHome(t)
	mustHome(t, home, "card", "add", "--id", "c-parent", "--objective", "o1", "--kind", "dev", "--title", "Existing card", "--dep", "c-missing-child")
	file := writeManifest(t, t.TempDir(), map[string]any{
		"version": 1,
		"cards":   []any{map[string]any{"id": "c-imported", "kind": "dev", "title": "Card", "body": "", "deps": []string{"c-parent", "c-parent", "c-keep", "c-parent"}}},
	})
	before := importFiles(t, home)
	args := []string{"--home", home, "--json", "card", "import", "--objective", "o1", "--file", file}
	for _, apply := range []bool{false, true} {
		cmd := append([]string{}, args...)
		if apply {
			cmd = append(cmd, "--apply")
		}
		code, _, errb := callCLI(t, cmd)
		if code == 0 || !strings.Contains(errb, "dependency c-missing-child of c-parent not found") {
			t.Fatalf("transitive dependency was not rejected: %d %s", code, errb)
		}
	}
	assertImportFilesUnchanged(t, home, before)
	mustHome(t, home, "card", "add", "--id", "c-missing-child", "--objective", "o1", "--kind", "dev", "--title", "Now exists")
	code, _, errb := callCLI(t, append(args, "--apply"))
	if code != 0 {
		t.Fatalf("apply corrected graph: %d %s", code, errb)
	}
	if got := strings.Join(importGetCard(t, home, "c-imported").Deps, ","); got != "c-parent,c-keep" {
		t.Fatalf("dependencies not deduplicated in first order: %q", got)
	}
}

func TestCardImportExistingStrictGraphUsesLogicalDependencies(t *testing.T) {
	home, _ := seedImportHome(t)
	mustCLI(t, "--home", home, "card", "add", "--objective", "o1", "--id", "strict-empty", "--title", "empty strict", "--strict-deps")
	mustCLI(t, "--home", home, "card", "add", "--objective", "o1", "--id", "strict-bound", "--title", "bound strict", "--strict-deps", "--dep", "c-keep")
	file := writeManifest(t, t.TempDir(), map[string]any{"version": 1, "cards": []any{
		map[string]any{"id": "downstream", "kind": "dev", "title": "synthetic", "body": "", "deps": []string{"strict-empty", "strict-bound"}},
	}})
	before := importFiles(t, home)
	args := []string{"--home", home, "--json", "card", "import", "--objective", "o1", "--file", file}
	code, out, errb := callCLI(t, args)
	if code != 0 || !strings.Contains(out, "preview") {
		t.Fatalf("preview: %d %s %s", code, out, errb)
	}
	assertImportFilesUnchanged(t, home, before)
	code, out, errb = callCLI(t, append(args, "--apply"))
	if code != 0 || !strings.Contains(out, "applied") {
		t.Fatalf("apply: %d %s %s", code, out, errb)
	}
	db := openHome(t, home)
	defer db.Close()
	got := mustGetCard(t, db, "downstream")
	if len(got.Deps) != 2 || got.Deps[0] != "strict-empty" || got.Deps[1] != "strict-bound" || got.StrictDeps {
		t.Fatalf("changed downstream declared deps: %+v", got)
	}
}
