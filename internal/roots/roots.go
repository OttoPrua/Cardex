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
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
	abs := absPath(strings.TrimSpace(home))
	l := Layout{Home: abs, AttemptsDir: filepath.Join(abs, "attempts")}
	if abs == "" {
		return l
	}
	data, err := os.ReadFile(filepath.Join(abs, "config.json"))
	if err != nil || len(bytesTrimSpace(data)) == 0 {
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
	return resolveConfigured(absPath(strings.TrimSpace(home)), raw)
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

// AttemptDirFor is the directory for one attempt.
// A new attempt uses attempts_dir when that key is set, and <home>/attempts
// otherwise. An attempt whose directory already exists under the home is kept
// there, so older evidence stays readable without moving it.
func AttemptDirFor(home, id string) string {
	l := Load(home)
	// The default directory is joined to the home string as given. Trimming it
	// would move evidence for a home whose last component ends in a space.
	if !safeSegment(id) || !l.CustomAttempts {
		return filepath.Join(home, "attempts", id)
	}
	primary := filepath.Join(l.AttemptsDir, id)
	if isDir(primary) {
		return primary
	}
	for _, legacy := range legacyAttemptDirs(home, l.Home, id) {
		if isDir(legacy) {
			return legacy
		}
	}
	return primary
}

func legacyAttemptDirs(home, absHome, id string) []string {
	out := []string{filepath.Join(home, "attempts", id)}
	if abs := filepath.Join(absHome, "attempts", id); filepath.Clean(abs) != filepath.Clean(out[0]) {
		out = append(out, abs)
	}
	return out
}

func underLegacyAttempts(home, dir, id string) bool {
	for _, legacy := range legacyAttemptDirs(home, Load(home).Home, id) {
		if filepath.Clean(dir) == filepath.Clean(legacy) {
			return true
		}
	}
	return false
}

// EvidenceRef is the path stored for one attempt file.
// Files that live under <home>/attempts keep the historical relative form
// attempts/<id>/<name>. Files that live under a configured attempts_dir are
// stored as an absolute path. An empty name refers to the attempt directory.
func EvidenceRef(home, id, name string) string {
	dir := AttemptDirFor(home, id)
	if underLegacyAttempts(home, dir, id) {
		if name == "" {
			return "attempts/" + id
		}
		return "attempts/" + id + "/" + name
	}
	if name == "" {
		return dir
	}
	return filepath.Join(dir, name)
}

// EvidenceRefs lists output.log, acceptance.log, and result.md that exist for
// the attempt. Relative entries are home-relative. A configured attempts_dir
// contributes absolute paths, and a legacy <home>/attempts/<id> tree is still
// listed when it holds files the new directory does not.
func EvidenceRefs(home, id string) []string {
	out := []string{}
	if strings.TrimSpace(home) == "" || !safeSegment(id) {
		return out
	}
	l := Load(home)
	primary := AttemptDirFor(home, id)
	seen := map[string]bool{}
	add := func(dir string, relative bool) {
		for _, name := range []string{"output.log", "acceptance.log", "result.md"} {
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			ref := path
			if relative {
				ref = "attempts/" + id + "/" + name
			}
			if seen[ref] {
				continue
			}
			seen[ref] = true
			out = append(out, ref)
		}
	}
	add(primary, underLegacyAttempts(home, primary, id))
	for _, legacy := range legacyAttemptDirs(home, l.Home, id) {
		if filepath.Clean(primary) == filepath.Clean(legacy) {
			continue
		}
		add(legacy, true)
	}
	return out
}

// ResolveStored opens a path recorded on an attempt.
// Absolute paths are used as written. Any other path, including the historical
// attempts/<id>/file form, is joined to the home. Nothing is moved.
func ResolveStored(home, stored string) string {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return ""
	}
	if filepath.IsAbs(stored) {
		return stored
	}
	// Historical refs are relative to the home string the caller has, including
	// a trailing space that TrimSpace would drop.
	return filepath.Join(home, filepath.FromSlash(stored))
}

// PrepareAttemptScratch creates <root>/<attemptID>/{tmp,gocache}.
// An empty root leaves the process temp directory alone and returns a no-op
// cleanup. Cleanup removes only that attempt directory.
func PrepareAttemptScratch(root, attemptID string) (string, func(), error) {
	noop := func() {}
	root = strings.TrimSpace(root)
	if root == "" || !safeSegment(attemptID) {
		return "", noop, nil
	}
	dir := filepath.Join(root, attemptID)
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		return "", noop, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "gocache"), 0o755); err != nil {
		return "", noop, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// ApplyTempEnv sets TMPDIR, GOTMPDIR, and GOCACHE to dir/tmp and dir/gocache
// when that key is absent from env. Keys already present are left unchanged.
// An empty dir returns env as given.
func ApplyTempEnv(env []string, dir string) []string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return env
	}
	tmp := filepath.Join(dir, "tmp")
	cache := filepath.Join(dir, "gocache")
	var extra []string
	if !hasEnv(env, "TMPDIR") {
		extra = append(extra, "TMPDIR="+tmp)
	}
	if !hasEnv(env, "GOTMPDIR") {
		extra = append(extra, "GOTMPDIR="+tmp)
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

// BackupBeforeWrite copies the database and its WAL sidecars into backups_dir
// when that key is set and the database file already has contents.
// An unset backups_dir does nothing. The source files are not removed.
func BackupBeforeWrite(dbPath string) error {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return nil
	}
	l := Load(filepath.Dir(dbPath))
	if !l.CustomBackups {
		return nil
	}
	st, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st.IsDir() || st.Size() == 0 {
		return nil
	}
	return CopyDBFiles(dbPath, l.BackupsDir)
}

// CopyDBFiles copies dbPath and, when present, its -wal and -shm sidecars into
// destDir under a new timestamped name. Existing files in destDir are left in
// place. The sources are not removed.
func CopyDBFiles(dbPath, destDir string) error {
	if strings.TrimSpace(dbPath) == "" || strings.TrimSpace(destDir) == "" {
		return fmt.Errorf("roots: backup path is empty")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	base := filepath.Base(dbPath)
	copied := false
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := dbPath + suffix
		st, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if st.IsDir() {
			continue
		}
		dst, err := unusedBackupName(destDir, stamp, base+suffix)
		if err != nil {
			return err
		}
		if err := copyFile(src, dst); err != nil {
			return err
		}
		copied = true
	}
	if !copied {
		return fmt.Errorf("roots: %s has nothing to copy", dbPath)
	}
	return nil
}

func unusedBackupName(dir, stamp, name string) (string, error) {
	dst := filepath.Join(dir, stamp+"-"+name)
	for n := 0; n < 100; n++ {
		if n > 0 {
			dst = filepath.Join(dir, fmt.Sprintf("%s-%d-%s", stamp, n, name))
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		return dst, nil
	}
	return "", fmt.Errorf("roots: backup name collision in %s", dir)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	serr := out.Close()
	if cerr != nil {
		return cerr
	}
	return serr
}

func configuredDir(home string, top map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := top[key]
	if !ok {
		return "", false
	}
	s, ok := jsonString(raw)
	if !ok {
		return "", false
	}
	return resolveConfigured(home, s)
}

func jsonString(raw json.RawMessage) (string, bool) {
	if len(bytesTrimSpace(raw)) == 0 || string(bytesTrimSpace(raw)) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func resolveConfigured(home, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if !filepath.IsAbs(raw) {
		if strings.TrimSpace(home) == "" {
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

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
