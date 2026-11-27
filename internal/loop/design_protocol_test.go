package loop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestDesignProtocolConclusionProseAbsorbsOnce(t *testing.T) {
	for _, mode := range []model.ObjectiveMode{model.ModeManual, model.ModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			st := openLoop(t)
			obj := seedObjective(t, st, "", mode, model.FreezeSafe)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			card := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardHarvesting, "设计\n")
			attempt := makeAttempt(t, st, card.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
			result := "## 结论\n接口冻结是发布检查点。\n\n下个版本说明取数方式。\n"
			for range 2 {
				if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomePassed, result); err != nil {
					t.Fatal(err)
				}
			}
			if got, n := cardStatus(t, st, card.ID), len(normalizeCards(t, st, obj.ID, card.ID)); got != model.CardDone || n != 0 {
				t.Fatalf("passed attempt treated as control: status=%s normalizers=%d", got, n)
			}
			if got := eventCount(t, st, attemptKey(attempt.ID), evAbsorbed); got != 1 {
				t.Fatalf("absorption events=%d", got)
			}
			if got := eventCount(t, st, "node:"+node.ID, evFreezable); got != 0 {
				t.Fatalf("freeze proposals=%d", got)
			}
		})
	}
}

func TestDesignProtocolFencedExampleIsNotControl(t *testing.T) {
	body := "## 结论\n示例说明。\n```markdown\n## 可冻结\n是\n```\n之后是正文。\n"
	if got := judgeResultFreeze(body); got != freezeNo {
		t.Fatalf("fenced control example produced %v", got)
	}
	if got := judgeResultFreeze(body + "\n## 可冻结\n是\n"); got != freezeYes {
		t.Fatalf("explicit real control not recognized: %v", got)
	}
}

