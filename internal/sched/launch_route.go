package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/route"
	"github.com/OttoPrua/Cardex/internal/store"
)

const (
	eventPinReleased        = "pin_released"
	eventLaunchRouteDropped = "launch_route_dropped"
	pinReasonMissing        = "missing"
)

// pinRelease is one explicit channel or model dropped for this request only.
// The objective row is never written. Channel and Model are the original pin.
type pinRelease struct {
	released       bool
	reason         string
	channel        string
	model          string
	failoverOrigin string
}

// resolveLaunchRoute reuses the route recorded on the latest launch_failed
// event since the last successful start when the effective spec is not an
// explicit channel. Card channel or model is that spec; otherwise the
// objective is. A card model therefore keeps the saved route when the
// objective names a channel. An explicit card channel, or an objective
// channel when the card specifies neither, still selects normally. A missing
// channel, or health other than ok, falls back once and records why. A saved
// channel at its concurrency limit, or outside this call's candidate set,
// waits: no attempt, no fallback, and no drop.
func (r *Runner) resolveLaunchRoute(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, channels []model.Channel, pos []model.Positioning, inUse map[string]int, quotas route.Quotas, classified model.Tier) (model.Route, model.RouteReason, pinRelease, bool, error) {
	if len(route.ExplicitCompatibleHold(launchRequest(card, obj, channels, pos, inUse, quotas, classified, r.now()))) != 0 {
		return model.Route{}, model.RouteReason{}, pinRelease{}, true, r.holdExplicitCompatibleCard(ctx, db, card, obj, card)
	}
	if _, _, _, held := loop.ReviewHold(card); held {
		return r.chooseHeldReviewRoute(ctx, db, card, obj, channels, pos, inUse, quotas, classified)
	}
	channel, modelName := route.EffectivePin(launchRequest(card, obj, channels, pos, inUse, quotas, classified, r.now()))
	if card.Channel == "" && card.Model == "" {
		obj.Channel, obj.Model = channel, modelName
	}
	if channel == "" {
		saved, ok, err := savedLaunchRoute(ctx, db, card.ID)
		if err != nil {
			return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
		}
		if ok {
			return r.reuseSavedRoute(ctx, db, card, obj, saved, channels, pos, inUse, quotas, classified)
		}
	}
	return r.chooseLaunchRoute(ctx, db, card, obj, channels, pos, inUse, quotas, classified)
}

