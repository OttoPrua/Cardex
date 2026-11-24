package store

import (
	"context"
	"strings"
)

// ReadCount is one management-side read total for an origin, a card, and a command.
type ReadCount struct {
	Origin  string
	CardID  string
	Command string
	Count   int
}

// AddReads records one read per card in a single write.
func (s *Store) AddReads(ctx context.Context, origin, command string, cards []string) error {
	ids, err := mustJSON(cards)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO mgmt_reads(origin, card_id, command, n)
		SELECT ?, value, ?, 1 FROM json_each(?) WHERE value != ''
		ON CONFLICT(origin, card_id, command) DO UPDATE SET n = n + 1`, origin, command, ids)
	return err
}

// ListReads returns counters, optionally for one origin.
func (s *Store) ListReads(ctx context.Context, origin string) ([]ReadCount, error) {
	q := `SELECT origin, card_id, command, n FROM mgmt_reads`
	var args []any
	origin = strings.TrimSpace(origin)
	if origin != "" {
		q += ` WHERE origin=?`
		args = append(args, origin)
	}
	q += ` ORDER BY origin, card_id, command`
	rows, err := s.q(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReadCount{}
	for rows.Next() {
		var item ReadCount
		if err := rows.Scan(&item.Origin, &item.CardID, &item.Command, &item.Count); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
