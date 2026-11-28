package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
)

func TestStrictReviewObjectScopeSurvivesPromptToActualExecutor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test executes only a Unix synthetic shell fixture")
	}
	for _, tc := range []struct {
		name                     string
		strict, versioned, dirty bool
	}{
		{"strict Git-only fallback", true, false, false},
		{"strict versioned handoff", true, true, false},
		{"worktree changed after prompt", true, false, true},
		{"ordinary keeps ambient Git environment", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			home, db, repo := newHome(t)
			_, oid := seedRepo(t, db, repo, "true")
			seedRoutes(t, db)
			ch, err := db.GetChannel(ctx, "cha")
			if err != nil {
				t.Fatal(err)
			}
			ch.Runner = model.RunnerCodex
			if err := db.UpdateChannel(ctx, &ch); err != nil {
				t.Fatal(err)
			}
			src := model.Card{ID: "source", Objective: oid, Kind: model.KindDev, Status: model.CardReviewing}
			if err := db.CreateCard(ctx, &src); err != nil {
				t.Fatal(err)
			}
			base := rev(t, repo, "HEAD")
			branch(t, repo, oid, src.ID, "a.txt", "ACTUAL_CANDIDATE\n")
			branch(t, repo, oid, "replacement", "a.txt", "ALTERNATE_CANDIDATE\n")
			candidateRef := cardBranchName(oid, src.ID)
			candidate := rev(t, repo, candidateRef)
			replacement := rev(t, repo, cardBranchName(oid, "replacement"))
			const namespace = "refs/synthetic-replacement/"
			git(t, repo, "update-ref", namespace+candidate, replacement)
			customGrafts := filepath.Join(t.TempDir(), "ambient-grafts")
			if err := os.WriteFile(customGrafts, []byte(candidate+" "+replacement+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			srcDir := filepath.Join(home, "attempts", "a-source")
			if err := os.MkdirAll(srcDir, 0700); err != nil {
				t.Fatal(err)
			}
			sourceAttempt := model.Attempt{ID: "a-source", Card: src.ID, Branch: candidateRef, BaseCommit: base, HeadCommit: candidate, Outcome: model.OutcomePassed, PromptPath: filepath.Join(srcDir, "prompt.txt")}
			if err := db.CreateAttempt(ctx, &sourceAttempt); err != nil {
				t.Fatal(err)
			}
			if tc.versioned {
				raw, err := json.Marshal(harvest.StageEvidence{Version: 1, Attempt: sourceAttempt.ID, Card: src.ID, Kind: src.Kind, Outcome: sourceAttempt.Outcome, Base: base, Candidate: candidate, Paths: []string{"untrusted-legacy-path.txt"}})
				if err != nil {
					t.Fatal(err)
				}
				if err := harvest.WriteStageJSON(srcDir, harvest.StageEvidenceName, raw); err != nil {
					t.Fatal(err)
				}
			}
			rv := model.Card{ID: "review", Objective: oid, Kind: model.KindReview, Tier: model.TierT2, Title: "Review synthetic scope",
				Deps: []string{src.ID}, Status: model.CardReady, Channel: ch.ID, StrictDeps: tc.strict, Sandbox: "off"}
			if err := db.CreateCard(ctx, &rv); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			shim := `#!/bin/sh
printf 'CHILD_REPLACE=%s\n' "${GIT_REPLACE_REF_BASE-}"
printf 'CHILD_GRAFT=%s\n' "${GIT_GRAFT_FILE-}"
printf 'CHILD_PAYLOAD=%s\n' "${CARDEX_TEST_SCOPE_SENTINEL-}"
git show HEAD:a.txt
printf 'CHILD_ANCESTRY=%s\n' "$(git rev-list --parents -n 1 HEAD)"
GIT_NO_REPLACE_OBJECTS=0 GIT_GRAFT_FILE=external-graft GIT_REPLACE_REF_BASE=refs/ambient-wrapper-override/ go test
`
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(shim), 0700); err != nil {
				t.Fatal(err)
			}
			// Stub the next go on PATH, not CardeX's per-attempt shim. This
			// captures the effective test descendant env without a nested suite.
			goBinary := `#!/bin/sh
printf 'GO_ARGS=%s\n' "$*"
printf 'GO_NO_REPLACE=%s\n' "${GIT_NO_REPLACE_OBJECTS-}"
printf 'GO_GRAFT=%s\n' "${GIT_GRAFT_FILE-}"
printf 'GO_REPLACE=%s\n' "${GIT_REPLACE_REF_BASE-}"
`
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(goBinary), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CARDEX_TEST_SCOPE_SENTINEL", "non-git-setting-preserved")
			t.Setenv("GIT_REPLACE_REF_BASE", namespace)
			t.Setenv("GIT_GRAFT_FILE", customGrafts)
			t.Setenv("GIT_NO_REPLACE_OBJECTS", "")
			if err := os.Unsetenv("GIT_NO_REPLACE_OBJECTS"); err != nil {
				t.Fatal(err)
			}
			if got := gitOutput(t, repo, "show", candidate+":a.txt"); got != "ALTERNATE_CANDIDATE" {
				t.Fatalf("ambient alternative must control plain Git before scoped launch: %q", got)
			}
			r := New(home)
			var captured model.Attempt
			var promptText string
			r.Launch = func(ctx context.Context, home, id string) (int, error) {
				a, err := db.GetAttempt(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				captured = a
				promptText = string(mustRead(t, a.PromptPath))
				if tc.dirty {
					if err := os.WriteFile(filepath.Join(a.Worktree, "a.txt"), []byte("CHANGED_AFTER_PROMPT\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return os.Getpid(), host.Run(ctx, home, id)
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if captured.ID == "" {
				t.Fatal("scheduler did not reach synthetic host")
			}
			if tc.strict {
				if captured.Branch != "" || !strings.Contains(promptText, "git --no-replace-objects diff --no-ext-diff --no-textconv") ||
					!strings.Contains(promptText, "历史验收不等于在本次环境重跑") {
					t.Errorf("strict review prompt does not describe the actual bounded Git command: %q", promptText)
				}
				if tc.versioned && (!strings.Contains(promptText, `"paths":["a.txt"]`) || strings.Contains(promptText, "untrusted-legacy-path.txt")) {
					t.Errorf("strict versioned evidence used wrong object paths: %q", promptText)
				}
			}
			log, _ := os.ReadFile(filepath.Join(filepath.Dir(captured.PromptPath), "output.log"))
			got := string(log)
			if tc.dirty {
				events, err := db.ListEvents(ctx, "attempt:"+captured.ID)
				if err != nil {
					t.Fatal(err)
				}
				var deferred bool
				for _, e := range events {
					if e.Type == "host_start" {
						t.Fatal("host started after exact checkout became dirty")
					}
					if e.Type == host.EventDependencyDeferred && strings.Contains(string(e.Data), "Git object scope is unverified") {
						deferred = true
					}
				}
				if !deferred || strings.Contains(got, "CHILD_PAYLOAD") {
					t.Fatalf("unsafe scope was not fail-closed: %q", got)
				}
				return
			}
			if !strings.Contains(got, "CHILD_PAYLOAD=non-git-setting-preserved") {
				t.Errorf("lost runner environment: %q", got)
			}
			if !strings.Contains(got, "GO_ARGS=test\n") || !strings.Contains(got, "GO_REPLACE=\n") {
				t.Errorf("test wrapper leaked inherited namespace or failed to launch: %q", got)
			}
			if tc.strict && (!strings.Contains(got, "GO_NO_REPLACE=1\n") || !strings.Contains(got, "GO_GRAFT="+os.DevNull+"\n")) {
				t.Errorf("strict actual go-test descendant lost object protection: %q", got)
			}
			if !tc.strict && (!strings.Contains(got, "GO_NO_REPLACE=\n") || !strings.Contains(got, "GO_GRAFT=\n")) {
				t.Errorf("ordinary go-test descendant did not retain legacy filtering: %q", got)
			}
			if tc.strict {
				if !strings.Contains(got, "\nACTUAL_CANDIDATE\n") || strings.Contains(got, "ALTERNATE_CANDIDATE") ||
					!strings.Contains(got, "CHILD_REPLACE=\n") || !strings.Contains(got, "CHILD_GRAFT="+os.DevNull+"\n") ||
					!strings.Contains(got, "CHILD_ANCESTRY="+candidate+" "+base+"\n") {
					t.Errorf("actual scoped child saw alternate Git objects or env: %q", got)
				}
			} else if !strings.Contains(got, "\nALTERNATE_CANDIDATE\n") || !strings.Contains(got, "CHILD_REPLACE="+namespace) || !strings.Contains(got, "CHILD_GRAFT="+customGrafts) || !strings.Contains(got, "CHILD_ANCESTRY="+candidate+" "+replacement+"\n") {
				t.Errorf("ordinary legacy child environment changed: %q", got)
			}
		})
	}
}
