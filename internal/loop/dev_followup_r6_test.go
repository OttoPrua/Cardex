package loop

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"cardex/internal/harvest"
	"cardex/internal/model"
	"cardex/internal/route"
)

func TestFollowupR6ReviewSelect(t *testing.T) {
	cases := []struct {
		name         string
		row          bool
		rowFamily    string
		wantFamily   string
		dropModel    string
		allowModel   string
		allowChannel string
	}{
		{
			name:         "empty positioning family uses the successful channel",
			row:          true,
			wantFamily:   "anthropic",
			dropModel:    "rev-anth",
			allowModel:   "rev-open",
			allowChannel: "other",
		},
		{
			name:         "non-empty positioning family wins",
			row:          true,
			rowFamily:    "writer",
			wantFamily:   "writer",
			dropModel:    "rev-writer",
			allowModel:   "rev-anth",
			allowChannel: "author",
		},
		{
			name:         "no positioning row uses the channel family",
			wantFamily:   "anthropic",
			dropModel:    "rev-anth",
			allowModel:   "rev-open",
			allowChannel: "other",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			author := f.channel("author", "anthropic", "author-m", "rev-anth", "rev-writer")
			other := f.channel("other", "openai", "rev-open")
			if tc.row {
				f.position("author-m", tc.rowFamily, model.TierT2)
			}
			f.position("rev-anth", "anthropic", model.TierT3)
			f.position("rev-open", "openai", model.TierT3)
			if tc.rowFamily != "" {
				f.position("rev-writer", "writer", model.TierT3)
			}
			card := f.card(model.Card{ID: "dev", Channel: author.ID, AuthorFamily: "openai"})
			attempt := f.attempt(card, 0, model.OutcomePassed, author.ID, "")
			attempt.Route.Model = "author-m"
			if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
				t.Fatal(err)
			}
			f.step()
			got := f.mustCard("dev")
			review := reviewOf(t, f, "dev")
			if got.AuthorFamily != tc.wantFamily || review.AuthorFamily != tc.wantFamily || !strings.Contains(review.Body, tc.wantFamily) {
				t.Fatalf("dev %q review %+v", got.AuthorFamily, review)
			}
			req, err := routeRequestFrom(f.ctx, f.db, review, f.objective(), time.Now().UTC(), "", false)
			if err != nil {
				t.Fatal(err)
			}
			if req.AuthorFamily != tc.wantFamily {
				t.Fatalf("request family %q", req.AuthorFamily)
			}
			picked, reason, err := route.Select(f.ctx, req)
			if err != nil {
				t.Fatalf("select %v filtered %+v", err, reason.Filtered)
			}
			if picked.Channel != tc.allowChannel || picked.Model != tc.allowModel {
				t.Fatalf("picked %+v filtered %+v", picked, reason.Filtered)
			}
			if !filteredModel(reason, author.ID, tc.dropModel, route.ReasonFamily) {
				t.Fatalf("success family stayed selectable %+v", reason.Filtered)
			}
			if filteredModel(reason, other.ID, "rev-open", route.ReasonFamily) {
				t.Fatalf("stale family was excluded %+v", reason.Filtered)
			}
			if !candidateModel(reason, "rev-open") {
				t.Fatalf("stale family was not selectable %+v", reason.Candidates)
			}
		})
	}
}

