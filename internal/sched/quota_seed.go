package sched

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"cardex/internal/model"
	"cardex/internal/quota"
)

// Snapshot records are ordinary ledger events, and an event is counted by every
// open window that covers its time. Each window's used count is therefore placed
// on a time inside that window and outside any shorter window already filled,
// so a larger window cannot broadcast its total onto a smaller limit.

type snapSpan struct {
	start  time.Time
	end    time.Time
	used   int64
	remain int64
}

type snapEvent struct {
	at     time.Time
	tokens int64
}

type quotaHit struct {
	observed time.Time
	reset    time.Time
}

func recordSnapshot(ctx context.Context, led *quota.Ledger, ch model.Channel, now time.Time) error {
	var spans []snapSpan
	for _, w := range ch.QuotaWindows {
		if w.LimitTokens <= 0 || w.UsedTokens <= 0 {
			continue
		}
		if !w.ResetAt.IsZero() && !now.Before(w.ResetAt) {
			continue
		}
		start, end, ok := ledgerBounds(w.Name, w.ResetRule, w.ResetAt, now)
		if !ok {
			continue
		}
		spans = append(spans, snapSpan{start: start, end: end, used: w.UsedTokens})
	}
	for _, ev := range attributeSnapshots(spans, now) {
		usage := model.Usage{Tokens: ev.tokens, Source: "snapshot", Confidence: 1}
		if err := led.Record(ctx, ch.ID, usage, ev.at, ev.at); err != nil {
			return err
		}
	}
	return nil
}

func attributeSnapshots(spans []snapSpan, now time.Time) []snapEvent {
	if len(spans) == 0 {
		return nil
	}
	order := make([]int, len(spans))
	for i := range spans {
		order[i] = i
		spans[i].remain = spans[i].used
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		ca, cb := containedBy(spans, ia), containedBy(spans, ib)
		if ca != cb {
			return ca > cb
		}
		return spans[ia].end.Sub(spans[ia].start) < spans[ib].end.Sub(spans[ib].start)
	})
	var out []snapEvent
	for _, i := range order {
		charge := spans[i].remain
		if charge <= 0 {
			continue
		}
		at, ok := feasibleTime(spans, i, charge, now)
		if !ok {
			continue
		}
		out = append(out, snapEvent{at: at, tokens: charge})
		for j := range spans {
			if spanContains(spans[j], at) {
				spans[j].remain -= charge
			}
		}
	}
	return out
}

func containedBy(spans []snapSpan, i int) int {
	n := 0
	di := spans[i].end.Sub(spans[i].start)
	for j := range spans {
		if i == j {
			continue
		}
		dj := spans[j].end.Sub(spans[j].start)
		if dj <= di {
			continue
		}
		if !spans[i].start.Before(spans[j].start) && !spans[j].end.Before(spans[i].end) {
			n++
		}
	}
	return n
}

func feasibleTime(spans []snapSpan, i int, charge int64, now time.Time) (time.Time, bool) {
	target := spans[i]
	cands := []time.Time{now, target.start, target.end.Add(-time.Nanosecond)}
	for j := range spans {
		if j == i {
			continue
		}
		cands = append(cands, spans[j].end, spans[j].start.Add(-time.Nanosecond))
	}
	var fallback time.Time
	found := false
	for _, c := range cands {
		if !spanContains(target, c) {
			continue
		}
		if !found {
			fallback = c
			found = true
		}
		affordable := true
		for j := range spans {
			if j == i {
				continue
			}
			if spanContains(spans[j], c) && spans[j].remain < charge {
				affordable = false
				break
			}
		}
		if affordable {
			return c, true
		}
	}
	if found {
		return fallback, true
	}
	return time.Time{}, false
}

func spanContains(s snapSpan, t time.Time) bool {
	return !t.Before(s.start) && t.Before(s.end)
}

