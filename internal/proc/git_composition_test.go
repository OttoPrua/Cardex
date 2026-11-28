package proc

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestGitExactScopeAndCompatibleCredentialScrubCompose(t *testing.T) {
	ctx := WithCompatibleCredentialNames(WithExactGitObjects(t.Context()), []string{"GIT_GRAFT_FILE", "GIT_NO_REPLACE_OBJECTS", "fixture-provider"})
	cmd := Git(ctx, []string{"GIT_GRAFT_FILE=ambient", "GIT_NO_REPLACE_OBJECTS=0", "GIT_DIR=untrusted", "FIXTURE-PROVIDER=synthetic", "KEEP=ordinary"}, "--version")
	for _, want := range []string{"GIT_GRAFT_FILE=" + os.DevNull, "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "KEEP=ordinary"} {
		if !slices.Contains(cmd.Env, want) {
			t.Errorf("missing fixed or ordinary value %s from %v", want, cmd.Env)
		}
	}
	for _, entry := range cmd.Env {
		if strings.Contains(entry, "ambient") || strings.Contains(entry, "untrusted") || strings.Contains(entry, "synthetic") || entry == "GIT_NO_REPLACE_OBJECTS=0" {
			t.Fatalf("ambient value inherited into strict Git: %s", entry)
		}
	}
}
