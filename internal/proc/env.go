package proc

import "strings"

// acceptanceDropPrefixes are environment names an acceptance command or an
// in-executor test must not inherit from the serve process.
//
// CARDEX_SCHED_* is the detached host launcher. CURSOR_, KIMI_, CLAUDE_,
// OPENCODE_, GROK_, GEMINI_, and CODEX_ are agent CLI configuration homes;
// runner tests read them and would otherwise see serve's directories.
//
// GIT_EXEC_PATH points git at a directory without git-remote-http, so http
// remotes fail with "remote-http is not a git command". GIT_TEMPLATE_DIR and
// GIT_CONFIG_* (init.templateDir) make git init skip .git/hooks.
var acceptanceDropPrefixes = []string{
	"CARDEX_SCHED_",
	"CURSOR_",
	"KIMI_",
	"CLAUDE_",
	"OPENCODE_",
	"GROK_",
	"GEMINI_",
	"CODEX_",
	"GIT_",
}

// AcceptanceEnv returns base without the serve-process variables that break
// acceptance commands and the tests they run. PATH, HOME, TMPDIR, LANG, and
// the Go build variables (GOCACHE, GOTMPDIR, GOMODCACHE, GOPATH, and the rest
// of the Go toolchain environment) are kept. The comparison is
// case-insensitive so a Windows Path-style spelling of GIT_EXEC_PATH is
// dropped too.
func AcceptanceEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" || dropAcceptanceEnv(key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func dropAcceptanceEnv(key string) bool {
	upper := strings.ToUpper(key)
	for _, prefix := range acceptanceDropPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}
