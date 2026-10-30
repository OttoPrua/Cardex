package store

import (
	"context"
	"fmt"

	"cardex/internal/model"
)

func preparePositioning(p *model.Positioning) error {
	if p == nil {
		return fmt.Errorf("nil positioning")
	}
	id, err := ensureID(p.ID, "pos")
	if err != nil {
		return err
	}
	p.ID = id
	if p.Tiers == nil {
		p.Tiers = []model.Tier{}
	}
	for _, tier := range p.Tiers {
		if !tier.Valid() || tier == "" {
			return invalid("tier", tier)
		}
	}
	p.Capabilities = normStrings(p.Capabilities)
	return nil
}

func (s *Store) CreatePositioning(ctx context.Context, p *model.Positioning) error {
	if err := preparePositioning(p); err != nil {
		return err
	}
	tiers, err := mustJSON(p.Tiers)
	if err != nil {
		return err
	}
	caps, err := mustJSON(p.Capabilities)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO positioning(id, version, model, family, tiers, capabilities, preference) VALUES (?,?,?,?,?,?,?)`,
		p.ID, p.Version, p.Model, p.Family, tiers, caps, p.Preference)
	return err
}

func (s *Store) GetPositioning(ctx context.Context, id string) (model.Positioning, error) {
	row := s.q(ctx).QueryRowContext(ctx, positioningSelect+` WHERE id=?`, id)
	return scanPositioning(row)
}

func (s *Store) UpdatePositioning(ctx context.Context, p *model.Positioning) error {
	if p == nil || p.ID == "" {
		return fmt.Errorf("positioning id required")
	}
	if err := preparePositioning(p); err != nil {
		return err
	}
	tiers, err := mustJSON(p.Tiers)
	if err != nil {
		return err
	}
	caps, err := mustJSON(p.Capabilities)
	if err != nil {
		return err
	}
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE positioning SET version=?, model=?, family=?, tiers=?, capabilities=?, preference=? WHERE id=?`,
		p.Version, p.Model, p.Family, tiers, caps, p.Preference, p.ID)
	return rowsAffected(res, err)
}

func (s *Store) DeletePositioning(ctx context.Context, id string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM positioning WHERE id=?`, id)
	return rowsAffected(res, err)
}

const positioningSelect = `SELECT id, version, model, family, tiers, capabilities, preference FROM positioning`

func (s *Store) ListPositioning(ctx context.Context) ([]model.Positioning, error) {
	rows, err := s.q(ctx).QueryContext(ctx, positioningSelect+` ORDER BY version, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Positioning{}
	for rows.Next() {
		item, err := scanPositioning(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanPositioning(sc scanner) (model.Positioning, error) {
	var p model.Positioning
	var tiers, caps string
	if err := sc.Scan(&p.ID, &p.Version, &p.Model, &p.Family, &tiers, &caps, &p.Preference); err != nil {
		return p, err
	}
	if err := parseJSON(tiers, &p.Tiers); err != nil {
		return p, err
	}
	if err := parseJSON(caps, &p.Capabilities); err != nil {
		return p, err
	}
	if p.Tiers == nil {
		p.Tiers = []model.Tier{}
	}
	p.Capabilities = normStrings(p.Capabilities)
	return p, nil
}
