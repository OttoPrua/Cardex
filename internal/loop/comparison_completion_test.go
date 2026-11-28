package loop

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func api2CompletionFixture(t *testing.T) (*fx, string, string) {
	t.Helper()
	repo, main := integrationRepo(t)
	literal, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	repo = literal
	gitRun(t, repo, "checkout", "--detach", "main")
	f := newFx(t, model.ModeAuto)
	f.pointRepo(repo)
	f.setCommands([]string{"test -f marker"})
	f.card(model.Card{ID: "finished", Status: model.CardMerged})
	return f, repo, main
}

func api2StandaloneCard(f *fx, edit func(*model.Card)) model.Card {
	f.t.Helper()
	card := model.Card{
		ID: "isolated-comparison", Objective: f.obj.ID, Kind: model.KindResearch, Title: "optional comparison", Body: "synthetic fixture question",
		Status: model.CardReady, Channel: "isolated-fixture", Model: "synthetic-fixture", Requires: []string{model.RequireStrictComparison},
	}
	if edit != nil {
		edit(&card)
	}
	if err := f.db.CreateCard(f.ctx, &card); err != nil {
		f.t.Fatal(err)
	}
	return card
}

func api2HoldPreparedCard(f *fx, card model.Card) {
	f.t.Helper()
	binding := strings.Repeat("b", 64)
	ch := model.Channel{ID: card.Channel, Runner: model.RunnerClaude, Health: model.HealthDegraded, Models: []string{card.Model},
		EngineProfile: "anthropic-compatible:fixture@" + binding}
	if err := f.db.CreateChannel(f.ctx, &ch); err != nil {
		f.t.Fatal(err)
	}
	_, input := card.ComparisonInput()
	a := model.Attempt{ID: "old-prepared-comparison", Card: card.ID, Route: model.Route{Channel: card.Channel, Model: card.Model},
		RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{Runner: model.RunnerClaude,
			Channel: card.Channel, Model: card.Model, CompatibleBindingID: binding, StrictComparison: true, ComparisonInputDigest: input}}}
	if err := f.db.CreateAttempt(f.ctx, &a); err != nil {
		f.t.Fatal(err)
	}
	got, err := f.db.GetAttempt(f.ctx, a.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.RefuseDisabledComparison(f.ctx, f.mustCard(card.ID), got, f.objective(), time.Now()); err != nil {
		f.t.Fatal(err)
	}
}

func api2HoldUnlaunchedCard(f *fx, card model.Card) {
	f.t.Helper()
	// The scheduler's ordinary pre-launch compatibility hold writer.
	if err := f.db.WithWriteTx(f.ctx, func(ctx context.Context) error {
		current, err := f.db.GetCard(ctx, card.ID)
		if err != nil {
			return err
		}
		return f.db.MarkCompatibleCardHeld(ctx, current)
	}); err != nil {
		f.t.Fatal(err)
	}
}

func api2CompletionDependency(f *fx, card model.Card, cross bool, implicitReview bool) model.Card {
	f.t.Helper()
	objective := f.obj.ID
	if cross {
		obj := f.objective()
		obj.ID = "dependent-objective"
		if err := f.db.CreateObjective(f.ctx, &obj); err != nil {
			f.t.Fatal(err)
		}
		objective = obj.ID
	}
	dependent := model.Card{ID: "finished-dependent", Objective: objective, Kind: model.KindDev, Status: model.CardDone,
		Title: "linked fixture", Deps: []string{card.ID}}
	if implicitReview {
		dependent.ID, dependent.Kind, dependent.Deps = "rv-"+card.ID, model.KindReview, nil
	}
	if err := f.db.CreateCard(f.ctx, &dependent); err != nil {
		f.t.Fatal(err)
	}
	return dependent
}