func TestFollowupR6LaterModelPin(t *testing.T) {
	ma := "ma"
	mb := "mb"
	cases := []struct {
		name        string
		startModel  string
		presetObj   bool
		cardModel   *string
		cardChannel string
		objModel    *string
		wantA       bool
		wantChannel string
		wantModel   string
		wantErr     bool
	}{
		{name: "later card model keeps the only provider", cardModel: &ma, wantA: true, wantChannel: "ch-a", wantModel: "ma"},
		{name: "later objective model keeps the only provider", objModel: &ma, wantA: true, wantChannel: "ch-a", wantModel: "ma"},
		{name: "no later model keeps the exclusion", wantA: false, wantChannel: "ch-b", wantModel: "mb"},
		{name: "card model the excluded channel cannot serve stays excluded", cardModel: &mb, objModel: &ma, wantA: false, wantChannel: "ch-b", wantModel: "mb"},
		{name: "rewriting the recorded card model does not restore the channel", startModel: "ma", cardModel: &ma, wantA: false, wantErr: true},
		{name: "unchanged objective model stays excluded", presetObj: true, wantA: false, wantChannel: "ch-b", wantModel: "mb"},
		{name: "channel written after the switch is kept", cardChannel: "ch-a", wantA: true, wantChannel: "ch-a", wantModel: "ma"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			a := f.channel("ch-a", "fam-a", "ma")
			b := f.channel("ch-b", "fam-b", "mb")
			f.position("ma", "fam-a", model.TierT2)
			f.position("mb", "fam-b", model.TierT2)
			if tc.presetObj {
				obj := f.objective()
				obj.Channel = a.ID
				obj.Model = "ma"
				if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
					t.Fatal(err)
				}
			}
			card := f.card(model.Card{ID: "c", Channel: a.ID, Model: tc.startModel, Tier: model.TierT2, Status: model.CardHarvesting})
			f.attempt(card, 0, model.OutcomeNoOutput, a.ID, "")
			f.step()
			got := f.mustCard("c")
			if got.Channel != b.ID || got.Model != "" {
				t.Fatalf("switch %+v", got)
			}
			if tc.cardModel != nil {
				got.Model = *tc.cardModel
			}
			if tc.cardChannel != "" {
				got.Channel = tc.cardChannel
			}
			if err := f.db.UpdateCard(f.ctx, &got); err != nil {
				t.Fatal(err)
			}
			stored := f.objective()
			if tc.objModel != nil {
				stored.Model = *tc.objModel
				if err := f.db.UpdateObjective(f.ctx, &stored); err != nil {
					t.Fatal(err)
				}
			}
			req, err := routeRequestFrom(f.ctx, f.db, f.mustCard("c"), f.objective(), time.Now().UTC(), "", false)
			if err != nil {
				t.Fatal(err)
			}
			if hasRouteChannel(req, a.ID) != tc.wantA {
				t.Fatalf("channel A present %v req channels %s", hasRouteChannel(req, a.ID), channelList(req))
			}
			picked, reason, err := route.Select(f.ctx, req)
			if tc.wantErr {
				if err == nil || hasRouteChannel(req, a.ID) {
					t.Fatalf("restored pick %+v err %v channels %s", picked, err, channelList(req))
				}
				return
			}
			if err != nil || picked.Channel != tc.wantChannel || picked.Model != tc.wantModel {
				t.Fatalf("picked %+v err %v reason %+v", picked, err, reason)
			}
		})
	}
}

func TestFollowupR6AuthEpisode(t *testing.T) {
	f := newFx(t, model.ModeManual)
	var got []Notice
	f.ctx = WithNotifier(f.ctx, recNote{got: &got})
	a := f.channel("ch-a", "fam-a", "ma")
	b := f.channel("ch-b", "fam-b", "mb")
	f.position("ma", "fam-a", model.TierT2)
	f.position("mb", "fam-b", model.TierT2)
	card := f.card(model.Card{ID: "dev", Channel: a.ID, Tier: model.TierT2})
	first := f.attempt(card, 0, model.OutcomeAuthFailed, a.ID, "token rejected")
	f.step()
	if notices := authNotices(got); len(notices) != 1 || notices[0].QuestionID != "auth-"+a.ID || notices[0].Episode != first.ID {
		t.Fatalf("first delivery %+v", got)
	}
	f.step()
	if len(authNotices(got)) != 1 {
		t.Fatalf("same episode resent %+v", got)
	}
	if !harvestDisposed(f, first.ID) {
		t.Fatal("first attempt was not disposed")
	}

	second := f.attempt(f.mustCard("dev"), 1, model.OutcomeAuthFailed, a.ID, "token rejected again")
	f.ctx = WithNotifier(f.ctx, recNote{fail: fmt.Errorf("down")})
	if err := f.stepErr(); err == nil {
		t.Fatal("expected delivery failure")
	}
	if !harvestDisposed(f, second.ID) {
		t.Fatal("failed delivery rolled the disposition back")
	}
	if deliveredEpisode(f, "auth-"+a.ID, second.ID) {
		t.Fatal("failed delivery was marked delivered")
	}
	f.ctx = WithNotifier(context.Background(), recNote{got: &got})
	f.step()
	notices := authNotices(got)
	if len(notices) != 2 || notices[1].QuestionID != notices[0].QuestionID || notices[1].Episode != second.ID || notices[1].Episode == notices[0].Episode {
		t.Fatalf("second episode %+v", got)
	}
	if !strings.Contains(notices[1].Text, "token rejected again") {
		t.Fatalf("second text %q", notices[1].Text)
	}
	f.step()
	if len(authNotices(got)) != 2 || f.mustCard("dev").Channel != b.ID {
		t.Fatalf("duplicate %+v channel %s", got, f.mustCard("dev").Channel)
	}
}

