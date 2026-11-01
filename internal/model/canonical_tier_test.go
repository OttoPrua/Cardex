package model_test

import (
	"testing"

	"cardex/internal/model"
)

func TestCanonicalTier(t *testing.T) {
	cases := []struct {
		raw  string
		want model.Tier
		ok   bool
	}{
		{raw: "t1", want: model.TierT1, ok: true},
		{raw: "T1", want: model.TierT1, ok: true},
		{raw: " T1 ", want: model.TierT1, ok: true},
		{raw: "t2", want: model.TierT2, ok: true},
		{raw: "T2", want: model.TierT2, ok: true},
		{raw: " T2 ", want: model.TierT2, ok: true},
		{raw: "t3", want: model.TierT3, ok: true},
		{raw: "T3", want: model.TierT3, ok: true},
		{raw: " T3 ", want: model.TierT3, ok: true},
		{raw: "", ok: false},
		{raw: "t4", ok: false},
	}
	for _, tc := range cases {
		tier := model.Tier(tc.raw)
		got, ok := model.CanonicalTier(tier)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("CanonicalTier(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
		if _, canon := model.CanonicalTier(tier); canon != tc.ok {
			t.Fatalf("tierField bool CanonicalTier(%q) = %v, want %v", tc.raw, canon, tc.ok)
		}
	}
}
