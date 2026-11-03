package sched

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"cardex/internal/model"
	"cardex/internal/route"
	"cardex/internal/store"
)

const (
	eventPinReleased = "pin_released"
	pinReasonMissing = "missing"
)

// pinRelease is one explicit channel or model dropped for this request only.
// The objective row is never written. Channel and Model are the original pin.
type pinRelease struct {
	released bool
	reason   string
	channel  string
	model    string
}

// chooseLaunchRoute selects the attempt route.
// Backlog fill keeps today's route errors so a single-channel probe can move on.
// Any other card whose stored pin cannot be launched is routed without that pin,
// or marked needs_input with one question when no candidate remains.
func (r *Runner) chooseLaunchRoute(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, channels []model.Channel, pos []model.Positioning, inUse map[string]int, quotas route.Quotas, classified model.Tier) (model.Route, model.RouteReason, pinRelease, bool, error) {
	probe, probeObj := card, obj
	var rel pinRelease
	if card.Kind != model.KindBacklog {
		var err error
		probe, probeObj, rel, err = releaseUnusablePin(ctx, r.now(), card, obj, channels, quotas)
		if err != nil {
			return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
		}
	}
	req := launchRequest(probe, probeObj, channels, pos, inUse, quotas, classified, r.now())
	rt, reason, err := chooseRoute(ctx, req)
	if err == nil {
		return rt, reason, rel, false, nil
	}
	routeErr := errors.Is(err, route.ErrNoCandidate) || errors.Is(err, route.ErrPinnedUnavailable)
	if card.Kind == model.KindBacklog || !routeErr || (!rel.released && !errors.Is(err, route.ErrPinnedUnavailable)) {
		return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
	}
	if probe.Channel != "" || probe.Model != "" || probeObj.Channel != "" || probeObj.Model != "" {
		if !rel.released {
			rel = pinnedRefusal(probe, probeObj, channels)
		}
		probe.Channel, probe.Model = "", ""
		probeObj.Channel, probeObj.Model = "", ""
		var err2 error
		rt, reason, err2 = chooseRoute(ctx, launchRequest(probe, probeObj, channels, pos, inUse, quotas, classified, r.now()))
		if err2 == nil {
			return rt, reason, rel, false, nil
		}
		if !errors.Is(err2, route.ErrNoCandidate) && !errors.Is(err2, route.ErrPinnedUnavailable) {
			return model.Route{}, model.RouteReason{}, pinRelease{}, false, err2
		}
	}
	if err := r.settleNoChannel(ctx, db, card, obj, rel.reason); err != nil {
		return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
	}
	return model.Route{}, model.RouteReason{}, pinRelease{}, true, nil
}

func launchRequest(card model.Card, obj model.Objective, channels []model.Channel, pos []model.Positioning, inUse map[string]int, quotas route.Quotas, classified model.Tier, now time.Time) route.Request {
	return route.Request{
		Card:         card,
		Objective:    obj,
		AuthorFamily: card.AuthorFamily,
		Channels:     channels,
		Positioning:  pos,
		InUse:        inUse,
		Quota:        quotas,
		Classified:   classified,
		Now:          now,
	}
}

// releaseUnusablePin drops an explicit channel or model route would keep launching.
// A healthy channel that is not blocked until a future reset stays pinned.
// Remaining quota at or below the red line does not release the pin.
// The returned card and objective are copies; the stored objective is not updated.
func releaseUnusablePin(ctx context.Context, now time.Time, card model.Card, obj model.Objective, channels []model.Channel, quotas route.Quotas) (model.Card, model.Objective, pinRelease, error) {
	var rel pinRelease
	if card.Channel != "" {
		bad, reason, err := channelUnusable(ctx, card.Channel, channels, quotas, now)
		if err != nil {
			return card, obj, pinRelease{}, err
		}
		if bad {
			rel = pinRelease{released: true, reason: reason, channel: card.Channel, model: card.Model}
			card.Channel = ""
			card.Model = ""
		}
	} else if card.Model != "" {
		bad, reason, err := modelPinUnusable(ctx, card.Model, channels, quotas, now)
		if err != nil {
			return card, obj, pinRelease{}, err
		}
		if bad {
			rel = pinRelease{released: true, reason: reason, model: card.Model}
			card.Model = ""
		}
	}
	if card.Channel == "" && card.Model == "" {
		if obj.Channel != "" {
			bad, reason, err := channelUnusable(ctx, obj.Channel, channels, quotas, now)
			if err != nil {
				return card, obj, pinRelease{}, err
			}
			if bad {
				if !rel.released {
					rel = pinRelease{released: true, reason: reason, channel: obj.Channel, model: obj.Model}
				}
				obj.Channel = ""
				obj.Model = ""
			}
		} else if obj.Model != "" {
			bad, reason, err := modelPinUnusable(ctx, obj.Model, channels, quotas, now)
			if err != nil {
				return card, obj, pinRelease{}, err
			}
			if bad {
				if !rel.released {
					rel = pinRelease{released: true, reason: reason, model: obj.Model}
				}
				obj.Model = ""
			}
		}
	}
	return card, obj, rel, nil
}