func TestFollowupR6NoticeOrigin(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		config string
		want   string
	}{
		{name: "retry uses the owning objective origin", origin: "file:owner-a", want: "file:owner-a"},
		{name: "empty origin uses notify.fallback", config: `{"notify":{"fallback":"file:fallback"}}`, want: "file:fallback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, model.ModeManual)
			obj := f.objective()
			obj.Origin = ""
			if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
				t.Fatal(err)
			}
			f.ctx = WithNotifier(f.ctx, recNote{fail: fmt.Errorf("down")})
			a := f.channel("ch-a", "fam-a", "ma")
			f.channel("ch-b", "fam-b", "mb")
			f.position("ma", "fam-a", model.TierT2)
			f.position("mb", "fam-b", model.TierT2)
			card := f.card(model.Card{ID: "dev", Channel: a.ID, Tier: model.TierT2})
			attempt := f.attempt(card, 0, model.OutcomeAuthFailed, a.ID, "token rejected")
			if err := f.stepErr(); err == nil {
				t.Fatal("expected delivery failure")
			}
			if !harvestDisposed(f, attempt.ID) {
				t.Fatal("attempt was not disposed")
			}
			obj = f.objective()
			obj.Origin = tc.origin
			if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				f.writeConfig(tc.config)
			}
			other := f.otherObjective("obj-b")
			other.Origin = "file:owner-b"
			if err := f.db.UpdateObjective(f.ctx, &other); err != nil {
				t.Fatal(err)
			}
			var got []Notice
			f.ctx = WithNotifier(context.Background(), recNote{got: &got})
			f.t.Setenv("CARDEX_HOME", f.home)
			if err := DevStep(f.ctx, other.ID); err != nil {
				t.Fatal(err)
			}
			notices := authNotices(got)
			if len(notices) != 1 || notices[0].Origin != tc.want || notices[0].Objective != f.obj.ID || notices[0].Origin == "file:owner-b" {
				t.Fatalf("origin %+v", got)
			}
		})
	}
}

func TestFollowupR6NormalizeAbsorb(t *testing.T) {
	question := "## 开放问题\nshould the parser keep comments\n\n## 可冻结\n是\n"
	for _, mode := range []model.ObjectiveMode{model.ModeAuto, model.ModeManual} {
		for _, kind := range []model.CardKind{model.KindDesign, model.KindPlan, model.KindReplan} {
			t.Run(string(mode)+" "+string(kind)+" normalize absorbs questions and freeze", func(t *testing.T) {
				f := newFx(t, mode)
				plantNormalize(t, f, kind, model.CardRunning, question)
				f.step()
				assertAbsorbed(t, f, kind, true, true, false)
			})
		}
		t.Run(string(mode)+" dev normalize only backfills", func(t *testing.T) {
			f := newFx(t, mode)
			plantNormalize(t, f, model.KindDev, model.CardRunning, "normalized body\n")
			f.step()
			got := f.mustCard("src")
			if got.Status != model.CardRunning || !strings.Contains(got.Body, "normalized body") {
				t.Fatalf("dev %+v", got)
			}
			if f.mustCard("norm").Status != model.CardDone || len(f.events(evFreezable)) != 0 {
				t.Fatalf("norm %s freezes %d", f.mustCard("norm").Status, len(f.events(evFreezable)))
			}
		})
	}

	prose := "## 开放问题\nshould the parser keep comments\n"
	list := `[{"key":"child","kind":"dev","title":"Child work"}]`
	mix := "## 开放问题\nshould the parser keep comments\n\n## 新卡\n" + list + "\n"
	for _, kind := range []model.CardKind{model.KindPlan, model.KindReplan} {
		cases := []struct {
			name     string
			body     string
			question bool
			child    bool
		}{
			{name: "prose questions are not needs_input", body: prose, question: true},
			{name: "card list creates the child", body: list, child: true},
			{name: "mix stores questions and creates the child", body: mix, question: true, child: true},
		}
		for _, tc := range cases {
			t.Run(string(kind)+" "+tc.name, func(t *testing.T) {
				f := newFx(t, model.ModeAuto)
				plantNormalize(t, f, kind, model.CardRunning, tc.body)
				f.step()
				assertAbsorbed(t, f, kind, false, tc.question, tc.child)
				if got := f.mustCard("src").Status; got == model.CardNeedsInput {
					t.Fatalf("needs_input %+v", f.mustCard("src"))
				}
			})
		}
	}

	t.Run("Dispose auto design normalize absorbs", func(t *testing.T) {
		f := newFx(t, model.ModeAuto)
		parent := f.card(model.Card{ID: "src", Kind: model.KindDesign, Status: model.CardRunning})
		norm := f.card(model.Card{ID: "norm", Kind: model.KindNormalize, Tier: model.TierT1, Status: model.CardHarvesting, Deps: []string{parent.ID}})
		disposePassed(t, f, norm, question)
		assertAbsorbed(t, f, model.KindDesign, true, true, false)
	})
}

