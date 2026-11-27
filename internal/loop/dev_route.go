package loop

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/quota"
	"github.com/OttoPrua/Cardex/internal/route"
	"github.com/OttoPrua/Cardex/internal/store"
)

// routeRequestFrom assembles the facts DevStep uses for an automatic channel
// choice. deciding clears card and objective pins on the probe so the choice
// itself is not an explicit pin. The readback projection distinguishes
// recorded prior pins and excludes the active chain's failed carriers. The
// scheduler separately preserves stored pins until their ordinary release.
// An empty positioning table supplies unsaved rows from each channel's own
// tier and models; those rows are not written to the store.
func routeRequestFrom(ctx context.Context, db *store.Store, card model.Card, objective model.Objective, now time.Time, failed string, deciding bool) (route.Request, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	channels, err := db.ListChannels(ctx)
	if err != nil {
		return route.Request{}, err
	}
	rows, err := db.ListPositioning(ctx)
	if err != nil {
		return route.Request{}, err
	}
	rows = rowsWithModel(rows)
	if len(rows) == 0 {
		channels, rows = candidatesFromChannels(channels)
	}
	inUse, attempts, err := globalOccupancy(ctx, db)
	if err != nil {
		return route.Request{}, err
	}
	events, err := db.ListEvents(ctx, "")
	if err != nil {
		return route.Request{}, err
	}
	led := quota.New()
	if err := fillRouteQuota(ctx, led, channels, attempts, now); err != nil {
		return route.Request{}, err
	}
	if err := RestoreQuota(ctx, led, events); err != nil {
		return route.Request{}, err
	}
	probe := card
	obj := objective
	after := lastPassingAttempt(attempts, card.ID)
	auto := latestAutoChoice(events, card.ID, after)
	excluded := autoExcludedSince(events, card.ID, after)
	if deciding {
		probe.Channel = ""
		probe.Model = ""
		obj.Channel = ""
		obj.Model = ""
		if failed != "" {
			excluded[failed] = true
		}
	} else {
		if failed == "" {
			failed = auto.exclude
		}
		if failed != "" {
			excluded[failed] = true
		}
		// The channel written by this switch is the automatic choice. Put a
		// user pin back only when that channel has not failed on this card,
		// including a failure from an earlier automatic switch.
		if auto.channel != "" && probe.Channel == auto.channel {
			probe.Channel = ""
			if auto.pin != "" && !excluded[auto.pin] {
				probe.Channel = auto.pin
			}
		}
		// A channel written after the automatic switch stays, even when an
		// earlier exclude names it. Only the switch's own channel is cleared.
		postEvent := auto.channel != "" && probe.Channel != "" && probe.Channel != auto.channel
		if !postEvent && excluded[probe.Channel] {
			probe.Channel = ""
		}
		if auto.hasObjChannel {
			if obj.Channel != "" && obj.Channel == auto.objectiveChannel && excluded[obj.Channel] {
				obj.Channel = ""
				if !auto.hasObjModel || obj.Model == auto.objectiveModel {
					obj.Model = ""
				}
			}
		} else if excluded[obj.Channel] {
			obj.Channel = ""
		}
	}
	omit := ""
	if !deciding {
		omit = failed
	}
	if !deciding && auto.channel != "" {
		if probe.Channel != "" && probe.Channel != auto.channel && omit == probe.Channel {
			omit = ""
		}
		if obj.Channel != "" && auto.hasObjChannel && obj.Channel != auto.objectiveChannel && omit == obj.Channel {
			omit = ""
		}
	}
	// A model written after the switch keeps every channel that can serve it,
	// including one this card's earlier failure removed.
	if !deciding && omit != "" {
		if name := laterPinnedModel(probe, obj, auto); name != "" && channelsOffer(channels, omit, name) {
			omit = ""
		}
	}
	return route.Request{
		Card:          probe,
		Objective:     obj,
		AuthorFamily:  card.AuthorFamily,
		Channels:      withoutChannel(channels, omit),
		Positioning:   rows,
		InUse:         inUse,
		Quota:         led,
		Now:           now,
		AvoidChannels: excluded,
	}, nil
}

