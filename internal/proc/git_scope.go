package proc

import (
	"context"
	"os"
	"strings"
)

type exactGitObjectsKey struct{}

// WithExactGitObjects scopes CardeX's own Git evidence commands.
func WithExactGitObjects(ctx context.Context) context.Context {
	return context.WithValue(ctx, exactGitObjectsKey{}, true)
}

// ExactGitObjects is false for ordinary legacy Git and executor paths.
func ExactGitObjects(ctx context.Context) bool {
	scoped, _ := ctx.Value(exactGitObjectsKey{}).(bool)
	return scoped
}

// GitObjectScopeEnv drops ambient Git overrides but retains the executor's
// non-Git homes, credentials, and process settings. A fixed empty graft file
// and disabled replacements ensure plain Git in this scope reads real objects.
func GitObjectScopeEnv(base []string) []string {
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if ok && !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE="+os.DevNull)
}
