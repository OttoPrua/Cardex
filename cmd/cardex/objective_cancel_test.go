package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestObjectiveCancelCascadeIsAtomicAuditedAndScoped(t *testing.T) {
	ctx, home := t.Context(), t.TempDir()
	db := openBriefDB(t, home)
	defer db.Close()
	for _, id := range []string{"cancel-test", "outside"} {
		o := model.Objective{ID: id, Mode: model.ModeManual, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive}
		if err := db.CreateObjective(ctx, &o); err != nil {
			t.Fatal(err)
		}
	}
	for _, example := range []model.Card{
		{ID: "a-ready", Objective: "cancel-test", Status: model.CardReady},
		{ID: "b-paused", Objective: "cancel-test", Status: model.CardPaused},
		{ID: "c-done", Objective: "cancel-test", Status: model.CardDone},
		{ID: "d-outside", Objective: "outside", Status: model.CardReady},
	} {
		example.Kind = model.KindDev
		if err := db.CreateCard(ctx, &example); err != nil {
			t.Fatal(err)
		}
	}
	attempt := model.Attempt{ID: "active-attempt", Card: "b-paused"}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	call := func() (int, string, string) {
		t.Helper()
		return runMain(t, "--home", home, "objective", "cancel", "cancel-test", "--cascade", "--json")
	}
	if code, out, errText := call(); code == 0 || out != "" || !strings.Contains(errText, "unfinished attempt") {
		t.Fatalf("active cancellation: code=%d out=%q err=%q", code, out, errText)
	}
	check := func(id string, want model.CardStatus) {
		t.Helper()
		c, err := db.GetCard(ctx, id)
		if err != nil || c.Status != want {
			t.Fatalf("%s status=%s error=%v want %s", id, c.Status, err, want)
		}
	}
	check("a-ready", model.CardReady)
	o, err := db.GetObjective(ctx, "cancel-test")
	if err != nil || o.Status != model.ObjectiveActive {
		t.Fatalf("partial objective change: %+v %v", o, err)
	}
	if e, err := db.ListEvents(ctx, "objective:cancel-test"); err != nil || len(e) != 0 {
		t.Fatalf("partial audit: %+v %v", e, err)
	}
	attempt.EndedAt = time.Now()
	if err := db.UpdateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		code, out, errText := call()
		var receipt objectiveCancelReceipt
		if code != 0 || errText != "" || json.Unmarshal([]byte(out), &receipt) != nil ||
			receipt.Status != model.ObjectiveCanceled || len(receipt.Cards) != (1-i)*2 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errText)
		}
	}
	check("a-ready", model.CardCanceled)
	check("b-paused", model.CardCanceled)
	check("c-done", model.CardDone)
	check("d-outside", model.CardReady)
	for _, entity := range []string{"objective:cancel-test", "card:a-ready", "card:b-paused"} {
		e, err := db.ListEvents(ctx, entity)
		if err != nil || len(e) != 1 || !strings.HasPrefix(e[0].Type, "objective_cascade_canceled") {
			t.Fatalf("%s: %#v %v", entity, e, err)
		}
	}
}
