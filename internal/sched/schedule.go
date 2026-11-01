package sched

import (
	"strconv"
	"strings"
	"time"
)

// cron-lite is a five-field expression: minute hour day-of-month month
// day-of-week. Each field is * or one integer. Sunday is 0. There are no
// ranges, lists, steps, or names.
func cronDue(expr string, now time.Time) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	return cronField(fields[0], now.Minute()) &&
		cronField(fields[1], now.Hour()) &&
		cronField(fields[2], now.Day()) &&
		cronField(fields[3], int(now.Month())) &&
		cronField(fields[4], int(now.Weekday()))
}

func cronField(field string, val int) bool {
	if field == "*" {
		return true
	}
	n, err := strconv.Atoi(field)
	if err != nil {
		return false
	}
	return n == val
}