// AutomaticFallbackExclusions returns the active chain's exclusions and event
// ID. It is only used after an unusable pin was released: a viable stored
// channel cannot distinguish an automatic suggestion from a same-valued pin.
func AutomaticFallbackExclusions(ctx context.Context, db *store.Store, card model.Card) (excluded map[string]bool, origin string, err error) {
	if card.Channel == "" {
		return nil, "", nil
	}
	events, err := db.ListEvents(ctx, "card:"+card.ID)
	if err != nil {
		return nil, "", err
	}
	attempts, err := db.ListAttempts(ctx, card.ID)
	if err != nil {
		return nil, "", err
	}
	after := lastPassingAttempt(attempts, card.ID)
	auto := latestAutoChoice(events, card.ID, after)
	if auto.exclude == "" || !matchesAutomaticTarget(events, card, auto, after) {
		return nil, "", nil
	}
	// Validate the switch against its cause, not the newest attempt. An
	// ordinary same-channel retry does not end the automatic failure chain.
	for _, a := range attempts {
		if a.Route.Channel != auto.exclude || a.EndedAt.IsZero() ||
			(!after.IsZero() && !a.EndedAt.After(after)) ||
			(auto.attempt == "" && a.EndedAt.After(auto.at)) || (auto.attempt != "" && a.ID != auto.attempt) {
			continue
		}
		switch a.Outcome {
		case model.OutcomeNoOutput, model.OutcomeQuotaHit, model.OutcomeAuthFailed, model.OutcomeUnavailable,
			model.OutcomeTransport, model.OutcomeTimeout:
			excluded := autoExcludedSince(events, card.ID, after)
			excluded[card.Channel] = true
			return excluded, auto.id, nil
		}
	}
	return nil, "", nil
}

type hardAutoRelease struct {
	Origin     string `json:"failover_origin"`
	PinChannel string `json:"pin_channel"`
	PinModel   string `json:"pin_model"`
	Channel    string `json:"channel"`
	Model      string `json:"model"`
}

func activeRelease(event model.Event, cardID string, after time.Time) (hardAutoRelease, bool) {
	var release hardAutoRelease
	ok := event.Type == "pin_released" && event.Entity == "card:"+cardID &&
		(after.IsZero() || event.TS.After(after)) && json.Unmarshal(event.Data, &release) == nil &&
		release.Origin != "" && release.PinChannel != "" && release.Channel != "" && release.PinChannel != release.Channel
	return release, ok
}

// Each durable hard release links the previous effective pin to the route
// actually written with it. Use links rather than timestamps for the chain's
// order, since callers can share the same clock value.
func matchesAutomaticTarget(events []model.Event, card model.Card, auto autoChoice, after time.Time) bool {
	if auto.channel == "" {
		return false
	}
	next := map[model.Route]model.Route{}
	for _, event := range events {
		if release, ok := activeRelease(event, card.ID, after); ok && release.Origin == auto.id {
			next[model.Route{Channel: release.PinChannel, Model: release.PinModel}] = model.Route{Channel: release.Channel, Model: release.Model}
		}
	}
	target := model.Route{Channel: auto.channel}
	for i := 0; i <= len(next); i++ {
		step, ok := next[target]
		if !ok {
			return target == (model.Route{Channel: card.Channel, Model: card.Model})
		}
		target = step
	}
	return false
}

