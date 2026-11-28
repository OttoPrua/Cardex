// Package route picks a channel and model before an attempt is launched. Design §7.1.
package route

import (
	"context"
	"errors"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/quota"
)

const (
	ReasonCompatibleRuntime = "compatible_runtime_disabled"
	ReasonHealth            = "health"        // 健康状态不是 ok。
	ReasonConcurrency       = "concurrency"   // 已达并发上限。
	ReasonRedline           = "redline"       // 剩余比例小于等于红线。
	ReasonBlocked           = "blocked"       // 在观测到的重置时间之前被封锁。
	ReasonCutoff            = "cutoff"        // 已用超过 98%。
	ReasonUnknownQuota      = "unknown_quota" // 余量未知且配置为 exclude。
	ReasonPriorFailure      = "prior_failure" // 此卡在本轮故障切换中已放弃该渠道。
	ReasonFamily            = "family"        // 复核卡优先换族：本档有别族可用时，同族候选被滤掉。
)

// ErrPinnedUnavailable means the explicit channel is missing or not healthy.
// Select does not fall through to automatic selection.
var ErrPinnedUnavailable = errors.New("pinned channel unavailable")

// ErrCompatibleRuntimeHeld is not a reason to release a user's explicit pin.
var ErrCompatibleRuntimeHeld = errors.New(model.CompatibleRuntimeHold)

// ErrNoCandidate means no channel survived tier match and the automatic filters.
var ErrNoCandidate = errors.New("no route candidate")

// Quotas is the remaining-balance view Select consults. *quota.Ledger implements it.
type Quotas interface {
	Remaining(ctx context.Context, channelID string) (quota.Estimate, error)
}

// Request is one routing decision.
type Request struct {
	Card          model.Card          // 待派发的卡，含 tier、requires 和显式渠道。
	Objective     model.Objective     // 所属目标，含显式渠道。卡上的指定优先于目标。
	AuthorFamily  string              // 开发卡作者的模型族；复核卡必须不同。空则用卡上的 AuthorFamily。
	AvoidChannels map[string]bool     // 同一故障链内自动切换已放弃的渠道；人工 pin 不受影响。
	Channels      []model.Channel     // 可派发的渠道。
	Positioning   []model.Positioning // 当前定位表。
	InUse         map[string]int      // 渠道 id → 正在使用的并发数。
	Quota         Quotas              // 余量；nil 表示全部未知。
	Classified    model.Tier          // 入队分级结果。卡上已有 tier 时不用。
	UnknownQuota  string              // allow 或 exclude，空为 allow。
	Now           time.Time           // 判断封锁和 Pace 的时刻；零值用 time.Now。
}
