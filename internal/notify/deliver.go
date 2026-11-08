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

	// Hermes identity written by the attempt currently inside Deliver.
	hermesWrote  bool
	hermesWake   string
	hermesDigest string
	hermesRoot   string
	hermesSub    string
	hermesKey    string
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
		root, sub := watchHermes(msg, state)
		if state.wakeSeq > 0 && state.wakeID != "" && !hermesAckCovers(root, sub, state.wakeID, state.digest) {
			if strings.TrimSpace(msg.SubscriptionID) == "" {
				msg.SubscriptionID = sub
			}
			return errors.Join(diag, s.rewriteHermes(msg, now, state.wakeSeq, root))
		}
		if state.wakeSeq > 0 && state.wakeID != "" && hermesAckCovers(root, sub, state.wakeID, state.digest) {
			if err := s.markHermesDelivered(home, msg.QuestionID, state); err != nil {
				return errors.Join(diag, err)
			}
		}
		return diag
	}
	seq := state.wakeSeq + 1
	if seq < 1 {
		seq = 1
	}
	s.hermesWrote = false
	s.hermesWake = ""
	s.hermesDigest = ""
	s.hermesRoot = ""
	s.hermesSub = ""
	s.hermesKey = ""
	completed, err := s.deliverOnce(ctx, msg, now, seq)
	next := deliveryState{
		at:        now,
		gap:       ReminderGap(state.gap),
		completed: completed,
		wakeSeq:   state.wakeSeq,
		wakeID:    state.wakeID,
		digest:    state.digest,
	}
	if s.hermesWrote {
		next.wakeSeq = seq
		next.wakeID = s.hermesWake
		next.digest = s.hermesDigest
		next.wakeSub = s.hermesSub
		next.wakeKey = s.hermesKey
		if s.hermesRoot != "" && s.hermesRoot != home {
			next.wakeRoot = s.hermesRoot
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

// rewriteHermes writes the current delivery again. The reminder gap stays put,
// and the wake sequence is the one this delivery already owns.
func (s *Service) rewriteHermes(msg Message, now time.Time, seq int, root string) error {
	if strings.TrimSpace(root) == "" {
		root = homeOf(msg)
	}
	var errs []error
	wrote := false
	tryAt := func(base, originID, key string) {
		wrote = true
		if strings.TrimSpace(base) == "" {
			base = root
		}
		if err := s.deliverHermes(base, originID, msg, now, seq, key); err != nil {
			errs = append(errs, err)
		}
	}
	if key := s.sent[msg.QuestionID].wakeKey; strings.HasPrefix(key, "receiver:") {
		id := strings.TrimPrefix(key, "receiver:")
		for _, rec := range msg.Receivers {
			if rec.ID != id || rec.Adapter != "hermes" {
				continue
			}
			msg.SubscriptionID = rec.ID
			tryAt(rec.Root, rec.Target, key)
			return errors.Join(errs...)
		}
	}
	if kind, id, ok := ParseOrigin(msg.Origin); ok && kind == "hermes" {
		tryAt(root, id, "origin")
	}
	if kind, id, ok := ParseOrigin(msg.Fallback); ok && kind == "hermes" && msg.Fallback != msg.Origin {
		tryAt(root, id, "fallback")
	}
	if !wrote {
		return nil
	}
	return errors.Join(errs...)
}

func watchHermes(msg Message, state deliveryState) (root, sub string) {
	root = state.wakeRoot
	if strings.TrimSpace(root) == "" {
		root = homeOf(msg)
	}
	sub = state.wakeSub
	if strings.TrimSpace(sub) == "" {
		sub = msg.SubscriptionID
	}
	return root, sub
}

func (s *Service) markHermesDelivered(home, id string, state deliveryState) error {
	if state.wakeKey == "" || state.completed[state.wakeKey] {
		return nil
	}
	if state.completed == nil {
		state.completed = map[string]bool{}
	}
	state.completed[state.wakeKey] = true
	s.sent[id] = state
	return saveReminder(home, id, state)
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
	case "codex", "lark", "file":
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
	body := fmt.Sprintf("question_id: %s\nobjective: %s\norigin: %s\n\n%s\n",
		msg.QuestionID, msg.Objective, msg.Origin, msg.Text)
	return inboxWrite(path, []byte(body), 0o644)
}