func channelUnusable(ctx context.Context, id string, channels []model.Channel, quotas route.Quotas, now time.Time) (bool, string, error) {
	if id == "" {
		return false, "", nil
	}
	var found *model.Channel
	for i := range channels {
		if channels[i].ID == id {
			found = &channels[i]
			break
		}
	}
	if found == nil {
		return true, pinReasonMissing, nil
	}
	if found.Health != model.HealthOK {
		return true, route.ReasonHealth, nil
	}
	blocked, err := quotaBlocked(ctx, quotas, id, now)
	if err != nil {
		return false, "", err
	}
	if blocked {
		return true, route.ReasonBlocked, nil
	}
	return false, "", nil
}

// modelPinUnusable reports whether Select's first healthy channel for the model
// is missing, not ok, or blocked until a reset. A later healthy channel does not
// keep the pin, because Select would still launch the blocked one.
func modelPinUnusable(ctx context.Context, name string, channels []model.Channel, quotas route.Quotas, now time.Time) (bool, string, error) {
	if name == "" {
		return false, "", nil
	}
	saw := false
	for i := range channels {
		ch := channels[i]
		if !channelOffers(ch, name) {
			continue
		}
		saw = true
		if ch.Health != model.HealthOK {
			continue
		}
		blocked, err := quotaBlocked(ctx, quotas, ch.ID, now)
		if err != nil {
			return false, "", err
		}
		if blocked {
			return true, route.ReasonBlocked, nil
		}
		return false, "", nil
	}
	if saw {
		return true, route.ReasonHealth, nil
	}
	return true, pinReasonMissing, nil
}

func channelOffers(ch model.Channel, name string) bool {
	for _, modelName := range ch.Models {
		if modelName == name {
			return true
		}
	}
	return false
}

func quotaBlocked(ctx context.Context, quotas route.Quotas, channelID string, now time.Time) (bool, error) {
	if quotas == nil || channelID == "" {
		return false, nil
	}
	est, err := quotas.Remaining(ctx, channelID)
	if err != nil {
		return false, err
	}
	return est.Blocked && !est.Reset.IsZero() && now.Before(est.Reset), nil
}

func pinnedRefusal(card model.Card, obj model.Objective, channels []model.Channel) pinRelease {
	channelID, modelName := card.Channel, card.Model
	if channelID == "" && modelName == "" {
		channelID, modelName = obj.Channel, obj.Model
	}
	reason := route.ReasonHealth
	if channelID != "" {
		reason = pinReasonMissing
		for _, ch := range channels {
			if ch.ID == channelID {
				reason = route.ReasonHealth
				break
			}
		}
	}
	return pinRelease{released: true, reason: reason, channel: channelID, model: modelName}
}

func noChannelQuestion(reason string) string {
	if reason == "" {
		return "no other channel is available"
	}
	return "no other channel is available (" + reason + ")"
}

// settleNoChannel records the one question §5 asks for when no candidate can take the card.
// A later launch with the same reason does not add a second question.
func (r *Runner) settleNoChannel(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, reason string) error {
	text := noChannelQuestion(reason)
	questions, err := db.ListQuestions(ctx, obj.ID)
	if err != nil {
		return err
	}
	open := false
	for _, question := range questions {
		if question.Scope == card.ID && question.Answer == "" && question.Text == text {
			open = true
			break
		}
	}
	if !open {
		question := model.Question{
			Objective: obj.ID,
			Scope:     card.ID,
			ScopeKind: "card",
			Kind:      model.QuestionCustom,
			Text:      text,
			Options:   []string{"approve", "reject"},
		}
		if err := db.CreateQuestion(ctx, &question); err != nil {
			return err
		}
	}
	card.Status = model.CardNeedsInput
	card.WaitReason = ""
	return db.UpdateCard(ctx, &card)
}

func appendPinRelease(ctx context.Context, db *store.Store, now time.Time, cardID string, rel pinRelease, rt model.Route) error {
	raw, err := json.Marshal(map[string]string{
		"pin_channel": rel.channel,
		"pin_model":   rel.model,
		"channel":     rt.Channel,
		"model":       rt.Model,
		"reason":      rel.reason,
	})
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		TS:     now.UTC(),
		Entity: "card:" + cardID,
		Type:   eventPinReleased,
		Data:   raw,
	})
}
