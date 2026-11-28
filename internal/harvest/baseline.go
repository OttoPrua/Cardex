package harvest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/roots"
)

// Version 1 could contain results influenced by candidate scratch files.
const baselineCacheVersion = 2

// Bump this namespace if the exact-object checkout/test policy changes.
// The ordinary version-2 namespace and on-disk shape remain unchanged.
const exactGitBaselineScope = "exact-git-objects-v1"

type baselineCache struct {
	Version int      `json:"version"`
	Scope   string   `json:"scope,omitempty"`
	Exit    int      `json:"exit"`
	Parsed  bool     `json:"parsed"`
	Names   []string `json:"names"`
}

func compareBaseline(ctx context.Context, home, repo, base string, run commandRun, env []string, scratchRoot string) *TestBaseline {
	current := parseGoTestOutput(string(run.Output), run.Exit)
	parsed, exit, ran := loadOrRunBaseline(ctx, home, repo, base, run.Command, env, scratchRoot)
	return compareParsed(current, parsed, exit, ran)
}

func loadOrRunBaseline(ctx context.Context, home, repo, base, command string, env []string, scratchRoot string) (parsedTests, int, bool) {
	base = strings.TrimSpace(base)
	if base == "" || strings.TrimSpace(repo) == "" || ctx.Err() != nil {
		return parsedTests{}, 0, false
	}
	if cache, ok := readBaselineCache(ctx, home, base, command); ok {
		return parsedTests{ok: cache.Parsed, names: cache.Names, context: map[string]string{}}, cache.Exit, true
	}
	scratchDir, cleanup, err := roots.PrepareAttemptScratch(scratchRoot, "baseline")
	if err != nil {
		return parsedTests{}, 0, false
	}
	defer cleanup()
	parent := scratchDir
	if parent == "" {
		parent = strings.TrimSpace(home)
	}
	exit, output, ran := runAtBase(ctx, parent, repo, base, command, roots.ApplyTempEnv(env, scratchDir))
	if !ran {
		return parsedTests{}, 0, false
	}
	parsed := parseGoTestOutput(string(output), exit)
	if parsed.ok {
		_ = writeBaselineCache(ctx, home, base, command, baselineCache{Version: baselineCacheVersion, Exit: exit, Parsed: true, Names: parsed.names})
	}
	return parsed, exit, true
}

func baselineCacheScope(ctx context.Context) string {
	if proc.ExactGitObjects(ctx) {
		return exactGitBaselineScope
	}
	return ""
}

func baselineCachePath(ctx context.Context, home, base, command string) (string, bool) {
	if strings.TrimSpace(home) == "" || strings.ContainsAny(base, `/\\`) || base == "." || base == ".." {
		return "", false
	}
	dir := filepath.Join(home, "baseline", base)
	if scope := baselineCacheScope(ctx); scope != "" {
		if !IsCommitID(base) {
			return "", false // do not cache a strict baseline under a mutable ref
		}
		dir = filepath.Join(dir, scope)
	}
	sum := sha256.Sum256([]byte(command))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), true
}

func readBaselineCache(ctx context.Context, home, base, command string) (baselineCache, bool) {
	path, ok := baselineCachePath(ctx, home, base, command)
	if !ok {
		return baselineCache{}, false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return baselineCache{}, false
	}
	var cache baselineCache
	if json.Unmarshal(body, &cache) != nil || cache.Version != baselineCacheVersion || cache.Scope != baselineCacheScope(ctx) || !cache.Parsed {
		return baselineCache{}, false
	}
	return cache, true
}

func writeBaselineCache(ctx context.Context, home, base, command string, cache baselineCache) error {
	path, ok := baselineCachePath(ctx, home, base, command)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	cache.Scope = baselineCacheScope(ctx)
	body, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644)
}

func runAtBase(ctx context.Context, parent, repo, base, command string, env []string) (int, []byte, bool) {
	if parent == "" {
		var err error
		parent, err = os.MkdirTemp("", "cardex-baseline-")
		if err != nil {
			return 0, nil, false
		}
		defer os.RemoveAll(parent)
	}
	wtRoot := filepath.Join(parent, "baseline", "wt")
	if err := os.MkdirAll(wtRoot, 0o755); err != nil {
		return 0, nil, false
	}
	wt, err := os.MkdirTemp(wtRoot, "wt-")
	if err != nil {
		return 0, nil, false
	}
	defer func() {
		_, _ = gitExec(context.WithoutCancel(ctx), repo, "worktree", "remove", "--force", wt)
		_ = os.RemoveAll(wt)
	}()
	if _, err := gitExec(ctx, repo, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "--detach", wt, base); err != nil {
		return 0, nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, defaultAcceptanceTimeout)
	defer cancel()
	run, _ := runAcceptanceCommand(cctx, wt, command, env)
	return run.Exit, run.Output, true
}
