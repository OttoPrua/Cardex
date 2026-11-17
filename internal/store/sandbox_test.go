package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestCardSandboxField(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := db.SchemaVersion(ctx); v != 6 {
		t.Fatalf("schema version %d, want 6", v)
	}
	mk := func(id, sb string) error {
		c := model.Card{ID: id, Objective: "o", Kind: model.KindDev, Status: model.CardReady, Title: "t", Sandbox: sb}
		return db.CreateCard(ctx, &c)
	}
	if err := mk("c-on", "on"); err != nil {
		t.Fatal(err)
	}
	if err := mk("c-off", "off"); err != nil {
		t.Fatal(err)
	}
	if err := mk("c-bad", "maybe"); err == nil {
		t.Fatal("sandbox=maybe accepted")
	}
	on, _ := db.GetCard(ctx, "c-on")
	off, _ := db.GetCard(ctx, "c-off")
	if on.Sandbox != "on" || off.Sandbox != "off" {
		t.Fatalf("on=%q off=%q", on.Sandbox, off.Sandbox)
	}
	// The switch is fixed at creation: an update cannot turn it off or back on.
	on.Sandbox = "off"
	on.Title = "t2"
	if err := db.UpdateCard(ctx, &on); err != nil {
		t.Fatal(err)
	}
	off.Sandbox = ""
	if err := db.UpdateCard(ctx, &off); err != nil {
		t.Fatal(err)
	}
	on, _ = db.GetCard(ctx, "c-on")
	off, _ = db.GetCard(ctx, "c-off")
	if on.Title != "t2" || on.Sandbox != "on" || off.Sandbox != "off" {
		t.Fatalf("after update on=%+v off=%q", on.Sandbox, off.Sandbox)
	}
	db.Close()
	// Reopening an already-migrated db is a no-op.
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func TestProjectSandboxDefault(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for id, sb := range map[string]string{"p-default": "", "p-on": "on", "p-off": "off"} {
		p := model.Project{ID: id, RepoPath: "/r", WorktreeRoot: "/w", Sandbox: sb}
		if err := db.CreateProject(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	bad := model.Project{ID: "p-bad", RepoPath: "/r", Sandbox: "maybe"}
	if err := db.CreateProject(ctx, &bad); err == nil {
		t.Fatal("project sandbox=maybe accepted")
	}
	want := map[string]string{"p-default": "", "p-on": "", "p-off": "off"}
	for id, w := range want {
		p, err := db.GetProject(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Sandbox != w {
			t.Fatalf("%s sandbox %q, want %q", id, p.Sandbox, w)
		}
	}
	// Unlike a card, the project default can change later.
	p, _ := db.GetProject(ctx, "p-off")
	p.Sandbox = "on"
	if err := db.UpdateProject(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if p, _ = db.GetProject(ctx, "p-off"); p.Sandbox != "" {
		t.Fatalf("after set on: %q", p.Sandbox)
	}
	list, err := db.ListProjects(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("list %d %v", len(list), err)
	}
}
