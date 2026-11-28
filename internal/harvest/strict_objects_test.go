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

func TestFrozenStagePathsExactUsesRealObjectsWithLocalReplacement(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	add := func(file string) {
		if err := os.WriteFile(filepath.Join(repo, file), []byte(file+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", file)
		runGit(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@invalid", "-c", "commit.gpgsign=false", "commit", "-m", file)
	}
	add("base.txt")
	base := strings.TrimSpace(runGitOut(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "checkout", "-b", "actual")
	add("actual.txt")
	head := strings.TrimSpace(runGitOut(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "checkout", "-b", "replacement", base)
	add("only-replacement.txt")
	replacement := strings.TrimSpace(runGitOut(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "replace", head, replacement)
	ordinary, err := FrozenStagePaths(t.Context(), repo, base, head)
	if err != nil || !slices.Equal(ordinary, []string{"only-replacement.txt"}) {
		t.Fatalf("ordinary control: %v %v", ordinary, err)
	}
	actual, err := FrozenStagePaths(proc.WithExactGitObjects(t.Context()), repo, base, head)
	if err != nil || !slices.Equal(actual, []string{"actual.txt"}) {
		t.Fatalf("strict stage lost actual object paths: %v %v", actual, err)
	}
}

func TestStrictAcceptanceFinalGitEnvironmentAlsoAppliesToBaseline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses POSIX environment parameter expansion")
	}
	ctx := proc.WithCompatibleCredentialNames(proc.WithExactGitObjects(t.Context()), []string{"CARD_FIXTURE_PROVIDER", "GIT_NO_REPLACE_OBJECTS", "GIT_GRAFT_FILE", "GIT_TERMINAL_PROMPT"})
	env := append(os.Environ(), "CARD_FIXTURE_PROVIDER=synthetic", "CARDEX_API_UNUSUAL=synthetic", "GIT_DIR=synthetic", "GIT_NO_REPLACE_OBJECTS=0", "GIT_GRAFT_FILE=synthetic", "GIT_TERMINAL_PROMPT=1")
	const cmd = `test -z "${CARD_FIXTURE_PROVIDER+x}" && test -z "${CARDEX_API_UNUSUAL+x}" && test -z "${GIT_DIR+x}" && test "$GIT_NO_REPLACE_OBJECTS" = 1 && test "$GIT_GRAFT_FILE" = /dev/null && test "$GIT_TERMINAL_PROMPT" = 0`
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	write(t, repo, "file.txt", "synthetic\n")
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "-c", "user.name=fixture", "-c", "user.email=fixture@invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	run, err := runAcceptanceCommand(ctx, repo, cmd, env)
	if err != nil || run.Exit != 0 {
		t.Fatalf("strict acceptance environment: %d %v", run.Exit, err)
	}
	base := strings.TrimSpace(runGitOut(t, repo, "rev-parse", "HEAD"))
	exit, output, ran := runAtBase(ctx, t.TempDir(), repo, base, cmd, env)
	if !ran || exit != 0 {
		t.Fatalf("strict baseline environment: ran=%t exit=%d, %q", ran, exit, output)
	}
}
