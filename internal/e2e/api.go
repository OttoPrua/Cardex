// Package e2e holds the R1 harness and the skipped S01–S24 scenarios.
package e2e

import "time"

// RemainingWindow is the quota window recorded for a fake channel.
type RemainingWindow struct {
	Ratio      float64   // 剩余比例。
	Reset      time.Time // 重置时间。
	Confidence float64   // 置信度。
}
