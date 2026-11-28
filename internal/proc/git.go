package proc

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

type compatibleCredentialContextKey struct{}

// WithCompatibleCredentialNames attaches only configured env names, never
// values. Nested boundaries retain names from earlier config snapshots for
// the lifetime of one operation.
func WithCompatibleCredentialNames(ctx context.Context, names []string) context.Context {
	if len(names) == 0 {
		return ctx
	}
	prior, _ := ctx.Value(compatibleCredentialContextKey{}).([]string)
	joined := append(append([]string{}, prior...), names...)
	return context.WithValue(ctx, compatibleCredentialContextKey{}, joined)
}

// CompatibleCredentialNames returns a copy of the operation's names-only
// snapshot, useful when an adapter could otherwise repackage an auth value.
func CompatibleCredentialNames(ctx context.Context) []string {
	names, _ := ctx.Value(compatibleCredentialContextKey{}).([]string)
	return append([]string{}, names...)
}

// CompatibleChildEnv strips the reserved API namespace plus configured
// references from a specific operation. The caller owns every other entry.
func CompatibleChildEnv(ctx context.Context, base []string) []string {
	names, _ := ctx.Value(compatibleCredentialContextKey{}).([]string)
	return WithoutCompatibleCredentials(base, names)
}

// WithoutCompatibleCredentials uses configured names without requiring a
// context, e.g. for the initial service-to-host handoff.
func WithoutCompatibleCredentials(base []string, names []string) []string {
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok && CompatibleCredentialName(key, names) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func CompatibleCredentialName(key string, names []string) bool {
	if strings.HasPrefix(strings.ToUpper(key), "CARDEX_API_") {
		return true
	}
	for _, name := range names {
		if strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}

// Git constructs a git child with the API credential scrub applied to the
// final environment, including caller-provided overlays. Git may execute
// hooks, filters, remote helpers, SSH, or other repository-configured code
// even when the immediate command looks read-only. A nil base inherits all
// unrelated process variables and settings, as ordinary Git did before.
func Git(ctx context.Context, base []string, args ...string) *exec.Cmd {
	if base == nil {
		base = os.Environ()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = CompatibleChildEnv(ctx, base)
	if ExactGitObjects(ctx) {
		// These are fixed non-secret controls. Apply them after stripping
		// provider references, even if a misconfigured profile names one.
		cmd.Env = append(GitObjectScopeEnv(cmd.Env), "GIT_TERMINAL_PROMPT=0")
	}
	return cmd
}
