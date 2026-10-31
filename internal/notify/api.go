// Package notify delivers questions to the origin session and the inbox. Design §9.
package notify

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned until R3-D fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Message is one question delivery.
type Message struct {
	QuestionID     string // 问题标识，用于去重。
	Objective      string // 所属目标。
	Origin         string // kind:id，先于后备目的地投递。
	Text           string // 正文。
	Home           string // CARDEX_HOME，file 适配器写到这里的 inbox/。
	Fallback       string // 配置的后备目的地。
	SubscriptionID string // hermes 订阅 id。origin 不含它，由调用方传入。
}

// Deliverer sends a message once and reminds later if it is still unanswered.
type Deliverer interface {
	Deliver(ctx context.Context, msg Message) error
}

// Stub is the unimplemented deliverer.
type Stub struct{}

func (Stub) Deliver(context.Context, Message) error { return ErrNotImplemented }
