package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Exec runs one external command. Tests pass a stub; production is not wired here.
type Exec func(ctx context.Context, argv []string) error

// ReminderGap is the wait before the next reminder. A non-positive previous
// gap returns the first reminder gap. Each later gap is strictly longer.
func ReminderGap(previous time.Duration) time.Duration {
	const first = time.Minute
	if previous <= 0 {
		return first
	}
	return previous * 2
}

// hermesHopWake is one Hermes inbox written for one delivery hop.
// Two hops in the same attempt do not share it: each ack marks only its hop,
// and a missing ack rewrites only that hop's wake.
type hermesHopWake struct {
	Key      string
	Root     string
	Sub      string
	WakeID   string
	Digest   string
	Seq      int
	OriginID string
}

type deliveryState struct {
	at        time.Time
	gap       time.Duration
	completed map[string]bool // destination key -> that hop succeeded
	wakeSeq   int             // Hermes wake sequence for this delivery; 0 if none
	wakeID    string
	digest    string
	wakeRoot  string
	wakeSub   string
	wakeKey   string
	wakes     []hermesHopWake
}

// Service delivers one question to the origin, then the fallback, then the inbox.
type Service struct {
	exec Exec
	now  func() time.Time
	// receiveHermes runs after a hermes inbound is written and before the ack
	// is read. Production leaves it nil and checks once. Tests install the
	// simulated receiver so the ack lands in the same attempt.
	receiveHermes func(home, subscriptionID string) error

	mu   sync.Mutex
	sent map[string]deliveryState

	// Hermes hops written by the attempt currently inside Deliver.
	hops []hermesHopWake
}

// New returns a deliverer. now may be nil, in which case time.Now is used.
func New(exec Exec, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{exec: exec, now: now, sent: map[string]deliveryState{}}
}

var _ Deliverer = (*Service)(nil)

// Deliver sends one question. The same question is not sent again until the
// clock moves past a reminder gap longer than the previous one. A partial
// failure still records completed hops and arms that gap, so an early call
// does not resend them. An unacked Hermes delivery is the exception: that
// call rewrites the same wake id and does not open a new reminder.
func (s *Service) Deliver(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := s.now()
	home := homeOf(msg)
	s.mu.Lock()
	defer s.mu.Unlock()
	// A recoverable ledger fault is a diagnostic. It does not return before the hops.
	diag := s.hydrateLocked(home, msg.QuestionID)
	state := s.sent[msg.QuestionID]
	if !s.dueLocked(msg.QuestionID, now) {
		return errors.Join(diag, s.settleHermes(home, msg, now, state))
	}
	seq := state.wakeSeq + 1
	if seq < 1 {
		seq = 1
	}
	s.hops = nil
	completed, err := s.deliverOnce(ctx, msg, now, seq)
	next := deliveryState{
		at:        now,
		gap:       ReminderGap(state.gap),
		completed: completed,
		wakeSeq:   state.wakeSeq,
		wakeID:    state.wakeID,
		digest:    state.digest,
	}
	if len(s.hops) > 0 {
		next.wakes = append([]hermesHopWake(nil), s.hops...)
		last := s.hops[len(s.hops)-1]
		next.wakeSeq = last.Seq
		next.wakeID = last.WakeID
		next.digest = last.Digest
		next.wakeSub = last.Sub
		next.wakeKey = last.Key
		if last.Root != "" && last.Root != home {
			next.wakeRoot = last.Root
		}
	}
	s.sent[msg.QuestionID] = next
	if saveErr := saveReminder(home, msg.QuestionID, next); saveErr != nil {
		return errors.Join(diag, err, saveErr)
	}
	return errors.Join(diag, err)
}

// hydrateLocked loads this question's reminder bookkeeping from home when
// this process has not seen it yet. A missing file means there was no delivery.
// A format fault still installs any record that parsed.
func (s *Service) hydrateLocked(home, id string) error {
	if _, ok := s.sent[id]; ok {
		return nil
	}
	loaded, found, err := loadReminder(home, id)
	if found {
		s.sent[id] = loaded
	}
	return err
}

