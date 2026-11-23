package harvest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type baselineCache struct {
	Version int      `json:"version"`
	Exit    int      `json:"exit"`
	Parsed  bool     `json:"parsed"`
	Names   []string `json:"names"`
}

func compareBaseline(ctx context.Context, home, repo, base string, run commandRun) *TestBaseline {
	current := parseGoTestOutput(string(run.Output), run.Exit)
	parsed, exit, ran := loadOrRunBaseline(ctx, home, repo, base, run.Command)
	return compareParsed(current, parsed, exit, ran)
}

func loadOrRunBaseline(ctx context.Context, home, repo, base, command string) (parsedTests, int, bool) {
	base = strings.TrimSpace(base)
	if base == "" || strings.TrimSpace(repo) == "" || ctx.Err() != nil {
		return parsedTests{}, 0, false
	}
	if cache, ok := readBaselineCache(home, base, command); ok {
		return parsedTests{ok: cache.Parsed, names: cache.Names, context: map[string]string{}}, cache.Exit, true
	}
	exit, output, ran := runAtBase(ctx, home, repo, base, command)
	if !ran {
		return parsedTests{}, 0, false
	}
	parsed := parseGoTestOutput(string(output), exit)
	if parsed.ok {
		_ = writeBaselineCache(home, base, command, baselineCache{Version: 1, Exit: exit, Parsed: true, Names: parsed.names})
	}
	return parsed, exit, true
}

func baselineCachePath(home, base, command string) (string, bool) {
	if strings.TrimSpace(home) == "" || strings.ContainsAny(base, `/\\`) || base == "." || base == ".." {
		return "", false
	}
	sum := sha256.Sum256([]byte(command))
	return filepath.Join(home, "baseline", base, hex.EncodeToString(sum[:])+".json"), true
}

func readBaselineCache(home, base, command string) (baselineCache, bool) {
	path, ok := baselineCachePath(home, base, command)
	if !ok {
		return baselineCache{}, false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return baselineCache{}, false
	}
	var cache baselineCache
	if json.Unmarshal(body, &cache) != nil || cache.Version != 1 || !cache.Parsed {
		return baselineCache{}, false
	}
	return cache, true
}

func writeBaselineCache(home, base, command string, cache baselineCache) error {
	path, ok := baselineCachePath(home, base, command)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644)
}

func runAtBase(ctx context.Context, home, repo, base, command string) (int, []byte, bool) {
	parent := strings.TrimSpace(home)
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
		_, _ = gitExec(context.Background(), repo, "worktree", "remove", "--force", wt)
		_ = os.RemoveAll(wt)
	}()
	if _, err := gitExec(ctx, repo, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "--detach", wt, base); err != nil {
		return 0, nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, defaultAcceptanceTimeout)
	defer cancel()
	run, _ := runAcceptanceCommand(cctx, wt, command)
	return run.Exit, run.Output, true
}
