// Package quota estimates how much of each channel window is left. Design §7.2 and §7.3.
package quota

import (
	"context"
	"errors"
	"time"

	"cardex/internal/model"
)

// ErrNotImplemented is returned by Stub. Ledger is the estimator.
var ErrNotImplemented = errors.New("not implemented")

// Estimate is the remaining ratio, the next reset, and how much to trust it.
type Estimate struct {
	Ratio         float64   // 剩余比例，1 表示全满。多个有上限的窗口取最小。
	Reset         time.Time // 该窗口的重置时间；硬封锁期间是观测到的重置时间。
	Confidence    float64   // 置信度，0 到 1。未知用量低于已知用量。
	Unknown       bool      // 没有配置上限时为真。
	Blocked       bool      // 观测到撞线后，在 Reset 之前为真。
	UsedTokens    int64     // 决定该结果的窗口里已计入的 token。
	LimitTokens   int64     // 该窗口的 token 上限。0 表示未配置。
	UsedRequests  int64     // 该窗口里的尝试次数。一次 Record 计一次。
	LimitRequests int64     // 该窗口的请求上限。0 表示未配置。
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
