package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/positioning"
	"github.com/OttoPrua/Cardex/internal/route"
	"github.com/OttoPrua/Cardex/internal/store"
)

// EventRouteNoCandidate records why a ready card was not dispatched. It is
// written when the explanation changes, so a card that sits in ready always has
// a visible reason and a next step instead of a silent skip.
const EventRouteNoCandidate = "route_no_candidate"

// EventPositioningStarter records the starter proposal offered on a fresh install.
const EventPositioningStarter = "positioning_starter_proposed"

const starterCheckWindow = time.Minute

// noCandidateExplanation is the payload of EventRouteNoCandidate.
type noCandidateExplanation struct {
	Card   string `json:"card"`
	Tier   string `json:"tier"`
	Reason string `json:"reason"`
	Hint   string `json:"hint"`
}

func hasModelRows(pos []model.Positioning) bool {
	for _, row := range pos {
		if row.Model != "" {
			return true
		}
	}
	return false
}

// explainNoCandidate says why routing found nothing for card. ok is false when
// routing succeeds now or every candidate is only busy (concurrency), which is
// normal waiting and not worth an event.
func explainNoCandidate(ctx context.Context, card model.Card, obj model.Objective, channels []model.Channel, pos []model.Positioning, inUse map[string]int, quotas route.Quotas, now time.Time) (noCandidateExplanation, bool) {
	tier := card.Tier
	if !tier.Valid() || tier == model.TierNone {
		tier = model.DefaultTier(card.Kind)
	}
	out := noCandidateExplanation{Card: card.ID, Tier: string(tier)}
	if len(channels) == 0 {
		out.Reason = "no channel is configured"
		out.Hint = "add one: cardex channel set -id <id> -runner <runner> -model <model>"
		return out, true
	}
	if !hasModelRows(pos) {
		out.Reason = "no positioning is approved, so no model is routable"
		out.Hint = "approve the starter proposal: cardex positioning proposals, then cardex positioning approve <id> (or cardex positioning propose first)"
		return out, true
	}
	_, why, err := route.Select(ctx, launchRequest(card, obj, channels, pos, inUse, quotas, "", now))
	if err == nil {
		return out, false
	}
	if len(why.Filtered) == 0 {
		if missing := missingCaps(card.Requires, channels, pos); len(missing) > 0 {
			out.Reason = fmt.Sprintf("the card requires %s, and no approved model a channel offers has that capability", strings.Join(missing, ", "))
			out.Hint = "add the capability to a positioning row of a model that has it (cardex positioning propose --file <json>, then approve), or drop it from the card"
			return out, true
		}
		out.Reason = fmt.Sprintf("no approved positioning row lists tier %s (or the next tier up) for a model a channel offers", tier)
		out.Hint = "list the model for this tier or add it to a channel, then cardex positioning propose / approve"
		return out, true
	}
	health := map[string]string{}
	for _, ch := range channels {
		health[ch.ID] = string(ch.Health)
	}
	parts := []string{}
	hints := []string{}
	seen := map[string]bool{}
	busyOnly := true
	for _, f := range why.Filtered {
		item := f.Channel + "/" + f.Model + ": " + f.Reason
		if f.Reason == route.ReasonHealth {
			item = f.Channel + "/" + f.Model + ": health " + health[f.Channel]
		}
		if !seen[item] {
			seen[item] = true
			parts = append(parts, item)
		}
		if f.Reason != route.ReasonConcurrency {
			busyOnly = false
		}
		var h string
		switch f.Reason {
		case route.ReasonHealth:
			if health[f.Channel] == string(model.HealthAuthFailed) {
				h = "channel " + f.Channel + " is not logged in: log in to its CLI, then cardex channel set -id " + f.Channel + " -health ok"
			} else {
				h = "channel " + f.Channel + " is " + health[f.Channel] + ": check it with cardex channel check " + f.Channel
			}
		case route.ReasonRedline, route.ReasonCutoff, route.ReasonBlocked:
			h = "channel " + f.Channel + " is out of quota for now; it is retried after the reset, or add another channel"
		case route.ReasonUnknownQuota:
			h = "channel " + f.Channel + " has no quota data and unknown_quota is exclude"
		}
		if h != "" && !seen["hint:"+h] {
			seen["hint:"+h] = true
			hints = append(hints, h)
		}
	}
	if busyOnly {
		return out, false
	}
	sort.Strings(parts)
	out.Reason = "every candidate was filtered: " + strings.Join(parts, "; ")
	out.Hint = strings.Join(hints, "; ")
	return out, true
}

