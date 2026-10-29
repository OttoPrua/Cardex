// Package quota estimates how much of each channel window is left. Design §7.2 and §7.3.
package quota

import (
	"context"
	"errors"
	"time"

	"cardex/internal/model"
)

// ErrNotImplemented is returned until R2-D fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Estimate is the remaining ratio, the next reset, and how much to trust it.
type Estimate struct {
	Ratio      float64   // 剩余比例，1 表示全满。
	Reset      time.Time // 重置时间。
	Confidence float64   // 置信度，0 到 1。
	Unknown    bool      // 没有配置上限时为真。
}

// Estimator accounts usage into configured windows.
type Estimator interface {
	Remaining(ctx context.Context, channelID string) (Estimate, error)
	Record(ctx context.Context, channelID string, usage model.Usage, started, ended time.Time) error
	ObserveLimit(ctx context.Context, channelID string, reset time.Time) error
}

// Stub is the unimplemented estimator.
type Stub struct{}

func (Stub) Remaining(context.Context, string) (Estimate, error) {
	return Estimate{}, ErrNotImplemented
}
func (Stub) Record(context.Context, string, model.Usage, time.Time, time.Time) error {
	return ErrNotImplemented
}
func (Stub) ObserveLimit(context.Context, string, time.Time) error {
	return ErrNotImplemented
}
