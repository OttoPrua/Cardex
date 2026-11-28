package positioning

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestHomeTierConventionPreservesLegacySeed(t *testing.T) {
	for _, tc := range []struct {
		name, marker string
		want         model.Tier
	}{
		{"unversioned", "", model.TierT1},
		{"current", model.TierConvention, model.TierT3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			config, err := json.Marshal(configFile{TierConvention: tc.marker, Positioning: []model.Positioning{{
				ID: "fixture", Model: "fixture-model", Tiers: []model.Tier{model.TierT3},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, config, 0600); err != nil {
				t.Fatal(err)
			}
			h, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			rows, err := h.Current(t.Context())
			if err != nil || len(rows) != 1 || len(rows[0].Tiers) != 1 || rows[0].Tiers[0] != tc.want {
				t.Fatalf("rows %+v, %v", rows, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(config) {
				t.Fatal("read changed original file")
			}
		})
	}
}

func TestHomeTierConventionRequiresFreshApprovalOfLegacyProposal(t *testing.T) {
	dir := t.TempDir()
	h, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	p := savedProposal{
		ID: "old", Status: "proposed", Diff: "a strong model at T3",
		Entries: []model.Positioning{{ID: "conf", Model: "fixture-strong", Tiers: []model.Tier{model.TierT3}}},
	}
	if err := h.writeProposal(p); err != nil {
		t.Fatal(err)
	}
	if err := h.Approve(t.Context(), p.ID); err == nil || !strings.Contains(err.Error(), "old tier labels") {
		t.Fatalf("stale proposal accepted: %v", err)
	}
	current, err := h.Current(t.Context())
	if err != nil || len(current) != 0 {
		t.Fatalf("old approval mutated current: %+v, %v", current, err)
	}
	p.Entries[0].Tiers[0] = model.TierT1
	fresh, err := h.Propose(t.Context(), p.Entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Approve(t.Context(), fresh.ID); err != nil {
		t.Fatal(err)
	}
	current, err = h.Current(t.Context())
	if err != nil || len(current) != 1 || current[0].Tiers[0] != model.TierT1 {
		t.Fatalf("fresh approval was not published: %+v, %v", current, err)
	}
}
