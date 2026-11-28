package store

import (
	"context"

	"github.com/OttoPrua/Cardex/internal/model"
)

// CardBlockerState is the compact state needed to explain a card and its
// dependencies. It omits bodies, claims, acceptance commands and attempt logs.
type CardBlockerState struct {
	model.Card
	ObjectiveStatus model.ObjectiveStatus
}

// ListCardBlockerStates includes closed cards for resolving dependency status.
// The caller decides which rows to report as blockers.
func (s *Store) ListCardBlockerStates(ctx context.Context) ([]CardBlockerState, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT c.id, c.objective_id, c.kind, c.status,
		c.deps, c.strict_deps, c.wait_reason, c.wait_question_id, c.legacy, COALESCE(o.status, '')
		FROM cards c LEFT JOIN objectives o ON o.id = c.objective_id
		ORDER BY c.objective_id, c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CardBlockerState
	for rows.Next() {
		var c CardBlockerState
		var deps, legacy string
		if err := rows.Scan(&c.ID, &c.Objective, &c.Kind, &c.Status, &deps, &c.StrictDeps, &c.WaitReason,
			&c.WaitQuestionID, &legacy, &c.ObjectiveStatus); err != nil {
			return nil, err
		}
		if err := parseJSON(deps, &c.Deps); err != nil {
			return nil, err
		}
		readStrictDependencies(&c.Card)
		if err := parseJSON(legacy, &c.Legacy); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
