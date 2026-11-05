package sched

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/store"
)

func (r *Runner) deliveryMessage(ctx context.Context, db *store.Store, q model.Question) (notify.Message, error) {
	msg := notify.Message{
		QuestionID: q.ID,
		Objective:  q.Objective,
		Text:       q.Text,
	}
	if err := r.fillDelivery(ctx, db, &msg); err != nil {
		return notify.Message{}, err
	}
	return msg, nil
}

// fillDelivery sets the four fields every delivery path shares.
// Home is this serve home. SubscriptionID stays empty: v2 does not infer it,
// and a channel SourceGroup is an account, not a subscription id.
// Fallback comes only from this home's config.json notify.fallback.
// Origin prefers the card origin event, otherwise the objective origin.
func (r *Runner) fillDelivery(ctx context.Context, db *store.Store, msg *notify.Message) error {
	if r == nil || msg == nil {
		return nil
	}
	msg.Home = r.Home
	msg.SubscriptionID = ""
	fallback, err := r.notifyFallback(ctx, db)
	if err != nil {
		return err
	}
	msg.Fallback = fallback
	if db == nil || strings.TrimSpace(msg.QuestionID) == "" {
		return nil
	}
	q, err := db.GetQuestion(ctx, msg.QuestionID)
	if err != nil {
		if strings.TrimSpace(msg.Origin) == "" && strings.TrimSpace(msg.Objective) != "" {
			if obj, objErr := db.GetObjective(ctx, msg.Objective); objErr == nil {
				msg.Origin = strings.TrimSpace(obj.Origin)
			}
		}
		return nil
	}
	if msg.Objective == "" {
		msg.Objective = q.Objective
	}
	origin := ""
	if obj, objErr := db.GetObjective(ctx, q.Objective); objErr == nil {
		origin = strings.TrimSpace(obj.Origin)
	}
	if q.ScopeKind == "card" && strings.TrimSpace(q.Scope) != "" {
		if cardOrigin, cardErr := cardOriginEvent(ctx, db, q.Scope); cardErr == nil && strings.TrimSpace(cardOrigin) != "" {
			origin = strings.TrimSpace(cardOrigin)
		}
	}
	msg.Origin = origin
	return nil
}

// notifyFallback reads <home>/config.json top-level notify.fallback.
// A missing file, missing key, empty value, or unparsable document selects no hop.
// A present value that is not <kind>:<id> also selects no hop and records one warning.
func (r *Runner) notifyFallback(ctx context.Context, db *store.Store) (string, error) {
	if r == nil || strings.TrimSpace(r.Home) == "" {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(r.Home, "config.json"))
	if err != nil {
		return "", nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return "", nil
	}
	raw, ok := top["notify"]
	if !ok {
		return "", nil
	}
	var body struct {
		Fallback string `json:"fallback"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return "", nil
	}
	fallback := strings.TrimSpace(body.Fallback)
	if fallback == "" {
		return "", nil
	}
	if _, _, ok := notify.ParseOrigin(fallback); !ok {
		if err := r.noteNotifyWarning(ctx, db, fallback); err != nil {
			return "", err
		}
		return "", nil
	}
	return fallback, nil
}

func (r *Runner) noteNotifyWarning(ctx context.Context, db *store.Store, fallback string) error {
	if db == nil {
		return nil
	}
	const entity = "config:notify"
	events, err := db.ListEvents(ctx, entity)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.Type == "notify_warning" {
			return nil
		}
	}
	raw, err := json.Marshal(map[string]string{
		"fallback": fallback,
		"reason":   "invalid notify.fallback",
	})
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		Entity: entity,
		Type:   "notify_warning",
		Data:   raw,
	})
}

// handoffDelivery records a missing hermes subscription as a skip, not a delivery
// failure and not a design exception. Any other hop error is a delivery_error.
// Both notes still return nil so the design step does not store design_error.
// ErrNotImplemented is left for the scheduler to continue past.
func (r *Runner) handoffDelivery(ctx context.Context, db *store.Store, msg notify.Message, cause error) error {
	if cause == nil || errors.Is(cause, notify.ErrNotImplemented) {
		return cause
	}
	skip, failed := deliveryErrorParts(cause)
	if skip {
		if err := r.noteDeliverySkip(ctx, db, msg.QuestionID); err != nil {
			return err
		}
	}
	if failed {
		if err := r.noteDelivery(ctx, db, model.Question{ID: msg.QuestionID}, cause); err != nil {
			return err
		}
	}
	return nil
}

func deliveryErrorParts(cause error) (skip, failed bool) {
	text := cause.Error()
	if strings.Contains(text, "invalid_subscription_id") {
		skip = true
	}
	for _, part := range strings.Split(text, "\n") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part != "invalid_subscription_id" {
			failed = true
		}
	}
	return skip, failed
}

func (r *Runner) noteDeliverySkip(ctx context.Context, db *store.Store, questionID string) error {
	if db == nil {
		return nil
	}
	raw, err := json.Marshal(map[string]string{
		"question": questionID,
		"reason":   "缺订阅 id",
	})
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		Entity: "question:" + questionID,
		Type:   "delivery_skip",
		Data:   raw,
	})
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