func (r *Runner) reuseSavedRoute(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, saved model.Route, channels []model.Channel, pos []model.Positioning, inUse map[string]int, quotas route.Quotas, classified model.Tier) (model.Route, model.RouteReason, pinRelease, bool, error) {
	configured, err := db.ListChannels(ctx)
	if err != nil {
		return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
	}
	ch, found := channelByID(configured, saved.Channel)
	if found && ch.CompatibleRuntimeHeld() {
		if card.Channel != "" || obj.Channel != "" {
			return model.Route{}, model.RouteReason{}, pinRelease{}, true, r.holdCompatibleCard(ctx, db, card, obj, func(tx context.Context) (bool, error) {
				current, ok, err := savedLaunchRoute(tx, db, card.ID)
				if err != nil || !ok || current != saved {
					return false, err
				}
				return heldCompatibleChannel(tx, db, saved.Channel)
			})
		}
		// Do not allow a stale automatic launch-failure receipt to displace
		// current eligible routes, but also never change an explicit API pin.
		return r.chooseLaunchRoute(ctx, db, card, obj, channels, pos, inUse, quotas, classified)
	}
	if !found || ch.Health != model.HealthOK {
		why := pinReasonMissing
		health := ""
		if found {
			why = route.ReasonHealth
			health = string(ch.Health)
		}
		if werr := r.writeRouteDropped(ctx, db, card.ID, saved, why, health); werr != nil {
			fmt.Fprintf(os.Stderr, "launch route dropped card=%s channel=%s reason=%s write=%v\n", card.ID, saved.Channel, why, werr)
		}
		return r.chooseLaunchRoute(ctx, db, card, obj, channels, pos, inUse, quotas, classified)
	}
	if _, inSet := channelByID(channels, saved.Channel); !inSet {
		return model.Route{}, model.RouteReason{}, pinRelease{}, false, route.ErrPinnedUnavailable
	}
	used := 0
	if inUse != nil {
		used = inUse[saved.Channel]
	}
	if ch.Concurrency > 0 && used >= ch.Concurrency {
		return model.Route{}, model.RouteReason{}, pinRelease{}, false, route.ErrPinnedUnavailable
	}
	modelName := saved.Model
	if modelName == "" && len(ch.Models) > 0 {
		modelName = ch.Models[0]
	}
	rt := model.Route{Channel: ch.ID, Model: modelName}
	tier := model.TierNone
	if card.Kind == model.KindReview {
		// Recover the tier of the retained model, including a borrowed tier.
		tier = dispatchExpectation(card, obj, classified, channels, pos).Tier
		ch.Models = []string{modelName}
		_, proof, err := route.Select(ctx, launchRequest(card, obj, []model.Channel{ch}, pos, nil, nil, classified, r.now()))
		if err == nil && proof.SelectedTier(rt) != model.TierNone {
			tier = proof.SelectedTier(rt)
		}
	}
	return rt, model.RouteReason{
		Candidates: []model.RouteCandidate{{
			Channel: rt.Channel, Model: rt.Model, Family: ch.Family, Tier: tier,
		}},
		Filtered: []model.FilteredCandidate{},
		Scores:   []model.RouteScore{{Channel: rt.Channel, Model: rt.Model}},
	}, pinRelease{}, false, nil
}

func (r *Runner) holdExplicitCompatibleCard(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, probe model.Card) error {
	return r.holdCompatibleCard(ctx, db, card, obj, func(tx context.Context) (bool, error) {
		configured, err := db.ListChannels(tx)
		if err != nil {
			return false, err
		}
		return len(route.ExplicitCompatibleHold(route.Request{Card: probe, Objective: obj, Channels: configured})) != 0, nil
	})
}

func heldCompatibleChannel(ctx context.Context, db *store.Store, id string) (bool, error) {
	ch, err := db.GetChannel(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && ch.CompatibleRuntimeHeld(), err
}

// An unreserved dispatch observation cannot rewrite operator changes. This
// applies equally to strict and legacy cards, and re-proves the route under
// the same writer lock rather than holding based on a stale in-memory pin.
func (r *Runner) holdCompatibleCard(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, routeStillHeld func(context.Context) (bool, error)) error {
	return db.WithWriteTx(ctx, func(tx context.Context) error {
		current, err := db.GetCard(tx, card.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !reflect.DeepEqual(current, card) || !canDispatch(current) {
			return err
		}
		objective, err := db.GetObjective(tx, current.Objective)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !reflect.DeepEqual(objective, obj) ||
			(objective.Status != model.ObjectiveActive && objective.Status != model.ObjectiveDoneCandidate) {
			return err
		}
		attempts, err := db.ListAttempts(tx, current.ID)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			if attemptOpen(attempt) {
				return nil
			}
		}
		stillHeld, err := routeStillHeld(tx)
		if err != nil || !stillHeld {
			return err
		}
		return db.MarkCompatibleCardHeld(tx, current)
	})
}

func channelByID(channels []model.Channel, id string) (model.Channel, bool) {
	for i := range channels {
		if channels[i].ID == id {
			return channels[i], true
		}
	}
	return model.Channel{}, false
}

// savedLaunchRoute is the latest launch_failed route since the last start
// that got past launch. An event with no channel does not count.
func savedLaunchRoute(ctx context.Context, db *store.Store, cardID string) (model.Route, bool, error) {
	attempts, err := db.ListAttempts(ctx, cardID)
	if err != nil {
		return model.Route{}, false, err
	}
	var success time.Time
	for _, attempt := range attempts {
		if attempt.Outcome == model.OutcomeLaunchFailed {
			continue
		}
		if attempt.PID > 0 || attempt.Outcome != "" {
			if success.IsZero() || attempt.StartedAt.After(success) {
				success = attempt.StartedAt
			}
		}
	}
	events, err := db.ListEvents(ctx, "card:"+cardID)
	if err != nil {
		return model.Route{}, false, err
	}
	var found model.Route
	ok := false
	for _, ev := range events {
		if ev.Type != model.EventLaunchFailed {
			continue
		}
		if !success.IsZero() && !ev.TS.After(success) {
			continue
		}
		var payload struct {
			Channel string `json:"channel"`
			Model   string `json:"model"`
		}
		if json.Unmarshal(ev.Data, &payload) != nil || payload.Channel == "" {
			continue
		}
		found = model.Route{Channel: payload.Channel, Model: payload.Model}
		ok = true
	}
	return found, ok, nil
}

func (r *Runner) writeRouteDropped(ctx context.Context, db *store.Store, cardID string, saved model.Route, why, health string) error {
	text := "saved route dropped: channel " + saved.Channel + " is missing"
	if why == route.ReasonHealth {
		text = "saved route dropped: channel " + saved.Channel + " health is " + health
	}
	payload := map[string]string{
		"channel": saved.Channel,
		"model":   saved.Model,
		"reason":  why,
		"text":    text,
	}
	if health != "" {
		payload["health"] = health
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		TS:     r.now().UTC(),
		Entity: "card:" + cardID,
		Type:   eventLaunchRouteDropped,
		Data:   data,
	})
}

