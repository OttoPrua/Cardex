package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestHoldCompatibleAttemptNeverTouchesNewerCard(t *testing.T) {
	ctx := t.Context()
	db, err := Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	obj := model.Objective{ID: "objective", Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe}
	if err := db.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	obj, err = db.GetObjective(ctx, obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"newer-attempt", "changed-card", "historical-ended"} {
		card := model.Card{ID: mode, Objective: obj.ID, Kind: model.KindResearch, Status: model.CardRunning}
		if err := db.CreateCard(ctx, &card); err != nil {
			t.Fatal(err)
		}
		a := model.Attempt{ID: "old-" + mode, Card: card.ID, Index: 0, RouteReason: model.RouteReason{Dispatch: &model.RouteDispatch{CompatibleBindingID: "synthetic"}}}
		if mode == "historical-ended" {
			a.EndedAt = time.Now()
		}
		if err := db.CreateAttempt(ctx, &a); err != nil {
			t.Fatal(err)
		}
		a, err = db.GetAttempt(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		before, err := db.GetCard(ctx, card.ID)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "newer-attempt" {
			newer := model.Attempt{ID: "newer-" + mode, Card: card.ID, Index: 1}
			if err := db.CreateAttempt(ctx, &newer); err != nil {
				t.Fatal(err)
			}
		} else if mode == "changed-card" || mode == "historical-ended" {
			edit := before
			if mode == "changed-card" {
				edit.Status = model.CardPaused
			}
			edit.Title = "latest title"
			if err := db.UpdateCard(ctx, &edit); err != nil {
				t.Fatal(err)
			}
		}
		want, err := db.GetCard(ctx, card.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.HoldCompatibleAttempt(ctx, before, a, &obj, time.Now()); err != nil {
			t.Fatal(err)
		}
		got, err := db.GetCard(ctx, card.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("newer card changed: got=%+v want=%+v error=%v", got, want, err)
		}
		settled, err := db.GetAttempt(ctx, a.ID)
		if err != nil || settled.Outcome != model.OutcomeNoOutput || settled.ErrorExcerpt != model.CompatibleRuntimeHold {
			t.Fatalf("old attempt did not retain hold evidence: %+v %v", settled, err)
		}
	}
}