// effectiveQuotaBlock keeps the latest still-valid reset. An expired reset
// cannot replace it, and attempt ID order is not the order of observations.
func effectiveQuotaBlock(hits []quotaHit, now time.Time) (time.Time, bool) {
	var best, bestAt time.Time
	found := false
	for _, h := range hits {
		if !h.reset.After(now) {
			continue
		}
		if !found || h.observed.After(bestAt) || (h.observed.Equal(bestAt) && h.reset.After(best)) {
			found = true
			best = h.reset
			bestAt = h.observed
		}
	}
	return best, found
}

var resetAssoc = regexp.MustCompile(`(?i)\bresets?(?:[ \t_.:='"\-]{0,12}(?:at|time|timestamp))?[ \t_.:='"\-]{0,12}(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))`)

// findReset returns a timestamp that is written as a reset. An event time in
// the same text is not a reset.
func findReset(text string) (time.Time, bool) {
	m := resetAssoc.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, m[1]); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, m[1]); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// ledgerBounds is the open interval the quota ledger uses for this window.
// Schedules cannot name a window on Record, so the snapshot time has to fall
// inside that interval and outside the windows that must not receive it.
func ledgerBounds(name, rule string, reset, now time.Time) (time.Time, time.Time, bool) {
	canon := canonWindow(name)
	if canon == "" {
		return time.Time{}, time.Time{}, false
	}
	now = now.UTC()
	anchor := reset.UTC()
	var start, end time.Time
	if canonRuleFixed(rule) {
		start, end = fixedWindow(canon, anchor, now)
	} else {
		start, end = rollingWindow(canon, anchor, now)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

func canonWindow(name string) string {
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

func canonRuleFixed(rule string) bool {
	switch strings.ToLower(strings.TrimSpace(rule)) {
	case "fixed", "fixed_instant", "fixed-instant", "clock", "固定", "固定时刻":
		return true
	default:
		return false
	}
}

func fixedWindow(name string, anchor, now time.Time) (time.Time, time.Time) {
	if anchor.IsZero() {
		anchor = time.Unix(0, 0).UTC()
	}
	switch name {
	case "daily":
		return fixedDailyWindow(anchor, now)
	case "weekly":
		return fixedWeeklyWindow(anchor, now)
	case "monthly":
		return fixedMonthlyWindow(anchor, now)
	default:
		return alignWindow(anchor, now, 5*time.Hour)
	}
}

func fixedDailyWindow(anchor, now time.Time) (time.Time, time.Time) {
	reset := time.Date(now.Year(), now.Month(), now.Day(), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
	if now.Before(reset) {
		return reset.AddDate(0, 0, -1), reset
	}
	return reset, reset.AddDate(0, 0, 1)
}

func fixedWeeklyWindow(anchor, now time.Time) (time.Time, time.Time) {
	delta := (int(now.Weekday()) - int(anchor.Weekday()) + 7) % 7
	start := time.Date(now.Year(), now.Month(), now.Day(), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC).AddDate(0, 0, -delta)
	if now.Before(start) {
		start = start.AddDate(0, 0, -7)
	}
	return start, start.AddDate(0, 0, 7)
}

func fixedMonthlyWindow(anchor, now time.Time) (time.Time, time.Time) {
	start := monthPoint(now.Year(), now.Month(), anchor)
	if now.Before(start) {
		prev := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
		start = monthPoint(prev.Year(), prev.Month(), anchor)
	}
	next := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	return start, monthPoint(next.Year(), next.Month(), anchor)
}

func monthPoint(year int, month time.Month, anchor time.Time) time.Time {
	return time.Date(year, month, clampMonthDay(year, month, anchor.Day()), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
}

func clampMonthDay(year int, month time.Month, day int) int {
	if day < 1 {
		return 1
	}
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		return last
	}
	return day
}

func alignWindow(anchor, now time.Time, dur time.Duration) (time.Time, time.Time) {
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

func rollingWindow(name string, anchor, now time.Time) (time.Time, time.Time) {
	if anchor.IsZero() {
		anchor = time.Unix(0, 0).UTC()
	}
	if name == "monthly" {
		return rollingMonthWindow(anchor, now)
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

func rollingMonthWindow(anchor, now time.Time) (time.Time, time.Time) {
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
