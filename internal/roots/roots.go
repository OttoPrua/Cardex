// Package roots resolves the optional on-disk roots in a Cardex home.
//
// config.json may set backups_dir, attempts_dir, and scratch_dir. An empty or
// absent value keeps today's locations: no separate database copies, attempt
// evidence under <home>/attempts, and the process temp directory for scratch.
// A relative value is joined to the home. A project scratch_dir wins over the
// home scratch_dir.
package roots

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FreeWarnBytes is the free-space floor, in bytes, below which doctor warns.
// The check only reads the volume. Zero disables the warning.
var FreeWarnBytes uint64 = 1 << 30

// Layout is the resolved roots for one home.
type Layout struct {
	Home           string
	BackupsDir     string
	AttemptsDir    string
	ScratchDir     string
	CustomBackups  bool
	CustomAttempts bool
	CustomScratch  bool
}

// Location is one directory doctor reports.
type Location struct {
	Name string
	Path string
}

// Load reads home/config.json. A missing or unparsable file uses the defaults.
// A key that is present but not a string is ignored, same as an empty value.
func Load(home string) Layout {
	abs := absPath(home)
	l := Layout{Home: abs, AttemptsDir: filepath.Join(abs, "attempts")}
	if abs == "" {
		return l
	}
	data, err := os.ReadFile(filepath.Join(abs, "config.json"))
	if err != nil || len(data) == 0 {
		return l
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return l
	}
	if dir, ok := configuredDir(abs, top, "backups_dir"); ok {
		l.BackupsDir = dir
		l.CustomBackups = true
	}
	if dir, ok := configuredDir(abs, top, "attempts_dir"); ok {
		if filepath.Clean(dir) != filepath.Clean(l.AttemptsDir) {
			l.AttemptsDir = dir
			l.CustomAttempts = true
		}
	}
	if dir, ok := configuredDir(abs, top, "scratch_dir"); ok {
		l.ScratchDir = dir
		l.CustomScratch = true
	}
	return l
}

// ResolveDir joins a relative configured path to home and cleans it.
// An empty value is not configured.
func ResolveDir(home, raw string) (string, bool) {
	return resolveConfigured(absPath(home), raw)
}

// EffectiveScratch returns the project directory when it is set, otherwise
// the home scratch directory. An empty result means the process temp directory.
func (l Layout) EffectiveScratch(projectScratch string) string {
	if dir, ok := resolveConfigured(l.Home, projectScratch); ok {
		return dir
	}
	return l.ScratchDir
}

// Locations are the directories doctor reports for this home.
// An unset backups_dir reports the home (the database stays there and no copy
// is made). An unset scratch_dir reports the process temp directory.
func (l Layout) Locations() []Location {
	backups := l.Home
	if l.CustomBackups {
		backups = l.BackupsDir
	}
	if backups == "" {
		backups = "."
	}
	attempts := l.AttemptsDir
	if attempts == "" {
		attempts = "."
	}
	scratch := l.ScratchDir
	if scratch == "" {
		scratch = os.TempDir()
	}
	return []Location{
		{Name: "backups", Path: backups},
		{Name: "attempts", Path: attempts},
		{Name: "scratch", Path: scratch},
	}
}

// AttemptDirFor locates evidence for a new attempt or a historical record
// with no canonical prompt path.
func AttemptDirFor(home, id string) string {
	layout := Load(home)
	legacy := filepath.Join(layout.Home, "attempts", id)
	if !safeSegment(id) || !layout.CustomAttempts {
		return legacy
	}
	dir := filepath.Join(layout.AttemptsDir, id)
	if !isDir(dir) && isDir(legacy) {
		return legacy
	}
	return dir
}

// AttemptDir keeps a scheduled attempt next to its persisted prompt if the
// configuration changes. Custom prompts elsewhere do not determine the root.
func AttemptDir(home, id, prompt string) string {
	if safeSegment(id) && filepath.Base(prompt) == "prompt.txt" {
		dir := filepath.Dir(ResolveStored(home, prompt))
		if filepath.Base(dir) == id {
			return dir
		}
	}
	return AttemptDirFor(home, id)
}

// EvidenceRef retains the historical home-relative form for the default tree.
func EvidenceRef(home, id, prompt, name string) string {
	dir := AttemptDir(home, id, prompt)
	if absPath(dir) == absPath(filepath.Join(home, "attempts", id)) {
		if name == "" {
			return "attempts/" + id
		}
		return "attempts/" + id + "/" + name
	}
	return filepath.Join(dir, name)
}

