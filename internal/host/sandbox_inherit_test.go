package host

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestSandboxOffInheritsProject(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, p := range []model.Project{{ID: "p-on", RepoPath: "/r", WorktreeRoot: "/w"}, {ID: "p-off", RepoPath: "/r", WorktreeRoot: "/w", Sandbox: "off"}} {
		p := p
		if err := db.CreateProject(ctx, &p); err != nil {
			t.Fatal(err)
		}
		o := model.Objective{ID: "o-" + p.ID, Project: p.ID, Title: "t", Mode: model.ModeManual, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive}
		if err := db.CreateObjective(ctx, &o); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		obj, card string
		off       bool
	}{
		{"o-p-on", "", false},
		{"o-p-on", "on", false},
		{"o-p-on", "off", true},
		{"o-p-off", "", true},
		{"o-p-off", "on", false},
		{"o-p-off", "off", true},
		{"o-missing", "", false},
	}
	for _, c := range cases {
		got := sandboxOff(ctx, db, model.Card{Objective: c.obj, Sandbox: c.card})
		if got != c.off {
			t.Errorf("objective %s card %q: off=%v, want %v", c.obj, c.card, got, c.off)
		}
	}
}
