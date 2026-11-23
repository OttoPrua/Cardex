package merge

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRunChecksDropsServeEnv(t *testing.T) {
	t.Setenv("CARDEX_SCHED_HOST", "1")
	t.Setenv("CARDEX_SCHED_ATTEMPT", "att-probe")
	t.Setenv("CURSOR_CONFIG_DIR", t.TempDir())
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("OPENCODE_DB", filepath.Join(t.TempDir(), "opencode.db"))
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("GIT_EXEC_PATH", t.TempDir())
	t.Setenv("GIT_TEMPLATE_DIR", t.TempDir())
	out, failed := runChecks(context.Background(), t.TempDir(), nil, serveEnvProbe)
	if failed {
		t.Fatalf("test command saw serve environment:\n%s", out)
	}
}

const serveEnvProbe = `
for key in CARDEX_SCHED_HOST CARDEX_SCHED_ATTEMPT CURSOR_CONFIG_DIR KIMI_CODE_HOME \
	CLAUDE_CONFIG_DIR OPENCODE_DB GROK_HOME GEMINI_CLI_HOME CODEX_HOME GIT_EXEC_PATH GIT_TEMPLATE_DIR
do
	eval "v=\${$key-}"
	if [ -n "$v" ]; then
		echo "leaked $key" >&2
		exit 1
	fi
done
test -n "$PATH" && test -n "$HOME" && test -n "$TMPDIR"
`