// chooseHeldReviewRoute enforces the original constraints before invoking the ordinary selector.
func (r *Runner) chooseHeldReviewRoute(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, channels []model.Channel, pos []model.Positioning, inUse map[string]int, quotas route.Quotas, classified model.Tier) (model.Route, model.RouteReason, pinRelease, bool, error) {
	floor, originalChannel, originalModel, _ := loop.ReviewHold(card)
	probe := card
	if probe.Tier.Rank() < floor.Rank() {
		probe.Tier = floor
	}
	if (originalChannel != "" && probe.Channel != "" && probe.Channel != originalChannel) ||
		(originalModel != "" && probe.Model != "" && probe.Model != originalModel) {
		err := r.settleHeldReview(ctx, db, card, obj, floor, originalChannel, originalModel, "card pin conflicts with the original review")
		return model.Route{}, model.RouteReason{}, pinRelease{}, err == nil, err
	}
	if originalChannel != "" {
		probe.Channel = originalChannel
	}
	if originalModel != "" {
		probe.Model = originalModel
	}
	if len(route.ExplicitCompatibleHold(launchRequest(probe, obj, channels, pos, inUse, quotas, classified, r.now()))) != 0 {
		return model.Route{}, model.RouteReason{}, pinRelease{}, true, r.holdExplicitCompatibleCard(ctx, db, card, obj, probe)
	}
	probe.Channel, probe.Model = route.EffectivePin(launchRequest(probe, obj, channels, pos, inUse, quotas, classified, r.now()))

	routed := channels
	if probe.Channel != "" || probe.Model != "" {
		routed = nil
		for _, ch := range channels {
			if probe.Channel != "" && ch.ID != probe.Channel {
				continue
			}
			var eligible []string
			for _, name := range ch.Models {
				if (probe.Model == "" || probe.Model == name) &&
					loop.ReviewFloorMet([]model.Channel{ch}, pos, ch.ID, name, probe.Tier) {
					eligible = append(eligible, name)
				}
			}
			if len(eligible) == 0 {
				continue
			}
			blocked, err := quotaBlocked(ctx, quotas, ch.ID, r.now())
			if err != nil {
				return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
			}
			if !blocked {
				ch.Models = eligible
				routed = append(routed, ch)
			}
		}
	}

	req := launchRequest(probe, obj, routed, pos, inUse, quotas, classified, r.now())
	req.Objective.Channel, req.Objective.Model = "", ""
	rt, reason, err := selectHeldReviewRoute(ctx, req)
	if err == nil {
		return rt, reason, pinRelease{}, false, nil
	}
	if !errors.Is(err, route.ErrNoCandidate) && !errors.Is(err, route.ErrPinnedUnavailable) {
		return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
	}
	if len(inUse) > 0 {
		req.InUse = nil
		_, _, withoutOccupancy := selectHeldReviewRoute(ctx, req)
		if withoutOccupancy == nil {
			return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
		}
		if !errors.Is(withoutOccupancy, route.ErrNoCandidate) && !errors.Is(withoutOccupancy, route.ErrPinnedUnavailable) {
			return model.Route{}, model.RouteReason{}, pinRelease{}, false, withoutOccupancy
		}
	}
	err = r.settleHeldReview(ctx, db, card, obj, probe.Tier, probe.Channel, probe.Model, "no available channel/model meets the retained tier and pins")
	return model.Route{}, model.RouteReason{}, pinRelease{}, err == nil, err
}

