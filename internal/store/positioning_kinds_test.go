package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestPositioningKindsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	row := model.Positioning{ID: "pos-astra-design", Version: 1, Model: "astra", Family: "openai",
		Tiers: []model.Tier{model.TierT3}, Preference: 1, Kinds: []model.CardKind{" Design ", model.KindPlan}}
	if err := db.CreatePositioning(ctx, &row); err != nil {
		t.Fatal(err)
	}
	all := model.Positioning{ID: "pos-all", Version: 1, Model: "grok", Family: "grok", Tiers: []model.Tier{model.TierT2}, Preference: 1}
	if err := db.CreatePositioning(ctx, &all); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetPositioning(ctx, "pos-astra-design")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Kinds) != 2 || got.Kinds[0] != model.KindDesign || got.Kinds[1] != model.KindPlan {
		t.Fatalf("kinds %v", got.Kinds)
	}
	if got, _ := db.GetPositioning(ctx, "pos-all"); got.Kinds != nil {
		t.Fatalf("all-kinds row kinds %v", got.Kinds)
	}
	bad := model.Positioning{ID: "pos-bad", Version: 1, Model: "x", Kinds: []model.CardKind{"designn"}}
	if err := db.CreatePositioning(ctx, &bad); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