func TestAPI2OptionalComparisonDoesNotBlockAutomaticObjective(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		name := "unstarted"
		if prepared {
			name = "prepared-historical-no-wire"
		}
		t.Run(name, func(t *testing.T) {
			f, repo, main := api2CompletionFixture(t)
			card := api2StandaloneCard(f, nil)
			if prepared {
				api2HoldPreparedCard(f, card)
			} else {
				api2HoldUnlaunchedCard(f, card)
			}
			before := f.mustCard(card.ID)
			reason := model.CompatibleRuntimeHold
			if prepared {
				reason = store.StrictComparisonDisabled
			}
			if before.Status != model.CardNeedsInput || before.WaitReason != reason {
				t.Fatalf("setup did not produce a real hard hold: %+v", before)
			}
			f.step()
			questions := questionsKind(f, model.QuestionDone)
			if f.objective().Status != model.ObjectiveDoneCandidate || len(questions) != 1 || questions[0].Answer != "" {
				t.Fatal("a no-wire optional comparison blocked automatic candidate")
			}
			if gitSHA(t, repo, "main") != main {
				t.Fatal("candidate moved main before approval")
			}
			q := questions[0]
			q.Answer = "approve"
			if err := f.db.UpdateQuestion(f.ctx, &q); err != nil {
				t.Fatal(err)
			}
			f.step()
			if f.objective().Status != model.ObjectiveDone || gitSHA(t, repo, "main") == main || len(f.events("merged_local")) != 1 {
				t.Fatal("approved automatic objective did not merge the real local fixture")
			}
			after := f.mustCard(card.ID)
			if after.Status != model.CardNeedsInput || after.WaitReason != before.WaitReason {
				t.Fatalf("objective completion changed comparison visibility or availability: %+v", after)
			}
			attempts, err := f.db.ListAttempts(f.ctx, card.ID)
			if err != nil || (!prepared && len(attempts) != 0) || (prepared && len(attempts) != 1) {
				t.Fatalf("completion spawned a comparison or replaced its refusal: %d %v", len(attempts), err)
			}
			for _, a := range attempts {
				got, err := f.db.GetStrictComparisonResult(f.ctx, a.ID)
				if err != nil || got != nil || a.PID != 0 || a.Exit != nil || a.Usage != (model.Usage{}) {
					t.Fatal("completion fabricated execution, usage, or an isolated result")
				}
			}
			if _, err := os.Stat(filepath.Join(repo, "pushed-by-hook")); !os.IsNotExist(err) {
				t.Fatal("local approval pushed")
			}
		})
	}
}

func TestAPI2ExplicitOrOrdinaryWorkStillBlocksAutomaticObjective(t *testing.T) {
	for _, mode := range []string{"explicit-outbound", "explicit-inbound", "cross-objective-inbound", "implicit-review-subject",
		"ordinary-research", "node-assigned", "unrelated-needs-input", "unanswered-card-decision", "ordinary-attempt",
		"without-no-wire-witness", "path-claim", "acceptance"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, main := api2CompletionFixture(t)
			c := api2StandaloneCard(f, func(c *model.Card) {
				switch mode {
				case "ordinary-research":
					c.Requires = nil
				case "explicit-outbound":
					c.Deps = []string{"finished"}
				case "node-assigned":
					c.Node = f.node.ID
				case "path-claim":
					c.Claims = []model.Claim{{Path: "synthetic/path"}}
				case "acceptance":
					c.Acceptance = []string{"true"}
				case "unrelated-needs-input":
					c.Status, c.WaitReason = model.CardNeedsInput, "owner needs to revise the card"
				}
			})
			switch mode {
			case "explicit-inbound":
				api2CompletionDependency(f, c, false, false)
			case "cross-objective-inbound":
				api2CompletionDependency(f, c, true, false)
			case "implicit-review-subject":
				api2CompletionDependency(f, c, false, true)
			case "unanswered-card-decision":
				q := model.Question{ID: "comparison-owner-decision", Objective: f.obj.ID, Scope: c.ID, ScopeKind: "card",
					Kind: model.QuestionCustom, Text: "explicit synthetic owner decision"}
				if err := f.db.CreateQuestion(f.ctx, &q); err != nil {
					t.Fatal(err)
				}
			case "ordinary-attempt":
				f.attempt(c, 0, model.OutcomePassed, c.Channel, "foreign ordinary history")
			case "without-no-wire-witness":
				// Historical metadata without the typed event is not proof.
				_, input := c.ComparisonInput()
				a := model.Attempt{ID: "no-record", Card: c.ID, Outcome: model.OutcomeUnavailable, ErrorClass: model.ErrorUnavailable,
					ErrorExcerpt: store.StrictComparisonDisabled,
					EndedAt:      time.Now().UTC(), Route: model.Route{Channel: c.Channel, Model: c.Model},
					RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{Runner: model.RunnerClaude,
						Channel: c.Channel, Model: c.Model, StrictComparison: true, ComparisonInputDigest: input}}}
				if err := f.db.CreateAttempt(f.ctx, &a); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "unrelated-needs-input" {
				api2HoldUnlaunchedCard(f, c)
			}
			f.step()
			if f.objective().Status != model.ObjectiveActive || len(questionsKind(f, model.QuestionDone)) != 0 ||
				gitSHA(t, repo, "main") != main {
				t.Fatal("required, unproved, or ordinary unfinished work was treated as optional")
			}
		})
	}
}

