package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestStageReviewUsesColdFrozenEvidence(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	seedRoutes(t, db)
	authorContext := "SYNTHETIC-AUTHOR-CONTEXT " + strings.Repeat("unrelated history. ", 600)
	file := filepath.Join(repo, "docs", "design", obj, "context.md")
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(authorContext), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "docs")
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit", "-m", "context")
	frozenBase := rev(t, repo, "HEAD")
	source := model.Card{ID: "source-demo", Objective: obj, Kind: model.KindDev, Title: "Synthetic task scope", Body: "SYNTHETIC-AUTHOR-TASK-BODY", Tier: model.TierT2, Status: model.CardReviewing, Acceptance: []string{"git diff --check"}}
	if err := db.CreateCard(ctx, &source); err != nil {
		t.Fatal(err)
	}
	branch(t, repo, obj, source.ID, "change.txt", "synthetic retained change\n")
	head := rev(t, repo, "cardex/"+obj+"/c/"+source.ID)
	wt := filepath.Join(t.TempDir(), "authored")
	git(t, repo, "worktree", "add", "--detach", wt, head)
	dir := filepath.Join(home, "attempts", "a-source")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	att := model.Attempt{ID: "a-source", Card: source.ID, Worktree: wt, BaseCommit: frozenBase, HeadCommit: head, Outcome: model.OutcomePassed, PromptPath: filepath.Join(dir, "prompt.txt")}
	if err := os.WriteFile(att.PromptPath, []byte("SYNTHETIC-AUTHOR-DIALOGUE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &att); err != nil {
		t.Fatal(err)
	}
	if _, err := harvest.Harvest(ctx, harvest.Input{Attempt: att, Card: source, Home: home, Output: []byte("SYNTHETIC-AUTHOR-OUTPUT")}); err != nil {
		t.Fatal(err)
	}
	rv := model.Card{ID: "rv-demo", Objective: obj, Kind: model.KindReview, Title: "Review synthetic change", Body: "Perform this review", Tier: model.TierT3, Status: model.CardReady, Deps: []string{source.ID}, ReworkNote: "SYNTHETIC-PREVIOUS-REVIEW"}
	if err := db.CreateCard(ctx, &rv); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	a := reviewAttempt(t, db, rv.ID)
	data, err := os.ReadFile(a.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("synthetic review prompt bytes: %d; vendor input tokens: unknown; vendor cached tokens: unknown", len(data))
	for _, want := range []string{"cardex-stage-handoff", frozenBase, head, "change.txt", "git diff --check", "acceptance.log", "Perform this review"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("no %q in prompt", want)
		}
	}
	for _, unwanted := range []string{"SYNTHETIC-AUTHOR-CONTEXT", "SYNTHETIC-AUTHOR-TASK-BODY", "SYNTHETIC-AUTHOR-DIALOGUE", "SYNTHETIC-AUTHOR-OUTPUT", "SYNTHETIC-PREVIOUS-REVIEW", harvest.StageEvidenceName, `"file":"result.md"`} {
		if strings.Contains(string(data), unwanted) {
			t.Errorf("prompt copied %q", unwanted)
		}
	}
}

