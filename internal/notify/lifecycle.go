package notify

import (
	"context"
	"fmt"
	"time"

	"github.com/OttoPrua/Cardex/internal/store"
)

const (
	lifecycleTimeout = 5 * time.Second
	lifecycleRetry   = time.Second
)

var lifecycleExec = CommandExec

func init() {
	store.PublishCardNote = func(ctx context.Context, db *store.Store, id int64) {
		deliverLifecycle(ctx, db, id, time.Now())
	}
}

// FlushLifecycle delivers pending events after a failed attempt or process exit.
func FlushLifecycle(ctx context.Context, db *store.Store) error {
	return flushLifecycle(ctx, db, time.Now())
}

func flushLifecycle(ctx context.Context, db *store.Store, now time.Time) error {
	ids, err := db.DueLifecycle(ctx, now)
	if err != nil {
		return err
	}
	for _, id := range ids {
		deliverLifecycle(ctx, db, id, now)
	}
	return ctx.Err()
}

func deliverLifecycle(ctx context.Context, db *store.Store, id int64, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	// Leave time to record the result before another process can take over.
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, lifecycleTimeout)
	defer cancel()
	claim, err := db.ClaimLifecycle(ctx, id, now, time.Now().Add(3*lifecycleTimeout))
	if err != nil {
		return
	}
	err = deliverOriginEvent(ctx, db.Home(), fmt.Sprintf("lifecycle-%d", id), claim)
	finishCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), lifecycleTimeout)
	defer finish()
	_ = db.FinishLifecycle(finishCtx, id, claim.Attempts, err == nil, now.Add(time.Since(started)+lifecycleRetry))
}

func deliverOriginEvent(ctx context.Context, home, eventID string, claim store.LifecycleDelivery) error {
	msg := Message{QuestionID: eventID, Objective: claim.Objective, Origin: claim.Origin, Text: claim.Payload, Home: home}
	kind, id, ok := ParseOrigin(msg.Origin)
	if !ok {
		return fmt.Errorf("notify: invalid lifecycle origin")
	}
	if kind == "hermes" {
		receivers, _, err := LoadReceivers(home)
		if err != nil {
			return err
		}
		msg.Receivers = receivers
	}
	hop, skip, err := originHop("origin", kind, id, msg)
	if err != nil {
		return err
	}
	if skip != "" {
		return &SkipError{Reason: skip}
	}
	if kind == "hermes" {
		const seq = 1
		root := hop.Root
		if root == "" {
			root = home
		}
		wake, ids := compactQuestionWake(hop.Sub, eventID, msg.Objective, seq)
		if hermesAckCovers(root, hop.Sub, ids[0], inboundDigest(wake)) {
			return nil
		}
		if err := writeInbox(root, msg); err != nil {
			return err
		}
	}
	return New(lifecycleExec, nil).deliverHop(ctx, msg, hop, time.Now().UTC(), 1)
}
