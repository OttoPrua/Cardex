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

	ok, err := acceptancePasses(context.Background(), repo, "integration", []string{acceptanceEnvProbe})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("acceptance failed with serve environment set")
	}
}

func TestGitEnvDropsServeGitVars(t *testing.T) {
	polluteAcceptanceEnv(t, t.TempDir(), t.TempDir())
	for _, kv := range gitEnv() {
		key, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(key) {
		case "GIT_TERMINAL_PROMPT", "GIT_EDITOR", "GIT_PAGER":
			continue
		}
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") || strings.HasPrefix(strings.ToUpper(key), "CURSOR_") || strings.HasPrefix(strings.ToUpper(key), "CARDEX_SCHED_") {
			t.Fatalf("git env kept %s", kv)
		}
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
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "init.templateDir")
	t.Setenv("GIT_CONFIG_VALUE_0", template)
}

const acceptanceEnvProbe = `
for key in CURSOR_CONFIG_DIR CURSOR_DATA_DIR KIMI_CODE_HOME KIMI_CODE_CACHE_DIR \
	CLAUDE_CONFIG_DIR CLAUDE_CODE_PLUGIN_CACHE_DIR OPENCODE_DB GROK_HOME GROK_LOG_FILE \
	GEMINI_CLI_HOME CODEX_HOME CARDEX_SCHED_HOST CARDEX_SCHED_ATTEMPT \
	GIT_EXEC_PATH GIT_TEMPLATE_DIR GIT_DIR GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0
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
