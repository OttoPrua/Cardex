package host

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIIsolatedEnvIsNotMergedWithParent(t *testing.T) {
	const other = "cdx-canary-b-1a2b3c"
	t.Setenv("ANTHROPIC_AUTH_TOKEN", other)
	t.Setenv("HTTPS_PROXY", "http://inherited.example:9")
	dir := t.TempDir()
	exe := filepath.Join(dir, "claude")
	cmd, err := commandForEnv([]string{exe, "--bare"}, []string{"PATH=" + dir, "CLAUDE_CONFIG_DIR=" + dir}, true)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(cmd.Env, "\n")
	if strings.Contains(text, other) || strings.Contains(text, "inherited.example") || strings.Contains(text, "ANTHROPIC_AUTH_TOKEN=") {
		t.Fatal("api child environment was merged with the parent")
	}
	if _, err := commandForEnv([]string{"claude", "--bare"}, []string{"CLAUDE_CONFIG_DIR=" + dir}, true); err == nil {
		t.Fatal("started without trusted executable search path")
	}
}
