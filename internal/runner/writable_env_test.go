package runner_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"cardex/internal/runner"
)

func TestWritablePathsFollowProcessEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearWritableEnv(t)

	t.Run("claude", func(t *testing.T) {
		base := runner.Claude{}.WritablePaths()
		assertHas(t, base, filepath.Join(home, ".claude"))
		assertHas(t, base, claudeCache(home))
		dir := t.TempDir()
		plugin := t.TempDir()
		assertLacks(t, base, dir)
		assertLacks(t, base, plugin)
		t.Setenv("CLAUDE_CONFIG_DIR", dir)
		t.Setenv("CLAUDE_CODE_PLUGIN_CACHE_DIR", plugin)
		got := runner.Claude{}.WritablePaths()
		assertHas(t, got, dir)
		assertHas(t, got, plugin)
		assertHas(t, got, filepath.Join(home, ".claude"))
		assertHas(t, got, claudeCache(home))
	})

	t.Run("grok", func(t *testing.T) {
		base := runner.Grok{}.WritablePaths()
		assertHas(t, base, filepath.Join(home, ".grok"))
		logDir := t.TempDir()
		logFile := filepath.Join(logDir, "grok.log")
		extra := t.TempDir()
		assertLacks(t, base, logDir)
		assertLacks(t, base, extra)
		t.Setenv("GROK_LOG_FILE", logFile)
		t.Setenv("GROK_LOG_DIR", extra)
		got := runner.Grok{}.WritablePaths()
		assertHas(t, got, logDir)
		assertHas(t, got, extra)
		assertHas(t, got, filepath.Join(home, ".grok"))
	})

	t.Run("opencode", func(t *testing.T) {
		base := runner.OpenCode{}.WritablePaths()
		assertHas(t, base, filepath.Join(home, ".config", "opencode"))
		dbDir := t.TempDir()
		assertLacks(t, base, dbDir)
		t.Setenv("OPENCODE_DB", filepath.Join(dbDir, "opencode.db"))
		got := runner.OpenCode{}.WritablePaths()
		assertHas(t, got, dbDir)
		assertHas(t, got, filepath.Join(home, ".config", "opencode"))
		assertHas(t, got, filepath.Join(home, ".local", "share", "opencode"))
	})

	t.Run("kimi", func(t *testing.T) {
		base := runner.Kimi{}.WritablePaths()
		assertHas(t, base, filepath.Join(home, ".kimi-code"))
		codeHome := t.TempDir()
		cacheDir := t.TempDir()
		configHome := t.TempDir()
		assertLacks(t, base, codeHome)
		assertLacks(t, base, cacheDir)
		assertLacks(t, base, configHome)
		t.Setenv("KIMI_CODE_HOME", codeHome)
		t.Setenv("KIMI_CODE_CACHE_DIR", cacheDir)
		withConfig := runner.Kimi{ConfigHome: configHome}.WritablePaths()
		assertHas(t, withConfig, codeHome)
		assertHas(t, withConfig, cacheDir)
		assertHas(t, withConfig, configHome)
		assertHas(t, withConfig, filepath.Join(home, ".kimi-code"))
		empty := runner.Kimi{}.WritablePaths()
		assertLacks(t, empty, configHome)
		assertHas(t, empty, filepath.Join(home, ".kimi-code"))
	})

	t.Run("cursor", func(t *testing.T) {
		base := runner.Cursor{}.WritablePaths()
		assertHas(t, base, filepath.Join(home, ".cursor"))
		configDir := t.TempDir()
		dataDir := t.TempDir()
		assertLacks(t, base, configDir)
		assertLacks(t, base, dataDir)
		t.Setenv("CURSOR_CONFIG_DIR", configDir)
		t.Setenv("CURSOR_DATA_DIR", dataDir)
		got := runner.Cursor{}.WritablePaths()
		assertHas(t, got, configDir)
		assertHas(t, got, dataDir)
		assertHas(t, got, filepath.Join(home, ".cursor"))
	})

	t.Run("agy", func(t *testing.T) {
		base := runner.AGY{}.WritablePaths()
		if base == nil {
			t.Fatal("agy WritablePaths is nil")
		}
		assertHas(t, base, filepath.Join(home, ".gemini"))
		assertHas(t, base, geminiCache(home))
		cliHome := t.TempDir()
		assertLacks(t, base, cliHome)
		t.Setenv("GEMINI_CLI_HOME", cliHome)
		got := runner.AGY{}.WritablePaths()
		assertHas(t, got, cliHome)
		assertHas(t, got, filepath.Join(home, ".gemini"))
		assertHas(t, got, geminiCache(home))
	})
}

func clearWritableEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CLAUDE_CONFIG_DIR",
		"CLAUDE_CODE_PLUGIN_CACHE_DIR",
		"GROK_HOME",
		"GROK_LOG_FILE",
		"GROK_LOG_DIR",
		"OPENCODE_DB",
		"KIMI_CODE_HOME",
		"KIMI_CODE_CACHE_DIR",
		"CURSOR_CONFIG_DIR",
		"CURSOR_DATA_DIR",
		"GEMINI_CLI_HOME",
	} {
		t.Setenv(key, "")
	}
}

func claudeCache(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Caches", "claude")
	}
	return filepath.Join(home, ".cache", "claude")
}

func geminiCache(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Caches", "gemini")
	}
	return filepath.Join(home, ".cache", "gemini")
}

func assertHas(t *testing.T, paths []string, want string) {
	t.Helper()
	for _, path := range paths {
		if path == want {
			return
		}
	}
	t.Fatalf("paths %q missing %s", paths, want)
}

func assertLacks(t *testing.T, paths []string, banned string) {
	t.Helper()
	for _, path := range paths {
		if path == banned {
			t.Fatalf("paths %q include %s", paths, banned)
		}
	}
}
