package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateLoadUserCopyWinsOverEmbedded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := writeDefaultTemplates(root); err != nil {
		t.Fatal(err)
	}
	custom := "user-copy {{FOCUS}} keeps {{UNKNOWN}}"
	if err := os.WriteFile(filepath.Join(templatesDir(root), "design-review.md"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadTemplate(root, "design-review")
	if err != nil {
		t.Fatal(err)
	}
	if got != custom {
		t.Fatalf("user copy must win over embedded, got %q", got)
	}
}

func TestTemplateLoadMissingUserCopyFallsBackToEmbedded(t *testing.T) {
	t.Parallel()
	fakeRoot := filepath.Join(t.TempDir(), "no-such-root")
	got, err := loadTemplate(fakeRoot, "design-review")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "审核关注点：{{FOCUS}}") {
		t.Fatalf("embedded fallback missing consumer marker, got %q", got)
	}
	userPath := filepath.Join(templatesDir(fakeRoot), "design-review.md")
	if _, err := os.Lstat(userPath); !os.IsNotExist(err) {
		t.Fatalf("fallback must not write a user copy, stat=%v", err)
	}
}

func TestTemplateRenderSinglePass(t *testing.T) {
	t.Parallel()
	out := renderTemplate("A={{A}} | B={{B}}", map[string]string{"A": "甲含 {{B}} 字样", "B": "乙内容"})
	if out != "A=甲含 {{B}} 字样 | B=乙内容" {
		t.Fatalf("inserted placeholder was re-substituted: %q", out)
	}
	if got := renderTemplate("x {{UNKNOWN}} y", map[string]string{"A": "1"}); got != "x {{UNKNOWN}} y" {
		t.Fatalf("unknown key must stay literal, got %q", got)
	}
	for i := 0; i < 30; i++ {
		if renderTemplate("{{A}}{{B}}{{C}}", map[string]string{"A": "a", "B": "b", "C": "c"}) != "abc" {
			t.Fatal("render must be independent of map iteration order")
		}
	}
}

func TestTemplateInitLeavesPreexistingCustomBytesUnchanged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(templatesDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(templatesDir(root), "coordinate.md")
	custom := []byte("custom-coordinate-bytes {{GOAL}}")
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultTemplates(root); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultTemplates(root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, custom) {
		t.Fatalf("init overwrote preexisting bytes: %q", got)
	}
}

