package store

import (
	"context"
	"strings"
	"testing"

	"cardex/internal/model"
)

func TestPositioningWriteSoftensNonCanonicalTiers(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	cases := []struct {
		name    string
		update  bool
		id      string
		tiers   []model.Tier
		want    []model.Tier
		nilPos  bool
		emptyID bool
		wantErr bool
	}{
		{name: "create t2", id: "c-t2", tiers: []model.Tier{"t2"}, want: []model.Tier{model.TierT2}},
		{name: "create spaced T3", id: "c-t3", tiers: []model.Tier{" T3 "}, want: []model.Tier{model.TierT3}},
		{name: "create unknown", id: "c-unk", tiers: []model.Tier{"not-a-tier"}, want: []model.Tier{}},
		{name: "create blank dropped", id: "c-blank", tiers: []model.Tier{"", "  "}, want: []model.Tier{}},
		{name: "create mixed", id: "c-mix", tiers: []model.Tier{"t2", "not-a-tier", " T3 "}, want: []model.Tier{model.TierT2, model.TierT3}},
		{name: "create canonical", id: "c-can", tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, want: []model.Tier{model.TierT1, model.TierT2, model.TierT3}},
		{name: "create nil", nilPos: true, wantErr: true},
		{name: "update t2", update: true, id: "u-t2", tiers: []model.Tier{"t2"}, want: []model.Tier{model.TierT2}},
		{name: "update spaced T3", update: true, id: "u-t3", tiers: []model.Tier{" T3 "}, want: []model.Tier{model.TierT3}},
		{name: "update unknown", update: true, id: "u-unk", tiers: []model.Tier{"not-a-tier"}, want: []model.Tier{}},
		{name: "update blank dropped", update: true, id: "u-blank", tiers: []model.Tier{"", "  "}, want: []model.Tier{}},
		{name: "update mixed", update: true, id: "u-mix", tiers: []model.Tier{"t2", "not-a-tier", " T3 "}, want: []model.Tier{model.TierT2, model.TierT3}},
		{name: "update canonical", update: true, id: "u-can", tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, want: []model.Tier{model.TierT1, model.TierT2, model.TierT3}},
		{name: "update empty id", update: true, emptyID: true, tiers: []model.Tier{"t2"}, wantErr: true},
		{name: "update nil", update: true, nilPos: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.update && !tc.nilPos && !tc.emptyID {
				seed := &model.Positioning{
					ID: tc.id, Model: "seed", Family: "fam",
					Tiers: []model.Tier{model.TierT1}, Capabilities: []string{},
				}
				if err := db.CreatePositioning(ctx, seed); err != nil {
					t.Fatal(err)
				}
			}
			var p *model.Positioning
			if !tc.nilPos {
				id := tc.id
				if tc.emptyID {
					id = ""
				}
				p = &model.Positioning{
					ID: id, Model: "m", Family: "fam",
					Tiers: append([]model.Tier(nil), tc.tiers...), Capabilities: []string{}, Preference: 1,
				}
			}
			var err error
			if tc.update {
				err = db.UpdatePositioning(ctx, p)
			} else {
				err = db.CreatePositioning(ctx, p)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if strings.Contains(err.Error(), "invalid tier") {
					t.Fatalf("tier rejected the write: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := db.GetPositioning(ctx, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Tiers) != len(tc.want) {
				t.Fatalf("tiers %#v want %#v", got.Tiers, tc.want)
			}
			for i := range tc.want {
				if got.Tiers[i] != tc.want[i] {
					t.Fatalf("tiers %#v want %#v", got.Tiers, tc.want)
				}
			}
			for _, tier := range got.Tiers {
				raw := string(tier)
				if raw == "t2" || raw == " T3 " || raw == "not-a-tier" || raw == "" || raw != strings.ToUpper(strings.TrimSpace(raw)) {
					t.Fatalf("non-canonical tier stored %q in %#v", raw, got.Tiers)
				}
			}
		})
	}
}