func TestAPI2NewComparisonDependencyHoldsPendingApproval(t *testing.T) {
	f, repo, main := api2CompletionFixture(t)
	c := api2StandaloneCard(f, nil)
	api2HoldUnlaunchedCard(f, c)
	f.step()
	qs := questionsKind(f, model.QuestionDone)
	if f.objective().Status != model.ObjectiveDoneCandidate || len(qs) != 1 {
		t.Fatal("comparison unexpectedly blocked the initial candidate")
	}
	dependent := api2CompletionDependency(f, c, true, false)
	q := qs[0]
	q.Answer = "approve"
	if err := f.db.UpdateQuestion(f.ctx, &q); err != nil {
		t.Fatal(err)
	}
	f.step()
	if f.objective().Status != model.ObjectiveDoneCandidate || gitSHA(t, repo, "main") != main || len(f.events("merged_local")) != 0 {
		t.Fatal("approval merged after a new cross-objective dependency")
	}
	reopened := f.mustCard(c.ID)
	reopened.Status, reopened.WaitReason = model.CardReady, ""
	if err := f.db.UpdateCard(f.ctx, &reopened); err != nil {
		t.Fatal(err)
	}
	f.step()
	if f.objective().Status != model.ObjectiveDoneCandidate || gitSHA(t, repo, "main") != main || len(f.events("merged_local")) != 0 {
		t.Fatal("approval merged while the linked comparison was reopened")
	}
	api2HoldUnlaunchedCard(f, c)
	dependent.Deps = nil
	if err := f.db.UpdateCard(f.ctx, &dependent); err != nil {
		t.Fatal(err)
	}
	f.step()
	if f.objective().Status != model.ObjectiveDone || gitSHA(t, repo, "main") == main || len(f.events("merged_local")) != 1 {
		t.Fatal("released optional comparison did not allow approved merge")
	}
}

