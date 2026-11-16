package main

import (
	"context"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestCardMergedAndAbsorbVerbs(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o", "--project", "p", "--title", "o")
	mustHome(t, home, "card", "add", "--id", "mq", "--objective", "o", "--title", "mq", "--status", "merge_queued")
	mustHome(t, home, "card", "add", "--id", "rd", "--objective", "o", "--title", "rd", "--status", "ready")
	mustHome(t, home, "card", "add", "--id", "run", "--objective", "o", "--title", "run", "--status", "running")

	mustHome(t, home, "card", "merged", "--note", "abc123", "mq")
	code, _, _ := callCLI(t, []string{"--home", home, "card", "merged", "rd"})
	if code == 0 {
		t.Fatal("ready card marked merged")
	}
	// No attempts: absorb must refuse and leave the card alone.
	code, _, _ = callCLI(t, []string{"--home", home, "card", "absorb", "run"})
	if code == 0 {
		t.Fatal("absorbed a card with no attempts")
	}
	withDB(t, home, func(db *store.Store) {
		ctx := context.Background()
		for id, want := range map[string]model.CardStatus{"mq": model.CardMerged, "rd": model.CardReady, "run": model.CardRunning} {
			c, err := db.GetCard(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if c.Status != want {
				t.Fatalf("%s status %s, want %s", id, c.Status, want)
			}
		}
		events, err := db.ListEvents(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range events {
			if e.Entity == "card:mq" && e.Type == "card_marked_merged" {
				found = true
			}
		}
		if !found {
			t.Fatal("missing card_marked_merged event")
		}
	})
}

func TestCardMergedWaitingOnCanceledRepair(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o", "--project", "p", "--title", "o")
	mustHome(t, home, "card", "add", "--id", "par", "--objective", "o", "--title", "par", "--status", "waiting")
	mustHome(t, home, "card", "add", "--id", "rp-par", "--objective", "o", "--title", "rp", "--status", "ready")
	mustHome(t, home, "card", "add", "--id", "bo", "--objective", "o", "--title", "bo", "--status", "waiting")
	withDB(t, home, func(db *store.Store) {
		ctx := context.Background()
		for id, reason := range map[string]string{"par": "repair rp-par", "bo": "backoff:2026-01-01T00:00:00Z"} {
			c, err := db.GetCard(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			c.WaitReason = reason
			if err := db.UpdateCard(ctx, &c); err != nil {
				t.Fatal(err)
			}
		}
	})
	if code, _, _ := callCLI(t, []string{"--home", home, "card", "merged", "par"}); code == 0 {
		t.Fatal("marked merged while its repair card is still live")
	}
	if code, _, _ := callCLI(t, []string{"--home", home, "card", "merged", "bo"}); code == 0 {
		t.Fatal("marked merged a card waiting on backoff")
	}
	mustHome(t, home, "card", "cancel", "rp-par")
	mustHome(t, home, "card", "merged", "--note", "3193748a", "par")
	withDB(t, home, func(db *store.Store) {
		ctx := context.Background()
		for id, want := range map[string]model.CardStatus{"par": model.CardMerged, "bo": model.CardWaiting} {
			c, err := db.GetCard(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if c.Status != want {
				t.Fatalf("%s status %s, want %s", id, c.Status, want)
			}
			if id == "par" && c.WaitReason != "" {
				t.Fatalf("wait reason not cleared: %q", c.WaitReason)
			}
		}
	})
}