func TestFollowupR6UnclearFreeze(t *testing.T) {
	cases := []struct {
		name    string
		kind    model.CardKind
		body    string
		dispose bool
	}{
		{name: "plan 可冻结 is not a short answer", kind: model.KindPlan, body: "## 可冻结\n再议一下\n"},
		{name: "replan 可冻结 is not a short answer", kind: model.KindReplan, body: "## 可冻结\n再议一下\n"},
		{name: "plan 结论 names 冻结", kind: model.KindPlan, body: "## 结论\n等接口确认后才可以冻结\n"},
		{name: "replan 结论 names 冻结", kind: model.KindReplan, body: "## 结论\n等接口确认后才可以冻结\n"},
		{name: "Dispose replan 可冻结 is not a short answer", kind: model.KindReplan, body: "## 可冻结\n等接口确认后才可以冻结\n", dispose: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(t, model.ModeAuto)
			card := f.card(model.Card{ID: "src", Kind: tc.kind, Status: model.CardHarvesting})
			if tc.dispose {
				disposePassed(t, f, card, tc.body)
			} else {
				dir := writeResult(t, tc.body)
				attempt := f.attempt(card, 0, model.OutcomePassed, f.channel("ch-a", "a").ID, "")
				attempt.Worktree = dir
				if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
					t.Fatal(err)
				}
				f.step()
			}
			assertNormalizeWaiting(t, f)
			f.step()
			assertNormalizeWaiting(t, f)
		})
	}

	t.Run("short 可冻结 finishes the plan", func(t *testing.T) {
		f := newFx(t, model.ModeAuto)
		card := f.card(model.Card{ID: "src", Kind: model.KindPlan, Status: model.CardHarvesting})
		dir := writeResult(t, "## 可冻结\n是\n")
		attempt := f.attempt(card, 0, model.OutcomePassed, f.channel("ch-a", "a").ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("src")
		if got.Status != model.CardDone && got.Status != model.CardMergeQueued {
			t.Fatalf("plan %+v", got)
		}
		if kindCount(f.cards(), model.KindNormalize) != 0 || len(f.events(evFreezable)) != 1 {
			t.Fatalf("normalize %d freezes %d", kindCount(f.cards(), model.KindNormalize), len(f.events(evFreezable)))
		}
	})

	t.Run("结论 without 冻结 does not dispatch normalize", func(t *testing.T) {
		f := newFx(t, model.ModeAuto)
		card := f.card(model.Card{ID: "src", Kind: model.KindPlan, Status: model.CardHarvesting})
		dir := writeResult(t, "## 结论\n再议一下\n")
		attempt := f.attempt(card, 0, model.OutcomePassed, f.channel("ch-a", "a").ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("src")
		if got.Status != model.CardDone && got.Status != model.CardMergeQueued {
			t.Fatalf("plan %+v", got)
		}
		if kindCount(f.cards(), model.KindNormalize) != 0 || got.Status == model.CardNeedsInput {
			t.Fatalf("normalize %d card %+v", kindCount(f.cards(), model.KindNormalize), got)
		}
	})

	t.Run("unparsable plan still needs input", func(t *testing.T) {
		f := newFx(t, model.ModeAuto)
		card := f.card(model.Card{ID: "src", Kind: model.KindPlan, Status: model.CardHarvesting})
		dir := writeResult(t, "{not json")
		attempt := f.attempt(card, 0, model.OutcomePassed, f.channel("ch-a", "a").ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("src")
		if got.Status != model.CardNeedsInput || len(f.events("plan_unparsable")) != 1 || kindCount(f.cards(), model.KindNormalize) != 0 {
			t.Fatalf("card %+v events %d", got, len(f.events("plan_unparsable")))
		}
	})
}

func plantNormalize(t *testing.T, f *fx, kind model.CardKind, status model.CardStatus, body string) {
	t.Helper()
	ch := f.channel("ch-a", "a")
	dir := writeResult(t, body)
	parent := f.card(model.Card{ID: "src", Kind: kind, Status: status, Body: "old\n"})
	norm := f.card(model.Card{ID: "norm", Kind: model.KindNormalize, Tier: model.TierT1, Status: model.CardHarvesting, Deps: []string{parent.ID}})
	attempt := f.attempt(norm, 0, model.OutcomePassed, ch.ID, "")
	attempt.Worktree = dir
	if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
		t.Fatal(err)
	}
}

func disposePassed(t *testing.T, f *fx, card model.Card, body string) {
	t.Helper()
	dir := writeResult(t, body)
	exit := 0
	attempt := model.Attempt{
		ID: card.ID + "-d", Card: card.ID, Worktree: dir,
		Exit: &exit, EndedAt: time.Now().UTC(),
	}
	if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
	if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed, ResultText: body}); err != nil {
		t.Fatal(err)
	}
}