func api2OpenFixtureDatabase(t *testing.T, home string) *sql.DB {
	t.Helper()
	u := url.URL{Path: filepath.ToSlash(filepath.Join(home, "cardex.db"))}
	db, err := sql.Open("sqlite", "file:"+u.EscapedPath()+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func api2FixtureReviewPin(f *fx, review model.Card, id string, index int, open bool, subject string, standalone bool) {
	f.t.Helper()
	a := model.Attempt{ID: id, Card: review.ID, Index: index}
	if !open {
		a.EndedAt, a.Outcome = time.Now().UTC(), model.OutcomePassed
	}
	if err := f.db.CreateAttempt(f.ctx, &a); err != nil {
		f.t.Fatal(err)
	}
	raw, err := json.Marshal(model.PinnedReviewSubject{Subject: subject, Standalone: standalone})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.AppendEvent(f.ctx, &model.Event{Entity: "attempt:" + a.ID, Type: model.ReviewSubjectEvent, Data: raw}); err != nil {
		f.t.Fatal(err)
	}
}

func TestAPI2CompletionReadsPersistedReviewGraph(t *testing.T) {
	for _, v := range []struct {
		mode  string
		block bool
	}{
		{"changed-deps-latest-pin", true}, {"older-active-pin", true}, {"not-linked-latest", false},
		{"legacy-tab-and-nbsp", true}, {"empty-first-dep-is-implicit-review", true},
	} {
		t.Run(v.mode, func(t *testing.T) {
			f, _, _ := api2CompletionFixture(t)
			c := api2StandaloneCard(f, nil)
			review := api2CompletionDependency(f, c, true, true)
			review.Deps = []string{"outside-fixture"}
			if err := f.db.UpdateCard(f.ctx, &review); err != nil {
				t.Fatal(err)
			}
			switch v.mode {
			case "changed-deps-latest-pin":
				api2FixtureReviewPin(f, review, "linked-latest-review", 0, false, c.ID, false)
			case "older-active-pin", "not-linked-latest":
				api2FixtureReviewPin(f, review, "linked-older-review", 0, v.mode == "older-active-pin", c.ID, false)
				api2FixtureReviewPin(f, review, "unlinked-latest-review", 1, false, review.ID, true)
			case "legacy-tab-and-nbsp", "empty-first-dep-is-implicit-review":
				// The normal Store writer trims new definitions. Stage a
				// historical persisted graph to exercise read semantics too.
				dep := "\t\u00a0"
				if v.mode == "legacy-tab-and-nbsp" {
					dep += c.ID
				}
				raw, err := json.Marshal([]string{dep})
				if err != nil {
					t.Fatal(err)
				}
				db := api2OpenFixtureDatabase(t, f.home)
				if _, err := db.ExecContext(f.ctx, "UPDATE cards SET deps=? WHERE id=?", string(raw), review.ID); err != nil {
					t.Fatal(err)
				}
			}
			api2HoldUnlaunchedCard(f, c)
			f.step()
			got := f.objective().Status
			if (got == model.ObjectiveActive) != v.block || (got == model.ObjectiveDoneCandidate) == v.block {
				t.Fatalf("review graph gate mismatch: state=%s want_blocker=%t", got, v.block)
			}
		})
	}
}

func TestAPI2LateOrdinaryWorkGuardsOnlyMarkedApproval(t *testing.T) {
	for _, marked := range []bool{false, true} {
		name := "legacy-unmarked"
		if marked {
			name = "marked-comparison-exemption"
		}
		t.Run(name, func(t *testing.T) {
			f, repo, main := api2CompletionFixture(t)
			if marked {
				c := api2StandaloneCard(f, nil)
				api2HoldUnlaunchedCard(f, c)
			}
			f.step()
			qs := questionsKind(f, model.QuestionDone)
			if f.objective().Status != model.ObjectiveDoneCandidate || len(qs) != 1 {
				t.Fatal("fixture did not become a candidate")
			}
			receipts := f.events(objectiveComparisonExemptionEvent)
			if marked {
				var receipt comparisonExemption
				if len(receipts) != 1 || json.Unmarshal(receipts[0].Data, &receipt) != nil || receipt.Question != qs[0].ID ||
					len(receipt.Cards) != 1 || receipt.Cards[0] != "isolated-comparison" {
					t.Fatalf("wrong durable exemption receipt: %+v", receipts)
				}
			} else if len(receipts) != 0 {
				t.Fatal("ordinary candidacy acquired an exemption marker")
			}
			late := f.card(model.Card{ID: "late-ordinary-research", Kind: model.KindResearch, Status: model.CardReady})
			q := qs[0]
			q.Answer = "approve"
			if err := f.db.UpdateQuestion(f.ctx, &q); err != nil {
				t.Fatal(err)
			}
			f.step()
			if marked {
				if f.objective().Status != model.ObjectiveDoneCandidate || gitSHA(t, repo, "main") != main || len(f.events("merged_local")) != 0 {
					t.Fatal("marked approval ignored newly unfinished ordinary research")
				}
				late.Status = model.CardDone
				if err := f.db.UpdateCard(f.ctx, &late); err != nil {
					t.Fatal(err)
				}
				f.step()
			}
			if f.objective().Status != model.ObjectiveDone || gitSHA(t, repo, "main") == main || len(f.events("merged_local")) != 1 {
				t.Fatal("ordinary approval behavior changed or resolved marked gate did not release")
			}
		})
	}
}

func TestAPI2PriorExemptionCardCannotDisappearBehindApproval(t *testing.T) {
	for _, mode := range []string{"deleted-with-explicit-reference", "deleted-with-pinned-review", "deleted-no-references",
		"moved-without-references", "repurposed-same-id"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, main := api2CompletionFixture(t)
			c := api2StandaloneCard(f, nil)
			api2HoldUnlaunchedCard(f, c)
			f.step()
			qs := questionsKind(f, model.QuestionDone)
			if f.objective().Status != model.ObjectiveDoneCandidate || len(qs) != 1 {
				t.Fatal("fixture did not become a candidate")
			}
			switch mode {
			case "deleted-with-explicit-reference":
				api2CompletionDependency(f, c, true, false)
			case "deleted-with-pinned-review":
				review := api2CompletionDependency(f, c, true, true)
				review.Deps = []string{"different-review-subject"}
				if err := f.db.UpdateCard(f.ctx, &review); err != nil {
					t.Fatal(err)
				}
				api2FixtureReviewPin(f, review, "pinned-after-candidacy", 0, true, c.ID, false)
			case "moved-without-references":
				destination := f.objective()
				destination.ID = "foreign-objective"
				if err := f.db.CreateObjective(f.ctx, &destination); err != nil {
					t.Fatal(err)
				}
				c.Objective = destination.ID
				if err := f.db.UpdateCard(f.ctx, &c); err != nil {
					t.Fatal(err)
				}
			case "repurposed-same-id":
				c.Requires, c.Status = nil, model.CardDone
				if err := f.db.UpdateCard(f.ctx, &c); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(mode, "deleted-") {
				if err := f.db.DeleteCard(f.ctx, c.ID); err != nil {
					t.Fatal(err)
				}
			}
			q := qs[0]
			q.Answer = "approve"
			if err := f.db.UpdateQuestion(f.ctx, &q); err != nil {
				t.Fatal(err)
			}
			f.step()
			if mode == "deleted-no-references" {
				if got := f.objective().Status; got != model.ObjectiveDone || gitSHA(t, repo, "main") == main {
					t.Fatalf("provably unreferenced deletion blocked local merge: %s", got)
				}
			} else if got := f.objective().Status; got != model.ObjectiveDoneCandidate || gitSHA(t, repo, "main") != main {
				t.Fatalf("original comparison identity disappeared behind approval: %s", got)
			}
		})
	}
}

func TestAPI2ReceiptKeepsExactRawComparisonIDAndBlocksAliases(t *testing.T) {
	for _, mode := range []string{"independent-exact-id", "normalized-dependency-alias", "normalized-pinned-review-alias",
		"normalized-card-question-alias", "whitespace-only-identity"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, main := api2CompletionFixture(t)
			c := api2StandaloneCard(f, func(c *model.Card) {
				c.ID = "\tcomparison-raw\u00a0"
				if mode == "whitespace-only-identity" {
					c.ID = "\t\u00a0"
				}
			})
			alias := strings.TrimSpace(c.ID)
			switch mode {
			case "normalized-dependency-alias":
				dependent := api2CompletionDependency(f, c, true, false)
				dependent.Deps = []string{alias}
				if err := f.db.UpdateCard(f.ctx, &dependent); err != nil {
					t.Fatal(err)
				}
			case "normalized-pinned-review-alias":
				review := api2CompletionDependency(f, c, true, true)
				review.Deps = []string{"another-review-subject"}
				if err := f.db.UpdateCard(f.ctx, &review); err != nil {
					t.Fatal(err)
				}
				api2FixtureReviewPin(f, review, "normalized-subject", 0, true, alias, false)
			case "normalized-card-question-alias":
				q := model.Question{Objective: f.obj.ID, Scope: alias, ScopeKind: "card", Kind: model.QuestionCustom, Text: "synthetic decision"}
				if err := f.db.CreateQuestion(f.ctx, &q); err != nil {
					t.Fatal(err)
				}
			}
			api2HoldUnlaunchedCard(f, c)
			f.step()
			qs := questionsKind(f, model.QuestionDone)
			if mode != "independent-exact-id" {
				if len(qs) != 0 || f.objective().Status != model.ObjectiveActive || gitSHA(t, repo, "main") != main {
					t.Fatal("ambiguous ID or linkage opened an unusable approval")
				}
				return
			}
			receipts := f.events(objectiveComparisonExemptionEvent)
			var receipt comparisonExemption
			if len(qs) != 1 || len(receipts) != 1 || json.Unmarshal(receipts[0].Data, &receipt) != nil ||
				len(receipt.Cards) != 1 || receipt.Cards[0] != c.ID || receipt.Cards[0] == alias {
				t.Fatal("stored comparison identity was not preserved exactly")
			}
			q := qs[0]
			q.Answer = "approve"
			if err := f.db.UpdateQuestion(f.ctx, &q); err != nil {
				t.Fatal(err)
			}
			f.step()
			if f.objective().Status != model.ObjectiveDone || gitSHA(t, repo, "main") == main {
				t.Fatal("exact raw-ID approval could not be used")
			}
			if f.mustCard(c.ID).Status != model.CardNeedsInput {
				t.Fatal("comparison itself changed state")
			}
		})
	}
}

