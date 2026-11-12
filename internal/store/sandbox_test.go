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
	if v, _ := db.SchemaVersion(ctx); v != 4 {
		t.Fatalf("schema version %d, want 4", v)
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
	if on.Sandbox != "" || off.Sandbox != "off" {
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
	if on.Title != "t2" || on.Sandbox != "" || off.Sandbox != "off" {
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
