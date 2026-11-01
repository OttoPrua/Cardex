package main

import (
	"context"
	"encoding/json"

	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/store"
)

// cardView is the card record plus the origin joined from the origin event.
// model.Card has no origin field; the event is the existing place that can keep it.
type cardView struct {
	model.Card
	Origin string `json:"origin,omitempty"`
}

func resolvedOrigin(args []string) string {
	kind, id, ok := notify.OriginFromArgs(args, nil)
	if !ok {
		return ""
	}
	return kind + ":" + id
}

func saveCardOrigin(ctx context.Context, db *store.Store, id, origin string) error {
	if origin == "" {
		return nil
	}
	data, err := json.Marshal(map[string]string{"origin": origin})
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		Entity: "card:" + id,
		Type:   "origin",
		Data:   data,
	})
}

func loadCardOrigin(ctx context.Context, db *store.Store, id string) (string, error) {
	evs, err := db.ListEvents(ctx, "card:"+id)
	if err != nil {
		return "", err
	}
	var origin string
	for _, ev := range evs {
		if ev.Type != "origin" {
			continue
		}
		var body struct {
			Origin string `json:"origin"`
		}
		if json.Unmarshal(ev.Data, &body) == nil && body.Origin != "" {
			origin = body.Origin
		}
	}
	return origin, nil
}
