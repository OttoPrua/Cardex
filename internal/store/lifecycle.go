package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Pulse is the compact wait result and lifecycle delivery payload.
type Pulse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Model      string `json:"model"`
	SHA        string `json:"sha"`
	QuestionID string `json:"question_id"`
}

const (
	eventReworkLimit     = "rework_limit"
	eventChannelDegraded = "channel_degraded"
	LifecycleMaxAttempts = 3
)

type cardNote struct {
	card       model.Card
	kind       string
	reason     string
	questionID string
	attempt    model.Attempt
}

// PublishCardNote is installed by notify for immediate best-effort delivery.
// Even without the hook, committed events remain available to the scheduler.
var PublishCardNote func(context.Context, *Store, int64)

func hasLifecycleStatus(status model.CardStatus) bool {
	switch status {
	case model.CardMerged, model.CardNeedsInput, model.CardFailed:
		return true
	default:
		return false
	}
}

// CardWaitState reads only the fields needed by wait and CardPulse.
func (s *Store) CardWaitState(ctx context.Context, id string) (model.Card, error) {
	return scanCardWaitState(s.q(ctx).QueryRowContext(ctx, cardWaitSelect+` WHERE id=?`, id))
}

const cardWaitSelect = `SELECT id, objective_id, status, wait_reason FROM cards`

func scanCardWaitState(row scanner) (model.Card, error) {
	var card model.Card
	err := row.Scan(&card.ID, &card.Objective, &card.Status, &card.WaitReason)
	return card, err
}

