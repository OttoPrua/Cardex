package sched

import (
	"context"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
)

func TestNotifySkipsQuestionsOfFinishedCards(t *testing.T) {
	ctx := context.Background()
	home, db, _ := newHome(t)
	if err := db.CreateProject(ctx, &model.Project{ID: "p"}); err != nil {
		t.Fatal(err)
	}
	obj := model.Objective{ID: "o", Project: "p", Title: "o", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: "main"}
	if err := db.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	for _, c := range []model.Card{
		{ID: "done", Objective: "o", Kind: model.KindDev, Title: "d", Status: model.CardDone},
		{ID: "gone", Objective: "o", Kind: model.KindDev, Title: "g", Status: model.CardCanceled},
		{ID: "decide", Objective: "o", Kind: model.KindDev, Title: "x", Status: model.CardDone},
		{ID: "open", Objective: "o", Kind: model.KindDev, Title: "o", Status: model.CardNeedsInput},
	} {
		c := c
		if err := db.CreateCard(ctx, &c); err != nil {
			t.Fatal(err)
		}
		q := model.Question{ID: "q-" + c.ID, Objective: "o", Scope: c.ID, ScopeKind: "card", Kind: model.QuestionCustom, Text: "no output after switching channel\n- attempt a1"}
		if c.ID == "decide" {
			q.Text = "decide this"
		}
		if err := db.CreateQuestion(ctx, &q); err != nil {
			t.Fatal(err)
		}
	}
	var sent []string
	r := New(home)
	r.Notify = func(_ context.Context, msg notify.Message) error {
		sent = append(sent, msg.QuestionID)
		return nil
	}
	if err := r.notify(ctx, db); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[0] != "q-decide" || sent[1] != "q-open" {
		t.Fatalf("sent %v", sent)
	}
}