func globalOccupancy(ctx context.Context, db *store.Store) (map[string]int, []model.Attempt, error) {
	cards, err := db.ListCards(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	inUse := map[string]int{}
	seen := map[string]bool{}
	var attempts []model.Attempt
	for _, card := range cards {
		if card.Status == model.CardRunning && card.Channel != "" {
			inUse[card.Channel]++
		}
		list, err := db.ListAttempts(ctx, card.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, attempt := range list {
			if attempt.ID == "" || seen[attempt.ID] {
				continue
			}
			seen[attempt.ID] = true
			attempts = append(attempts, attempt)
		}
	}
	return inUse, attempts, nil
}

// withoutChannel preserves the readback projection's omission of the most
// recent failed channel; AvoidChannels filters other failed carriers.
func withoutChannel(channels []model.Channel, failed string) []model.Channel {
	if failed == "" {
		out := append([]model.Channel(nil), channels...)
		return out
	}
	out := make([]model.Channel, 0, len(channels))
	for _, ch := range channels {
		if ch.ID == failed {
			continue
		}
		out = append(out, ch)
	}
	return out
}

type autoChoice struct {
	id               string
	at               time.Time
	attempt          string
	channel          string
	pin              string
	exclude          string
	modelPin         string
	hasModelPin      bool
	objectiveChannel string
	hasObjChannel    bool
	objectiveModel   string
	hasObjModel      bool
}

func latestAutoChoice(events []model.Event, cardID string, after time.Time) autoChoice {
	var last autoChoice
	for _, event := range events {
		if !activeAutoRoute(event, cardID, after) {
			continue
		}
		var data struct {
			Channel          string  `json:"channel"`
			Pin              string  `json:"pin"`
			Exclude          string  `json:"exclude"`
			Attempt          string  `json:"attempt"`
			ModelPin         *string `json:"model_pin"`
			ObjectiveChannel *string `json:"objective_channel"`
			ObjectiveModel   *string `json:"objective_model"`
		}
		if json.Unmarshal(event.Data, &data) != nil {
			continue
		}
		last = autoChoice{id: event.ID, at: event.TS, attempt: data.Attempt, channel: data.Channel, pin: data.Pin, exclude: data.Exclude}
		if data.ModelPin != nil {
			last.modelPin = *data.ModelPin
			last.hasModelPin = true
		}
		if data.ObjectiveChannel != nil {
			last.objectiveChannel = *data.ObjectiveChannel
			last.hasObjChannel = true
		}
		if data.ObjectiveModel != nil {
			last.objectiveModel = *data.ObjectiveModel
			last.hasObjModel = true
		}
	}
	return last
}

// laterPinnedModel is a Model assignment written after the latest automatic
// switch. The model recorded on that switch is the earlier pin, not a new one.
// A card model is the one route.Select will use, so an unchanged card model
// does not fall through to the objective.
func laterPinnedModel(card model.Card, obj model.Objective, auto autoChoice) string {
	if auto.channel == "" {
		return ""
	}
	if strings.TrimSpace(card.Model) != "" {
		if auto.hasModelPin && strings.TrimSpace(card.Model) != auto.modelPin {
			return strings.TrimSpace(card.Model)
		}
		return ""
	}
	if auto.hasObjModel {
		if m := strings.TrimSpace(obj.Model); m != "" && m != auto.objectiveModel {
			return m
		}
	}
	return ""
}

func channelsOffer(channels []model.Channel, id, modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return false
	}
	for _, ch := range channels {
		if ch.ID != id {
			continue
		}
		for _, name := range ch.Models {
			if name == modelName || surfaceModel(name) == modelName {
				return true
			}
		}
	}
	return false
}

func activeAutoRoute(event model.Event, cardID string, after time.Time) bool {
	return event.Type == "auto_route" && event.Entity == "card:"+cardID && (after.IsZero() || event.TS.After(after))
}

// Later switches keep suppressing the earlier failed carriers of this chain,
// not just the most recently failed one.
func autoExcludedSince(events []model.Event, cardID string, after time.Time) map[string]bool {
	out := map[string]bool{}
	roots := map[string]bool{}
	for _, event := range events {
		if !activeAutoRoute(event, cardID, after) {
			continue
		}
		var data struct {
			Exclude string `json:"exclude"`
		}
		if json.Unmarshal(event.Data, &data) != nil || data.Exclude == "" {
			continue
		}
		out[data.Exclude] = true
		roots[event.ID] = true
	}
	for _, event := range events {
		if release, ok := activeRelease(event, cardID, after); ok && roots[release.Origin] {
			out[release.PinChannel] = true
		}
	}
	return out
}

// A successful attempt ends the automatic failure chain. Later rework does
// not inherit the transient exclusions of an attempt that has already passed.
func lastPassingAttempt(attempts []model.Attempt, cardID string) time.Time {
	var at time.Time
	for _, attempt := range attempts {
		if attempt.Card == cardID && attempt.Outcome == model.OutcomePassed && attempt.EndedAt.After(at) {
			at = attempt.EndedAt
		}
	}
	return at
}

// keptPin is the user pin to remember across an automatic write. A channel
// stored by the previous auto_route is not itself a user pin. A pin that
// names a channel an earlier automatic switch already failed is not kept.
func keptPin(events []model.Event, card model.Card, after time.Time) string {
	auto := latestAutoChoice(events, card.ID, after)
	pin := card.Channel
	if matchesAutomaticTarget(events, card, auto, after) {
		pin = auto.pin
	}
	if autoExcludedSince(events, card.ID, after)[pin] {
		return ""
	}
	return pin
}

// modelChannelSep aliases a model name that several channels offer, so one
// channel's tier cannot qualify another channel.
const modelChannelSep = "\x1f"

// candidatesFromChannels builds selector rows from each channel's own tier
// and models. Shared model names are aliased per channel. Nothing is stored.
func candidatesFromChannels(channels []model.Channel) ([]model.Channel, []model.Positioning) {
	counts := map[string]int{}
	for _, ch := range channels {
		for _, name := range uniqueModels(ch.Models) {
			counts[name]++
		}
	}
	bound := make([]model.Channel, len(channels))
	var rows []model.Positioning
	for i, ch := range channels {
		bound[i] = ch
		names := uniqueModels(ch.Models)
		offered := make([]string, 0, len(names))
		for _, name := range names {
			label := name
			if counts[name] > 1 {
				label = name + modelChannelSep + ch.ID
			}
			offered = append(offered, label)
			if ch.Tier == "" || ch.Tier == model.TierNone {
				continue
			}
			rows = append(rows, model.Positioning{
				Model: label,
				Tiers: []model.Tier{ch.Tier},
			})
		}
		bound[i].Models = offered
	}
	return bound, rows
}

func uniqueModels(models []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range models {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func surfaceModel(name string) string {
	if i := strings.Index(name, modelChannelSep); i >= 0 {
		return name[:i]
	}
	return name
}

type tokenAt struct {
	at     time.Time
	tokens int64
}

// fillRouteQuota records each attempt once, at its own end time, then adds
// only the part of a window snapshot that those attempts do not already
// cover. Overlapping snapshots are not stamped at now, so a 5h window and a
// daily window are not added together into the shorter one.
func fillRouteQuota(ctx context.Context, led *quota.Ledger, channels []model.Channel, attempts []model.Attempt, now time.Time) error {
	led.SetClock(func() time.Time { return now })
	for _, ch := range channels {
		led.Configure(ch)
	}
	byChannel := map[string][]tokenAt{}
	var recorded []struct {
		channel string
		tokenAt
	}
	for _, attempt := range attempts {
		if attempt.Route.Channel == "" || attempt.Usage.Tokens <= 0 {
			continue
		}
		ended := attempt.EndedAt
		if ended.IsZero() {
			ended = attempt.StartedAt
		}
		if ended.IsZero() {
			ended = now
		}
		item := tokenAt{at: ended.UTC(), tokens: attempt.Usage.Tokens}
		byChannel[attempt.Route.Channel] = append(byChannel[attempt.Route.Channel], item)
		recorded = append(recorded, struct {
			channel string
			tokenAt
		}{attempt.Route.Channel, item})
	}
	for _, ch := range channels {
		for _, extra := range windowResidual(ch, byChannel[ch.ID], now) {
			recorded = append(recorded, struct {
				channel string
				tokenAt
			}{ch.ID, extra})
		}
	}
	for _, item := range recorded {
		usage := model.Usage{Tokens: item.tokens, Source: "route"}
		if err := led.Record(ctx, item.channel, usage, item.at, item.at); err != nil {
			return err
		}
	}
	return nil
}

type windowSpan struct {
	start time.Time
	end   time.Time
	used  int64
}

func windowResidual(ch model.Channel, attempts []tokenAt, now time.Time) []tokenAt {
	var spans []windowSpan
	for _, window := range ch.QuotaWindows {
		start, end, ok := qBounds(window, now)
		if !ok || window.UsedTokens <= 0 {
			continue
		}
		spans = append(spans, windowSpan{start: start, end: end, used: window.UsedTokens})
	}
	sortSpans(spans)
	var placed []tokenAt
	for i, span := range spans {
		already := covered(span, attempts, placed)
		need := span.used - already
		if need <= 0 {
			continue
		}
		shorter := make([]windowSpan, i)
		copy(shorter, spans[:i])
		at, ok := placeOutside(span, shorter)
		if !ok {
			continue
		}
		placed = append(placed, tokenAt{at: at, tokens: need})
	}
	return placed
}

func covered(span windowSpan, attempts, placed []tokenAt) int64 {
	var n int64
	for _, item := range attempts {
		if inSpan(item.at, span) {
			n += item.tokens
		}
	}
	for _, item := range placed {
		if inSpan(item.at, span) {
			n += item.tokens
		}
	}
	return n
}

func inSpan(at time.Time, span windowSpan) bool {
	return !at.Before(span.start) && at.Before(span.end)
}

func sortSpans(spans []windowSpan) {
	for i := 1; i < len(spans); i++ {
		j := i
		for j > 0 && spanBefore(spans[j], spans[j-1]) {
			spans[j], spans[j-1] = spans[j-1], spans[j]
			j--
		}
	}
}

func spanBefore(a, b windowSpan) bool {
	da := a.end.Sub(a.start)
	db := b.end.Sub(b.start)
	if da != db {
		return da < db
	}
	return a.start.After(b.start)
}

// placeOutside picks a time inside span and outside every shorter span so
// the residual is not added into a tighter overlapping window.
func placeOutside(span windowSpan, shorter []windowSpan) (time.Time, bool) {
	t := span.start
	for t.Before(span.end) {
		var jump time.Time
		for _, other := range shorter {
			if inSpan(t, other) && other.end.After(jump) {
				jump = other.end
			}
		}
		if jump.IsZero() {
			return t, true
		}
		t = jump
	}
	return time.Time{}, false
}

func qBounds(w model.QuotaWindow, now time.Time) (time.Time, time.Time, bool) {
	name := qCanonName(w.Name)
	if name == "" || (w.LimitTokens <= 0 && w.LimitRequests <= 0) {
		return time.Time{}, time.Time{}, false
	}
	now = now.UTC()
	anchor := w.ResetAt.UTC()
	var start, end time.Time
	if qFixed(w.ResetRule) {
		start, end = qFixedBounds(name, anchor, now)
	} else {
		start, end = qRollingBounds(name, anchor, now)
	}
	return start, end, end.After(start)
}

func qCanonName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "5h", "5hr", "5hour", "5hours":
		return "5h"
	case "daily", "day":
		return "daily"
	case "weekly", "week":
		return "weekly"
	case "monthly", "month":
		return "monthly"
	default:
		return ""
	}
}

func qFixed(rule string) bool {
	switch strings.ToLower(strings.TrimSpace(rule)) {
	case "fixed", "fixed_instant", "fixed-instant", "clock", "固定", "固定时刻":
		return true
	default:
		return false
	}
}

func qFixedBounds(name string, anchor, now time.Time) (time.Time, time.Time) {
	if anchor.IsZero() {
		anchor = time.Unix(0, 0).UTC()
	}
	switch name {
	case "daily":
		return qFixedDaily(anchor, now)
	case "weekly":
		return qFixedWeekly(anchor, now)
	case "monthly":
		return qFixedMonthly(anchor, now)
	default:
		return qAlign(anchor, now, 5*time.Hour)
	}
}

func qFixedDaily(anchor, now time.Time) (time.Time, time.Time) {
	reset := time.Date(now.Year(), now.Month(), now.Day(), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
	if now.Before(reset) {
		return reset.AddDate(0, 0, -1), reset
	}
	return reset, reset.AddDate(0, 0, 1)
}

func qFixedWeekly(anchor, now time.Time) (time.Time, time.Time) {
	delta := (int(now.Weekday()) - int(anchor.Weekday()) + 7) % 7
	start := time.Date(now.Year(), now.Month(), now.Day(), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC).AddDate(0, 0, -delta)
	if now.Before(start) {
		start = start.AddDate(0, 0, -7)
	}
	return start, start.AddDate(0, 0, 7)
}

func qFixedMonthly(anchor, now time.Time) (time.Time, time.Time) {
	start := qMonthlyPoint(now.Year(), now.Month(), anchor)
	if now.Before(start) {
		prev := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
		start = qMonthlyPoint(prev.Year(), prev.Month(), anchor)
	}
	next := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	return start, qMonthlyPoint(next.Year(), next.Month(), anchor)
}

func qMonthlyPoint(year int, month time.Month, anchor time.Time) time.Time {
	return time.Date(year, month, qClampDay(year, month, anchor.Day()), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
}

func qClampDay(year int, month time.Month, day int) int {
	if day < 1 {
		return 1
	}
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		return last
	}
	return day
}

func qAlign(anchor, now time.Time, dur time.Duration) (time.Time, time.Time) {
	if dur <= 0 {
		dur = 5 * time.Hour
	}
	var start time.Time
	if !now.Before(anchor) {
		steps := int64(now.Sub(anchor) / dur)
		start = anchor.Add(time.Duration(steps) * dur)
	} else {
		steps := int64((anchor.Sub(now) + dur - 1) / dur)
		start = anchor.Add(-time.Duration(steps) * dur)
	}
	for now.Before(start) {
		start = start.Add(-dur)
	}
	for !now.Before(start.Add(dur)) {
		start = start.Add(dur)
	}
	return start, start.Add(dur)
}

func qRollingBounds(name string, anchor, now time.Time) (time.Time, time.Time) {
	if anchor.IsZero() {
		anchor = time.Unix(0, 0).UTC()
	}
	if name == "monthly" {
		return qRollingMonthly(anchor, now)
	}
	dur := 5 * time.Hour
	switch name {
	case "daily":
		dur = 24 * time.Hour
	case "weekly":
		dur = 7 * 24 * time.Hour
	}
	end := anchor
	if now.Before(end) {
		if end.Sub(now) > dur {
			back := int64(end.Sub(now) / dur)
			end = end.Add(-time.Duration(back) * dur)
			if !now.Before(end) {
				end = end.Add(dur)
			}
		}
	} else {
		ahead := int64(now.Sub(end)/dur) + 1
		end = end.Add(time.Duration(ahead) * dur)
	}
	start := end.Add(-dur)
	if now.Before(start) {
		return start.Add(-dur), start
	}
	if !now.Before(end) {
		return end, end.Add(dur)
	}
	return start, end
}

func qRollingMonthly(anchor, now time.Time) (time.Time, time.Time) {
	end := anchor
	for i := 0; i < 4800 && !now.Before(end); i++ {
		next := end.AddDate(0, 1, 0)
		if !next.After(end) {
			break
		}
		end = next
	}
	start := end.AddDate(0, -1, 0)
	for i := 0; i < 4800 && now.Before(start); i++ {
		end = start
		start = end.AddDate(0, -1, 0)
	}
	return start, end
}