// EvidenceRefs lists the evidence files that exist for the attempt. Include
// the legacy tree for records that predate the persisted prompt location.
func EvidenceRefs(home, id, prompt string) []string {
	out := []string{}
	if strings.TrimSpace(home) == "" || !safeSegment(id) {
		return out
	}
	primary := AttemptDir(home, id, prompt)
	legacy := filepath.Join(home, "attempts", id)
	dirs := []string{primary}
	if absPath(primary) != absPath(legacy) {
		dirs = append(dirs, legacy)
	}
	for _, dir := range dirs {
		for _, name := range []string{"output.log", "acceptance.log", "result.md"} {
			path := filepath.Join(dir, name)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				if absPath(dir) == absPath(legacy) {
					path = "attempts/" + id + "/" + name
				}
				out = append(out, path)
			}
		}
	}
	return out
}

// ResolveStored uses absolute paths as written and joins legacy refs to home.
func ResolveStored(home, stored string) string {
	if stored == "" || filepath.IsAbs(stored) {
		return stored
	}
	return filepath.Join(absPath(home), filepath.FromSlash(stored))
}

// PrepareAttemptScratch allocates a private directory for one invocation.
// The unique directory keeps a retry or concurrent acceptance check from
// inheriting or deleting someone else's files.
func PrepareAttemptScratch(root, attemptID string) (string, func() error, error) {
	if root == "" {
		return "", func() error { return nil }, nil
	}
	if !safeSegment(attemptID) {
		return "", nil, fmt.Errorf("invalid scratch id %q", attemptID)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(root, "cardex-"+attemptID+"-")
	if err != nil {
		return "", nil, err
	}
	f, err := os.Open(dir)
	if err != nil {
		_ = os.Remove(dir)
		return "", nil, err
	}
	created, err := f.Stat()
	_ = f.Close()
	if err != nil {
		_ = os.Remove(dir)
		return "", nil, err
	}
	cleanup := func() error {
		current, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !current.IsDir() || !os.SameFile(created, current) {
			return fmt.Errorf("attempt scratch directory was replaced: %s", dir)
		}
		return os.RemoveAll(dir)
	}
	for _, name := range []string{"tmp", "gocache"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			_ = cleanup()
			return "", nil, err
		}
	}
	return dir, cleanup, nil
}

// ApplyTempEnv supplies scratch defaults for missing temp and Go cache keys.
// Windows programs also use TMP and TEMP for their temporary directory.
func ApplyTempEnv(env []string, dir string) []string {
	if dir == "" {
		return env
	}
	tmp := filepath.Join(dir, "tmp")
	cache := filepath.Join(dir, "gocache")
	tempKeys := []string{"TMPDIR", "GOTMPDIR"}
	if runtime.GOOS == "windows" && !hasEnv(env, "TMP") && !hasEnv(env, "TEMP") {
		tempKeys = append(tempKeys, "TMP", "TEMP")
	}
	var extra []string
	for _, key := range tempKeys {
		if !hasEnv(env, key) {
			extra = append(extra, key+"="+tmp)
		}
	}
	if !hasEnv(env, "GOCACHE") {
		extra = append(extra, "GOCACHE="+cache)
	}
	if len(extra) == 0 {
		return env
	}
	out := make([]string, len(env), len(env)+len(extra))
	copy(out, env)
	return append(out, extra...)
}

// BelowFreeFloor reports that free bytes are under a positive floor.
func BelowFreeFloor(free, floor uint64) bool {
	return floor > 0 && free < floor
}

func configuredDir(home string, top map[string]json.RawMessage, key string) (string, bool) {
	var value string
	if json.Unmarshal(top[key], &value) != nil {
		return "", false
	}
	return resolveConfigured(home, value)
}

func resolveConfigured(home, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if !filepath.IsAbs(raw) {
		if home == "" {
			return "", false
		}
		raw = filepath.Join(home, raw)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return filepath.Clean(raw), true
	}
	return filepath.Clean(abs), true
}

func absPath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

func safeSegment(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\\`)
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func hasEnv(env []string, key string) bool {
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if envKeyEqual(k, key) {
			return true
		}
	}
	return false
}

func envKeyEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
