package roots

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadDefaultsAndRelativeDirs(t *testing.T) {
	home := t.TempDir()
	got := Load(home)
	if got.CustomBackups || got.CustomAttempts || got.CustomScratch || got.BackupsDir != "" || got.ScratchDir != "" {
		t.Fatalf("unset layout %+v", got)
	}
	if filepath.Clean(got.AttemptsDir) != filepath.Join(got.Home, "attempts") {
		t.Fatalf("attempts %s home %s", got.AttemptsDir, got.Home)
	}
	body := `{
		"backups_dir": "bak",
		"attempts_dir": "attempts",
		"scratch_dir": "scratch",
		"backups_dir_ignored": 1
	}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got = Load(home)
	if !got.CustomBackups || got.BackupsDir != filepath.Join(got.Home, "bak") {
		t.Fatalf("backups %+v", got)
	}
	if got.CustomAttempts || got.AttemptsDir != filepath.Join(got.Home, "attempts") {
		t.Fatalf("default attempts path stayed custom: %+v", got)
	}
	if !got.CustomScratch || got.ScratchDir != filepath.Join(got.Home, "scratch") {
		t.Fatalf("scratch %+v", got)
	}
	if dir, ok := ResolveDir(home, "rel"); !ok || dir != filepath.Join(got.Home, "rel") {
		t.Fatalf("resolve %s %v", dir, ok)
	}
	if _, ok := ResolveDir(home, "  "); ok {
		t.Fatal("empty dir resolved")
	}
}

func TestNonStringDirsStayUnset(t *testing.T) {
	home := t.TempDir()
	body := `{"backups_dir": 1, "attempts_dir": null, "scratch_dir": "", "engines": {"kimi": {}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load(home)
	if got.CustomBackups || got.CustomAttempts || got.CustomScratch {
		t.Fatalf("non-string keys configured the layout: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(home); got.CustomScratch || got.CustomBackups {
		t.Fatalf("invalid json configured the layout: %+v", got)
	}
}

func TestEvidencePathsStayCompatible(t *testing.T) {
	home := t.TempDir()
	ext := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"attempts_dir":`+quote(ext)+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	id := "a1"
	if got := EvidenceRef(home, id, "", "output.log"); got != filepath.Join(ext, id, "output.log") {
		t.Fatalf("new ref %s", got)
	}
	if got := EvidenceRef(home, id, "", ""); got != filepath.Join(ext, id) {
		t.Fatalf("dir ref %s", got)
	}
	legacy := filepath.Join(home, "attempts", id)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "output.log"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "result.md"), []byte("r"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := AttemptDir(home, id, ""); got != legacy {
		t.Fatalf("legacy dir %s", got)
	}
	if got := AttemptDirFor(home, id); got != filepath.Join(ext, id) {
		t.Fatalf("new attempt reused legacy dir %s", got)
	}
	if got := EvidenceRef(home, id, "", "output.log"); got != "attempts/"+id+"/output.log" {
		t.Fatalf("legacy ref %s", got)
	}
	refs := EvidenceRefs(home, id, "")
	if strings.Join(refs, ",") != "attempts/"+id+"/output.log,attempts/"+id+"/result.md" {
		t.Fatalf("refs %v", refs)
	}
	stored := ResolveStored(home, "attempts/"+id+"/output.log")
	if stored != filepath.Join(Load(home).Home, "attempts", id, "output.log") {
		t.Fatalf("resolved %s", stored)
	}
	abs := filepath.Join(ext, id, "output.log")
	if ResolveStored(home, abs) != abs {
		t.Fatalf("absolute stored path changed")
	}

	if err := os.MkdirAll(filepath.Join(ext, id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, id, "acceptance.log"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := AttemptDir(home, id, ""); got != filepath.Join(ext, id) {
		t.Fatalf("primary dir %s", got)
	}
	refs = EvidenceRefs(home, id, "")
	want := []string{
		filepath.Join(ext, id, "acceptance.log"),
		"attempts/" + id + "/output.log",
		"attempts/" + id + "/result.md",
	}
	if strings.Join(refs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mixed refs\n%s", strings.Join(refs, "\n"))
	}
}

func TestAttemptDirWithRelativeHome(t *testing.T) {
	parent := t.TempDir()
	t.Chdir(parent)
	home := "home"
	want := filepath.Join(parent, home, "attempts", "a1")
	if got := AttemptDirFor(home, "a1"); got != want {
		t.Fatalf("new attempt directory %q, want %q", got, want)
	}
	if got := AttemptDir(home, "a1", "attempts/a1/prompt.txt"); got != want {
		t.Fatalf("legacy attempt directory %q, want %q", got, want)
	}
}

func TestApplyTempEnvPreservesExplicitKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attempt")
	base := []string{"PATH=/bin", "TMPDIR=", "GOTMPDIR=keep", "GOCACHE=set"}
	out := ApplyTempEnv(base, dir)
	if len(base) != 4 {
		t.Fatalf("input mutated: %v", base)
	}
	if envValue(out, "TMPDIR") != "" || envValue(out, "GOTMPDIR") != "keep" || envValue(out, "GOCACHE") != "set" {
		t.Fatalf("explicit keys changed: %v", out)
	}
	if len(out) != len(base) {
		t.Fatalf("extra keys added: %v", out)
	}
	fresh := ApplyTempEnv([]string{"PATH=/bin"}, dir)
	if envValue(fresh, "TMPDIR") != filepath.Join(dir, "tmp") || envValue(fresh, "GOTMPDIR") != filepath.Join(dir, "tmp") {
		t.Fatalf("tmp %v", fresh)
	}
	if envValue(fresh, "GOCACHE") != filepath.Join(dir, "gocache") {
		t.Fatalf("cache %v", fresh)
	}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"TMP", "TEMP"} {
			if envValue(fresh, key) != filepath.Join(dir, "tmp") {
				t.Fatalf("%s missing from %v", key, fresh)
			}
		}
		explicit := ApplyTempEnv([]string{"Temp=explicit"}, dir)
		if hasEnv(explicit, "TMP") || envValue(explicit, "Temp") != "explicit" {
			t.Fatalf("explicit Windows temp location changed: %v", explicit)
		}
	}
	if got := ApplyTempEnv([]string{"A=b"}, ""); len(got) != 1 || got[0] != "A=b" {
		t.Fatalf("empty dir %v", got)
	}
}

func TestPrepareAttemptScratchCleansOnlyThatAttempt(t *testing.T) {
	root := t.TempDir()
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, cleanup, err := PrepareAttemptScratch(root, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmp", "f"), []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("attempt scratch still present: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(other, "keep")); err != nil || string(b) != "x" {
		t.Fatalf("sibling %q %v", b, err)
	}
	if dir, cleanup, err := PrepareAttemptScratch("", "a1"); dir != "" || err != nil {
		t.Fatalf("empty root %q %v", dir, err)
	} else {
		cleanup()
	}
	if dir, _, err := PrepareAttemptScratch(root, ".."); dir != "" || err == nil {
		t.Fatalf("unsafe id %q %v", dir, err)
	}
}

func TestScratchCleanupPreservesReplacementDirectory(t *testing.T) {
	root := t.TempDir()
	dir, cleanup, err := PrepareAttemptScratch(root, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "keep")
	if err := os.WriteFile(marker, []byte("existing data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err == nil {
		t.Fatal("cleanup accepted a replacement directory")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "existing data" {
		t.Fatalf("replacement marker %q, error %v", got, err)
	}
}

func TestScratchWithSameIDDoesNotReuseExistingData(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "a1")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	first, cleanFirst, err := PrepareAttemptScratch(root, "a1")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanFirst()
	second, cleanSecond, err := PrepareAttemptScratch(root, "a1")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanSecond()
	if first == second || first == existing || second == existing {
		t.Fatalf("reused paths %s and %s", first, second)
	}
	cleanFirst()
	for _, path := range []string{existing, second} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVolumeAndFreeFloor(t *testing.T) {
	info, err := Volume(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mount == "" || info.Free == 0 || info.Total == 0 {
		t.Fatalf("volume %+v", info)
	}
	missing := filepath.Join(t.TempDir(), "missing", "child")
	walked, err := Volume(missing)
	if err != nil {
		t.Fatal(err)
	}
	if walked.Mount == "" || walked.Free == 0 {
		t.Fatalf("walked %+v", walked)
	}
	if BelowFreeFloor(10, 0) || BelowFreeFloor(11, 11) || !BelowFreeFloor(10, 11) {
		t.Fatal("floor")
	}
}

func TestEffectiveScratchPrefersProject(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"scratch_dir":"home-scratch"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	l := Load(home)
	if got := l.EffectiveScratch(""); got != filepath.Join(l.Home, "home-scratch") {
		t.Fatalf("home scratch %s", got)
	}
	if got := l.EffectiveScratch("proj"); got != filepath.Join(l.Home, "proj") {
		t.Fatalf("project scratch %s", got)
	}
	if Load(t.TempDir()).EffectiveScratch("") != "" {
		t.Fatal("unset scratch was not empty")
	}
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok && k == key {
			return v
		}
	}
	return "missing"
}
