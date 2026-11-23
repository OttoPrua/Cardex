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

// AddRead increments the management read counter. An empty part is ignored.
func (s *Store) AddRead(ctx context.Context, origin, cardID, command string) error {
	origin = strings.TrimSpace(origin)
	cardID = strings.TrimSpace(cardID)
	command = strings.TrimSpace(command)
	if s == nil || origin == "" || cardID == "" || command == "" {
		return nil
	}
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO mgmt_reads(origin, card_id, command, n) VALUES (?,?,?,1)
		ON CONFLICT(origin, card_id, command) DO UPDATE SET n = n + 1`, origin, cardID, command)
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
