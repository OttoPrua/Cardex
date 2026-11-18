package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
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
	kept := make([]model.Tier, 0, len(p.Tiers))
	for _, tier := range p.Tiers {
		canon, ok := model.CanonicalTier(tier)
		if !ok {
			continue
		}
		kept = append(kept, canon)
	}
	p.Tiers = kept
	p.Capabilities = normStrings(p.Capabilities)
	kinds := make([]model.CardKind, 0, len(p.Kinds))
	for _, k := range p.Kinds {
		k = model.CardKind(strings.ToLower(strings.TrimSpace(string(k))))
		if !k.Valid() {
			return fmt.Errorf("positioning %s: unknown card kind %q", p.ID, k)
		}
		kinds = append(kinds, k)
	}
	p.Kinds = nil
	if len(kinds) > 0 {
		p.Kinds = kinds
	}
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
	kinds, err := mustJSON(p.Kinds)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO positioning(id, version, model, family, tiers, capabilities, preference, kinds) VALUES (?,?,?,?,?,?,?,?)`,
		p.ID, p.Version, p.Model, p.Family, tiers, caps, p.Preference, kinds)
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
	kinds, err := mustJSON(p.Kinds)
	if err != nil {
		return err
	}
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE positioning SET version=?, model=?, family=?, tiers=?, capabilities=?, preference=?, kinds=? WHERE id=?`,
		p.Version, p.Model, p.Family, tiers, caps, p.Preference, kinds, p.ID)
	return rowsAffected(res, err)
}

func (s *Store) DeletePositioning(ctx context.Context, id string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM positioning WHERE id=?`, id)
	return rowsAffected(res, err)
}

const positioningSelect = `SELECT id, version, model, family, tiers, capabilities, preference, kinds FROM positioning`

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
	var tiers, caps, kinds string
	if err := sc.Scan(&p.ID, &p.Version, &p.Model, &p.Family, &tiers, &caps, &p.Preference, &kinds); err != nil {
		return p, err
	}
	if err := parseJSON(tiers, &p.Tiers); err != nil {
		return p, err
	}
	if err := parseJSON(caps, &p.Capabilities); err != nil {
		return p, err
	}
	if err := parseJSON(kinds, &p.Kinds); err != nil {
		return p, err
	}
	if p.Tiers == nil {
		p.Tiers = []model.Tier{}
	}
	if len(p.Kinds) == 0 {
		p.Kinds = nil
	}
	p.Capabilities = normStrings(p.Capabilities)
	return p, nil
}
