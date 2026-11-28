package harvest

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/proc"
)

func TestStrictBaselineDoesNotReuseLegacyReplacementResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a local POSIX go shim, not an actual Go child")
	}
	ctx := t.Context()
	repo, home := t.TempDir(), t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	commit := func(label string) string {
		write(t, repo, "fixture.txt", label+"\n")
		runGit(t, repo, "add", "fixture.txt")
		runGit(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@invalid", "-c", "commit.gpgsign=false", "commit", "-m", label)
		return strings.TrimSpace(runGitOut(t, repo, "rev-parse", "HEAD"))
	}
	root := commit("root")
	base := commit("actual-base")
	runGit(t, repo, "checkout", "-b", "legacy-replacement", root)
	alternate := commit("legacy-replacement")
	runGit(t, repo, "replace", base, alternate)

	bin, counter := t.TempDir(), filepath.Join(t.TempDir(), "baseline-executions")
	shim := `#!/bin/sh
test "$1" = test || exit 2
content="$(cat fixture.txt)" || exit 2
printf '%s\n' "$content" >> "$CARDEX_FIXTURE_BASELINE_COUNT" || exit 2
case "$content" in
legacy-replacement) printf '%s\n' '--- FAIL: TestAPI (0.00s)' 'FAIL'; exit 1;;
actual-base) printf '%s\n' 'PASS'; exit 0;;
*) exit 2;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	env := append(proc.AcceptanceEnv(os.Environ()),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CARDEX_FIXTURE_BASELINE_COUNT="+counter)
	run := commandRun{Command: "go test ./...", Exit: 1, Output: []byte("--- FAIL: TestAPI (0.00s)\nFAIL\n")}
	legacy := compareBaseline(ctx, home, repo, base, run, env, "")
	if legacy.Status != "compared" || legacy.Exit == nil || *legacy.Exit != 1 || !slices.Equal(legacy.Still, []string{"TestAPI"}) {
		t.Fatalf("legacy must cache the actual replacement-object failure: %+v", legacy)
	}
	exactCtx := proc.WithExactGitObjects(ctx)
	exact := compareBaseline(exactCtx, home, repo, base, run, env, "")
	if exact.Status != "compared" || exact.Exit == nil || *exact.Exit != 0 || len(exact.New) != 1 ||
		exact.New[0].Name != "TestAPI" || len(exact.Still) != 0 {
		t.Fatalf("strict reused legacy replacement baseline instead of reporting a new failure: %+v", exact)
	}
	legacyAgain := compareBaseline(ctx, home, repo, base, run, env, "")
	exactAgain := compareBaseline(proc.WithExactGitObjects(ctx), home, repo, base, run, env, "")
	if !slices.Equal(legacyAgain.Still, []string{"TestAPI"}) ||
		len(exactAgain.New) != 1 || exactAgain.New[0].Name != "TestAPI" {
		t.Fatalf("one namespace contaminated the other namespace: legacy=%+v exact=%+v", legacyAgain, exactAgain)
	}
	if count, err := os.ReadFile(counter); err != nil || string(count) != "legacy-replacement\nactual-base\n" {
		t.Fatalf("expected one execution per isolated cache: %q %v", count, err)
	}

	legacyPath, legacyOK := baselineCachePath(ctx, home, base, run.Command)
	exactPath, exactOK := baselineCachePath(exactCtx, home, base, run.Command)
	if !legacyOK || !exactOK || legacyPath == exactPath ||
		filepath.Dir(exactPath) != filepath.Join(filepath.Dir(legacyPath), exactGitBaselineScope) {
		t.Fatalf("cache scopes not distinct: legacy=%q strict=%q", legacyPath, exactPath)
	}
	oldWire, err := os.ReadFile(legacyPath)
	if err != nil || strings.Contains(string(oldWire), `"scope"`) {
		t.Fatalf("legacy wire changed: %s %v", oldWire, err)
	}
	if _, ok := baselineCachePath(exactCtx, home, "main", run.Command); ok {
		t.Fatal("strict cache accepts a mutable ref")
	}
	// Even copying an unscoped entry into the strict directory cannot make
	// it an exact-scope receipt. A new run must replace it.
	if err := os.WriteFile(exactPath, oldWire, 0600); err != nil {
		t.Fatal(err)
	}
	recovered := compareBaseline(exactCtx, home, repo, base, run, env, "")
	if len(recovered.New) != 1 || recovered.New[0].Name != "TestAPI" {
		t.Fatalf("strict accepted a copied unscoped receipt: %+v", recovered)
	}
	if count, err := os.ReadFile(counter); err != nil || string(count) != "legacy-replacement\nactual-base\nactual-base\n" {
		t.Fatalf("strict did not replace the wrong scope via a fresh run: %q %v", count, err)
	}
}
