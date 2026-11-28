package loop

import (
	"encoding/json"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestPostmergeNormalizerLaterAttemptCanApply(t *testing.T) {
	ctx := t.Context()
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeManual, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	src := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardHarvesting, "fixture")
	sa := makeAttempt(t, st, src.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
	d := depsOf(st)
	if err := AbsorbDisposed(ctx, d, sa.ID, model.OutcomePassed, "unstructured fixture"); err != nil {
		t.Fatal(err)
	}
	norm := normalizeCards(t, st, obj.ID, src.ID)[0]
	old := makeAttempt(t, st, norm.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
	if err := AbsorbDisposed(ctx, d, old.ID, model.OutcomePassed, "not yet normalized"); err != nil {
		t.Fatal(err)
	}
	if got := cardStatus(t, st, src.ID); got != model.CardNeedsInput {
		t.Fatalf("first attempt must block; source=%s", got)
	}
	if err := setCardStatus(t, st, norm.ID, model.CardHarvesting); err != nil {
		t.Fatal(err)
	}
	second := makeAttempt(t, st, norm.ID, 1, "base", goneWorktree(t), model.OutcomeTestsFailed)
	if err := AbsorbDisposed(ctx, d, second.ID, model.OutcomeTestsFailed, "failed retried fixture"); err != nil {
		t.Fatal(err)
	}
	if got := eventCount(t, st, attemptKey(sa.ID), normalizeBlockedEvent); got != 2 {
		t.Fatalf("expected each blocked retry's evidence, got %d", got)
	}
	if err := setCardStatus(t, st, norm.ID, model.CardHarvesting); err != nil {
		t.Fatal(err)
	}
	next := makeAttempt(t, st, norm.ID, 2, "base", goneWorktree(t), model.OutcomePassed)
	for range 2 {
		if err := AbsorbDisposed(ctx, d, next.ID, model.OutcomePassed, "## 开放问题\n- [owner] retry result\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := AbsorbDisposed(ctx, d, old.ID, model.OutcomePassed, "## 开放问题\n- [owner] stale fixture\n"); err != nil {
		t.Fatal(err)
	}
	if questionText(nodeOf(t, st, node.ID), "stale fixture") {
		t.Fatal("a stale normalizer result was applied after retry resolution")
	}
	if got := cardStatus(t, st, src.ID); got != model.CardDone || !questionText(nodeOf(t, st, node.ID), "retry result") {
		t.Fatalf("valid later attempt did not apply; source=%s", got)
	}
	if got := eventCount(t, st, attemptKey(sa.ID), evAbsorbed); got != 1 {
		t.Fatalf("source absorbed %d times", got)
	}
}

func TestPostmergeStandaloneAutolinkDoesNotHidePlan(t *testing.T) {
	const list = "[{\x22key\x22:\x22child\x22,\x22kind\x22:\x22dev\x22,\x22title\x22:\x22candidate\x22}]"
	for _, line := range []string{"<https://example.test/spec>", "<team@example.test>", "<mailto:team@example.test>"} {
		if got := ParseCardList("## 结论\n"+line+"\n\n## 新卡\n"+list, "", nil); got.Bad || len(got.Cards) != 1 {
			t.Errorf("%s hid valid list: %+v", line, got)
		}
	}
	for _, raw := range []string{"<div>", "\uFEFF<div>", "<svg:foreignObject>", "<https://example.test/spec><div>", "<https://example.test/spec> <div>", "<!-- closed --> <div>"} {
		if got := ParseCardList("## 结论\n"+raw+"\n##新卡\n"+list+"\n</div>", "", nil); !got.Bad {
			t.Errorf("raw block %q granted plan authority: %+v", raw, got)
		}
	}
	for _, raw := range []string{"```markdown\n##新卡\n" + list + "\n```", "- example\n  ##新卡\n  " + list, "    ##新卡\n    " + list} {
		if got := ParseCardList(raw, "", nil); !got.Bad {
			t.Errorf("example granted compact plan authority: %+v", got)
		}
	}
}

func TestPostmergeCompactDomainHeadingsApply(t *testing.T) {
	ctx := t.Context()
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeManual, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	plan := seedCard(t, st, obj.ID, node.ID, model.KindPlan, model.CardHarvesting, "fixture")
	d := depsOf(st)
	held, err := d.applyCardList(ctx, obj, plan, "## 结论\nOrdinary text\n##新卡\n[{\x22key\x22:\x22child\x22,\x22kind\x22:\x22dev\x22,\x22title\x22:\x22compact child\x22}]")
	if err != nil || held {
		t.Fatalf("valid compact plan held=%v err=%v", held, err)
	}
	cards, err := st.ListCards(ctx, obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cards {
		found = found || c.Title == "compact child"
	}
	if !found {
		t.Error("valid compact plan created no new card")
	}
	research := seedCard(t, st, obj.ID, "", model.KindResearch, model.CardHarvesting, "fixture")
	if err := d.absorbInventory(ctx, obj, research, "##事实\n### Q0\n结论：compact fact\n证据：fixture.md\n", "base"); err != nil {
		t.Fatal(err)
	}
	events, err := st.ListEvents(ctx, "card:"+research.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Type != evAbsorbed {
			continue
		}
		var got model.OpenQuestion
		if err := json.Unmarshal(e.Data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status != model.OQAnswered {
			t.Errorf("compact fact not applied: %+v", got)
		}
		return
	}
	t.Fatal("missing research absorb event")
}

func TestPostmergeNormalizerDoesNotOverwriteOwnerHold(t *testing.T) {
	ctx := t.Context()
	st := openLoop(t)
	obj := seedObjective(t, st, "", model.ModeManual, model.FreezeSafe)
	node := seedNode(t, st, obj.ID, "", model.LayerModule)
	src := seedCard(t, st, obj.ID, node.ID, model.KindDesign, model.CardHarvesting, "fixture")
	sourceAttempt := makeAttempt(t, st, src.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
	d := depsOf(st)
	if err := AbsorbDisposed(ctx, d, sourceAttempt.ID, model.OutcomePassed, "unstructured fixture"); err != nil {
		t.Fatal(err)
	}
	norm := normalizeCards(t, st, obj.ID, src.ID)[0]
	earlier := makeAttempt(t, st, norm.ID, 0, "base", goneWorktree(t), model.OutcomePassed)
	if err := AbsorbDisposed(ctx, d, earlier.ID, model.OutcomePassed, "unstructured extraction"); err != nil {
		t.Fatal(err)
	}
	held, err := st.GetCard(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	held.WaitReason = "synthetic owner hold"
	if err := st.UpdateCard(ctx, &held); err != nil {
		t.Fatal(err)
	}
	if err := setCardStatus(t, st, norm.ID, model.CardHarvesting); err != nil {
		t.Fatal(err)
	}
	next := makeAttempt(t, st, norm.ID, 1, "base", goneWorktree(t), model.OutcomePassed)
	if err := AbsorbDisposed(ctx, d, next.ID, model.OutcomePassed, "## 开放问题\n- [owner] unintended change\n"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetCard(ctx, src.ID)
	if err != nil || got.Status != model.CardNeedsInput || got.WaitReason != held.WaitReason || questionText(nodeOf(t, st, node.ID), "unintended change") {
		t.Fatalf("owner hold was overwritten: %+v err=%v", got, err)
	}
}
