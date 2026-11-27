package store

import (
	"context"

	"github.com/OttoPrua/Cardex/internal/model"
)

// ObjectiveTokenLimitReached checks the objective-wide hard limit against
// positive recorded attempt usage, like the scheduler budget accounting. It
// subtracts each charge from the limit so total usage cannot overflow int64.
func (s *Store) ObjectiveTokenLimitReached(ctx context.Context, objectiveID string, limit int64) (bool, error) {
	if limit <= 0 {
		return false, nil
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT a.usage FROM attempts a
		JOIN cards c ON c.id=a.card_id WHERE c.objective_id=?`, objectiveID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var raw string
		var usage model.Usage
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		if err := parseJSON(raw, &usage); err != nil {
			return false, err
		}
		if usage.Tokens >= limit {
			return true, nil
		}
		if usage.Tokens > 0 {
			limit -= usage.Tokens
		}
	}
	return false, rows.Err()
}
