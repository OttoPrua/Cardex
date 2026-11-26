package loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcceptanceDropsServeEnv(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README")
	gitRun(t, repo, "commit", "-m", "base")
	gitRun(t, repo, "checkout", "-b", "integration")

	execPath := t.TempDir()
	template := t.TempDir()
	polluteAcceptanceEnv(t, execPath, template)

	ok, err := acceptancePasses(context.Background(), repo, "integration", []string{acceptanceEnvProbe}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("acceptance failed with serve environment set")
	}
}

func TestAcceptGitEnvDropsServeGitVars(t *testing.T) {
	polluteAcceptanceEnv(t, t.TempDir(), t.TempDir())
	for _, kv := range acceptGitEnv() {
		key, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(key) {
		case "GIT_TERMINAL_PROMPT", "GIT_EDITOR", "GIT_PAGER":
			continue
		}
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") || strings.HasPrefix(strings.ToUpper(key), "CURSOR_") || strings.HasPrefix(strings.ToUpper(key), "CARDEX_SCHED_") {
			t.Fatalf("accept git env kept %s", kv)
		}
	}
}

func TestGitEnvKeepsConfigForLocalMerge(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	execPath := t.TempDir()
	polluteAcceptanceEnv(t, execPath, t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	var global, execSeen string
	for _, kv := range gitEnv() {
		key, val, _ := strings.Cut(kv, "=")
		switch key {
		case "GIT_CONFIG_GLOBAL":
			global = val
		case "GIT_EXEC_PATH":
			execSeen = val
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE":
			t.Fatalf("merge git env kept %s", kv)
		}
	}
	if global != cfg {
		t.Fatalf("GIT_CONFIG_GLOBAL = %q, want %q", global, cfg)
	}
	if execSeen != execPath {
		t.Fatalf("GIT_EXEC_PATH = %q, want %q", execSeen, execPath)
	}
}

func TestMergeLocalHonorsGitConfigGlobal(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README")
	gitRun(t, repo, "commit", "-m", "base")
	gitRun(t, repo, "checkout", "-b", "integration")
	if err := os.WriteFile(filepath.Join(repo, "marker"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "marker")
	gitRun(t, repo, "commit", "-m", "marker")
	gitRun(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "main-only"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "main-only")
	gitRun(t, repo, "commit", "-m", "main only")

	hooks := t.TempDir()
	marker := filepath.Join(t.TempDir(), "hook-ran")
	script := "#!/bin/sh\nprintf x > '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-merge-commit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	body := "[core]\n\thooksPath = " + filepath.ToSlash(hooks) + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	if err := mergeLocal(repo, "integration"); err == nil {
		t.Fatal("merge skipped the pre-merge-commit hook from GIT_CONFIG_GLOBAL")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("hook did not run: %v", err)
	}
}

func polluteAcceptanceEnv(t *testing.T, execPath, template string) {
	t.Helper()
	t.Setenv("CARDEX_SCHED_HOST", "1")
	t.Setenv("CARDEX_SCHED_ATTEMPT", "att-probe")
	t.Setenv("CURSOR_CONFIG_DIR", t.TempDir())
	t.Setenv("CURSOR_DATA_DIR", t.TempDir())
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	t.Setenv("KIMI_CODE_CACHE_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_PLUGIN_CACHE_DIR", t.TempDir())
	t.Setenv("OPENCODE_DB", filepath.Join(t.TempDir(), "opencode.db"))
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("GROK_LOG_FILE", filepath.Join(t.TempDir(), "grok.log"))
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("GIT_EXEC_PATH", execPath)
	t.Setenv("GIT_TEMPLATE_DIR", template)
	t.Setenv("GIT_DIR", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "init.templateDir")
	t.Setenv("GIT_CONFIG_VALUE_0", template)
}

const acceptanceEnvProbe = `
for key in CURSOR_CONFIG_DIR CURSOR_DATA_DIR KIMI_CODE_HOME KIMI_CODE_CACHE_DIR \
	CLAUDE_CONFIG_DIR CLAUDE_CODE_PLUGIN_CACHE_DIR OPENCODE_DB GROK_HOME GROK_LOG_FILE \
	GEMINI_CLI_HOME CODEX_HOME CARDEX_SCHED_HOST CARDEX_SCHED_ATTEMPT \
	GIT_EXEC_PATH GIT_TEMPLATE_DIR GIT_DIR GIT_CONFIG_GLOBAL GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0
do
	eval "v=\${$key-}"
	if [ -n "$v" ]; then
		echo "leaked $key" >&2
		exit 1
	fi
done
test -n "$PATH" || exit 1
test -n "$HOME" || exit 1
git init -q -b main childrepo || exit 1
test -d childrepo/.git/hooks || { echo "hooks missing" >&2; exit 1; }
helper="$(git --exec-path)/git-remote-http"
test -e "$helper" || { echo "remote-http missing: $helper" >&2; exit 1; }
`
