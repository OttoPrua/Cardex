package proc_test

import (
	"slices"
	"testing"

	"github.com/OttoPrua/Cardex/internal/proc"
)

func TestAcceptanceEnvDropsServePollution(t *testing.T) {
	in := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/builder",
		"TMPDIR=/tmp",
		"GOCACHE=/cache/go",
		"GOTMPDIR=/tmp/go",
		"GOMODCACHE=/cache/mod",
		"GOPATH=/home/builder/go",
		"LANG=en_US.UTF-8",
		"GOPROXY=https://proxy.golang.org,direct",
		"GOSUMDB=sum.golang.org",
		"CARDEX_HOME=/home/builder/.cardex",
		"CARDEX_FAKE_CHILD=1",
		"USER=builder",
		"CARDEX_SCHED_HOST=1",
		"CARDEX_SCHED_ATTEMPT=att-1",
		"CURSOR_CONFIG_DIR=/agents/cursor/config",
		"cursor_data_dir=/agents/cursor/data",
		"KIMI_CODE_HOME=/agents/kimi/home",
		"KIMI_CODE_CACHE_DIR=/agents/kimi/cache",
		"CLAUDE_CONFIG_DIR=/agents/claude",
		"CLAUDE_CODE_PLUGIN_CACHE_DIR=/agents/claude/plugins",
		"OPENCODE_DB=/agents/opencode/opencode.db",
		"GROK_LOG_FILE=/agents/grok/grok.log",
		"GROK_HOME=/agents/grok",
		"GEMINI_CLI_HOME=/agents/gemini",
		"CODEX_HOME=/agents/codex",
		"GIT_EXEC_PATH=/agents/bin",
		"GIT_TEMPLATE_DIR=/agents/empty-template",
		"GIT_DIR=/agents/wrong.git",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=init.templateDir",
		"GIT_CONFIG_VALUE_0=/agents/empty-template",
		"GIT_TERMINAL_PROMPT=0",
		"Path=/usr/bin",
	}
	got := proc.AcceptanceEnv(in)
	want := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/builder",
		"TMPDIR=/tmp",
		"GOCACHE=/cache/go",
		"GOTMPDIR=/tmp/go",
		"GOMODCACHE=/cache/mod",
		"GOPATH=/home/builder/go",
		"LANG=en_US.UTF-8",
		"GOPROXY=https://proxy.golang.org,direct",
		"GOSUMDB=sum.golang.org",
		"CARDEX_HOME=/home/builder/.cardex",
		"CARDEX_FAKE_CHILD=1",
		"USER=builder",
		"Path=/usr/bin",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("env\n got %q\nwant %q", got, want)
	}
}

func TestAcceptanceEnvKeepsEntriesWithoutPrefix(t *testing.T) {
	got := proc.AcceptanceEnv([]string{"MY_GIT_EXEC_PATH=/kept", "NOTGIT=1", "malformed", "=novalue"})
	want := []string{"MY_GIT_EXEC_PATH=/kept", "NOTGIT=1"}
	if !slices.Equal(got, want) {
		t.Fatalf("env\n got %q\nwant %q", got, want)
	}
}
