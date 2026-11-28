package sched

import (
	"context"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestDefaultRankingGate(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	t.Setenv("CARDEX_HOME", home)
	_, obj := seedRepo(t, db, repo, "true")
	r := New(home)
	r.Launch = func(context.Context, string, string) (int, error) { return 7, nil }

	notices := func() []model.Question {
		qs, err := db.ListQuestions(ctx, obj)
		if err != nil {
			t.Fatal(err)
		}
		var out []model.Question
		for _, q := range qs {
			if q.Scope == scheduleNoticeScope {
				out = append(out, q)
			}
		}
		return out
	}

	// Nothing approved yet: no card and no notice.
	if err := r.applyDefaultSchedule(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got := rankingCards(t, db, obj); len(got) != 0 {
		t.Fatalf("cards before approval %+v", got)
	}
	if got := notices(); len(got) != 0 {
		t.Fatalf("notice before approval %+v", got)
	}

	// Approved table without web: skipped, one notice to the objective.
	ch := model.Channel{ID: "ch", Runner: model.RunnerFake, Family: "alpha", Models: []string{"m"}, Concurrency: 1, Health: model.HealthOK}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	pos := model.Positioning{ID: "pos", Model: "m", Family: "alpha", Tiers: []model.Tier{model.TierT3, model.TierT2, model.TierT1}, Preference: 1}
	if err := db.CreatePositioning(ctx, &pos); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := r.applyDefaultSchedule(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	if got := rankingCards(t, db, obj); len(got) != 0 {
		t.Fatalf("cards without web %+v", got)
	}
	got := notices()
	if len(got) != 1 || !strings.Contains(got[0].Text, "web") || got[0].Kind != model.QuestionCustom {
		t.Fatalf("notices %+v", got)
	}

	// Once a row has web the card is created.
	pos.Capabilities = []string{"web"}
	if err := db.UpdatePositioning(ctx, &pos); err != nil {
		t.Fatal(err)
	}
	if err := r.applyDefaultSchedule(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got := rankingCards(t, db, obj); len(got) != 1 {
		t.Fatalf("cards with web %+v", got)
	}
}
