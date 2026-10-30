package positioning

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cardex/internal/model"
)

func TestCurrentProposeApprove(t *testing.T) {
	home := t.TempDir()
	cfg := []byte(`{
		"ignored": true,
		"positioning": [
			{"id":"p1","model":"opus","family":"anthropic","tiers":["T3"],"capabilities":["web"],"preference":1}
		]
	}`)
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cur, err := st.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) != 1 || cur[0].Model != "opus" || cur[0].Family != "anthropic" || cur[0].Version != 1 || cur[0].Preference != 1 {
		t.Fatalf("seed current %+v", cur)
	}
	prop, err := st.Propose(ctx, []model.Positioning{{
		Model: "sonnet", Family: "anthropic", Tiers: []model.Tier{model.TierT2},
		Capabilities: []string{"web"}, Preference: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if prop.ID == "" || len(prop.Entries) != 1 || prop.Entries[0].Model != "sonnet" {
		t.Fatalf("proposal %+v", prop)
	}
	if err := st.Reject(ctx, prop.ID); err == nil {
		t.Fatal("Reject returned nil")
	}
	still, err := st.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(still) != 1 || still[0].Model != "opus" || still[0].Version != 1 {
		t.Fatalf("propose changed current %+v", still)
	}
	if err := st.Approve(ctx, prop.ID); err != nil {
		t.Fatal(err)
	}
	next, err := st.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Model != "sonnet" || next[0].Version <= cur[0].Version {
		t.Fatalf("approved %+v previous %d", next, cur[0].Version)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(cfg) {
		t.Fatal("approve rewrote config.json")
	}
}

func TestOpenRefusesCardexHome(t *testing.T) {
	user, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(user, ".cardex")); err == nil {
		t.Fatal("opened ~/.cardex")
	}
}
