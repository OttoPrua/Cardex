package quota

import (
	"strings"
	"time"
)

type windowSpec struct {
	Name     string
	Rule     string
	Anchor   time.Time
	Limit    int64
	Requests int64
}

func canonName(name string) string {
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

func canonRule(rule string) string {
	switch strings.ToLower(strings.TrimSpace(rule)) {
	case "fixed", "fixed_instant", "fixed-instant", "clock", "固定", "固定时刻":
		return "fixed"
	default:
		return "rolling"
	}
}

func (w windowSpec) bounds(now time.Time) (start, end time.Time, ok bool) {
	name := canonName(w.Name)
	if name == "" || (w.Limit <= 0 && w.Requests <= 0) {
		return time.Time{}, time.Time{}, false
	}
	now = now.UTC()
	anchor := w.Anchor.UTC()
	if w.Rule == "fixed" {
		start, end = fixedBounds(name, anchor, now)
	} else {
		start, end = rollingBounds(name, anchor, now)
	}
	return start, end, end.After(start)
}

func fixedBounds(name string, anchor, now time.Time) (time.Time, time.Time) {
	if anchor.IsZero() {
		anchor = time.Unix(0, 0).UTC()
	}
	switch name {
	case "daily":
		return fixedDaily(anchor, now)
	case "weekly":
		return fixedWeekly(anchor, now)
	case "monthly":
		return fixedMonthly(anchor, now)
	default:
		return alignDuration(anchor, now, 5*time.Hour)
	}
}

func fixedDaily(anchor, now time.Time) (time.Time, time.Time) {
	reset := time.Date(now.Year(), now.Month(), now.Day(), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
	if now.Before(reset) {
		return reset.AddDate(0, 0, -1), reset
	}
	return reset, reset.AddDate(0, 0, 1)
}

func fixedWeekly(anchor, now time.Time) (time.Time, time.Time) {
	delta := (int(now.Weekday()) - int(anchor.Weekday()) + 7) % 7
	start := time.Date(now.Year(), now.Month(), now.Day(), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC).AddDate(0, 0, -delta)
	if now.Before(start) {
		start = start.AddDate(0, 0, -7)
	}
	return start, start.AddDate(0, 0, 7)
}

func fixedMonthly(anchor, now time.Time) (time.Time, time.Time) {
	start := monthlyPoint(now.Year(), now.Month(), anchor)
	if now.Before(start) {
		prev := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
		start = monthlyPoint(prev.Year(), prev.Month(), anchor)
	}
	next := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	return start, monthlyPoint(next.Year(), next.Month(), anchor)
}

func monthlyPoint(year int, month time.Month, anchor time.Time) time.Time {
	return time.Date(year, month, clampDay(year, month, anchor.Day()), anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC)
}

func clampDay(year int, month time.Month, day int) int {
	if day < 1 {
		return 1
	}
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		return last
	}
	return day
}

func alignDuration(anchor, now time.Time, dur time.Duration) (time.Time, time.Time) {
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

func rollingBounds(name string, anchor, now time.Time) (time.Time, time.Time) {
	if anchor.IsZero() {
		anchor = time.Unix(0, 0).UTC()
	}
	if name == "monthly" {
		return rollingMonthly(anchor, now)
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

func rollingMonthly(anchor, now time.Time) (time.Time, time.Time) {
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
