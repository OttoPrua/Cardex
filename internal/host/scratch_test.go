package host_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestRunScratchInjectsUnsetTempEnvAndCleansOnlyThatAttempt(t *testing.T) {
	scratch := t.TempDir()
	if err := os.MkdirAll(filepath.Join(scratch, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "other", "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	home, id, _ := seedScratchAttempt(t, echoEnvScript)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"scratch_dir":`+jsonString(scratch)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreUnset(t, "TMPDIR", "GOTMPDIR", "GOCACHE")
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	log := readAttemptLog(t, filepath.Join(home, "attempts", id, "output.log"))
	dir := checkScratchLog(t, scratch, id, log)
	wantTmp := filepath.Join(dir, "tmp")
	wantCache := filepath.Join(dir, "gocache")
	if !strings.Contains(log, "TMPDIR="+wantTmp) || !strings.Contains(log, "GOTMPDIR="+wantTmp) || !strings.Contains(log, "GOCACHE="+wantCache) {
		t.Fatalf("log\n%s", log)
	}
	if b, err := os.ReadFile(filepath.Join(scratch, "other", "keep")); err != nil || string(b) != "x" {
		t.Fatalf("sibling %q %v", b, err)
	}
}

func TestRunScratchKeepsExplicitTMPDIR(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "explicit-tmp")
	t.Setenv("TMPDIR", sentinel)
	scratch := t.TempDir()
	home, id, _ := seedScratchAttempt(t, echoEnvScript)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"scratch_dir":`+jsonString(scratch)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	log := readAttemptLog(t, filepath.Join(home, "attempts", id, "output.log"))
	if !strings.Contains(log, "TMPDIR="+sentinel) {
		t.Fatalf("log\n%s", log)
	}
	if strings.Contains(log, "TMPDIR="+scratch+string(os.PathSeparator)) {
		t.Fatalf("scratch overwrote TMPDIR\n%s", log)
	}
}

func TestRunProjectScratchOverridesHome(t *testing.T) {
	homeScratch := t.TempDir()
	projScratch := t.TempDir()
	home, id, _ := seedScratchAttempt(t, echoEnvScript)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"scratch_dir":`+jsonString(homeScratch)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	attachProjectScratch(t, home, projScratch)
	restoreUnset(t, "TMPDIR", "GOTMPDIR", "GOCACHE")
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	log := readAttemptLog(t, filepath.Join(home, "attempts", id, "output.log"))
	checkScratchLog(t, projScratch, id, log)
	if strings.Contains(log, homeScratch) {
		t.Fatalf("home scratch used\n%s", log)
	}
	if entries, err := os.ReadDir(homeScratch); err != nil || len(entries) != 0 {
		t.Fatalf("home scratch entries %v, error %v", entries, err)
	}
}

func TestRunCustomAttemptsDirWritesThere(t *testing.T) {
	home, id, _ := seedScratchAttempt(t, echoEnvScript)
	ext := filepath.Join(home, "ext-attempts")
	if err := os.Rename(filepath.Join(home, "attempts"), ext); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"attempts_dir":`+jsonString(ext)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := host.Run(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ext, id, "output.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "attempts", id, "output.log")); !os.IsNotExist(err) {
		t.Fatalf("home attempts gained a new log: %v", err)
	}
}

func checkScratchLog(t *testing.T, root, id, log string) string {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		tmp, ok := strings.CutPrefix(strings.TrimSpace(line), "TMPDIR=")
		if !ok {
			continue
		}
		dir := filepath.Dir(tmp)
		if filepath.Dir(dir) != root || !strings.HasPrefix(filepath.Base(dir), "cardex-"+id+"-") || filepath.Base(tmp) != "tmp" {
			t.Fatalf("TMPDIR %q is outside attempt scratch %s", tmp, root)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("attempt scratch remains: %v", err)
		}
		return dir
	}
	t.Fatalf("missing TMPDIR in log\n%s", log)
	return ""
}

func seedScratchAttempt(t *testing.T, build func(work string) any) (home, id, work string) {
	t.Helper()
	return seedPreparedCard(t, t.TempDir(), model.RunnerFake, "", 1, func(c *model.Card) {
		c.Sandbox = "off"
	}, build)
}

func echoEnvScript(string) any {
	return map[string]any{
		"attempts": map[string]any{
			"0": map[string]any{
				"echo_env": []string{"TMPDIR", "GOTMPDIR", "GOCACHE"},
			},
		},
	}
}

func attachProjectScratch(t *testing.T, home, scratch string) {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	p := model.Project{ID: "p1", RepoPath: home, WorktreeRoot: home, ScratchDir: scratch}
	if err := db.CreateProject(ctx, &p); err != nil {
		t.Fatal(err)
	}
	o := model.Objective{
		ID: "o", Project: "p1", Title: "t",
		Mode: model.ModeManual, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive,
	}
	if err := db.CreateObjective(ctx, &o); err != nil {
		t.Fatal(err)
	}
}

func restoreUnset(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		key := key
		val, ok := os.LookupEnv(key)
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(key, val)
				return
			}
			_ = os.Unsetenv(key)
		})
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func readAttemptLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
