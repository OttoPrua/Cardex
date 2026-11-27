package harvest

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestM3HarvestGitEvidence(t *testing.T) {
	repo, head := newRepo(t)
	for _, tt := range []struct{ name, dir, base, command, kind string }{
		{"not repository and usage", t.TempDir(), head, "git diff", "not_repository"},
		{"missing repository while inferring base", t.TempDir(), "", "git rev-parse", "not_repository"},
		{"missing base", repo, "private-basename-do-not-print", "git diff", "missing_base"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := harvestInput(tt.dir, tt.base, model.Card{ID: "c-harvest", Kind: model.KindDev}, 0, nil, "")
			in.Attempt.ID = "a-harvest"
			_, err := Harvest(context.Background(), in)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() == 0 {
				t.Fatalf("Git exit status not preserved: %v", err)
			}
			s := err.Error()
			for _, field := range []string{"command=" + strconv.Quote(tt.command), "kind=" + tt.kind, `card="c-harvest"`, `attempt="a-harvest"`, `worktree=` + strconv.Quote(tt.dir)} {
				if !strings.Contains(s, field) {
					t.Errorf("missing %s in %q", field, s)
				}
			}
			if strings.Contains(s, "\n") || strings.Contains(strings.ToLower(s), "usage:") || strings.Contains(s, "private-basename-do-not-print") {
				t.Fatalf("usage or Git arguments leaked: %q", s)
			}
		})
	}
}
