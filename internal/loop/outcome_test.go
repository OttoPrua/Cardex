package loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/quota"
	"cardex/internal/route"
	"cardex/internal/store"
)

type fx struct {
	t    *testing.T
	ctx  context.Context
	home string
	db   *store.Store
	obj  model.Objective
	node model.DesignNode
}

func newFx(t *testing.T, mode model.ObjectiveMode) *fx {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	project := &model.Project{ID: "p", RepoPath: home}
	if err := db.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	obj := &model.Objective{
		ID: "obj", Project: project.ID, Title: "objective",
		Mode: mode, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive,
		Origin: "file:origin", IntegrationBranch: "integration",
		Budget: model.Budget{SoftTokens: 1000, HardTokens: 5000},
	}
	if err := db.CreateObjective(ctx, obj); err != nil {
		t.Fatal(err)
	}
	node := &model.DesignNode{ID: "node", Objective: obj.ID, Layer: model.LayerDetail, Status: model.NodeFrozen}
	if err := db.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	return &fx{t: t, ctx: ctx, home: home, db: db, obj: *obj, node: *node}
}

func (f *fx) channel(id, family string, models ...string) model.Channel {
	f.t.Helper()
	if len(models) == 0 {
		models = []string{"fake"}
	}
	ch := &model.Channel{
		ID: id, Runner: model.RunnerFake, Family: family, Tier: model.TierT2,
		Health: model.HealthOK, Models: models, Concurrency: 1, SourceGroup: family,
		QuotaWindows: []model.QuotaWindow{{
			Name: "5h", ResetRule: "rolling", LimitTokens: 100,
			ResetAt: time.Now().Add(2 * time.Hour).UTC(), Remaining: 1,
		}},
	}
	if err := f.db.CreateChannel(f.ctx, ch); err != nil {
		f.t.Fatal(err)
	}
	return *ch
}

func (f *fx) nodeID(id string, status model.NodeStatus) model.DesignNode {
	f.t.Helper()
	node := &model.DesignNode{ID: id, Objective: f.obj.ID, Layer: model.LayerModule, Status: status}
	if err := f.db.CreateNode(f.ctx, node); err != nil {
		f.t.Fatal(err)
	}
	return *node
}

func (f *fx) card(card model.Card) model.Card {
	f.t.Helper()
	if card.Objective == "" {
		card.Objective = f.obj.ID
	}
	if card.Node == "" {
		card.Node = f.node.ID
	}
	if card.Kind == "" {
		card.Kind = model.KindDev
	}
	if card.Tier == "" || card.Tier == model.TierNone {
		card.Tier = model.DefaultTier(card.Kind)
	}
	if card.Status == "" {
		card.Status = model.CardHarvesting
	}
	if card.Title == "" {
		card.Title = card.ID
	}
	if card.Budget.SoftTokens == 0 && card.Budget.HardTokens == 0 {
		card.Budget = model.Budget{SoftTokens: 1, HardTokens: 1}
	}
	if err := f.db.CreateCard(f.ctx, &card); err != nil {
		f.t.Fatal(err)
	}
	return card
}

func (f *fx) attempt(card model.Card, index int, outcome model.Outcome, channel, excerpt string) model.Attempt {
	f.t.Helper()
	attempt := &model.Attempt{
		ID: card.ID + "-" + itoa(index), Card: card.ID, Index: index,
		Route:        model.Route{Channel: channel, Model: "fake"},
		Outcome:      outcome,
		ErrorClass:   model.ErrorClass(string(outcome)),
		ErrorExcerpt: excerpt,
		EndedAt:      time.Now().UTC(),
		Branch:       "cardex/obj/c/" + card.ID,
	}
	if err := f.db.CreateAttempt(f.ctx, attempt); err != nil {
		f.t.Fatal(err)
	}
	return *attempt
}

