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

// ExecutorEnv is the environment of a process the dev executor starts.
// go test under that process must not see CARDEX_SCHED_* (sched's init would
// enter host.Run before the test) or GIT_* (exec-path and templates break
// git inside the suite). The same prefixes AcceptanceEnv drops are removed,
// then agent CLI homes are put back: the executor still has to start the tool
// with CURSOR_*, KIMI_*, CLAUDE_*, and the other homes it was given. The
// caller merges the runner's own variables afterwards so those win.
func ExecutorEnv(base []string) []string {
	out := AcceptanceEnv(base)
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" || !toolHomeEnv(key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func toolHomeEnv(key string) bool {
	upper := strings.ToUpper(key)
	for _, prefix := range acceptanceDropPrefixes {
		if prefix == "CARDEX_SCHED_" || prefix == "GIT_" {
			continue
		}
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}
