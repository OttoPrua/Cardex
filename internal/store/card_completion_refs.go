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
	found, err := s.cardDefinitionReferences(ctx, cardID, false)
	if err != nil || found {
		return found, err
	}
	return s.cardPinnedReviewReferences(ctx, cardID)
}

// CardHasStrictExecutionReference detects an explicit prerequisite of a
// strict card. The first subject of a review or repair is not relabeled as
// an execution prerequisite. The query is deliberately cross-objective.
func (s *Store) CardHasStrictExecutionReference(ctx context.Context, cardID string) (bool, error) {
	return s.cardDefinitionReferences(ctx, cardID, true)
}

// completionReferenceMatches treats both exact stored IDs and whitespace
// variants as possible links. It never normalizes the stored identity or
// chooses between different cards that happen to have the same trimmed ID.
func completionReferenceMatches(ref, id string) bool {
	return ref == id || (strings.TrimSpace(id) != "" && strings.TrimSpace(ref) == strings.TrimSpace(id))
}

func (s *Store) cardDefinitionReferences(ctx context.Context, cardID string, strictOnly bool) (bool, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id, kind, deps, strict_deps FROM cards
		WHERE id<>? AND (?=0 OR strict_deps=1)`, cardID, strictOnly)
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
		depsIDs := c.Deps
		if strictOnly {
			depsIDs = c.ExecutionDependencyIDs()
		}
		for _, dep := range depsIDs {
			if completionReferenceMatches(dep, cardID) {
				return true, nil
			}
		}
		if !strictOnly && c.Kind == model.KindReview {
			subject, _ := model.ReviewSubject(c)
			if completionReferenceMatches(subject, cardID) {
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
		next := model.PinnedReviewSubject{Subject: parsed.Subject, Standalone: *parsed.Standalone}
		if last == attempt && pin != next {
			return true, nil
		}
		if !next.Standalone && completionReferenceMatches(next.Subject, cardID) {
			return true, nil
		}
		last, pin = attempt, next
	}
	return false, rows.Err()
}

// CardHasPendingCompletionDecision uses the same conservative linkage as
// the dependency graph while preserving the exact stored scope ID.
func (s *Store) CardHasPendingCompletionDecision(ctx context.Context, cardID string) (bool, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT scope FROM questions WHERE scope_kind IN ('card','') AND answer=''`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			return false, err
		}
		if completionReferenceMatches(scope, cardID) {
			return true, nil
		}
	}
	return false, rows.Err()
}