// ListCardWaitStates avoids fetching claims and card bodies on each wait check.
func (s *Store) ListCardWaitStates(ctx context.Context, objectiveID string) ([]model.Card, error) {
	rows, err := s.q(ctx).QueryContext(ctx, cardWaitSelect+` WHERE objective_id=? ORDER BY id`, objectiveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []model.Card
	for rows.Next() {
		card, err := scanCardWaitState(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	return cards, rows.Err()
}

func (s *Store) noteStatus(ctx context.Context, next model.Card) {
	if hasLifecycleStatus(next.Status) {
		s.queueCardNote(ctx, cardNote{card: next, kind: string(next.Status)})
	}
}

// NoteReworkLimit records the explicit rework decision, even if the card was already blocked.
func (s *Store) NoteReworkLimit(ctx context.Context, card model.Card, reason, questionID string) error {
	return s.WithTx(ctx, func(ctx context.Context) error {
		s.queueCardNote(ctx, cardNote{card: card, kind: eventReworkLimit, reason: reason, questionID: questionID})
		return nil
	})
}

// NoteChannelDegraded records the attempt that caused the channel to degrade.
func (s *Store) NoteChannelDegraded(ctx context.Context, card model.Card, attempt model.Attempt, reason string) error {
	return s.WithTx(ctx, func(ctx context.Context) error {
		s.queueCardNote(ctx, cardNote{card: card, kind: eventChannelDegraded, reason: reason, attempt: attempt})
		return nil
	})
}

func (s *Store) queueCardNote(ctx context.Context, note cardNote) {
	notes := ctx.Value(notesKey{}).(*commitNotes)
	notes.pending = append(notes.pending, note)
}

const lifecycleEnabled = `CASE o.events WHEN 'on' THEN 1 WHEN 'off' THEN 0 ELSE COALESCE(p.events = 'on', 0) END`

func (s *Store) persistLifecycle(ctx context.Context, note cardNote) (int64, error) {
	var origin string
	err := s.q(ctx).QueryRowContext(ctx, `SELECT o.origin FROM objectives o
		LEFT JOIN projects p ON p.id=o.project_id WHERE o.id=? AND `+lifecycleEnabled, note.card.Objective).Scan(&origin)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && strings.TrimSpace(origin) == "") {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	pulse, attemptID, err := s.cardPulse(ctx, note.card)
	if err != nil {
		return 0, err
	}
	pulse.Status = note.kind
	if note.reason != "" {
		pulse.Reason = note.reason
	}
	if note.attempt.ID != "" {
		attemptID = note.attempt.ID
		pulse.Model = note.attempt.Route.Model
		pulse.SHA = firstCommit(note.attempt.HeadCommit, note.attempt.BaseCommit)
	}
	episode := attemptID
	if note.kind == eventReworkLimit {
		pulse.QuestionID = note.questionID
		episode = note.questionID
	} else if note.kind == string(model.CardNeedsInput) && pulse.QuestionID != "" {
		episode = pulse.QuestionID
	}
	pulse.Reason = compactReason(pulse.Reason)
	key, err := json.Marshal([]string{pulse.ID, pulse.Status, episode})
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(pulse)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.q(ctx).QueryRowContext(ctx, `INSERT INTO lifecycle_outbox(event_key, objective_id, origin, payload)
		VALUES(?,?,?,?) ON CONFLICT(event_key) DO NOTHING RETURNING id`,
		string(key), note.card.Objective, origin, string(body)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return id, err
}

// CardPulse reports observed attempt metadata and unanswered question, if any.
func (s *Store) CardPulse(ctx context.Context, card model.Card) (Pulse, error) {
	pulse, _, err := s.cardPulse(ctx, card)
	return pulse, err
}

func (s *Store) cardPulse(ctx context.Context, card model.Card) (Pulse, string, error) {
	pulse := Pulse{ID: card.ID, Status: string(card.Status), Reason: card.WaitReason}
	attempt, err := scanAttempt(s.q(ctx).QueryRowContext(ctx, attemptSelect+
		` WHERE card_id=? ORDER BY attempt_index DESC, ended_at DESC, id DESC LIMIT 1`, card.ID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return pulse, "", err
	}
	pulse.Model = attempt.Route.Model
	pulse.SHA = firstCommit(attempt.HeadCommit, attempt.BaseCommit)
	question, err := s.pendingLifecycleQuestion(ctx, card.Objective, card.ID)
	if err != nil {
		return pulse, "", err
	}
	pulse.QuestionID = question.ID
	if pulse.Reason == "" {
		switch card.Status {
		case model.CardNeedsInput:
			pulse.Reason = question.Text
			if pulse.Reason == "" {
				pulse.Reason = attempt.ErrorExcerpt
			}
		case model.CardFailed:
			pulse.Reason = attempt.ErrorExcerpt
		}
	}
	pulse.Reason = compactReason(pulse.Reason)
	return pulse, attempt.ID, nil
}

// ObjectivePulse uses the objective's latest observed attempt when its own state is reported.
func (s *Store) ObjectivePulse(ctx context.Context, obj model.Objective) (Pulse, error) {
	pulse := Pulse{ID: obj.ID, Status: string(obj.Status)}
	err := s.q(ctx).QueryRowContext(ctx, `SELECT a.model, COALESCE(NULLIF(a.head_commit,''), a.base_commit)
		FROM attempts a JOIN cards c ON c.id=a.card_id WHERE c.objective_id=?
		ORDER BY a.started_at DESC, a.ended_at DESC, a.id DESC LIMIT 1`, obj.ID).Scan(&pulse.Model, &pulse.SHA)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return pulse, err
	}
	question, err := s.pendingLifecycleQuestion(ctx, obj.ID, "")
	pulse.QuestionID = question.ID
	return pulse, err
}

func compactReason(reason string) string {
	const limit = 512
	runes := []rune(strings.Join(strings.Fields(reason), " "))
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return string(runes)
}

func firstCommit(head, base string) string {
	if head != "" {
		return head
	}
	return base
}

func (s *Store) pendingLifecycleQuestion(ctx context.Context, objectiveID, cardID string) (model.Question, error) {
	var q model.Question
	// Cross-objective reviews can open questions scoped to the subject card.
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id, scope, text FROM questions
		WHERE answer='' AND ((objective_id=?1 AND (scope IN ('', ?1, ?2) OR ?2=''))
			OR (?2<>'' AND scope_kind='card' AND scope=?2))
		ORDER BY (scope=?2) DESC, rowid DESC LIMIT 1`, objectiveID, cardID).
		Scan(&q.ID, &q.Scope, &q.Text)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return q, err
}

// LifecycleDelivery is a claimed event. Attempts is also the claim version.
type LifecycleDelivery struct {
	Objective string
	Origin    string
	Payload   string
	Attempts  int
}

const lifecycleCanDeliver = `EXISTS (SELECT 1 FROM objectives o LEFT JOIN projects p ON p.id=o.project_id
	WHERE o.id=lifecycle_outbox.objective_id AND o.origin=lifecycle_outbox.origin AND ` + lifecycleEnabled + `)`

const lifecycleDue = `done=0 AND attempts < ? AND available_at <= ? AND ` + lifecycleCanDeliver

// DueLifecycle lists pending events available for delivery or retry.
func (s *Store) DueLifecycle(ctx context.Context, now time.Time) ([]int64, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id FROM lifecycle_outbox WHERE `+lifecycleDue+` ORDER BY id`,
		LifecycleMaxAttempts, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClaimLifecycle advances the attempt count before calling the adapter. The
// lease lets a later tick retry an event if the delivery process exited.
func (s *Store) ClaimLifecycle(ctx context.Context, id int64, now, leaseUntil time.Time) (LifecycleDelivery, error) {
	var delivery LifecycleDelivery
	err := s.q(ctx).QueryRowContext(ctx, `UPDATE lifecycle_outbox SET attempts=attempts+1, available_at=?
		WHERE id=? AND `+lifecycleDue+` RETURNING objective_id, origin, payload, attempts`,
		leaseUntil.UnixMilli(), id, LifecycleMaxAttempts, now.UnixMilli()).
		Scan(&delivery.Objective, &delivery.Origin, &delivery.Payload, &delivery.Attempts)
	return delivery, err
}

func (s *Store) FinishLifecycle(ctx context.Context, id int64, claim int, delivered bool, retryAt time.Time) error {
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE lifecycle_outbox SET done=?, available_at=? WHERE id=? AND attempts=?`,
		delivered, retryAt.UnixMilli(), id, claim)
	return err
}
