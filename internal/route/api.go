// Package route picks a channel and model before an attempt is launched. Design §7.1.
package route

import (
	"context"
	"errors"

	"cardex/internal/model"
)

// ErrNotImplemented is returned until R2-D fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Request is one routing decision.
type Request struct {
	Card         model.Card      // 待派发的卡，含 tier、requires 和显式渠道。
	Objective    model.Objective // 所属目标，含显式渠道。
	AuthorFamily string          // 开发卡作者的模型族；复核卡必须不同。
}

// Select returns the chosen route and a structured reason.
func Select(ctx context.Context, req Request) (model.Route, model.RouteReason, error) {
	return model.Route{}, model.RouteReason{}, ErrNotImplemented
}
