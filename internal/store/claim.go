package store

import (
	"context"
	"fmt"

	"cardex/internal/model"
)

func (s *Store) insertClaim(ctx context.Context, c *model.Claim) error {
	id, err := ensureID(c.ID, "cl")
	if err != nil {
		return err
	}
	c.ID = id
	if c.Path == "" {
		return fmt.Errorf("claim path required")
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO claims(id, card_id, path, symbol) VALUES (?,?,?,?)`, c.ID, c.Card, c.Path, c.Symbol)
	return err
}

func (s *Store) CreateClaim(ctx context.Context, c *model.Claim) error {
	if c == nil {
		return fmt.Errorf("nil claim")
	}
	return s.insertClaim(ctx, c)
}

func (s *Store) GetClaim(ctx context.Context, id string) (model.Claim, error) {
	row := s.q(ctx).QueryRowContext(ctx, `SELECT id, card_id, path, symbol FROM claims WHERE id=?`, id)
	return scanClaim(row)
}

func (s *Store) UpdateClaim(ctx context.Context, c *model.Claim) error {
	if c == nil || c.ID == "" {
		return fmt.Errorf("claim id required")
	}
	if c.Path == "" {
		return fmt.Errorf("claim path required")
	}
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE claims SET card_id=?, path=?, symbol=? WHERE id=?`, c.Card, c.Path, c.Symbol, c.ID)
	return rowsAffected(res, err)
}

func (s *Store) DeleteClaim(ctx context.Context, id string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM claims WHERE id=?`, id)
	return rowsAffected(res, err)
}

func (s *Store) ListClaims(ctx context.Context, cardID string) ([]model.Claim, error) {
	if cardID != "" {
		return s.claimsFor(ctx, cardID)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id, card_id, path, symbol FROM claims ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectClaims(rows)
}

func (s *Store) claimsFor(ctx context.Context, cardID string) ([]model.Claim, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id, card_id, path, symbol FROM claims WHERE card_id=? ORDER BY id`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectClaims(rows)
}

func collectClaims(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]model.Claim, error) {
	out := []model.Claim{}
	for rows.Next() {
		item, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanClaim(sc scanner) (model.Claim, error) {
	var c model.Claim
	err := sc.Scan(&c.ID, &c.Card, &c.Path, &c.Symbol)
	return c, err
}
