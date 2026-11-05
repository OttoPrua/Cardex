package sched

import (
	"context"
	"encoding/json"
	"strings"

	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/store"
)

func (r *Runner) deliveryMessage(ctx context.Context, db *store.Store, q model.Question, objectiveOrigin string) notify.Message {
	origin := strings.TrimSpace(objectiveOrigin)
	if q.ScopeKind == "card" && strings.TrimSpace(q.Scope) != "" {
		if cardOrigin, err := cardOriginEvent(ctx, db, q.Scope); err == nil && cardOrigin != "" {
			origin = cardOrigin
		}
	}
	fallback := ""
	if strings.TrimSpace(objectiveOrigin) != "" && objectiveOrigin != origin {
		fallback = objectiveOrigin
	}
	msg := notify.Message{
		QuestionID:     q.ID,
		Objective:      q.Objective,
		Text:           q.Text,
		Home:           r.Home,
		Origin:         origin,
		Fallback:       fallback,
		SubscriptionID: r.subscriptionFor(ctx, db, q),
	}
	return msg
}

func (r *Runner) subscriptionFor(ctx context.Context, db *store.Store, q model.Question) string {
	if obj, err := db.GetObjective(ctx, q.Objective); err == nil {
		if id := sourceGroup(ctx, db, obj.Channel); id != "" {
			return id
		}
	}
	if q.ScopeKind == "card" && strings.TrimSpace(q.Scope) != "" {
		if card, err := db.GetCard(ctx, q.Scope); err == nil {
			if id := sourceGroup(ctx, db, card.Channel); id != "" {
				return id
			}
		}
	}
	return ""
}

func sourceGroup(ctx context.Context, db *store.Store, channelID string) string {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return ""
	}
	ch, err := db.GetChannel(ctx, channelID)
	if err != nil {
		return ""
	}
	if !notify.AcceptSubscriptionID(ch.SourceGroup) {
		return ""
	}
	return ch.SourceGroup
}

func cardOriginEvent(ctx context.Context, db *store.Store, cardID string) (string, error) {
	events, err := db.ListEvents(ctx, "card:"+cardID)
	if err != nil {
		return "", err
	}
	origin := ""
	for _, event := range events {
		if event.Type != "origin" {
			continue
		}
		var body struct {
			Origin string `json:"origin"`
		}
		if json.Unmarshal(event.Data, &body) == nil && strings.TrimSpace(body.Origin) != "" {
			origin = body.Origin
		}
	}
	return origin, nil
}

func (r *Runner) noteDelivery(ctx context.Context, db *store.Store, q model.Question, cause error) error {
	raw, err := json.Marshal(map[string]string{
		"question": q.ID,
		"error":    cause.Error(),
	})
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		Entity: "question:" + q.ID,
		Type:   "delivery_error",
		Data:   raw,
	})
}
