package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"cardex/internal/model"
)

// CreateEvent appends an event. Events cannot be updated or deleted.
func (s *Store) CreateEvent(ctx context.Context, e *model.Event) error {
	return s.AppendEvent(ctx, e)
}

// AppendEvent inserts one event row.
func (s *Store) AppendEvent(ctx context.Context, e *model.Event) error {
	if e == nil {
		return fmt.Errorf("nil event")
	}
	id, err := ensureID(e.ID, "e")
	if err != nil {
		return err
	}
	e.ID = id
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if len(e.Data) == 0 {
		e.Data = json.RawMessage(`{}`)
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO events(id, ts, entity, event_type, data) VALUES (?,?,?,?,?)`,
		e.ID, formatTime(e.TS), e.Entity, e.Type, string(e.Data))
	return err
}

// UpdateEvent is rejected. The event table is append-only.
func (s *Store) UpdateEvent(context.Context, *model.Event) error { return ErrAppendOnly }

// DeleteEvent is rejected. The event table is append-only.
func (s *Store) DeleteEvent(context.Context, string) error { return ErrAppendOnly }

func (s *Store) GetEvent(ctx context.Context, id string) (model.Event, error) {
	row := s.q(ctx).QueryRowContext(ctx, `SELECT id, ts, entity, event_type, data FROM events WHERE id=?`, id)
	return scanEvent(row)
}

func (s *Store) ListEvents(ctx context.Context, entity string) ([]model.Event, error) {
	q := `SELECT id, ts, entity, event_type, data FROM events`
	var args []any
	if entity != "" {
		q += ` WHERE entity=?`
		args = append(args, entity)
	}
	q += ` ORDER BY ts, id`
	rows, err := s.q(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		item, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanEvent(sc scanner) (model.Event, error) {
	var e model.Event
	var ts, data string
	if err := sc.Scan(&e.ID, &ts, &e.Entity, &e.Type, &data); err != nil {
		return e, err
	}
	var err error
	if e.TS, err = parseTime(ts); err != nil {
		return e, err
	}
	if data == "" {
		data = `{}`
	}
	e.Data = json.RawMessage(data)
	return e, nil
}