func (f *fx) step() {
	f.t.Helper()
	f.t.Setenv("CARDEX_HOME", f.home)
	if err := DevStep(f.ctx, f.obj.ID); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) cards() []model.Card {
	f.t.Helper()
	cards, err := f.db.ListCards(f.ctx, f.obj.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return cards
}

func (f *fx) questions() []model.Question {
	f.t.Helper()
	questions, err := f.db.ListQuestions(f.ctx, f.obj.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return questions
}

func (f *fx) events(typ string) []model.Event {
	f.t.Helper()
	events, err := f.db.ListEvents(f.ctx, "")
	if err != nil {
		f.t.Fatal(err)
	}
	if typ == "" {
		return events
	}
	var out []model.Event
	for _, event := range events {
		if event.Type == typ {
			out = append(out, event)
		}
	}
	return out
}

func (f *fx) mustCard(id string) model.Card {
	f.t.Helper()
	card, err := f.db.GetCard(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return card
}

func (f *fx) objective() model.Objective {
	f.t.Helper()
	obj, err := f.db.GetObjective(f.ctx, f.obj.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return obj
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func kindCount(cards []model.Card, kind model.CardKind) int {
	n := 0
	for _, card := range cards {
		if card.Kind == kind {
			n++
		}
	}
	return n
}

func hasDepID(card model.Card, id string) bool {
	for _, dep := range card.Deps {
		if dep == id {
			return true
		}
	}
	return false
}

func writeResult(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".cardex-result.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDevStepOutcomeRows(t *testing.T) {
	t.Run("passed dev creates one review and keeps the author family", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		author := f.channel("author", "anthropic", "author-m")
		other := f.channel("other", "openai", "other-m")
		_ = other
		card := f.card(model.Card{ID: "dev", Channel: author.ID})
		f.attempt(card, 0, model.OutcomePassed, author.ID, "")
		f.step()
		got := f.mustCard("dev")
		if got.Status != model.CardReviewing || got.AuthorFamily != "anthropic" {
			t.Fatalf("dev card %+v", got)
		}
		reviews := 0
		var review model.Card
		for _, card := range f.cards() {
			if card.Kind != model.KindReview {
				continue
			}
			reviews++
			review = card
		}
		if reviews != 1 || review.AuthorFamily != "anthropic" || !hasDepID(review, "dev") || review.Status != model.CardReady {
			t.Fatalf("review %+v count %d", review, reviews)
		}
		if !strings.Contains(review.Body, "anthropic") {
			t.Fatalf("review body %q", review.Body)
		}
		channels := []model.Channel{}
		listed, err := f.db.ListChannels(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		channels = listed
		led := quota.New()
		for _, ch := range channels {
			led.Configure(ch)
		}
		picked, reason, err := route.Select(f.ctx, route.Request{
			Card:      review,
			Objective: f.objective(),
			Channels:  channels,
			Positioning: []model.Positioning{
				{Model: "author-m", Family: "anthropic", Tiers: []model.Tier{model.TierT3}},
				{Model: "other-m", Family: "openai", Tiers: []model.Tier{model.TierT3}},
			},
			Quota: led,
			Now:   time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if picked.Channel != "other" {
			t.Fatalf("picked %+v filtered %+v", picked, reason.Filtered)
		}
		filtered := false
		for _, item := range reason.Filtered {
			if item.Channel == "author" && item.Reason == route.ReasonFamily {
				filtered = true
			}
		}
		if !filtered {
			t.Fatalf("author family was not excluded: %+v", reason.Filtered)
		}
		f.step()
		if kindCount(f.cards(), model.KindReview) != 1 {
			t.Fatal("second step created another review")
		}
	})

	t.Run("tests_failed reworks until the limit then asks", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		f.channel("ch-b", "b")
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		first := f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, "")
		f.step()
		got := f.mustCard("dev")
		if got.ReworkCount != 1 || got.Status != model.CardReady || got.Base != first.Branch || got.Channel != ch.ID {
			t.Fatalf("first rework %+v", got)
		}
		if len(f.questions()) != 0 {
			t.Fatal("question before the limit")
		}
		f.attempt(card, 1, model.OutcomeTestsFailed, ch.ID, "")
		f.step()
		got = f.mustCard("dev")
		if got.ReworkCount != 2 || got.Status != model.CardReady || got.Channel != ch.ID {
			t.Fatalf("second rework %+v", got)
		}
		f.attempt(card, 2, model.OutcomeTestsFailed, ch.ID, "")
		f.step()
		got = f.mustCard("dev")
		if got.ReworkCount != 2 || got.Status != model.CardNeedsInput || got.Channel != ch.ID {
			t.Fatalf("over limit %+v", got)
		}
		if len(f.questions()) != 1 || f.questions()[0].Answer != "" {
			t.Fatalf("questions %+v", f.questions())
		}
		f.step()
		if f.mustCard("dev").ReworkCount != 2 || len(f.questions()) != 1 {
			t.Fatal("second step reworked again")
		}
	})

	t.Run("no_output switches once then asks", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "a")
		b := f.channel("ch-b", "b")
		card := f.card(model.Card{ID: "dev", Channel: a.ID})
		f.attempt(card, 0, model.OutcomeNoOutput, a.ID, "")
		f.step()
		got := f.mustCard("dev")
		if got.Status != model.CardReady || got.Channel != b.ID {
			t.Fatalf("reschedule %+v", got)
		}
		if len(f.questions()) != 0 {
			t.Fatal("question on the first miss")
		}
		f.step()
		if f.mustCard("dev").Channel != b.ID || len(f.questions()) != 0 {
			t.Fatal("second step changed the first reschedule")
		}
		f.attempt(card, 1, model.OutcomeNoOutput, b.ID, "")
		f.step()
		got = f.mustCard("dev")
		if got.Channel != b.ID || got.Status != model.CardNeedsInput || len(f.questions()) != 1 {
			t.Fatalf("second miss %+v questions %d", got, len(f.questions()))
		}
		f.step()
		if f.mustCard("dev").Channel != b.ID || len(f.questions()) != 1 {
			t.Fatal("second step rescheduled again")
		}
	})

	t.Run("result_unparsable normalizes once and a failed normalize needs input", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		card := f.card(model.Card{ID: "dev", Channel: ch.ID})
		f.attempt(card, 0, model.OutcomeResultUnparsable, ch.ID, "")
		f.step()
		if kindCount(f.cards(), model.KindNormalize) != 1 {
			t.Fatalf("cards %+v", f.cards())
		}
		var normalize model.Card
		for _, item := range f.cards() {
			if item.Kind == model.KindNormalize {
				normalize = item
			}
		}
		got := f.mustCard("dev")
		if got.Status != model.CardWaiting || !strings.Contains(got.WaitReason, normalize.ID) || !hasDepID(normalize, "dev") {
			t.Fatalf("dev %+v normalize %+v", got, normalize)
		}
		f.step()
		if kindCount(f.cards(), model.KindNormalize) != 1 {
			t.Fatal("second step created another normalize card")
		}
		f.attempt(normalize, 0, model.OutcomeTestsFailed, ch.ID, "")
		f.step()
		if f.mustCard("dev").Status != model.CardNeedsInput || f.mustCard(normalize.ID).Status != model.CardFailed {
			t.Fatalf("dev %s normalize %s", f.mustCard("dev").Status, f.mustCard(normalize.ID).Status)
		}
	})

	t.Run("quota_hit blocks the channel and continues on the next one", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		reset := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
		a := f.channel("ch-a", "a")
		b := f.channel("ch-b", "b")
		card := f.card(model.Card{ID: "dev", Channel: a.ID, Base: "main"})
		attempt := f.attempt(card, 0, model.OutcomeQuotaHit, a.ID, reset.Format(time.RFC3339Nano))
		f.step()
		got := f.mustCard("dev")
		if got.Status != model.CardReady || got.Channel != b.ID || got.Base != attempt.Branch {
			t.Fatalf("card %+v", got)
		}
		blocked, err := f.db.GetChannel(f.ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if blocked.Health != model.HealthOK || len(blocked.QuotaWindows) == 0 || blocked.QuotaWindows[0].Remaining != 0 || !blocked.QuotaWindows[0].ResetAt.Equal(reset) {
			t.Fatalf("channel %+v", blocked)
		}
		led := quota.New()
		led.SetClock(func() time.Time { return time.Now() })
		led.Configure(blocked)
		if err := RestoreQuota(f.ctx, led, f.events("")); err != nil {
			t.Fatal(err)
		}
		est, err := led.Remaining(f.ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !est.Blocked || !est.Reset.Equal(reset) {
			t.Fatalf("estimate %+v", est)
		}
		open, err := f.db.GetChannel(f.ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		led.Configure(open)
		picked, reason, err := route.Select(f.ctx, route.Request{
			Card:      model.Card{Kind: model.KindDev, Tier: model.TierT2},
			Objective: f.objective(),
			Channels:  []model.Channel{blocked, open},
			Positioning: []model.Positioning{
				{Model: "fake", Family: "a", Tiers: []model.Tier{model.TierT2}},
			},
			Quota: led,
			Now:   time.Now(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if picked.Channel != b.ID {
			t.Fatalf("picked blocked channel %+v filtered %+v", picked, reason.Filtered)
		}
		f.step()
		again, err := f.db.GetChannel(f.ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if again.QuotaWindows[0].Remaining != 0 || f.mustCard("dev").Channel != b.ID || len(f.events("quota_block")) != 1 {
			t.Fatal("second step changed the quota block")
		}
	})

	t.Run("a blocked channel is not selected again before reset", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		reset := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
		a := f.channel("ch-a", "a")
		b := f.channel("ch-b", "b")
		card := f.card(model.Card{ID: "dev", Channel: a.ID})
		f.attempt(card, 0, model.OutcomeQuotaHit, a.ID, reset.Format(time.RFC3339Nano))
		f.step()
		if f.mustCard("dev").Channel != b.ID {
			t.Fatalf("first move %+v", f.mustCard("dev"))
		}
		f.attempt(f.mustCard("dev"), 1, model.OutcomeNoOutput, b.ID, "")
		f.step()
		got := f.mustCard("dev")
		questions := f.questions()
		if got.Channel != b.ID || got.Status != model.CardNeedsInput || len(questions) != 1 || !strings.Contains(questions[0].Text, "no other channel") {
			t.Fatalf("card %+v questions %+v", got, questions)
		}
		blocked, err := f.db.GetChannel(f.ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if blocked.Health != model.HealthOK || !blocked.QuotaWindows[0].ResetAt.After(time.Now()) {
			t.Fatalf("block expired %+v", blocked)
		}
		f.step()
		if f.mustCard("dev").Channel != b.ID || len(f.questions()) != 1 {
			t.Fatal("second step returned to the blocked channel")
		}

		f2 := newFx(t, model.ModeManual)
		reset = time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
		a2 := f2.channel("ch-a", "a")
		b2 := f2.channel("ch-b", "b")
		c2 := f2.channel("ch-c", "c")
		card2 := f2.card(model.Card{ID: "dev", Channel: a2.ID})
		f2.attempt(card2, 0, model.OutcomeQuotaHit, a2.ID, reset.Format(time.RFC3339Nano))
		f2.step()
		if f2.mustCard("dev").Channel != b2.ID {
			t.Fatalf("first move %+v", f2.mustCard("dev"))
		}
		f2.attempt(f2.mustCard("dev"), 1, model.OutcomeNoOutput, b2.ID, "")
		f2.step()
		got = f2.mustCard("dev")
		if got.Channel != c2.ID || got.Status != model.CardReady || len(f2.questions()) != 0 {
			t.Fatalf("picked %+v questions %+v", got, f2.questions())
		}
		f2.step()
		if f2.mustCard("dev").Channel != c2.ID {
			t.Fatal("second step left the unblocked channel")
		}
	})

	t.Run("auth_failed notifies once and switches channel", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		dir := t.TempDir()
		f.ctx = WithNotifier(f.ctx, FileNotifier{Dir: dir})
		a := f.channel("ch-a", "a")
		b := f.channel("ch-b", "b")
		card := f.card(model.Card{ID: "dev", Channel: a.ID})
		f.attempt(card, 0, model.OutcomeAuthFailed, a.ID, "token rejected")
		f.step()
		health, err := f.db.GetChannel(f.ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if health.Health != model.HealthAuthFailed {
			t.Fatalf("health %s", health.Health)
		}
		if f.mustCard("dev").Channel != b.ID || f.mustCard("dev").Status != model.CardReady {
			t.Fatalf("card %+v", f.mustCard("dev"))
		}
		if len(f.events("notify")) != 1 {
			t.Fatalf("notifications %d", len(f.events("notify")))
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 {
			t.Fatalf("files %+v", files)
		}
		body, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), f.obj.Origin) || !strings.Contains(string(body), "auth_failed") {
			t.Fatalf("notice %s", body)
		}
		f.step()
		files, err = os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		again, err := f.db.GetChannel(f.ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 || again.Health != model.HealthAuthFailed || len(f.events("notify")) != 1 {
			t.Fatal("second step notified again")
		}
	})

	t.Run("transport and timeout retry twice then switch", func(t *testing.T) {
		for _, outcome := range []model.Outcome{model.OutcomeTransport, model.OutcomeTimeout} {
			t.Run(string(outcome), func(t *testing.T) {
				f := newFx(t, model.ModeManual)
				a := f.channel("ch-a", "a")
				b := f.channel("ch-b", "b")
				card := f.card(model.Card{ID: "dev", Channel: a.ID})
				for i := 0; i < 2; i++ {
					f.attempt(card, i, outcome, a.ID, "")
					f.step()
					got := f.mustCard("dev")
					if got.Channel != a.ID || got.Status != model.CardReady {
						t.Fatalf("retry %d %+v", i, got)
					}
				}
				f.attempt(card, 2, outcome, a.ID, "")
				f.step()
				if f.mustCard("dev").Channel != b.ID || f.mustCard("dev").Status != model.CardReady {
					t.Fatalf("switch %+v", f.mustCard("dev"))
				}
				f.step()
				if f.mustCard("dev").Channel != b.ID {
					t.Fatal("second step switched again")
				}
			})
		}
	})

	t.Run("refusal asks without switching", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "a")
		f.channel("ch-b", "b")
		card := f.card(model.Card{ID: "dev", Channel: a.ID})
		f.attempt(card, 0, model.OutcomeRefusal, a.ID, "")
		f.step()
		got := f.mustCard("dev")
		questions := f.questions()
		if got.Channel != a.ID || got.Status != model.CardNeedsInput || len(questions) != 1 || questions[0].Kind != model.QuestionRefusal || questions[0].Answer != "" {
			t.Fatalf("card %+v questions %+v", got, questions)
		}
		f.step()
		if f.mustCard("dev").Channel != a.ID || len(f.questions()) != 1 {
			t.Fatal("second step switched or asked again")
		}
	})

	t.Run("permission_denied names the operation", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "a")
		card := f.card(model.Card{ID: "dev", Channel: a.ID})
		f.attempt(card, 0, model.OutcomePermissionDenied, a.ID, "unlink /tmp/x")
		f.step()
		questions := f.questions()
		if len(questions) != 1 || !strings.Contains(questions[0].Text, "unlink /tmp/x") || f.mustCard("dev").Channel != a.ID {
			t.Fatalf("questions %+v card %+v", questions, f.mustCard("dev"))
		}
		f.step()
		if len(f.questions()) != 1 {
			t.Fatal("second step asked again")
		}
	})

	t.Run("budget_limited continues once while the objective has room", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "a")
		card := f.card(model.Card{ID: "dev", Channel: a.ID})
		attempt := f.attempt(card, 0, model.OutcomeBudgetLimited, a.ID, "")
		stored := attempt
		stored.Usage.Tokens = 10
		if err := f.db.UpdateAttempt(f.ctx, &stored); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("dev")
		if got.Status != model.CardReady || got.Channel != a.ID || got.Base != attempt.Branch || kindCount(f.cards(), model.KindReview) != 0 || len(f.questions()) != 0 {
			t.Fatalf("card %+v questions %d", got, len(f.questions()))
		}
		if f.objective().Status != model.ObjectiveActive {
			t.Fatal("objective paused while under the hard line")
		}
		f.step()
		if f.mustCard("dev").Status != model.CardReady || len(f.questions()) != 0 {
			t.Fatal("second step changed the continuation")
		}
	})

	t.Run("budget_limited asks when the objective hard line is reached", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		obj := f.objective()
		obj.Budget = model.Budget{SoftTokens: 40, HardTokens: 50}
		if err := f.db.UpdateObjective(f.ctx, &obj); err != nil {
			t.Fatal(err)
		}
		a := f.channel("ch-a", "a")
		card := f.card(model.Card{ID: "dev", Channel: a.ID})
		attempt := f.attempt(card, 0, model.OutcomeBudgetLimited, a.ID, "")
		attempt.Usage.Tokens = 50
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("dev")
		found := false
		for _, question := range f.questions() {
			if question.Scope == "dev" && question.Kind == model.QuestionBudget && strings.Contains(question.Text, "no remaining capacity") && question.Answer == "" {
				found = true
			}
		}
		if got.Status != model.CardNeedsInput || kindCount(f.cards(), model.KindReview) != 0 || !found {
			t.Fatalf("card %+v questions %+v", got, f.questions())
		}
		f.step()
		n := 0
		for _, question := range f.questions() {
			if question.Scope == "dev" {
				n++
			}
		}
		if n != 1 {
			t.Fatal("second step asked again")
		}
	})

	t.Run("violation voids the attempt and keeps the branch", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		a := f.channel("ch-a", "a")
		f.channel("ch-b", "b")
		card := f.card(model.Card{ID: "dev", Channel: a.ID})
		dir := t.TempDir()
		attempt := f.attempt(card, 0, model.OutcomeViolation, a.ID, "touched forbidden path")
		attempt.Worktree = dir
		attempt.Branch = "cardex/obj/c/dev"
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("dev")
		stored, err := f.db.GetAttempt(f.ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.CardNeedsInput || got.Channel != a.ID || kindCount(f.cards(), model.KindReview) != 0 {
			t.Fatalf("card %+v", got)
		}
		if stored.Branch != attempt.Branch || stored.Outcome != model.OutcomeViolation {
			t.Fatalf("attempt %+v", stored)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatal(err)
		}
		if len(f.questions()) != 1 || len(f.events("attempt_voided")) != 1 {
			t.Fatalf("questions %+v voided %d", f.questions(), len(f.events("attempt_voided")))
		}
		f.step()
		if len(f.questions()) != 1 || len(f.events("attempt_voided")) != 1 {
			t.Fatal("second step voided again")
		}
	})
}

func TestDevStepAbsorption(t *testing.T) {
	t.Run("plan list defaults, invalid dep, one broken cycle edge, and an out of scope question", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		f.nodeID("other", model.NodeFrozen)
		ch := f.channel("ch-a", "a")
		dir := writeResult(t, "```json\n[\n"+
			`{"key":"bare"},`+"\n"+
			`{"key":"a","kind":"dev","title":"A","deps":["ghost","b",]},`+"\n"+
			`{"key":"b","kind":"dev","title":"B","deps":["a"]},`+"\n"+
			`{"key":"keep","kind":"dev","title":"Keep","deps":["a"]},`+"\n"+
			`{"key":"out","kind":"dev","title":"Outside","node":"other"}`+"\n"+
			"]\n```\n")
		card := f.card(model.Card{ID: "plan", Kind: model.KindPlan, Tier: model.TierT3, Channel: ch.ID})
		attempt := f.attempt(card, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		bare := cardByTitle(f, "bare")
		if bare.Kind != model.KindDev || bare.Title != "bare" || bare.Tier != model.TierT2 || bare.Body != "" || len(bare.Deps) != 0 || bare.Status != model.CardReady || bare.Node != f.node.ID || bare.ID == "bare" {
			t.Fatalf("defaults %+v", bare)
		}
		a := cardByTitle(f, "A")
		b := cardByTitle(f, "B")
		keep := cardByTitle(f, "Keep")
		edges := 0
		if hasDepID(a, b.ID) {
			edges++
		}
		if hasDepID(b, a.ID) {
			edges++
		}
		if edges != 1 || hasDepID(a, "ghost") || !hasDepID(keep, a.ID) {
			t.Fatalf("deps a=%v b=%v keep=%v", a.Deps, b.Deps, keep.Deps)
		}
		if _, err := f.db.GetCard(f.ctx, "out"); err == nil {
			t.Fatal("out of scope card was created")
		}
		foundDep := false
		for _, event := range f.events("invalid_dep") {
			if strings.Contains(string(event.Data), "ghost") {
				foundDep = true
			}
		}
		if !foundDep {
			t.Fatalf("events %+v", f.events("invalid_dep"))
		}
		node, err := f.db.GetNode(f.ctx, f.node.ID)
		if err != nil {
			t.Fatal(err)
		}
		other, err := f.db.GetNode(f.ctx, "other")
		if err != nil {
			t.Fatal(err)
		}
		if !openText(node, "依赖成环") || !openText(other, "设计问题") || !openText(other, "Outside") {
			t.Fatalf("node questions %+v other %+v", node.OpenQuestions, other.OpenQuestions)
		}
		before := len(f.cards())
		f.step()
		if len(f.cards()) != before || len(f.events("invalid_dep")) != 1 {
			t.Fatal("second step absorbed the plan again")
		}
	})

	t.Run("unparsable plan json records an event and creates no card", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		dir := writeResult(t, "{not json")
		card := f.card(model.Card{ID: "plan", Kind: model.KindPlan, Tier: model.TierT3, Channel: ch.ID})
		attempt := f.attempt(card, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		if len(f.cards()) != 1 || f.mustCard("plan").Status != model.CardNeedsInput || len(f.events("plan_unparsable")) != 1 {
			t.Fatalf("cards %+v", f.cards())
		}
	})

	t.Run("review pass queues the card and rework stores the opinion until the limit", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		dev := f.card(model.Card{ID: "dev", Status: model.CardReviewing, Channel: ch.ID})
		f.attempt(dev, 0, model.OutcomePassed, ch.ID, "")
		dir := writeResult(t, "## 复核结论\npass\n")
		review := f.card(model.Card{ID: "rev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardHarvesting, Deps: []string{"dev"}})
		attempt := f.attempt(review, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		if f.mustCard("dev").Status != model.CardMergeQueued || f.mustCard("rev").Status != model.CardDone {
			t.Fatalf("dev %s review %s", f.mustCard("dev").Status, f.mustCard("rev").Status)
		}

		f2 := newFx(t, model.ModeManual)
		ch2 := f2.channel("ch-a", "a")
		dev2 := f2.card(model.Card{ID: "dev", Status: model.CardReviewing, Channel: ch2.ID})
		worked := f2.attempt(dev2, 0, "", ch2.ID, "")
		dir2 := writeResult(t, "## 复核结论\nrework\nfix the boundary check\n")
		review2 := f2.card(model.Card{ID: "rev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardHarvesting, Deps: []string{"dev"}})
		attempt2 := f2.attempt(review2, 0, model.OutcomePassed, ch2.ID, "")
		attempt2.Worktree = dir2
		if err := f2.db.UpdateAttempt(f2.ctx, &attempt2); err != nil {
			t.Fatal(err)
		}
		f2.step()
		got := f2.mustCard("dev")
		if got.Status != model.CardReady || got.ReworkCount != 1 || got.ReworkNote != "fix the boundary check" || got.Base != worked.Branch {
			t.Fatalf("rework %+v", got)
		}
		if len(f2.questions()) != 0 {
			t.Fatalf("unexpected questions %+v", f2.questions())
		}

		f3 := newFx(t, model.ModeManual)
		ch3 := f3.channel("ch-a", "a")
		f3.card(model.Card{ID: "dev", Status: model.CardReviewing, Channel: ch3.ID, ReworkCount: 2})
		dir3 := writeResult(t, "## 复核结论\nrework\nstill broken\n")
		review3 := f3.card(model.Card{ID: "rev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardHarvesting, Deps: []string{"dev"}})
		attempt3 := f3.attempt(review3, 0, model.OutcomePassed, ch3.ID, "")
		attempt3.Worktree = dir3
		if err := f3.db.UpdateAttempt(f3.ctx, &attempt3); err != nil {
			t.Fatal(err)
		}
		f3.step()
		got = f3.mustCard("dev")
		if got.ReworkCount != 2 || got.Status != model.CardNeedsInput || len(f3.questions()) != 1 {
			t.Fatalf("limit %+v questions %+v", got, f3.questions())
		}
		f3.step()
		if f3.mustCard("dev").ReworkCount != 2 || len(f3.questions()) != 1 {
			t.Fatal("second step reworked past the limit")
		}
	})

	t.Run("a passed attempt after review rework creates a new review", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "author")
		dev := f.card(model.Card{ID: "dev", Status: model.CardReviewing, Channel: ch.ID, AuthorFamily: "author"})
		f.attempt(dev, 0, "", ch.ID, "")
		dir := writeResult(t, "## 复核结论\nrework\nfix the boundary check\n")
		review := f.card(model.Card{ID: "rev", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardHarvesting, Deps: []string{"dev"}, AuthorFamily: "author"})
		attempt := f.attempt(review, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		if f.mustCard("dev").Status != model.CardReady || f.mustCard("rev").Status != model.CardDone {
			t.Fatalf("rework dev %s review %s", f.mustCard("dev").Status, f.mustCard("rev").Status)
		}
		f.attempt(f.mustCard("dev"), 1, model.OutcomePassed, ch.ID, "")
		f.step()
		got := f.mustCard("dev")
		reviews := cardsKind(f, model.KindReview)
		readyN := 0
		var fresh model.Card
		for _, item := range reviews {
			if item.Status == model.CardReady {
				readyN++
				fresh = item
			}
		}
		if got.Status != model.CardReviewing || len(reviews) != 2 || readyN != 1 || !hasDepID(fresh, "dev") || fresh.AuthorFamily != "author" || f.mustCard("rev").Status != model.CardDone {
			t.Fatalf("dev %+v reviews %+v", got, reviews)
		}
		f.step()
		if f.mustCard("dev").Status != model.CardReviewing || len(cardsKind(f, model.KindReview)) != 2 {
			t.Fatal("second step created another review")
		}
	})

	t.Run("passed normalize writes the result onto the original card", func(t *testing.T) {
		f := newFx(t, model.ModeManual)
		ch := f.channel("ch-a", "a")
		originDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(originDir, ".cardex-result.md"), []byte("garbage"), 0o644); err != nil {
			t.Fatal(err)
		}
		dev := f.card(model.Card{ID: "dev", Status: model.CardWaiting, WaitReason: "normalize:norm", Body: "implement the parser"})
		prior := f.attempt(dev, 0, model.OutcomeResultUnparsable, ch.ID, "")
		prior.Worktree = originDir
		if err := f.db.UpdateAttempt(f.ctx, &prior); err != nil {
			t.Fatal(err)
		}
		dir := writeResult(t, "EXTRACTED-RESULT")
		norm := f.card(model.Card{ID: "norm", Kind: model.KindNormalize, Tier: model.TierT1, Status: model.CardHarvesting, Deps: []string{"dev"}})
		attempt := f.attempt(norm, 0, model.OutcomePassed, ch.ID, "")
		attempt.Worktree = dir
		if err := f.db.UpdateAttempt(f.ctx, &attempt); err != nil {
			t.Fatal(err)
		}
		f.step()
		got := f.mustCard("dev")
		if !strings.Contains(got.Body, "EXTRACTED-RESULT") || !strings.Contains(got.Body, "implement the parser") {
			t.Fatalf("body %q", got.Body)
		}
		file, err := os.ReadFile(filepath.Join(originDir, ".cardex-result.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(file) != "EXTRACTED-RESULT" {
			t.Fatalf("result file %q", file)
		}
		if len(f.events("normalize_backfill")) != 1 || !strings.Contains(string(f.events("normalize_backfill")[0].Data), "EXTRACTED-RESULT") {
			t.Fatalf("events %+v", f.events("normalize_backfill"))
		}
		f.step()
		if strings.Count(f.mustCard("dev").Body, "EXTRACTED-RESULT") != 1 {
			t.Fatal("second step wrote the result again")
		}
	})
}

func cardByTitle(f *fx, title string) model.Card {
	f.t.Helper()
	var found model.Card
	n := 0
	for _, card := range f.cards() {
		if card.Title == title {
			found = card
			n++
		}
	}
	if n != 1 {
		f.t.Fatalf("title %q matched %d cards", title, n)
	}
	return found
}

func openText(node model.DesignNode, text string) bool {
	for _, question := range node.OpenQuestions {
		if strings.Contains(question.Text, text) {
			return true
		}
	}
	return false
}
