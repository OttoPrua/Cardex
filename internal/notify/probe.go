package notify

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// hermesTestSeq keeps each notify test wake distinct from earlier tests,
// including one left behind after the same receiver id is registered again.
var hermesTestSeq atomic.Uint64

func hermesTestQuestionID(now time.Time) string {
	n := hermesTestSeq.Add(1)
	stamp := now.UnixNano()
	if stamp < 0 {
		stamp = 0
	}
	return fmt.Sprintf("notify-test-%d-%d", stamp, n)
}

// PrepareHermesTest writes one hermes test wake under the receiver root.
// root is the inbox base the ack must be written under. ackPath is <wake>.ack.json.
func PrepareHermesTest(home string, rec Receiver, now time.Time) (root, ackPath, wakeID, digest string, err error) {
	rec = normalizeReceiver(rec)
	if err := validateReceiver(rec); err != nil {
		return "", "", "", "", err
	}
	if rec.Adapter != "hermes" {
		return "", "", "", "", fmt.Errorf("notify: adapter %q", rec.Adapter)
	}
	root = rec.Root
	if strings.TrimSpace(root) == "" {
		root = home
	}
	if now.IsZero() {
		now = time.Now()
	}
	msg := Message{
		QuestionID:     hermesTestQuestionID(now),
		Objective:      "notify-test",
		Text:           "cardex notify test " + rec.ID,
		Home:           home,
		SubscriptionID: rec.ID,
	}
	wakeID, digest, err = writeHermesInbound(root, rec.Target, msg, now, 1)
	if err != nil {
		return "", "", "", "", err
	}
	return root, hermesAckPath(root, rec.ID, wakeID), wakeID, digest, nil
}

// WaitHermesAck reports whether the ack appears before the bound elapses.
// A non-positive bound checks once.
func WaitHermesAck(root, sub, wakeID, digest string, wait time.Duration) bool {
	if hermesAckCovers(root, sub, wakeID, digest) {
		return true
	}
	if wait <= 0 {
		return false
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		if hermesAckCovers(root, sub, wakeID, digest) {
			return true
		}
	}
	return false
}

// TestDeliver sends one non-hermes test message. Hermes uses PrepareHermesTest.
func TestDeliver(ctx context.Context, exec Exec, home string, rec Receiver) error {
	rec = normalizeReceiver(rec)
	if err := validateReceiver(rec); err != nil {
		return err
	}
	text := "cardex notify test " + rec.ID
	switch rec.Adapter {
	case "codex":
		if exec == nil {
			return fmt.Errorf("notify: exec is nil")
		}
		return exec(ctx, codexArgv(rec.Target, text))
	case "lark":
		if exec == nil {
			return fmt.Errorf("notify: exec is nil")
		}
		return exec(ctx, larkArgv(rec.Target, text))
	case "file":
		return writeReceiverFile(rec.Target, Message{
			QuestionID: "notify-test",
			Objective:  "notify-test",
			Origin:     "file:" + rec.Target,
			Text:       text,
			Home:       home,
		})
	default:
		return fmt.Errorf("notify: adapter %q", rec.Adapter)
	}
}