func TestDesignProtocolNormalizeBindsExactlyOneAttempt(t *testing.T) {
	for _, edge := range []string{"pass", "failed", "unusable pass", "stale"} {
		t.Run(edge, func(t *testing.T) {
			ctx := t.Context()
			st := openLoop(t)
			obj := seedObjective(t, st, "", model.ModeManual, model.FreezeSafe)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			source := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardHarvesting, "设计")
			sourceAttempt := makeAttempt(t, st, source.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
			for range 2 {
				if err := AbsorbDisposed(ctx, depsOf(st), sourceAttempt.ID, model.OutcomePassed, "ordinary text without a section"); err != nil {
					t.Fatal(err)
				}
			}
			norms := normalizeCards(t, st, obj.ID, source.ID)
			card, err := st.GetCard(ctx, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if card.Status != model.CardWaiting || len(norms) != 1 || !strings.Contains(card.WaitReason, sourceAttempt.ID) || !strings.Contains(card.WaitReason, norms[0].ID) {
				t.Fatalf("waiting card=%+v normalizers=%+v", card, norms)
			}
			norm := norms[0]
			checkEvent := func(entity, kind, reason, normalizeAttempt string) {
				t.Helper()
				events, err := st.ListEvents(ctx, entity)
				if err != nil {
					t.Fatal(err)
				}
				found := 0
				for _, event := range events {
					if event.Type != kind {
						continue
					}
					found++
					var link normalizationLink
					if err := json.Unmarshal(event.Data, &link); err != nil ||
						link.SourceCard != source.ID || link.SourceAttempt != sourceAttempt.ID ||
						link.Normalize != norm.ID || link.NormalizeAttempt != normalizeAttempt || link.Reason != reason {
						t.Fatalf("invalid evidence %s: %+v (err %v)", kind, link, err)
					}
				}
				if found != 1 {
					t.Fatalf("%s events=%d", kind, found)
				}
			}
			checkEvent(attemptKey(sourceAttempt.ID), NormalizePlannedEvent, normalizeBadArtifact, "")
			attempt := makeAttempt(t, st, norm.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
			wantStatus, wantReason := model.CardDone, normalizeApplied
			outcome, text := model.OutcomePassed, "## 开放问题\n- [owner] Recovery question\n"
			if edge == "failed" {
				wantStatus, wantReason, outcome = model.CardNeedsInput, normalizeFailed, model.OutcomeTestsFailed
			} else if edge == "unusable pass" {
				wantStatus, wantReason, text = model.CardNeedsInput, normalizeInvalid, "still does not have a design section"
			} else if edge == "stale" {
				wantStatus, wantReason = model.CardRunning, normalizeStale
				makeAttempt(t, st, source.ID, 1, "new", goneWorktree(t), "")
				if err := setCardStatus(t, st, source.ID, wantStatus); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := AbsorbDisposed(ctx, depsOf(st), attempt.ID, outcome, text); err != nil {
					t.Fatal(err)
				}
			}
			checkEvent(attemptKey(attempt.ID), normalizeResolvedEvent, wantReason, attempt.ID)
			fresh, err := st.GetCard(ctx, source.ID)
			if err != nil || fresh.Status != wantStatus {
				t.Fatalf("source=%+v err=%v", fresh, err)
			}
			if edge == "pass" && fresh.WaitReason != "" {
				t.Fatalf("stale wait after success: %s", fresh.WaitReason)
			}
			if fresh.Status == model.CardNeedsInput {
				if !strings.Contains(fresh.WaitReason, wantReason) || hasAbsorbed(t, st, source.ID) ||
					eventCount(t, st, attemptKey(sourceAttempt.ID), evAbsorbed) != 0 {
					t.Fatalf("wrong needs-input reason or falsely absorbed: %s", fresh.WaitReason)
				}
				checkEvent(attemptKey(sourceAttempt.ID), normalizeBlockedEvent, wantReason, attempt.ID)
			}
			if got := questionText(nodeOf(t, st, node.ID), "Recovery question"); got != (edge == "pass") {
				t.Fatalf("absorbed invalid, stale, or failed output: %v", got)
			}
			stored, err := st.GetAttempt(ctx, sourceAttempt.ID)
			if err != nil || stored.Outcome != model.OutcomePassed {
				t.Fatalf("harvest pass changed when source was held: %+v %v", stored, err)
			}
		})
	}
}

// The test fixture explicitly associates manually planted normalize cards.
func linkFixtureNormalize(t *testing.T, st *store.Store, source, normalize string) {
	t.Helper()
	d := Deps{Store: st}
	attempt, err := d.currentAttemptID(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.writeNormalizationPlan(t.Context(), normalizationLink{SourceCard: source, SourceAttempt: attempt, Normalize: normalize, Reason: normalizeBadArtifact}); err != nil {
		t.Fatal(err)
	}
}

func TestDesignProtocolRejectsUndeclaredNormalizer(t *testing.T) {
	for _, haveAttempt := range []bool{false, true} {
		st := openLoop(t)
		obj := seedObjective(t, st, "", model.ModeManual, model.FreezeSafe)
		node := seedNode(t, st, obj.ID, "", model.LayerModule)
		source := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardHarvesting, "design")
		if haveAttempt {
			makeAttempt(t, st, source.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
		}
		norm := model.Card{Objective: obj.ID, Node: node.ID, Kind: model.KindNormalize, Tier: model.TierT1, Status: model.CardRunning, Deps: []string{source.ID}}
		if err := st.CreateCard(t.Context(), &norm); err != nil {
			t.Fatal(err)
		}
		attempt := makeAttempt(t, st, norm.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
		if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomePassed, "## 开放问题\n- [owner] untrusted normalized prose\n"); err != nil {
			t.Fatal(err)
		}
		if cardStatus(t, st, source.ID) != model.CardHarvesting || questionText(nodeOf(t, st, node.ID), "untrusted normalized prose") {
			t.Fatal("an unbound normalizer changed its dependency")
		}
	}
}

func TestDesignProtocolControlRequiresOneRealHeading(t *testing.T) {
	for _, tc := range []struct {
		text string
		want freezeVerdict
	}{
		{"## 结论\nSome prose\n    ## 可冻结\n    是\n", freezeNo},
		{"## 可冻结\n    是\n", freezeUnclear},
		{"## 可冻结\n\t是\n", freezeUnclear},
		{"<!--\n## 可冻结\n是\n-->\n## 结论\nNormal text\n", freezeNo},
		{"<details>\n## 可冻结\n是\n</details>\n## 结论\nNormal text\n", freezeNo},
		{"<div>\n## 可冻结\n是\n</div>\n## 可冻结\n是\n", freezeNo},
		{"<div>\n<div>\n</div>\n## 可冻结\n是\n</div>\n", freezeNo},
		{"<hr><div>\n## 可冻结\n是\n</div>\n", freezeNo},
		{"\uFEFF<div>\n## 可冻结\n是\n## 结论\nHTML example\n</div>\n", freezeNo},
		{"\uFEFF## 可冻结\n是\n", freezeYes},
		{"<!-- illustration --> <div>\n## 可冻结\n是\n</div>\n", freezeNo},
		{"<![CDATA[\n## 可冻结\n是\n]]>\n", freezeNo},
		{"## 可冻结\n是\n## 结论\nBefore raw HTML\n<div>\n## 可冻结\n是\n</div>\n", freezeYes},
		{"- Example\n  ## 可冻结\n  是\n", freezeNo},
		{"1. Example\n   ## 可冻结\n   是\n", freezeNo},
		{"## 结论\nSome prose\n\t## 可冻结\n\t是\n", freezeNo},
		{"## 可冻结\n是\n## 可冻结\n否\n", freezeUnclear},
		{"## 可冻结\n是\n## 可冻结\n是\n", freezeUnclear},
		{"## 结论\n是\n## 结论\n否\n", freezeUnclear},
		{"  ## 可冻结\n是\n", freezeYes},
		{"##可冻结\n是\n", freezeNo},
		{"## 可冻结\n否\n## 结论\n是\n", freezeNo},
	} {
		if got := judgeResultFreeze(tc.text); got != tc.want {
			t.Fatalf("for %q, got %v want %v", tc.text, got, tc.want)
		}
	}
}

func TestDesignProtocolCancellationChecksStoredEdgesUnderWriter(t *testing.T) {
	for _, target := range []string{"source", "normalize"} {
		t.Run(target, func(t *testing.T) {
			ctx := t.Context()
			st := openLoop(t)
			d := depsOf(st)
			obj := seedObjective(t, st, "", model.ModeManual, model.FreezeSafe)
			node := seedNode(t, st, obj.ID, "", model.LayerModule)
			source := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardHarvesting, "design")
			attempt := makeAttempt(t, st, source.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
			if target == "source" {
				if err := setCardStatus(t, st, source.ID, model.CardCanceled); err != nil {
					t.Fatal(err)
				}
				if _, err := d.recoverUnparsed(ctx, obj, source, attempt.ID, normalizeBadArtifact); err != nil {
					t.Fatal(err)
				}
				if got := len(normalizeCards(t, st, obj.ID, source.ID)); got != 0 {
					t.Fatalf("canceled source created %d normalizers", got)
				}
				if eventCount(t, st, attemptKey(attempt.ID), NormalizePlannedEvent) != 0 {
					t.Fatal("canceled source gained a normalization link")
				}
				return
			}
			if _, err := d.recoverUnparsed(ctx, obj, source, attempt.ID, normalizeBadArtifact); err != nil {
				t.Fatal(err)
			}
			norm := normalizeCards(t, st, obj.ID, source.ID)[0] // pre-cancellation snapshot
			na := makeAttempt(t, st, norm.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
			if err := setCardStatus(t, st, norm.ID, model.CardCanceled); err != nil {
				t.Fatal(err)
			}
			if err := d.applyNormalize(ctx, obj, norm, na.ID, "## 开放问题\n- [owner] canceled extraction\n", model.OutcomePassed); err != nil {
				t.Fatal(err)
			}
			if got := cardStatus(t, st, source.ID); got != model.CardNeedsInput {
				t.Fatalf("source status %s", got)
			}
			if questionText(nodeOf(t, st, node.ID), "canceled extraction") || hasAbsorbed(t, st, source.ID) {
				t.Fatal("canceled extraction was applied")
			}
		})
	}
}

func TestDesignProtocolExamplesCannotCreateQuestionsOrCardLists(t *testing.T) {
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeManual, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	card := seedCard(t, st, obj.ID, node.ID, model.KindPlan, model.CardHarvesting, "plan")
	attempt := makeAttempt(t, st, card.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
	text := "## 结论\nAn example follows.\n~~~markdown\n## 开放问题\n- [owner] example question\n## 新卡\n[{\"key\":\"fake\",\"kind\":\"dev\",\"title\":\"example child\"}]\n~~~\n    ## 可冻结\n    是\n"
	if err := AbsorbDisposed(t.Context(), depsOf(st), attempt.ID, model.OutcomePassed, text); err != nil {
		t.Fatal(err)
	}
	if got := cardStatus(t, st, card.ID); got != model.CardDone {
		t.Fatalf("ordinary plan status %s", got)
	}
	if questionText(nodeOf(t, st, node.ID), "example question") || len(cardsOf(t, st, obj.ID)) != 1 {
		t.Fatal("example text created state")
	}
	if _, ok := structuralPlanBody(text); ok {
		t.Fatal("example registered a card list")
	}
}

func TestDesignProtocolLosingAttemptCannotAbsorbOrdinaryOrListedPlan(t *testing.T) {
	for _, kind := range []model.CardKind{model.KindDesign, model.KindPlan} {
		t.Run(string(kind), func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			source := f.card(model.Card{ID: "source", Kind: kind, Status: model.CardHarvesting})
			old := f.attempt(source, 0, "", "", "")
			body := "## 开放问题\n- [owner] stale question\n## 可冻结\n是\n"
			if kind == model.KindPlan {
				body += "## 新卡\n[{\"key\":\"child\",\"kind\":\"dev\",\"title\":\"stale child\"}]\n"
			}
			old.Worktree = writeResult(t, body)
			if err := f.db.UpdateAttempt(f.ctx, &old); err != nil {
				t.Fatal(err)
			}
			f.attempt(source, 1, "", "", "")
			ctx := WithAttempt(WithStore(WithHome(f.ctx, f.home), f.db), old.ID)
			if err := Dispose(ctx, &source, harvest.Result{Outcome: model.OutcomePassed, ResultText: body}); err != nil {
				t.Fatal(err)
			}
			if got := f.mustCard(source.ID); got.Status != model.CardHarvesting {
				t.Fatalf("stale result changed source: %+v", got)
			}
			if len(f.cards()) != 1 || len(f.events(evFreezable)) != 0 || questionText(nodeOf(t, f.db, f.node.ID), "stale question") {
				t.Fatal("stale result mutated design")
			}
		})
	}
}

func TestDesignDisposeStaleSuccessAndFailureAreEvidenceOnly(t *testing.T) {
	for _, kind := range []model.CardKind{model.KindDesign, model.KindPlan, model.KindReplan, model.KindNormalize} {
		for _, outcome := range []model.Outcome{model.OutcomePassed, model.OutcomeTestsFailed} {
			t.Run(string(kind)+"/"+string(outcome), func(t *testing.T) {
				f := newFx(t, model.ModeManual)
				var candidate model.Card
				if kind == model.KindNormalize {
					parent := f.card(model.Card{ID: "source", Kind: model.KindDesign, Status: model.CardWaiting})
					f.attempt(parent, 0, model.OutcomePassed, "", "")
					candidate = f.card(model.Card{ID: "normalize", Kind: kind, Status: model.CardRunning, Deps: []string{parent.ID}})
				} else {
					candidate = f.card(model.Card{ID: "source", Kind: kind, Status: model.CardRunning})
				}
				res := harvest.Result{Outcome: outcome, ResultText: "## 开放问题\n- [owner] stale callback must not apply\n## 可冻结\n是\n", Claims: []model.Claim{{Path: "stale-claim"}}}
				old := f.attempt(candidate, 0, "", "", "")
				old.Worktree = writeResult(t, res.ResultText)
				if err := f.db.UpdateAttempt(f.ctx, &old); err != nil {
					t.Fatal(err)
				}
				newer := f.attempt(candidate, 1, "", "", "")
				ctx := WithAttempt(WithStore(WithHome(f.ctx, f.home), f.db), old.ID)
				for range 2 {
					staleCopy := candidate
					staleCopy.Claims = res.Claims
					if err := Dispose(ctx, &staleCopy, res); err != nil {
						t.Fatal(err)
					}
					if staleCopy.Status != model.CardRunning || len(staleCopy.Claims) != 0 {
						t.Fatalf("stale callback offered changed card %+v", staleCopy)
					}
				}
				current := f.mustCard(candidate.ID)
				if current.Status != model.CardRunning || current.ReworkCount != 0 || len(current.Claims) != 0 {
					t.Fatalf("new run was mutated: %+v", current)
				}
				events, err := f.db.ListEvents(f.ctx, attemptKey(old.ID))
				if err != nil || len(events) != 1 || events[0].Type != staleDesignAttemptEvent || !strings.Contains(string(events[0].Data), newer.ID) {
					t.Fatalf("unexpected replay evidence %+v err %v", events, err)
				}
				if kind == model.KindNormalize && f.mustCard("source").Status != model.CardWaiting {
					t.Fatal("stale normalizer touched source")
				}
				if questionText(nodeOf(t, f.db, f.node.ID), "stale callback must not apply") {
					t.Fatal("stale design output was applied")
				}
			})
		}
	}
}
