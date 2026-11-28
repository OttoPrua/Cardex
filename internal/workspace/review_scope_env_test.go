package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/proc"
)

func TestStrictReviewGitChildScrubsAndRestoresExactScope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture executable is a POSIX shell")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "git")
	if err := os.WriteFile(probe, []byte(`#!/bin/sh
test -z "${CARDEX_API_UNUSUAL+x}" &&
test -z "${CARD_FIXTURE_PROVIDER+x}" &&
test -z "${GIT_DIR+x}" &&
test "$GIT_NO_REPLACE_OBJECTS" = 1 &&
test "$GIT_GRAFT_FILE" = /dev/null &&
test "$GIT_TERMINAL_PROMPT" = 0 &&
printf 'controlled probe\n'
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CARDEX_API_UNUSUAL", "synthetic")
	t.Setenv("CARD_FIXTURE_PROVIDER", "synthetic")
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "0")
	t.Setenv("GIT_GRAFT_FILE", "synthetic")
	t.Setenv("GIT_DIR", "synthetic")
	ctx := proc.WithCompatibleCredentialNames(t.Context(), []string{"CARD_FIXTURE_PROVIDER", "GIT_NO_REPLACE_OBJECTS", "GIT_GRAFT_FILE"})
	got, err := scopedReviewGit(ctx, dir, "status")
	if err != nil || strings.TrimSpace(got) != "controlled probe" {
		t.Fatalf("scoped helper did not see final environment: %q %v", got, err)
	}
}
