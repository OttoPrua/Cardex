package loop

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"cardex/internal/model"
	"cardex/internal/quota"
	"cardex/internal/route"
	"cardex/internal/store"
)

// routeRequestFrom assembles the facts DevStep uses for an automatic channel
// choice. deciding clears card and objective pins on the probe so the choice
// itself is not an explicit pin. A later call leaves those fields as stored
// and drops the failed channel unless that stored pin names it.
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
	if deciding {
		probe.Channel = ""
		probe.Model = ""
		obj.Channel = ""
		obj.Model = ""
	} else if failed == "" {
		failed = latestExclude(events, card.ID)
	}
	pin := probe.Channel
	if pin == "" {
		pin = obj.Channel
	}
	return route.Request{
		Card:         probe,
		Objective:    obj,
		AuthorFamily: card.AuthorFamily,
		Channels:     withoutChannel(channels, failed, pin),
		Positioning:  rows,
		InUse:        inUse,
		Quota:        led,
		Now:          now,
	}, nil
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

// withoutChannel drops failed unless the stored pin is that channel.
// route.Select has no exclude field; omitting it from Channels is the
// exclusion the current request type can express.
func withoutChannel(channels []model.Channel, failed, pin string) []model.Channel {
	if failed == "" || failed == pin {
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

func latestExclude(events []model.Event, cardID string) string {
	exclude := ""
	for _, event := range events {
		if event.Type != "auto_route" || event.Entity != "card:"+cardID {
			continue
		}
		var data struct {
			Exclude string `json:"exclude"`
		}
		if json.Unmarshal(event.Data, &data) != nil {
			continue
		}
		exclude = data.Exclude
	}
	return exclude
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
