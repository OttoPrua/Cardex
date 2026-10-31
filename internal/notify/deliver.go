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
}

// New returns a deliverer. now may be nil, in which case time.Now is used.
func New(exec Exec, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{exec: exec, now: now, sent: map[string]deliveryState{}}
}

var _ Deliverer = (*Service)(nil)

// Deliver sends msg once. The same question id is skipped until the clock
// moves past a reminder gap that is longer than the previous gap.
// A partial failure still records which destinations completed and arms that
// gap, so a later call does not resend successes or retry failures early.
func (s *Service) Deliver(ctx context.Context, msg Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dueLocked(msg.QuestionID, now) {
		return nil
	}
	completed, err := s.deliverOnce(ctx, msg, now)
	prev := s.sent[msg.QuestionID].gap
	s.sent[msg.QuestionID] = deliveryState{
		at:        now,
		gap:       ReminderGap(prev),
		completed: completed,
	}
	return err
}

func (s *Service) dueLocked(id string, now time.Time) bool {
	state, ok := s.sent[id]
	if !ok {
		return true
	}
	return now.Sub(state.at) > state.gap
}

func (s *Service) deliverOnce(ctx context.Context, msg Message, now time.Time) (map[string]bool, error) {
	home := msg.Home
	if home == "" {
		home = os.Getenv("CARDEX_HOME")
	}
	completed := map[string]bool{}
	var errs []error
	if kind, id, ok := ParseOrigin(msg.Origin); ok {
		if err := s.deliverAdapter(ctx, home, kind, id, msg, now); err != nil {
			errs = append(errs, err)
		} else {
			completed["origin"] = true
		}
	} else if strings.TrimSpace(msg.Origin) != "" {
		errs = append(errs, fmt.Errorf("notify: origin %q", msg.Origin))
	}
	if kind, id, ok := ParseOrigin(msg.Fallback); ok && msg.Fallback != msg.Origin {
		if err := s.deliverAdapter(ctx, home, kind, id, msg, now); err != nil {
			errs = append(errs, err)
		} else {
			completed["fallback"] = true
		}
	} else if strings.TrimSpace(msg.Fallback) != "" && msg.Fallback != msg.Origin {
		errs = append(errs, fmt.Errorf("notify: fallback %q", msg.Fallback))
	}
	if err := writeInbox(home, msg); err != nil {
		errs = append(errs, err)
	} else {
		completed["inbox"] = true
	}
	return completed, errors.Join(errs...)
}

func (s *Service) deliverAdapter(ctx context.Context, home, kind, id string, msg Message, now time.Time) error {
	switch kind {
	case "codex":
		return s.execArgv(ctx, codexArgv(id, msg.Text))
	case "hermes":
		return s.deliverHermes(home, id, msg, now)
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
	return []string{"lark-cli", "im", "send", "--chat-id", chatID, "--text", message}
}

func safeQuestionName(id string) string {
	name := filepath.Base(strings.ReplaceAll(id, string(filepath.Separator), "_"))
	name = strings.ReplaceAll(name, "/", "_")
	if name == "" || name == "." || name == ".." {
		return "question"
	}
	return name
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