// settleHermes applies each stored Hermes wake on its own.
// An ack marks that hop delivered. A missing ack rewrites that same wake.
// The reminder gap and sequence stay where the last due attempt left them.
func (s *Service) settleHermes(home string, msg Message, now time.Time, state deliveryState) error {
	wakes := state.wakes
	if len(wakes) == 0 && state.wakeSeq > 0 && state.wakeID != "" {
		wakes = []hermesHopWake{{
			Key:      state.wakeKey,
			Root:     state.wakeRoot,
			Sub:      state.wakeSub,
			WakeID:   state.wakeID,
			Digest:   state.digest,
			Seq:      state.wakeSeq,
			OriginID: hopOriginID(msg, state.wakeKey),
		}}
	}
	if len(wakes) == 0 {
		return nil
	}
	var errs []error
	changed := false
	for _, wake := range wakes {
		if wake.WakeID == "" {
			continue
		}
		root, sub := wake.Root, wake.Sub
		if strings.TrimSpace(root) == "" {
			root = homeOf(msg)
		}
		if strings.TrimSpace(sub) == "" {
			sub = msg.SubscriptionID
		}
		if hermesAckCovers(root, sub, wake.WakeID, wake.Digest) {
			if wake.Key != "" && !state.completed[wake.Key] {
				if state.completed == nil {
					state.completed = map[string]bool{}
				}
				state.completed[wake.Key] = true
				changed = true
			}
			continue
		}
		if err := s.rewriteOne(msg, now, wake); err != nil {
			errs = append(errs, err)
		}
	}
	if !changed {
		return errors.Join(errs...)
	}
	s.sent[msg.QuestionID] = state
	if err := saveReminder(home, msg.QuestionID, state); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// rewriteOne writes one hop again. It does not touch any other hop's inbox.
func (s *Service) rewriteOne(msg Message, now time.Time, wake hermesHopWake) error {
	originID := wake.OriginID
	if originID == "" {
		originID = hopOriginID(msg, wake.Key)
	}
	if originID == "" {
		return nil
	}
	sub := wake.Sub
	if strings.TrimSpace(sub) == "" {
		sub = msg.SubscriptionID
	}
	root := wake.Root
	if strings.TrimSpace(root) == "" {
		root = homeOf(msg)
	}
	hopMsg := msg
	hopMsg.SubscriptionID = sub
	seq := wake.Seq
	if seq < 1 {
		seq = 1
	}
	return s.deliverHermes(root, originID, hopMsg, now, seq, wake.Key)
}

func hopOriginID(msg Message, key string) string {
	switch {
	case key == "origin":
		if kind, id, ok := ParseOrigin(msg.Origin); ok && kind == "hermes" {
			return id
		}
	case key == "fallback":
		if kind, id, ok := ParseOrigin(msg.Fallback); ok && kind == "hermes" {
			return id
		}
	case strings.HasPrefix(key, "receiver:"):
		id := strings.TrimPrefix(key, "receiver:")
		for _, rec := range msg.Receivers {
			if rec.ID == id && rec.Adapter == "hermes" {
				return rec.Target
			}
		}
	}
	return ""
}

func homeOf(msg Message) string {
	if msg.Home != "" {
		return msg.Home
	}
	return os.Getenv("CARDEX_HOME")
}

func (s *Service) dueLocked(id string, now time.Time) bool {
	state, ok := s.sent[id]
	if !ok {
		return true
	}
	return now.Sub(state.at) > state.gap
}

func (s *Service) deliverOnce(ctx context.Context, msg Message, now time.Time, seq int) (map[string]bool, error) {
	completed := map[string]bool{}
	var errs []error
	hops, skips, problems := Resolve(msg)
	for _, reason := range skips {
		errs = append(errs, &SkipError{Reason: reason})
	}
	errs = append(errs, problems...)
	for _, hop := range hops {
		if err := s.deliverHop(ctx, msg, hop, now, seq); err != nil {
			errs = append(errs, err)
		} else {
			completed[hop.Key] = true
		}
	}
	return completed, errors.Join(errs...)
}

func (s *Service) deliverHop(ctx context.Context, msg Message, hop Hop, now time.Time, seq int) error {
	home := homeOf(msg)
	switch hop.Kind {
	case "inbox":
		return writeInbox(home, msg)
	case "hermes":
		hopMsg := msg
		hopMsg.SubscriptionID = hop.Sub
		return s.deliverHermes(hop.Root, hop.ID, hopMsg, now, seq, hop.Key)
	case "codex", "lark":
		return s.deliverAdapter(ctx, home, hop.Kind, hop.ID, msg, now, seq)
	case "file":
		if strings.HasPrefix(hop.Key, "receiver:") {
			return writeReceiverFile(hop.ID, msg)
		}
		return s.deliverAdapter(ctx, home, hop.Kind, hop.ID, msg, now, seq)
	default:
		return fmt.Errorf("notify: adapter %q", hop.Kind)
	}
}

func (s *Service) deliverAdapter(ctx context.Context, home, kind, id string, msg Message, now time.Time, seq int) error {
	switch kind {
	case "codex":
		return s.execArgv(ctx, codexArgv(id, msg.Text))
	case "hermes":
		return s.deliverHermes(home, id, msg, now, seq, "hermes")
	case "lark":
		return s.execArgv(ctx, larkArgv(id, msg.Text))
	case "file":
		return writeInbox(home, msg)
	default:
		return fmt.Errorf("notify: adapter %q", kind)
	}
}

func (s *Service) execArgv(ctx context.Context, argv []string) error {
	if s.exec == nil {
		return errors.New("notify: exec is nil")
	}
	return s.exec(ctx, argv)
}

func codexArgv(thread, message string) []string {
	return []string{"codex", "queue", "--thread", thread, "--message", message}
}

func larkArgv(chatID, message string) []string {
	return []string{"lark-cli", "im", "+messages-send", "--chat-id", chatID, "--text", message}
}

func safeQuestionName(id string) string {
	return deliveryIdentity(id)
}

func inboxPath(home, questionID string) string {
	return filepath.Join(home, "inbox", safeQuestionName(questionID)+".txt")
}

// inboxWrite persists one inbox file. Tests replace it to count attempts.
var inboxWrite = os.WriteFile

func writeInbox(home string, msg Message) error {
	if strings.TrimSpace(home) == "" {
		return errors.New("notify: CARDEX_HOME is empty")
	}
	path := inboxPath(home, msg.QuestionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return inboxWrite(path, []byte(noticeBody(msg)), 0o644)
}

func noticeBody(msg Message) string {
	return fmt.Sprintf("question_id: %s\nobjective: %s\norigin: %s\n\n%s\n",
		msg.QuestionID, msg.Objective, msg.Origin, msg.Text)
}

// writeReceiverFile writes one file-receiver notice to its configured path.
// Origin and fallback file hops stay on the home inbox.
func writeReceiverFile(target string, msg Message) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("notify: file target is empty")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(noticeBody(msg)), 0o644)
}