func selectHeldReviewRoute(ctx context.Context, req route.Request) (model.Route, model.RouteReason, error) {
	card := req.Card
	if len(req.InUse) > 0 && (card.Channel != "" || card.Model != "") {
		// Explicit pins bypass the selector's concurrency filter.
		var free []model.Channel
		for _, ch := range req.Channels {
			if ch.Concurrency <= 0 || req.InUse[ch.ID] < ch.Concurrency {
				free = append(free, ch)
			}
		}
		req.Channels = free
	}
	rt, reason, err := chooseRoute(ctx, req)
	if errors.Is(err, route.ErrNoCandidate) && card.Tier == model.TierT3 {
		// The ordinary selector borrows one tier. A held review can also use T1.
		req.Card.Tier = model.TierT1
		rt, reason, err = chooseRoute(ctx, req)
	}
	if err == nil && (reason.SelectedTier(rt).Rank() < card.Tier.Rank() ||
		(card.Channel != "" && rt.Channel != card.Channel) || (card.Model != "" && rt.Model != card.Model) ||
		!loop.ReviewFloorMet(req.Channels, req.Positioning, rt.Channel, rt.Model, card.Tier)) {
		err = route.ErrNoCandidate
	}
	return rt, reason, err
}

func (r *Runner) settleHeldReview(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, tier model.Tier, channel, modelName, reason string) error {
	text := fmt.Sprintf("review hold unmet: tier %s channel %q model %q: %s", tier, channel, modelName, reason)
	return r.settleNoChannel(ctx, db, card, obj, text)
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
	req := launchRequest(probe, probeObj, routed, pos, inUse, quotas, classified, r.now())
	if rel.released && rel.channel == card.Channel {
		var err error
		req.AvoidChannels, rel.failoverOrigin, err = loop.AutomaticFallbackExclusions(ctx, db, card)
		if err != nil {
			return model.Route{}, model.RouteReason{}, pinRelease{}, false, err
		}
	}
	rt, reason, err := chooseRoute(ctx, req)
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
	if found.CompatibleRuntimeHeld() {
		return true, route.ReasonCompatibleRuntime, nil
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
		if ch.Health != model.HealthOK || ch.CompatibleRuntimeHeld() {
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
	_, err := withObservedCard(ctx, db, card, func(ctx context.Context) error {
		return r.recordNoChannel(ctx, db, card, obj, reason)
	})
	return err
}

func (r *Runner) recordNoChannel(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, reason string) error {
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
	payload := map[string]string{
		"pin_channel": rel.channel,
		"pin_model":   rel.model,
		"channel":     rt.Channel,
		"model":       rt.Model,
		"reason":      rel.reason,
	}
	if rel.failoverOrigin != "" {
		payload["failover_origin"] = rel.failoverOrigin
	}
	raw, err := json.Marshal(payload)
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