func assertAbsorbed(t *testing.T, f *fx, kind model.CardKind, freeze, question, child bool) {
	t.Helper()
	got := f.mustCard("src")
	if got.Status == model.CardRunning || got.Status == model.CardReady || got.Status == model.CardWaiting || got.Status == model.CardHarvesting {
		t.Fatalf("%s left open %+v", kind, got)
	}
	if got.Status != model.CardDone && got.Status != model.CardMergeQueued {
		t.Fatalf("%s %+v", kind, got)
	}
	node := mustNode(t, f)
	if openText(node, "should the parser keep comments") != question {
		t.Fatalf("questions %+v", node.OpenQuestions)
	}
	if freeze && len(f.events(evFreezable)) != 1 {
		t.Fatalf("freezes %d", len(f.events(evFreezable)))
	}
	if !freeze && len(f.events(evFreezable)) != 0 {
		t.Fatalf("unexpected freeze %d", len(f.events(evFreezable)))
	}
	if f.mustCard("norm").Status != model.CardDone {
		t.Fatalf("norm %s", f.mustCard("norm").Status)
	}
	if child {
		if cardByTitle(f, "Child work").Kind != model.KindDev {
			t.Fatalf("child %+v", f.cards())
		}
	}
}

func assertNormalizeWaiting(t *testing.T, f *fx) {
	t.Helper()
	got := f.mustCard("src")
	if got.Status == model.CardDone || got.Status == model.CardMergeQueued || got.Status == model.CardNeedsInput {
		t.Fatalf("source finished %+v", got)
	}
	n := 0
	for _, card := range f.cards() {
		if card.Kind != model.KindNormalize || !hasDepID(card, "src") {
			continue
		}
		n++
		if card.Title != "整理" || card.Status == model.CardDone {
			t.Fatalf("normalize %+v", card)
		}
	}
	if n != 1 {
		t.Fatalf("normalize count %d cards %+v", n, f.cards())
	}
}

func reviewOf(t *testing.T, f *fx, id string) model.Card {
	t.Helper()
	var found model.Card
	n := 0
	for _, card := range f.cards() {
		if card.Kind == model.KindReview && hasDepID(card, id) {
			found = card
			n++
		}
	}
	if n != 1 {
		t.Fatalf("reviews %d", n)
	}
	return found
}

func candidateModel(reason model.RouteReason, modelName string) bool {
	for _, item := range reason.Candidates {
		if item.Model == modelName {
			return true
		}
	}
	return false
}

func filteredModel(reason model.RouteReason, channel, modelName, why string) bool {
	for _, item := range reason.Filtered {
		if item.Channel == channel && item.Model == modelName && item.Reason == why {
			return true
		}
	}
	return false
}

func hasRouteChannel(req route.Request, id string) bool {
	for _, ch := range req.Channels {
		if ch.ID == id {
			return true
		}
	}
	return false
}

func channelList(req route.Request) string {
	ids := make([]string, 0, len(req.Channels))
	for _, ch := range req.Channels {
		ids = append(ids, ch.ID)
	}
	return strings.Join(ids, ",")
}

func authNotices(ns []Notice) []Notice {
	var out []Notice
	for _, n := range ns {
		if n.Kind == "auth_failed" {
			out = append(out, n)
		}
	}
	return out
}

func harvestDisposed(f *fx, id string) bool {
	f.t.Helper()
	for _, event := range f.events("dev_disposed") {
		if event.Entity == "attempt:"+id {
			return true
		}
	}
	return false
}

func deliveredEpisode(f *fx, id, episode string) bool {
	f.t.Helper()
	for _, event := range f.events("notice_delivered") {
		if event.Entity == "notice:"+id && eventEpisode(event) == episode {
			return true
		}
	}
	return false
}

func mustNode(t *testing.T, f *fx) model.DesignNode {
	t.Helper()
	node, err := f.db.GetNode(f.ctx, f.node.ID)
	if err != nil {
		t.Fatal(err)
	}
	return node
}