func TestTemplateStatusUnknownVsCurrentEquality(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(templatesDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	customPath := filepath.Join(templatesDir(root), "design-review.md")
	if err := os.WriteFile(customPath, []byte("old-install custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	shipped, err := loadTemplate(filepath.Join(t.TempDir(), "missing"), "fix-cycle")
	if err != nil {
		t.Fatal(err)
	}
	eqPath := filepath.Join(templatesDir(root), "fix-cycle.md")
	if err := os.WriteFile(eqPath, []byte(shipped), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultTemplates(root); err != nil {
		t.Fatal(err)
	}
	rows, err := listTemplateStatuses(root)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]templateFileStatus{}
	for _, row := range rows {
		byName[row.Name] = row
		if strings.Contains(strings.ToLower(row.Source+" "+row.Difference), "unmodified") {
			t.Fatalf("must not label unmodified from a new-version hash: %+v", row)
		}
	}
	dr := byName["design-review"]
	if dr.Source != "UNKNOWN" {
		t.Fatalf("preexisting custom without baseline must be UNKNOWN, got %+v", dr)
	}
	fc := byName["fix-cycle"]
	if fc.Source != "current-equality" || fc.Difference != "equal-to-current-shipped" {
		t.Fatalf("byte-equal to current shipped without baseline is current equality, got %+v", fc)
	}
	if fc.Source == "shipped" {
		t.Fatal("current equality must stay distinct from historic shipped provenance")
	}
	coord := byName["coordinate"]
	if coord.Source != "shipped" || coord.Difference != "none" {
		t.Fatalf("newly installed copy must record shipped digest, got %+v", coord)
	}
}

func TestTemplateStatusKnownPriorBaselineOutdatedAfterUpgrade(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := writeDefaultTemplates(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(templatesDir(root), "coordinate.md")
	oldShipped := []byte("previously-installed-shipped-v1 {{GOAL}}")
	if err := os.WriteFile(path, oldShipped, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordTemplateBaseline(root, "coordinate.md", oldShipped); err != nil {
		t.Fatal(err)
	}
	rows, err := listTemplateStatuses(root)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]templateFileStatus{}
	for _, row := range rows {
		byName[row.Name] = row
		if strings.Contains(strings.ToLower(row.Source+" "+row.Difference), "unmodified") {
			t.Fatalf("must not label unmodified: %+v", row)
		}
	}
	got := byName["coordinate"]
	if got.Source == "UNKNOWN" {
		t.Fatalf("known prior baseline must remain known after new shipped bytes, got %+v", got)
	}
	if got.Source != "recorded" || got.Difference != "outdated" {
		t.Fatalf("old recorded baseline vs new embed must be recorded/outdated, got %+v", got)
	}

	if err := os.WriteFile(path, []byte("user-edited after old install"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err = listTemplateStatuses(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name != "coordinate" {
			continue
		}
		if row.Source != "recorded" || row.Difference != "custom" {
			t.Fatalf("bytes matching neither recorded nor current shipped must be recorded/custom, got %+v", row)
		}
	}
}

func TestTemplateSelectedRefreshBackupThenShippedBaseline(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := writeDefaultTemplates(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(templatesDir(root), "prompt-assembly.md")
	old := []byte("user-edited prompt-assembly {{GOAL}}")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	unselectedPath := filepath.Join(templatesDir(root), "coordinate.md")
	unselected, err := os.ReadFile(unselectedPath)
	if err != nil {
		t.Fatal(err)
	}
	unselectedCustom := append([]byte("keep-unselected "), unselected...)
	if err := os.WriteFile(unselectedPath, unselectedCustom, 0o644); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(templatesDir(root), "notes.md")
	if err := os.WriteFile(extra, []byte("extra-custom"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := refreshSelectedTemplates(root, []string{"prompt-assembly"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].BackupPath == "" {
		t.Fatalf("first refresh must write an accessible backup, got %+v", results)
	}
	backup, err := os.ReadFile(results[0].BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, old) {
		t.Fatalf("backup bytes must equal pre-refresh file, got %q", backup)
	}
	now, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := embeddedTemplateBytes("prompt-assembly.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(now, shipped) {
		t.Fatal("refresh must replace selection with shipped bytes")
	}
	rows, err := listTemplateStatuses(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == "prompt-assembly" && (row.Source == "UNKNOWN" || row.Source == "current-equality") {
			t.Fatalf("refreshed selection must have truthful shipped baseline, got %+v", row)
		}
		if row.Name == "prompt-assembly" && (row.Source != "shipped" || row.Difference != "none") {
			t.Fatalf("refreshed selection status=%+v", row)
		}
	}
	gotUnselected, err := os.ReadFile(unselectedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotUnselected, unselectedCustom) {
		t.Fatal("unselected custom file changed")
	}
	gotExtra, err := os.ReadFile(extra)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotExtra) != "extra-custom" {
		t.Fatal("unselected extra file changed")
	}

	second, err := refreshSelectedTemplates(root, []string{"prompt-assembly"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("second refresh must succeed, got %+v", second)
	}
	if second[0].BackupPath != "" {
		t.Fatalf("second refresh must not recursively copy backups, got %+v", second)
	}
	backupAfter, err := os.ReadFile(results[0].BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backupAfter, old) {
		t.Fatal("second refresh overwrote the first backup")
	}
	entries, err := os.ReadDir(filepath.Dir(results[0].BackupPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("unchanged refresh must stay idempotent, backup count=%d", len(entries))
	}
}

func TestTemplateRefreshPreservesHistoricalBackups(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := writeDefaultTemplates(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(templatesDir(root), "prompt-assembly.md")
	first := []byte("custom-one {{GOAL}}")
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatal(err)
	}
	r1, err := refreshSelectedTemplates(root, []string{"prompt-assembly"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r1) != 1 || r1[0].BackupPath == "" {
		t.Fatalf("first refresh backup missing: %+v", r1)
	}
	second := []byte("custom-two {{GOAL}}")
	if err := os.WriteFile(path, second, 0o644); err != nil {
		t.Fatal(err)
	}
	r2, err := refreshSelectedTemplates(root, []string{"prompt-assembly"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r2) != 1 || r2[0].BackupPath == "" {
		t.Fatalf("second refresh backup missing: %+v", r2)
	}
	if r2[0].BackupPath == r1[0].BackupPath {
		t.Fatal("second refresh overwrote the first backup path")
	}
	b1, err := os.ReadFile(r1[0].BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(r2[0].BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, first) {
		t.Fatalf("first historical backup lost, got %q", b1)
	}
	if !bytes.Equal(b2, second) {
		t.Fatalf("second historical backup mismatch, got %q", b2)
	}
	r3, err := refreshSelectedTemplates(root, []string{"prompt-assembly"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r3) != 1 || r3[0].BackupPath != "" {
		t.Fatalf("unchanged refresh must not add a backup, got %+v", r3)
	}
	entries, err := os.ReadDir(filepath.Dir(r1[0].BackupPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("historical backups must both remain, got %d", len(entries))
	}
}

func TestTemplateRefreshRefusesUnsafeNamesAndWritesNothingOutside(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := writeDefaultTemplates(root); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	sentinel := filepath.Join(outsideDir, "sentinel.md")
	if err := os.WriteFile(sentinel, []byte("outside-original"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(templatesDir(root), "retro.md"))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"..", "../retro", sentinel, filepath.Join(root, "..", "escape.md")} {
		if _, err := refreshSelectedTemplates(root, []string{name}); err == nil {
			t.Fatalf("unsafe name %q must be refused", name)
		}
	}

	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside-original" {
		t.Fatalf("absolute/outside path was written: %q", got)
	}
	after, err := os.ReadFile(filepath.Join(templatesDir(root), "retro.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused names still mutated templates")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(root), "escape.md")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote outside root: %v", err)
	}

	dest := filepath.Join(templatesDir(root), "retro.md")
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, dest); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := refreshSelectedTemplates(root, []string{"retro"}); err == nil {
		t.Fatal("symlink-out-of-root must be refused")
	}
	got, err = os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside-original" {
		t.Fatalf("symlink write escaped root: %q", got)
	}
}

func TestTemplateInitRefreshRefuseTemplatesDirSymlinkOutsideRoot(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel.md")
	if err := os.WriteFile(sentinel, []byte("outside-original"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "templates")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := writeDefaultTemplates(root); err == nil {
		t.Fatal("init must refuse templates dir symlink outside root")
	}
	if _, err := refreshSelectedTemplates(root, []string{"coordinate"}); err == nil {
		t.Fatal("refresh must refuse templates dir symlink outside root")
	}
	if _, err := listTemplateStatuses(root); err == nil {
		t.Fatal("status must refuse templates dir symlink outside root")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside-original" {
		t.Fatalf("outside sentinel mutated: %q", got)
	}
	after, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("init/refresh wrote outside root, before=%d after=%d", len(before), len(after))
	}
}

func TestTemplateChosenRootAliasAllowsInitStatusRefresh(t *testing.T) {
	t.Parallel()
	realRoot := t.TempDir()
	alias := filepath.Join(t.TempDir(), "chosen-root-alias")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := writeDefaultTemplates(alias); err != nil {
		t.Fatalf("init via chosen-root alias must succeed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(realRoot, "templates", "design-review.md")); err != nil {
		t.Fatalf("init via alias must write into the actual selected root: %v", err)
	}
	rows, err := listTemplateStatuses(alias)
	if err != nil {
		t.Fatalf("status via chosen-root alias must succeed: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("status via alias returned no templates")
	}
	custom := []byte("alias-custom {{GOAL}}")
	if err := os.WriteFile(filepath.Join(realRoot, "templates", "coordinate.md"), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := refreshSelectedTemplates(alias, []string{"coordinate"})
	if err != nil {
		t.Fatalf("refresh via chosen-root alias must succeed: %v", err)
	}
	if len(results) != 1 || results[0].BackupPath == "" {
		t.Fatalf("refresh via alias missing backup: %+v", results)
	}
	got, err := os.ReadFile(results[0].BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, custom) {
		t.Fatalf("alias refresh backup mismatch: %q", got)
	}
	actual, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	backup := results[0].BackupPath
	if resolved, err := filepath.EvalSymlinks(backup); err == nil {
		backup = resolved
	}
	if !pathInsideRoot(filepath.Clean(actual), filepath.Clean(backup)) {
		t.Fatalf("backup must stay under the actual selected root, backup=%s actual=%s", results[0].BackupPath, actual)
	}

	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel.md")
	if err := os.WriteFile(sentinel, []byte("outside-original"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	escapedReal := t.TempDir()
	escapedAlias := filepath.Join(t.TempDir(), "escaped-root-alias")
	if err := os.Symlink(escapedReal, escapedAlias); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(escapedReal, "templates")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := writeDefaultTemplates(escapedAlias); err == nil {
		t.Fatal("init via alias must still refuse templates dir symlink outside the actual selected root")
	}
	if _, err := refreshSelectedTemplates(escapedAlias, []string{"coordinate"}); err == nil {
		t.Fatal("refresh via alias must still refuse escaping templates descendant")
	}
	if _, err := listTemplateStatuses(escapedAlias); err == nil {
		t.Fatal("status via alias must still refuse escaping templates descendant")
	}
	gotSentinel, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSentinel) != "outside-original" {
		t.Fatalf("outside sentinel mutated: %q", gotSentinel)
	}
	after, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("alias init/refresh wrote outside actual root, before=%d after=%d", len(before), len(after))
	}
}

func TestTemplateRefreshBackupFailurePreservesOriginal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := writeDefaultTemplates(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(templatesDir(root), "workflow-writer.md")
	original := []byte("do-not-lose-me {{MODULE}}")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templatesDir(root), templateBackupDirName), []byte("not-a-dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshSelectedTemplates(root, []string{"workflow-writer"}); err == nil {
		t.Fatal("backup failure must error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("backup failure mutated original: %q", got)
	}
}

func TestTemplateConsumerPlaceholdersAndShapes(t *testing.T) {
	t.Parallel()
	fake := filepath.Join(t.TempDir(), "missing")
	cases := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{"design-review", map[string]string{"DIR": "/proj", "FOCUS": "focus"}, []string{"审核关注点：focus", "/proj", "```json", "Windows x64", "本机原始工作区"}},
		{"fix-cycle", map[string]string{"ROUND": "1", "VERDICT": "concerns", "TITLE": "t", "SUMMARY": "s", "FINDINGS": "f", "ORIG_PROMPT": "p", "REVIEW_LOG": "l"}, []string{"按类闭合", "1", "concerns"}},
		{"coordinate", map[string]string{"GOAL": "g1", "DIR": "/d", "QUEUE": "q1", "PROGRESS": "p1"}, []string{"g1", "/d", "q1", "p1", `"review_after":false`, `"route_class"`}},
		{"prompt-assembly", map[string]string{"GOAL": "g2", "DIR": "/e"}, []string{"g2", "/e", `"review_after":false`, `"risk_class"`}},
		{"workflow-writer", map[string]string{"MODULE": "m", "MODE": "serial", "DIR": "/w", "GOAL": "g3", "CRITERIA": "c", "ROUND": "2"}, []string{"m", "serial", "/w", "g3", "c", "2"}},
		{"progress-brief", map[string]string{"OUT": "/out.json", "KEY": "k1"}, []string{"/out.json", "k1"}},
		{"progress-pull", map[string]string{}, []string{"```json", "next_prompt"}},
		{"retro", map[string]string{"N": "10", "ARCHIVE_DIR": "/a", "TASKS_DIR": "/t", "PROGRESS_DIR": "/p"}, []string{"10", "/a", "/t", "/p"}},
		{"crosscheck-merge", map[string]string{"TASK": "tk", "A": "aa", "B": "bb"}, []string{"tk", "aa", "bb", "```json"}},
		{"crosscheck-solo", map[string]string{"TASK": "solo-task"}, []string{"solo-task", "```json"}},
		{"fable-adversarial-merge", map[string]string{"TASK": "ft", "A": "fa"}, []string{"ft", "fa", "owner_hold"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tpl, err := loadTemplate(fake, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(tpl, "owner_mixed_routing") || strings.Contains(tpl, "sync-lane-to-5090") {
				t.Fatal("generic default still contains personal owner routing or private owner paths")
			}
			if strings.Contains(tpl, "尽力兼容") {
				t.Fatal("Windows coverage still uses old blanket best-effort wording")
			}
			out := renderTemplate(tpl, tc.vars)
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Fatalf("rendered %s missing %q", tc.name, w)
				}
			}
			if strings.Contains(tpl, "{{NO_SUCH_KEY}}") {
				t.Fatal("fixture")
			}
			if got := renderTemplate(tpl+" {{NO_SUCH_KEY}}", tc.vars); !strings.Contains(got, "{{NO_SUCH_KEY}}") {
				t.Fatal("unknown key must remain literal on real templates")
			}
		})
	}
}

func TestTemplateCLIInitStatusRefresh(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(templatesDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	custom := []byte("preexisting-unknown {{FOCUS}}")
	if err := os.WriteFile(filepath.Join(templatesDir(root), "design-review.md"), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	initOut, err := captureStdout(t, func() error {
		return cmdTemplates([]string{"init", "-root", root})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(initOut, templatesDir(root)) {
		t.Fatalf("init output missing templates dir: %q", initOut)
	}
	got, err := os.ReadFile(filepath.Join(templatesDir(root), "design-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, custom) {
		t.Fatal("CLI init overwrote preexisting custom bytes")
	}

	status1, err := captureStdout(t, func() error {
		return cmdTemplates([]string{"status", "-root", root})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status1, "design-review") || !strings.Contains(status1, "UNKNOWN") {
		t.Fatalf("status must list templates and UNKNOWN baseline, got %q", status1)
	}
	if strings.Contains(strings.ToLower(status1), "unmodified") {
		t.Fatalf("status labeled unmodified: %q", status1)
	}
	if !strings.Contains(status1, "coordinate") {
		t.Fatalf("status did not list initialized templates: %q", status1)
	}

	refresh1, err := captureStdout(t, func() error {
		return cmdTemplates([]string{"refresh", "-root", root, "design-review"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refresh1, "backup=") {
		t.Fatalf("refresh must report backup path, got %q", refresh1)
	}
	backupPath := strings.TrimSpace(refresh1[strings.Index(refresh1, "backup=")+len("backup="):])
	backupPath = strings.Fields(backupPath)[0]
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, custom) {
		t.Fatal("CLI backup bytes != pre-refresh file")
	}
	status2, err := captureStdout(t, func() error {
		return cmdTemplates([]string{"status", "-root", root})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(status2, "design-review") && strings.Contains(status2, "UNKNOWN") {
		for _, line := range strings.Split(status2, "\n") {
			if strings.Contains(line, "design-review") && strings.Contains(line, "UNKNOWN") {
				t.Fatalf("refreshed design-review still UNKNOWN: %q", line)
			}
		}
	}

	refresh2, err := captureStdout(t, func() error {
		return cmdTemplates([]string{"refresh", "-root", root, "design-review"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refresh2, "refreshed design-review") {
		t.Fatalf("second refresh must succeed, got %q", refresh2)
	}
}
