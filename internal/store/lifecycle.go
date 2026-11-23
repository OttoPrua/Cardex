package store

import (
	"context"

	"github.com/OttoPrua/Cardex/internal/model"
)

// CardNote is one card lifecycle observation. PublishCardNote delivers it
// after the write commits. Kind is merged, needs_input, failed,
// rework_limit, or channel_degraded. Suffix distinguishes channels.
type CardNote struct {
	CardID string
	Kind   string
	Reason string
	Suffix string
}

// PublishCardNote is installed by the notify package. A nil hook skips delivery.
var PublishCardNote func(db *Store, note CardNote)

func (s *Store) noteStatus(ctx context.Context, prev, next model.Card) {
	if prev.Status == next.Status || next.ID == "" {
		return
	}
	switch next.Status {
	case model.CardMerged, model.CardNeedsInput, model.CardFailed:
		s.queueNote(ctx, CardNote{CardID: next.ID, Kind: string(next.Status)})
	}
}

// NoteLifecycle records a lifecycle event that is not a plain status change,
// such as channel degradation. Delivery runs after the current transaction commits.
func (s *Store) NoteLifecycle(ctx context.Context, cardID, kind, reason, suffix string) {
	if s == nil || cardID == "" || kind == "" {
		return
	}
	s.queueNote(ctx, CardNote{CardID: cardID, Kind: kind, Reason: reason, Suffix: suffix})
}

func (s *Store) queueNote(ctx context.Context, note CardNote) {
	if PublishCardNote == nil {
		return
	}
	s.afterCommit(ctx, func() {
		if PublishCardNote != nil {
			PublishCardNote(s, note)
		}
	})
}
