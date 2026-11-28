package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

// CardHasDependencyReference checks stored graph edges across objectives,
// including legacy review IDs and an active or latest attempt's pinned
// review subject. Requirements in prose are not interpreted.
func (s *Store) CardHasDependencyReference(ctx context.Context, cardID string) (bool, error) {
	found, err := s.cardDefinitionReferences(ctx, cardID)
	if err != nil || found {
		return found, err
	}
	return s.cardPinnedReviewReferences(ctx, cardID)
}

func (s *Store) cardDefinitionReferences(ctx context.Context, cardID string) (bool, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id, kind, deps, strict_deps FROM cards WHERE id<>?`, cardID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var c model.Card
		var deps string
		if err := rows.Scan(&c.ID, &c.Kind, &deps, &c.StrictDeps); err != nil {
			return false, err
		}
		if err := parseJSON(deps, &c.Deps); err != nil {
			return false, fmt.Errorf("cannot verify stored completion dependencies: %w", err)
		}
		readStrictDependencies(&c)
		for _, dep := range c.Deps {
			if strings.TrimSpace(dep) == cardID {
				return true, nil
			}
		}
		if c.Kind == model.KindReview {
			subject, _ := model.ReviewSubject(c)
			if subject == cardID {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

func (s *Store) cardPinnedReviewReferences(ctx context.Context, cardID string) (bool, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT a.id, e.data
		FROM events e JOIN attempts a ON e.entity='attempt:'||a.id
		JOIN cards c ON c.id=a.card_id
		WHERE c.id<>? AND e.event_type=? AND (
			(a.exited=0 AND a.ended_at='' AND a.outcome='') OR
			a.id=(SELECT latest.id FROM attempts latest
				WHERE latest.card_id=a.card_id ORDER BY latest.attempt_index DESC, latest.id DESC LIMIT 1)
		) ORDER BY a.id, e.ts, e.id`, cardID, model.ReviewSubjectEvent)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var last string
	var pin model.PinnedReviewSubject
	for rows.Next() {
		var attempt string
		var raw []byte
		if err := rows.Scan(&attempt, &raw); err != nil {
			return false, err
		}
		var parsed struct {
			Subject    string `json:"subject"`
			Standalone *bool  `json:"standalone"`
		}
		if json.Unmarshal(raw, &parsed) != nil || parsed.Standalone == nil || strings.TrimSpace(parsed.Subject) == "" {
			// Cannot prove independence when relevant review metadata is
			// damaged. Do not fall back to a mutable declaration.
			return true, nil
		}
		next := model.PinnedReviewSubject{Subject: strings.TrimSpace(parsed.Subject), Standalone: *parsed.Standalone}
		if last == attempt && pin != next {
			return true, nil
		}
		if !next.Standalone && next.Subject == cardID {
			return true, nil
		}
		last, pin = attempt, next
	}
	return false, rows.Err()
}
