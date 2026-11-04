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
	routed := channels
	var rel pinRelease
	if card.Kind != model.KindBacklog {
		var err error
		probe, probeObj, routed, rel, err = releaseUnusablePin(ctx, r.now(), card, obj, channels, quotas)
		if err != nil {
			return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
		}
	}
	rt, reason, err := chooseRoute(ctx, launchRequest(probe, probeObj, routed, pos, inUse, quotas, classified, r.now()))
	if err == nil {
		return rt, reason, rel, false, nil
	}
	routeErr := errors.Is(err, route.ErrNoCandidate) || errors.Is(err, route.ErrPinnedUnavailable)
	if card.Kind == model.KindBacklog || !routeErr || (!rel.released && !errors.Is(err, route.ErrPinnedUnavailable)) {
		return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
	}
	if probe.Channel != "" || probe.Model != "" || probeObj.Channel != "" || probeObj.Model != "" {
		keep, filtered, keepErr := usableModelCarriers(ctx, r.now(), probe, probeObj, channels, quotas)
		if keepErr != nil {
			return model.Route{}, model.RouteReason{}, pinRelease{}, false, keepErr
		}
		next := channels
		if keep {
			next = filtered
		} else {
			if !rel.released {
				rel = pinnedRefusal(probe, probeObj, channels)
			}
			probe.Channel, probe.Model = "", ""
			probeObj.Channel, probeObj.Model = "", ""
		}
		var err2 error
		rt, reason, err2 = chooseRoute(ctx, launchRequest(probe, probeObj, next, pos, inUse, quotas, classified, r.now()))
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
// releaseUnusablePin drops an explicit channel or model route would keep launching.
// A pin is unusable only when its channel is missing, health is not ok, or the
// channel is blocked until a known reset. Remaining quota at or below the red
// line does not release it. An unusable effective pin clears both the card and
// target copies so automatic selection cannot inherit the other one. A model pin
// with a healthy unblocked carrier stays, and only the unusable carriers are
// removed from the returned channel list. The stored objective is not updated.
func releaseUnusablePin(ctx context.Context, now time.Time, card model.Card, obj model.Objective, channels []model.Channel, quotas route.Quotas) (model.Card, model.Objective, []model.Channel, pinRelease, error) {
	var rel pinRelease
	if card.Channel != "" {
		bad, reason, err := channelUnusable(ctx, card.Channel, channels, quotas, now)
		if err != nil {
			return card, obj, nil, pinRelease{}, err
		}
		if !bad {
			return card, obj, channels, rel, nil
		}
		rel = pinRelease{released: true, reason: reason, channel: card.Channel, model: card.Model}
		card.Channel, card.Model = "", ""
		obj.Channel, obj.Model = "", ""
		return card, obj, channels, rel, nil
	}
	if card.Model != "" {
		return releaseModelPin(ctx, now, card, obj, channels, quotas, card.Model)
	}
	if obj.Channel != "" {
		bad, reason, err := channelUnusable(ctx, obj.Channel, channels, quotas, now)
		if err != nil {
			return card, obj, nil, pinRelease{}, err
		}
		if !bad {
			return card, obj, channels, rel, nil
		}
		rel = pinRelease{released: true, reason: reason, channel: obj.Channel, model: obj.Model}
		card.Channel, card.Model = "", ""
		obj.Channel, obj.Model = "", ""
		return card, obj, channels, rel, nil
	}
	if obj.Model != "" {
		return releaseModelPin(ctx, now, card, obj, channels, quotas, obj.Model)
	}
	return card, obj, channels, rel, nil
}

func releaseModelPin(ctx context.Context, now time.Time, card model.Card, obj model.Objective, channels []model.Channel, quotas route.Quotas, name string) (model.Card, model.Objective, []model.Channel, pinRelease, error) {
	bad, reason, err := modelPinUnusable(ctx, name, channels, quotas, now)
	if err != nil {
		return card, obj, nil, pinRelease{}, err
	}
	if !bad {
		filtered, err := withoutUnusableModelCarriers(ctx, name, channels, quotas, now)
		if err != nil {
			return card, obj, nil, pinRelease{}, err
		}
		return card, obj, filtered, pinRelease{}, nil
	}
	rel := pinRelease{released: true, reason: reason, model: name}
	if card.Model == "" {
		rel.channel = obj.Channel
		rel.model = obj.Model
	}
	card.Channel, card.Model = "", ""
	obj.Channel, obj.Model = "", ""
	return card, obj, channels, rel, nil
}

// withoutUnusableModelCarriers removes carriers the model pin must not launch.
// Channels that do not offer the model stay, so a later automatic selection can
// still see them when the pin itself is released by another path.
func withoutUnusableModelCarriers(ctx context.Context, name string, channels []model.Channel, quotas route.Quotas, now time.Time) ([]model.Channel, error) {
	kept := make([]model.Channel, 0, len(channels))
	for i := range channels {
		ch := channels[i]
		if channelOffers(ch, name) {
			bad, _, err := channelUnusable(ctx, ch.ID, channels, quotas, now)
			if err != nil {
				return nil, err
			}
			if bad {
				continue
			}
		}
		kept = append(kept, ch)
	}
	return kept, nil
}

// usableModelCarriers is the second release site. A model pin that still has a
// healthy unblocked carrier is kept, and the unusable carriers are omitted.
// A channel pin, or a model whose carriers are all unusable, is not kept.
func usableModelCarriers(ctx context.Context, now time.Time, card model.Card, obj model.Objective, channels []model.Channel, quotas route.Quotas) (bool, []model.Channel, error) {
	name := ""
	if card.Channel == "" && card.Model != "" {
		name = card.Model
	} else if card.Channel == "" && card.Model == "" && obj.Channel == "" && obj.Model != "" {
		name = obj.Model
	}
	if name == "" {
		return false, channels, nil
	}
	bad, _, err := modelPinUnusable(ctx, name, channels, quotas, now)
	if err != nil {
		return false, nil, err
	}
	if bad {
		return false, channels, nil
	}
	filtered, err := withoutUnusableModelCarriers(ctx, name, channels, quotas, now)
	if err != nil {
		return false, nil, err
	}
	return true, filtered, nil
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

// modelPinUnusable reports that every carrier of the model is missing, not ok,
// or blocked until a known reset. One healthy unblocked carrier keeps the pin.
// Remaining quota at or below the red line does not make a carrier unusable.
func modelPinUnusable(ctx context.Context, name string, channels []model.Channel, quotas route.Quotas, now time.Time) (bool, string, error) {
	if name == "" {
		return false, "", nil
	}
	saw := false
	blocked := false
	for i := range channels {
		ch := channels[i]
		if !channelOffers(ch, name) {
			continue
		}
		saw = true
		if ch.Health != model.HealthOK {
			continue
		}
		held, err := quotaBlocked(ctx, quotas, ch.ID, now)
		if err != nil {
			return false, "", err
		}
		if held {
			blocked = true
			continue
		}
		return false, "", nil
	}
	if !saw {
		return true, pinReasonMissing, nil
	}
	if blocked {
		return true, route.ReasonBlocked, nil
	}
	return true, route.ReasonHealth, nil
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