// noteNoCandidate writes EventRouteNoCandidate when card's explanation changed.
func (r *Runner) noteNoCandidate(ctx context.Context, db *store.Store, card model.Card, obj model.Objective, channels []model.Channel, pos []model.Positioning, inUse map[string]int, quotas route.Quotas) {
	if r == nil || db == nil || ctx.Err() != nil {
		return
	}
	ex, ok := explainNoCandidate(ctx, card, obj, channels, pos, inUse, quotas, r.now())
	if !ok {
		return
	}
	key := ex.Reason + "\x00" + ex.Hint
	r.tickMu.Lock()
	if r.noCand == nil {
		r.noCand = map[string]string{}
	}
	if r.noCand[card.ID] == key {
		r.tickMu.Unlock()
		return
	}
	r.noCand[card.ID] = key
	r.tickMu.Unlock()
	reportServeEvent(ctx, "transition state=route_unavailable")
	fmt.Fprintf(os.Stderr, "route_no_candidate card=%s tier=%s reason=%s hint=%s\n", ex.Card, ex.Tier, ex.Reason, ex.Hint)
	data, err := json.Marshal(ex)
	if err != nil {
		return
	}
	_ = db.AppendEvent(ctx, &model.Event{TS: r.now().UTC(), Entity: "card:" + card.ID, Type: EventRouteNoCandidate, Data: data})
}

// ensureStarter offers the starter positioning proposal on a fresh install,
// at most once per starterCheckWindow. It never approves anything.
func (r *Runner) ensureStarter(ctx context.Context, db *store.Store, channels []model.Channel, pos []model.Positioning) {
	if r == nil || r.Home == "" || db == nil || hasModelRows(pos) || len(channels) == 0 {
		return
	}
	now := r.now()
	r.tickMu.Lock()
	if !r.starterAt.IsZero() && now.Sub(r.starterAt) < starterCheckWindow {
		r.tickMu.Unlock()
		return
	}
	r.starterAt = now
	r.tickMu.Unlock()
	h, err := positioning.Open(r.Home)
	if err != nil {
		return
	}
	defer h.Close()
	p, created, ok, err := h.EnsureStarter(ctx, channels)
	if err != nil || !ok || !created {
		return
	}
	fmt.Fprintf(os.Stderr, "positioning: no table is approved; proposed starter %s. approve it: cardex positioning approve %s\n", p.ID, p.ID)
	data, _ := json.Marshal(map[string]any{"proposal": p.ID, "entries": len(p.Entries), "approve": "cardex positioning approve " + p.ID})
	_ = db.AppendEvent(ctx, &model.Event{TS: now.UTC(), Entity: "positioning", Type: EventPositioningStarter, Data: data})
}

// missingCaps lists card requirements that no approved row offered by any
// channel provides. "browser" is met by the host and never missing.
func missingCaps(need []string, channels []model.Channel, pos []model.Positioning) []string {
	offered := map[string]bool{}
	for _, ch := range channels {
		for _, m := range ch.Models {
			offered[m] = true
		}
	}
	have := map[string]bool{}
	for _, row := range pos {
		if row.Model == "" || !offered[row.Model] {
			continue
		}
		for _, c := range row.Capabilities {
			have[c] = true
		}
	}
	var missing []string
	for _, c := range need {
		if c == "browser" || have[c] {
			continue
		}
		missing = append(missing, c)
	}
	return missing
}
