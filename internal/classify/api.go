// Package classify assigns a tier when a card is enqueued without one. Design §7.4.
package classify

import (
	"context"
	"time"

	"cardex/internal/model"
)

// ClassifyTimeout is the deadline on the single classifier call.
const ClassifyTimeout = 30 * time.Second

// PromptTemplate is the fixed classifier prompt compiled into the package.
const PromptTemplate = `Classify this Cardex card as exactly one tier: T1, T2, or T3.
Reply with exactly two lines and no other text:
TIER: <T1|T2|T3>
REASON: <one sentence>
`

// Call is the injected single classifier call. Tests observe the deadline on ctx.
type Call func(ctx context.Context, channelID, prompt string) (string, error)

// Options configures one classification. Cache is owned by the caller.
type Options struct {
	Channel string // classifier 渠道。空则视为不可用。
	Call    Call   // 单次调用。nil 视为渠道不可用。
	Cache   *Cache // 按卡正文哈希缓存成功结果。nil 表示不缓存。
}

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