func TestStagePlanThenImplementationAndSafeFallback(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	proj, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.GetProject(ctx, proj)
	if err != nil {
		t.Fatal(err)
	}
	p.ForbiddenPaths = []string{"protected/"}
	if err := db.UpdateProject(ctx, &p); err != nil {
		t.Fatal(err)
	}
	designPath := "docs/design/" + objID + "/path.md"
	contextPath := "docs/design/" + objID + "/context.md"
	for path, body := range map[string]string{designPath: "SYNTHETIC-CURRENT-DESIGN", contextPath: "SYNTHETIC-PREVIOUS-HISTORY"} {
		target := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", "docs")
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit", "-m", "design")
	head := rev(t, repo, "HEAD")
	node := model.DesignNode{ID: "n-main", Objective: objID, Layer: model.LayerModule, Status: model.NodeFrozen, DocPath: designPath}
	if err := db.CreateNode(ctx, &node); err != nil {
		t.Fatal(err)
	}

	design := model.Card{ID: "design-stage", Objective: objID, Node: node.ID, Kind: model.KindDesign, Title: "frozen design", Status: model.CardDone}
	firstPlan := model.Card{ID: "plan-stage", Objective: objID, Node: node.ID, Kind: model.KindPlan, Title: "Current implementation plan", Body: "PLAN-CARD-INSTRUCTIONS", Status: model.CardReady}
	dev := model.Card{ID: "scoped-dev", Objective: objID, Node: node.ID, Kind: model.KindDev, Title: "Current dev task", Body: "DEV-CARD-INSTRUCTIONS", Status: model.CardReady}
	for _, card := range []*model.Card{&design, &firstPlan, &dev} {
		if err := db.CreateCard(ctx, card); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []model.Question{
		{ID: "q-owner", Objective: objID, Scope: node.ID, ScopeKind: "node", Kind: model.QuestionFreeze, Text: "Freeze this design", Answer: "approved"},
		{ID: "q-hold", Objective: objID, Scope: node.ID, ScopeKind: "node", Kind: model.QuestionCustom, Text: "One open decision"},
		{ID: "q-other", Objective: objID, Scope: "n-other", ScopeKind: "node", Kind: model.QuestionCustom, Text: "SYNTHETIC-UNRELATED-DECISION"},
	} {
		if err := db.CreateQuestion(ctx, &row); err != nil {
			t.Fatal(err)
		}
	}
	stageEvent(t, db, "node:"+node.ID, "loop.freezable", map[string]any{"card": design.ID, "gen": 0})
	stageEvent(t, db, "node:"+node.ID, "loop.freezable", map[string]any{"card": "old-design", "gen": 19})
	if err := os.WriteFile(filepath.Join(repo, ".cardex-result.md"), []byte("## 结论\nSynthetic design\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first := stageFixtureAttempt(t, db, home, repo, design, "stage-design", head, head, model.OutcomePassed, 0)
	planAttempt := stageFixtureAttempt(t, db, home, repo, firstPlan, "stage-plan", head, head, model.OutcomePassed, 0)
	stageEvent(t, db, "card:"+dev.ID, loop.StageParentEvent, stageLink{Card: firstPlan.ID, Attempt: planAttempt.ID})

	r := New(home)
	type target struct {
		card                    model.Card
		id, wantStage, sourceID string
	}
	for _, step := range []target{
		{firstPlan, "dest-plan", "planning", first.ID}, {dev, "dest-dev", "implementation", planAttempt.ID},
	} {
		dest, prompt := writeStageTestPrompt(t, r, db, step.card, obj, repo, head, step.id, 0)
		for _, want := range []string{step.wantStage, step.sourceID, "protected/", "approved", "One open decision", contextPath, designPath, step.card.Body} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s: missing %q", step.id, want)
			}
		}
		for _, unwanted := range []string{"SYNTHETIC-PREVIOUS-HISTORY", "SYNTHETIC-UNRELATED-DECISION", "SYNTHETIC-CURRENT-DESIGN"} {
			if strings.Contains(prompt, unwanted) {
				t.Errorf("%s: leaked %q", step.id, unwanted)
			}
		}
		body, err := os.ReadFile(filepath.Join(filepath.Dir(dest.PromptPath), stageHandoffName))
		if err != nil {
			t.Fatal(err)
		}
		var manifest stageHandoff
		if err := json.Unmarshal(body, &manifest); err != nil || manifest.Version != 1 || manifest.Source.Attempt != step.sourceID || manifest.Stage != step.wantStage {
			t.Fatalf("invalid handoff %s: %+v %v", step.id, manifest, err)
		}
	}

	// A malformed or missing exact source artifact leaves the original prompt
	// byte content in use and records the fallback for this attempt only.
	if err := os.WriteFile(filepath.Join(filepath.Dir(planAttempt.PromptPath), harvest.StageEvidenceName), []byte(`{"version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	fallbackAttempt, old := writeStageTestPrompt(t, r, db, dev, obj, repo, head, "dest-fallback", 0)
	if !strings.Contains(old, "SYNTHETIC-PREVIOUS-HISTORY") || strings.Contains(old, "cardex-stage-handoff") {
		t.Fatal("missing evidence did not use legacy prompt")
	}
	if events := eventsOfType(t, db, "attempt:"+fallbackAttempt.ID, stageHandoffFallback); len(events) != 1 || !strings.Contains(string(events[0].Data), "stage_evidence_unavailable") {
		t.Fatalf("fallback event not scoped to attempt: %+v", events)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(fallbackAttempt.PromptPath), stageHandoffName)); !os.IsNotExist(err) {
		t.Fatalf("fallback wrote handoff: %v", err)
	}
}

func TestStageReworkUsesOnlyTheExactReviewedCandidate(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	base := rev(t, repo, "HEAD")
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit", "--allow-empty", "-m", "reviewed")
	head := rev(t, repo, "HEAD")
	dev := model.Card{ID: "dev-rework", Objective: objID, Kind: model.KindDev, Title: "rework", Body: "SYNTHETIC-DEV-REWORK", Status: model.CardReady, ReworkCount: 1, ReworkNote: "Check synthetic boundary"}
	review := model.Card{ID: "prior-review", Objective: objID, Kind: model.KindReview, Title: "prior review", Status: model.CardDone, Deps: []string{dev.ID}}
	for _, card := range []*model.Card{&dev, &review} {
		if err := db.CreateCard(ctx, card); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".cardex-result.md"), []byte("## 复核结论\nrework\n- P1 a.txt:1 Synthetic bounded finding\n"), 0600); err != nil {
		t.Fatal(err)
	}
	att := stageFixtureAttempt(t, db, home, repo, review, "reviewed-attempt", head, "", model.OutcomePassed, 0)
	stageEvent(t, db, "attempt:"+att.ID, model.ReviewSubjectEvent, model.PinnedReviewSubject{Subject: dev.ID})
	stageEvent(t, db, "card:"+dev.ID, "rework", map[string]any{"source": "review", "attempt_id": att.ID, "rework_count": 1, "question_opened": false})
	r := New(home)
	_, prompt := writeStageTestPrompt(t, r, db, dev, obj, repo, head, "correct-rework", 1)
	for _, want := range []string{"review_rework", "Synthetic bounded finding", "Check synthetic boundary", "result.md", "SYNTHETIC-DEV-REWORK"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("review rework is missing %q", want)
		}
	}
	if strings.Count(prompt, "Check synthetic boundary") != 1 {
		t.Error("review note not included exactly once")
	}
	mismatch, legacy := writeStageTestPrompt(t, r, db, dev, obj, repo, base, "stale-rework", 1)
	if strings.Contains(legacy, "review_rework") || !strings.Contains(legacy, "Check synthetic boundary") {
		t.Fatal("mismatched commit did not fall back to its old card note")
	}
	if events := eventsOfType(t, db, "attempt:"+mismatch.ID, stageHandoffFallback); len(events) != 1 || !strings.Contains(string(events[0].Data), "source_not_current") {
		t.Fatalf("missing mismatch fallback: %+v", events)
	}
}

func TestStageReviewDiffIncludesPreReworkChanges(t *testing.T) {
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	base := rev(t, repo, "HEAD")
	for i, file := range []string{"base-change.txt", "fix.txt"} {
		if err := os.WriteFile(filepath.Join(repo, file), []byte("synthetic\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", file)
		git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit", "-m", file)
		next := rev(t, repo, "HEAD")
		if i == 0 {
			src := model.Card{ID: "source", Objective: obj, Kind: model.KindDev, Title: "synthetic", Status: model.CardReviewing}
			if err := db.CreateCard(t.Context(), &src); err != nil {
				t.Fatal(err)
			}
			stageFixtureAttempt(t, db, home, repo, src, "attempt-before-rework", base, next, model.OutcomePassed, 0)
		}
	}
	head, currentBase := rev(t, repo, "HEAD"), rev(t, repo, "HEAD^")
	card, err := db.GetCard(t.Context(), "source")
	if err != nil {
		t.Fatal(err)
	}
	last := stageFixtureAttempt(t, db, home, repo, card, "attempt-after-rework", currentBase, head, model.OutcomePassed, 1)
	diff, err := stageReviewDiff(t.Context(), db, card, last, repo)
	if err != nil || diff == nil || diff.Base != base || diff.Candidate != head || strings.Join(diff.Paths, ",") != "base-change.txt,fix.txt" {
		t.Fatalf("full frozen range missing: %+v %v", diff, err)
	}
}

func stageEvent(t *testing.T, db *store.Store, entity, kind string, body any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AppendEvent(t.Context(), &model.Event{Entity: entity, Type: kind, Data: raw}); err != nil {
		t.Fatal(err)
	}
}

func stageFixtureAttempt(t *testing.T, db *store.Store, home, repo string, card model.Card, id, base, head string, outcome model.Outcome, index int) model.Attempt {
	t.Helper()
	a := model.Attempt{ID: id, Card: card.ID, Worktree: repo, BaseCommit: base, HeadCommit: head, Outcome: outcome, Index: index, PromptPath: filepath.Join(home, "attempts", id, "prompt.txt")}
	if err := db.CreateAttempt(t.Context(), &a); err != nil {
		t.Fatal(err)
	}
	result, err := harvest.Harvest(t.Context(), harvest.Input{Attempt: a, Card: card, Home: home})
	if err != nil || result.EvidenceErr != nil || result.Outcome != outcome {
		t.Fatalf("fixture evidence: %+v / %v", result, err)
	}
	return a
}

func writeStageTestPrompt(t *testing.T, r *Runner, db *store.Store, card model.Card, obj model.Objective, repo, head, id string, index int) (model.Attempt, string) {
	t.Helper()
	a := model.Attempt{ID: id, Card: card.ID, Worktree: repo, BaseCommit: head, Index: index, PromptPath: filepath.Join(r.Home, "attempts", id, "prompt.txt")}
	if err := r.writePrompt(t.Context(), db, card, obj, model.Project{RepoPath: repo}, &a); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.PromptPath)
	if err != nil {
		t.Fatal(err)
	}
	return a, string(data)
}

func TestStageEvidenceDoesNotReuseVerifiedArtifactAfterDirtyReharvest(t *testing.T) {
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "true")
	base := rev(t, repo, "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "changed.txt"), []byte("synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "changed.txt")
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit", "-m", "synthetic candidate")
	head := rev(t, repo, "HEAD")
	card := model.Card{ID: "source", Objective: obj, Kind: model.KindDev, Title: "synthetic", Status: model.CardRunning,
		Acceptance: []string{"true"}}
	if err := db.CreateCard(t.Context(), &card); err != nil {
		t.Fatal(err)
	}
	// Start with a legitimate artifact. Reharvest of the same attempt still
	// reports its normal outcome; acceptance modified the worktree, however,
	// so the old generation may not be attached to the new log.
	a := stageFixtureAttempt(t, db, home, repo, card, "dirty-attempt", base, head, model.OutcomePassed, 0)
	before, err := harvest.LoadStageEvidence(home, a)
	if err != nil || len(before.Checks) != 1 || before.Checks[0].Command != "true" {
		t.Fatalf("clean acceptance did not produce evidence: %+v / %v", before, err)
	}
	card.Acceptance = []string{"echo changed >> a.txt"}
	res, err := harvest.Harvest(t.Context(), harvest.Input{Attempt: a, Card: card, Home: home})
	if err != nil || res.EvidenceErr != nil || res.Outcome != model.OutcomePassed {
		t.Fatalf("ordinary dirty reharvest outcome: %+v / %v", res, err)
	}
	if _, err := harvest.LoadStageEvidence(home, a); !os.IsNotExist(err) {
		t.Fatalf("dirty reharvest kept stale stage evidence: %v", err)
	}
	log, err := os.ReadFile(filepath.Join(filepath.Dir(a.PromptPath), "acceptance.log"))
	if err != nil || !strings.Contains(string(log), "echo changed") {
		t.Fatalf("dirty reharvest did not rewrite verification: %v", err)
	}
}

func TestStageReviewFallbackNeverRehydratesAuthorContext(t *testing.T) {
	home, db, repo := newHome(t)
	_, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(t.Context(), objID)
	if err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(repo, "docs", "design", objID, "context.md")
	if err := os.MkdirAll(filepath.Dir(contextPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contextPath, []byte("SYNTHETIC-OLD-AUTHOR-CONTEXT"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "docs")
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit", "-m", "configured context")
	base := rev(t, repo, "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "candidate.txt"), []byte("synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "candidate.txt")
	git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit", "-m", "candidate")
	head := rev(t, repo, "HEAD")
	src := model.Card{ID: "cold-source", Objective: objID, Kind: model.KindDev, Title: "synthetic candidate", Status: model.CardReviewing}
	if err := db.CreateCard(t.Context(), &src); err != nil {
		t.Fatal(err)
	}
	sourceAttempt := stageFixtureAttempt(t, db, home, repo, src, "cold-source-attempt", base, head, model.OutcomePassed, 0)
	path := filepath.Join(filepath.Dir(sourceAttempt.PromptPath), harvest.StageEvidenceName)
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var large harvest.StageEvidence
	if err := json.Unmarshal(valid, &large); err != nil {
		t.Fatal(err)
	}
	large.Checks = append(large.Checks, harvest.StageCheck{Command: strings.Repeat("synthetic verification", 1000)})
	largeJSON, err := json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}

	r := New(home)
	for _, tc := range []struct {
		name, reason             string
		body                     []byte
		destBase                 string
		writeFailure, standalone bool
	}{
		{name: "missing", reason: "stage_evidence_unavailable", destBase: head},
		{name: "malformed", reason: "stage_evidence_unavailable", body: []byte("{malformed"), destBase: head},
		{name: "oversize", reason: "stage_evidence_exceeds_prompt_budget", body: largeJSON, destBase: head},
		{name: "stale", reason: "source_not_current", body: valid, destBase: base},
		{name: "write-failure", reason: "stage_handoff_write_failed", body: valid, destBase: head, writeFailure: true},
		{name: "standalone", reason: "source_attempt_missing", body: valid, destBase: head, standalone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			destID := "review-" + tc.name
			entity := "attempt:" + destID
			card := model.Card{ID: "task-" + tc.name, Objective: objID, Kind: model.KindReview, Title: "current review",
				Body:       "CURRENT-REVIEW-SCOPE\n上一档复核（T1）没把握，已升到 T2。上一档意见：SYNTHETIC-OLD-OPINION\n",
				ReworkNote: "SYNTHETIC-OLD-REWORK"}
			if tc.standalone {
				stageEvent(t, db, entity, model.ReviewSubjectEvent, model.PinnedReviewSubject{Subject: card.ID, Standalone: true})
			} else {
				stageEvent(t, db, entity, model.ReviewSubjectEvent, model.PinnedReviewSubject{Subject: src.ID})
			}
			if tc.body == nil {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, tc.body, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.writeFailure {
				bad := filepath.Join(home, "attempts", destID, stageHandoffName)
				if err := os.MkdirAll(bad, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bad, "obstruction"), []byte("synthetic"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, prompt := writeStageTestPrompt(t, r, db, card, obj, repo, tc.destBase, destID, 0)
			for _, want := range []string{"CURRENT-REVIEW-SCOPE", `"evidence_status":"unavailable"`, tc.reason, "冻结差异和验收证据不可用"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("prompt missing %q", want)
				}
			}
			for _, unwanted := range []string{"SYNTHETIC-OLD-AUTHOR-CONTEXT", "SYNTHETIC-OLD-OPINION", "SYNTHETIC-OLD-REWORK", "cardex-context", "handoff.diff", `"candidate"`} {
				if strings.Contains(prompt, unwanted) {
					t.Errorf("fallback copied or claimed %q", unwanted)
				}
			}
			if events := eventsOfType(t, db, entity, stageHandoffFallback); len(events) != 1 || !strings.Contains(string(events[0].Data), tc.reason) {
				t.Fatalf("fallback missing reason: %+v", events)
			}
		})
	}
}

func TestStageReviewKeepsPinnedGitRangeWithoutAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		foreign bool
		strict  bool
	}{
		{"pre-upgrade", false, false},
		{"explicit-cross-objective-pin", true, false},
		{"strict-cross-objective-detached", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			home, db, repo := newHome(t)
			project, objID := seedRepo(t, db, repo, "true")
			seedRoutes(t, db)
			olderIntegration := rev(t, repo, "HEAD")
			git(t, repo, "branch", "review-integration", olderIntegration)
			if err := os.WriteFile(filepath.Join(repo, "source-integration.txt"), []byte("synthetic base\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", "source-integration.txt")
			git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit", "-m", "source base")
			sourceBase := rev(t, repo, "HEAD")
			git(t, repo, "branch", "source-integration", sourceBase)
			targetObj, err := db.GetObjective(ctx, objID)
			if err != nil {
				t.Fatal(err)
			}
			targetObj.IntegrationBranch = "source-integration"
			sourceObjID := objID
			if tc.foreign {
				sourceObj := targetObj
				sourceObj.ID = "different-objective"
				sourceObj.Project = project
				if err := db.CreateObjective(ctx, &sourceObj); err != nil {
					t.Fatal(err)
				}
				sourceObjID = sourceObj.ID
				targetObj.IntegrationBranch = "review-integration"
			}
			if err := db.UpdateObjective(ctx, &targetObj); err != nil {
				t.Fatal(err)
			}
			src := model.Card{ID: "pinned-source", Objective: sourceObjID, Kind: model.KindDev, Title: "synthetic source", Status: model.CardReviewing}
			if err := db.CreateCard(ctx, &src); err != nil {
				t.Fatal(err)
			}
			branch(t, repo, sourceObjID, src.ID, "candidate.txt", "synthetic change\n")
			candidateRef := cardBranchName(sourceObjID, src.ID)
			intermediate := rev(t, repo, candidateRef)
			git(t, repo, "checkout", candidateRef)
			if err := os.WriteFile(filepath.Join(repo, "rework.txt"), []byte("synthetic rework\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, repo, "add", "rework.txt")
			git(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit", "-m", "candidate rework")
			head := rev(t, repo, "HEAD")
			git(t, repo, "checkout", "main")
			sourceDir := filepath.Join(home, "attempts", "stage-old-source")
			if err := os.MkdirAll(sourceDir, 0700); err != nil {
				t.Fatal(err)
			}
			old := model.Attempt{ID: "stage-old-source", Card: src.ID, Branch: candidateRef, BaseCommit: sourceBase, HeadCommit: intermediate, Outcome: model.OutcomePassed, PromptPath: filepath.Join(sourceDir, "prompt.txt")}
			if err := db.CreateAttempt(ctx, &old); err != nil {
				t.Fatal(err)
			}
			reworked := model.Attempt{ID: "stage-reworked-source", Card: src.ID, Index: 1, Branch: candidateRef, BaseCommit: intermediate, HeadCommit: head, Outcome: model.OutcomePassed}
			if err := db.CreateAttempt(ctx, &reworked); err != nil {
				t.Fatal(err)
			}
			for file, data := range map[string]string{"prompt.txt": "SYNTHETIC-OLD-AUTHOR-PROMPT", "acceptance.log": "SYNTHETIC-OLD-RAW-ACCEPTANCE", "result.md": "SYNTHETIC-OLD-REVIEW-RESULT"} {
				if err := os.WriteFile(filepath.Join(sourceDir, file), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			rv := model.Card{ID: "review-linked", Objective: objID, Kind: model.KindReview, Title: "review the exact candidate",
				Body:       "CURRENT-EXPLICIT-SCOPE\n上一档复核（T1）没把握，已升到 T2。上一档意见：SYNTHETIC-OLD-OPINION\n",
				ReworkNote: "SYNTHETIC-OLD-NOTE", Deps: []string{src.ID}, Status: model.CardReady, Tier: model.TierT3, StrictDeps: tc.strict}
			if err := db.CreateCard(ctx, &rv); err != nil {
				t.Fatal(err)
			}
			r := New(home)
			r.Launch = func(context.Context, string, string) (int, error) { return 77, nil }
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			a := reviewAttempt(t, db, rv.ID)
			if tc.strict && (a.Branch != "" || gitOutput(t, a.Worktree, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD") {
				t.Fatalf("strict review unexpectedly owned the source branch: %+v", a)
			}
			prompt := string(mustRead(t, a.PromptPath))
			for _, want := range []string{"CURRENT-EXPLICIT-SCOPE", `"evidence_status":"acceptance_unavailable"`, `"base":"` + sourceBase + `"`, `"candidate":"` + head + `"`, "既有验收证据不可用", "独立执行任务允许的验证"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("missing scoped Git-only evidence %q", want)
				}
			}
			for _, unwanted := range []string{"SYNTHETIC-OLD-", "cardex-context", "acceptance.log", `"evidence_references"`, "冻结差异和验收证据不可用", olderIntegration} {
				if strings.Contains(prompt, unwanted) {
					t.Errorf("cold fallback copied or misidentified %q", unwanted)
				}
			}

			// An integration advance to either an ancestor of the pinned
			// candidate or the candidate itself must not narrow its range.
			for _, live := range []string{intermediate, head} {
				git(t, repo, "branch", "-f", "source-integration", live)
				if err := r.writePrompt(ctx, db, rv, targetObj, model.Project{RepoPath: repo}, &a); err != nil {
					t.Fatal(err)
				}
				got := string(mustRead(t, a.PromptPath))
				if !strings.Contains(got, `"base":"`+sourceBase+`"`) ||
					!strings.Contains(got, `"candidate":"`+head+`"`) || strings.Contains(got, `"base":"`+live+`"`) {
					t.Error("advancing integration changed the historical base")
				}
			}

			attached := filepath.Join(t.TempDir(), "attached")
			git(t, repo, "worktree", "add", attached, candidateRef)
			dirty := filepath.Join(t.TempDir(), "dirty")
			git(t, repo, "worktree", "add", "--detach", dirty, head)
			if err := os.WriteFile(filepath.Join(dirty, "candidate.txt"), []byte("dirty tracked edit\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// A subject pin alone does not authorize a different branch,
			// a mismatched HEAD, or an attached or dirty checkout.
			type badCheckout struct {
				name, branch, sha, worktree string
				persist                     bool
			}
			bads := []badCheckout{
				{"wrong-branch", "unrelated", head, a.Worktree, false},
				{"wrong-worktree-sha", candidateRef, sourceBase, a.Worktree, false},
				{"attached", candidateRef, head, attached, false},
				{"dirty", candidateRef, head, dirty, false},
			}
			if tc.strict {
				bads = append(bads, badCheckout{
					"unowned-branchless", "", head, a.Worktree, false,
				}, badCheckout{
					"owned-branchless-wrong-sha", "", sourceBase, a.Worktree, true,
				}, badCheckout{
					"owned-branchless-attached", "", head, attached, true,
				}, badCheckout{
					"owned-branchless-dirty", "", head, dirty, true,
				})
			}
			for _, bad := range bads {
				dest := model.Attempt{ID: bad.name, Card: rv.ID, Branch: bad.branch, Worktree: bad.worktree, BaseCommit: bad.sha}
				if bad.persist {
					if err := db.CreateAttempt(ctx, &dest); err != nil {
						t.Fatal(err)
					}
				}
				stageEvent(t, db, "attempt:"+dest.ID, model.ReviewSubjectEvent, model.PinnedReviewSubject{Subject: src.ID})
				dest.PromptPath = filepath.Join(home, "attempts", dest.ID, "prompt.txt")
				if err := r.writePrompt(ctx, db, rv, targetObj, model.Project{RepoPath: repo}, &dest); err != nil {
					t.Fatal(err)
				}
				badPrompt := string(mustRead(t, dest.PromptPath))
				if !strings.Contains(badPrompt, `"evidence_status":"unavailable"`) || strings.Contains(badPrompt, `"diff"`) {
					t.Errorf("%s received an unverified Git range", bad.name)
				}
			}

			if tc.strict {
				// Git prints the requested original SHA even when a local
				// replace ref made this checkout contain a different tree.
				alterSrc := filepath.Join(t.TempDir(), "alter-source")
				git(t, repo, "worktree", "add", "--detach", alterSrc, intermediate)
				if err := os.WriteFile(filepath.Join(alterSrc, "replaced-only.txt"), []byte("synthetic different checkout"), 0600); err != nil {
					t.Fatal(err)
				}
				git(t, alterSrc, "add", "replaced-only.txt")
				git(t, alterSrc, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "commit", "-m", "different content")
				replacement := rev(t, alterSrc, "HEAD")
				if replacement == head {
					t.Fatal("fixture must provide a different commit")
				}
				git(t, repo, "replace", head, replacement)
				poisoned := filepath.Join(t.TempDir(), "replaced-review")
				git(t, repo, "worktree", "add", "--detach", poisoned, head)
				if _, err := os.Stat(filepath.Join(poisoned, "replaced-only.txt")); err != nil || rev(t, poisoned, "HEAD") != head {
					t.Fatalf("replacement does not reproduce original-SHA/different-content state: %v", err)
				}
				dest := a
				dest.Worktree, dest.PromptPath = poisoned, filepath.Join(home, "attempts", a.ID, "replacement-prompt.txt")
				if err := db.UpdateAttempt(ctx, &dest); err != nil {
					t.Fatal(err)
				}
				if err := r.writePrompt(ctx, db, rv, targetObj, model.Project{RepoPath: repo}, &dest); err != nil {
					t.Fatal(err)
				}
				replacePrompt := string(mustRead(t, dest.PromptPath))
				if !strings.Contains(replacePrompt, `"evidence_status":"unavailable"`) || strings.Contains(replacePrompt, `"diff"`) {
					t.Error("strict review certified different content under a replacement commit")
				}
				git(t, repo, "replace", "-d", head)
				if err := db.UpdateAttempt(ctx, &a); err != nil {
					t.Fatal(err)
				}
				grafts := filepath.Join(repo, ".git", "info", "grafts")
				if err := os.WriteFile(grafts, []byte(head+" "+intermediate+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := r.writePrompt(ctx, db, rv, targetObj, model.Project{RepoPath: repo}, &a); err != nil {
					t.Fatal(err)
				}
				graftPrompt := string(mustRead(t, a.PromptPath))
				if !strings.Contains(graftPrompt, `"reason":"strict_review_git_object_scope_unverified"`) || strings.Contains(graftPrompt, `"diff"`) {
					t.Error("strict review certified a scope with repo-local grafts")
				}
				if err := os.Remove(grafts); err != nil {
					t.Fatal(err)
				}
				if err := r.writePrompt(ctx, db, rv, targetObj, model.Project{RepoPath: repo}, &a); err != nil {
					t.Fatal(err)
				}
				recoveredPrompt := string(mustRead(t, a.PromptPath))
				if !strings.Contains(recoveredPrompt, `"evidence_status":"acceptance_unavailable"`) {
					t.Error("unmodified exact scope did not recover after removing synthetic overlays")
				}
			}

			// Without matching initial lineage, never substitute the
			// narrower last rework's base or today's integration ref.
			old.Branch = "unrelated"
			if err := db.UpdateAttempt(ctx, &old); err != nil {
				t.Fatal(err)
			}
			if err := r.writePrompt(ctx, db, rv, targetObj, model.Project{RepoPath: repo}, &a); err != nil {
				t.Fatal(err)
			}
			got := string(mustRead(t, a.PromptPath))
			if !strings.Contains(got, `"evidence_status":"unavailable"`) || strings.Contains(got, `"diff"`) {
				t.Fatal("incomplete historical lineage received a guessed range")
			}
		})
	}
}