func TestAPI2CanceledOrFailedComparisonCannotSatisfyCrossObjectiveStrictDependency(t *testing.T) {
	for _, status := range []model.CardStatus{model.CardCanceled, model.CardFailed, model.CardDone, model.CardMerged} {
		t.Run(string(status), func(t *testing.T) {
			f, repo, main := api2CompletionFixture(t)
			c := api2StandaloneCard(f, nil)
			api2HoldUnlaunchedCard(f, c)
			f.step()
			qs := questionsKind(f, model.QuestionDone)
			if len(qs) != 1 || f.objective().Status != model.ObjectiveDoneCandidate {
				t.Fatal("independent comparison did not initially open approval")
			}
			c = f.mustCard(c.ID)
			c.Status = status
			if err := f.db.UpdateCard(f.ctx, &c); err != nil {
				t.Fatal(err)
			}
			dependent := api2CompletionDependency(f, c, true, false)
			dependent.StrictDeps, dependent.Status = true, model.CardReady
			if err := f.db.UpdateCard(f.ctx, &dependent); err != nil {
				t.Fatal(err)
			}
			q := qs[0]
			q.Answer = "approve"
			if err := f.db.UpdateQuestion(f.ctx, &q); err != nil {
				t.Fatal(err)
			}
			f.step()
			if status == model.CardDone || status == model.CardMerged {
				if f.objective().Status != model.ObjectiveDone || gitSHA(t, repo, "main") == main {
					t.Fatal("satisfied strict prerequisite blocked approval")
				}
			} else if f.objective().Status != model.ObjectiveDoneCandidate || gitSHA(t, repo, "main") != main {
				t.Fatal("canceled or failed comparison silently satisfied cross-objective strict execution dependency")
			}
		})
	}
}
