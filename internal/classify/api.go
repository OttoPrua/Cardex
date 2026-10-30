// Package classify assigns a tier when a card is enqueued without one. Design §7.4.
package classify

import (
	"context"
	"errors"

	"cardex/internal/model"
)

// ErrNotImplemented is returned until R2-D fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Source says whether a tier came from the model, the cache, or the kind rule.
type Source string

const (
	SourceModel Source = "model" // 分类模型的单次调用。
	SourceCache Source = "cache" // 卡正文哈希命中缓存。
	SourceRule  Source = "rule"  // 超时或失败后按 kind 的默认 tier。
)

// Result is a tier plus where it came from.
type Result struct {
	Tier   model.Tier // 写入卡的等级。
	Source Source     // model、cache 或 rule。
	Reason string     // 一句理由。
}

// Classify skips cards that already have a tier.
func Classify(ctx context.Context, card model.Card) (Result, error) {
	return Result{}, ErrNotImplemented
}
