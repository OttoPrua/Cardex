package proc

import "strings"

// ManagedExecutorEnv is a workflow hint on CardeX executor subprocesses.
const ManagedExecutorEnv = "CARDEX_MANAGED_EXECUTOR"

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
	if upper == ManagedExecutorEnv {
		return true
	}
	for _, prefix := range acceptanceDropPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// ExecutorEnv is the environment of the dev executor process itself.
// CARDEX_SCHED_* is removed so a cardex binary started as the executor does
// not re-enter host.Run from sched's init. Tool homes (CURSOR_*, KIMI_*,
// CLAUDE_*, and the rest) stay, because the CLI needs them to start. GIT_*
// stays too, so ordinary git commands still see GIT_CONFIG_GLOBAL.
//
// go test that this process launches is not cleaned here. The host puts a
// wrapper ahead of PATH; that wrapper applies AcceptanceEnv only when the
// go command is test.
func ExecutorEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" || strings.EqualFold(key, ManagedExecutorEnv) || strings.HasPrefix(strings.ToUpper(key), "CARDEX_SCHED_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
