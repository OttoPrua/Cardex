package store

import (
	"context"
	"fmt"

	"github.com/OttoPrua/Cardex/internal/model"
)

func prepareCard(c *model.Card) error {
	if c == nil {
		return fmt.Errorf("nil card")
	}
	id, err := ensureID(c.ID, "c")
	if err != nil {
		return err
	}
	c.ID = id
	if !c.Kind.Valid() {
		return invalid("card kind", c.Kind)
	}
	if !c.Tier.Valid() {
		return invalid("tier", c.Tier)
	}
	if !c.Status.Valid() {
		return invalid("card status", c.Status)
	}
	c.Requires = normStrings(c.Requires)
	c.Deps = normStrings(c.Deps)
	c.Acceptance = normStrings(c.Acceptance)
	if c.Claims == nil {
		c.Claims = []model.Claim{}
	}
	for i := range c.Claims {
		c.Claims[i].Card = c.ID
		if c.Claims[i].Path == "" {
			return fmt.Errorf("claim path required")
		}
		cid, err := ensureID(c.Claims[i].ID, "cl")
		if err != nil {
			return err
		}
		c.Claims[i].ID = cid
	}
	return nil
}

func (s *Store) CreateCard(ctx context.Context, c *model.Card) error {
	if err := prepareCard(c); err != nil {
		return err
	}
	return s.WithTx(ctx, func(ctx context.Context) error {
		if err := s.insertCard(ctx, c); err != nil {
			return err
		}
		for i := range c.Claims {
			if err := s.insertClaim(ctx, &c.Claims[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) insertCard(ctx context.Context, c *model.Card) error {
	requires, err := mustJSON(c.Requires)
	if err != nil {
		return err
	}
	deps, err := mustJSON(c.Deps)
	if err != nil {
		return err
	}
	acceptance, err := mustJSON(c.Acceptance)
	if err != nil {
		return err
	}
	legacy, err := mustJSON(c.Legacy)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO cards(id, objective_id, node_id, kind, title, body, tier, requires, deps, acceptance, base, status, wait_reason, rework_count, legacy, channel, model, rework_note, author_family, soft_tokens, hard_tokens) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Objective, c.Node, string(c.Kind), c.Title, c.Body, string(c.Tier), requires, deps, acceptance, c.Base, string(c.Status), c.WaitReason, c.ReworkCount, legacy, c.Channel, c.Model, c.ReworkNote, c.AuthorFamily, c.Budget.SoftTokens, c.Budget.HardTokens)
	return err
}

func (s *Store) GetCard(ctx context.Context, id string) (model.Card, error) {
	row := s.q(ctx).QueryRowContext(ctx, cardSelect+` WHERE id=?`, id)
	c, err := scanCard(row)
	if err != nil {
		return c, err
	}
	c.Claims, err = s.claimsFor(ctx, c.ID)
	return c, err
}

func (s *Store) UpdateCard(ctx context.Context, c *model.Card) error {
	if c == nil || c.ID == "" {
		return fmt.Errorf("card id required")
	}
	saved := c.ID
	if err := prepareCard(c); err != nil {
		return err
	}
	if c.ID != saved {
		return fmt.Errorf("card id changed")
	}
	return s.WithTx(ctx, func(ctx context.Context) error {
		requires, err := mustJSON(c.Requires)
		if err != nil {
			return err
		}
		deps, err := mustJSON(c.Deps)
		if err != nil {
			return err
		}
		acceptance, err := mustJSON(c.Acceptance)
		if err != nil {
			return err
		}
		legacy, err := mustJSON(c.Legacy)
		if err != nil {
			return err
		}
		res, err := s.q(ctx).ExecContext(ctx, `UPDATE cards SET objective_id=?, node_id=?, kind=?, title=?, body=?, tier=?, requires=?, deps=?, acceptance=?, base=?, status=?, wait_reason=?, rework_count=?, legacy=?, channel=?, model=?, rework_note=?, author_family=?, soft_tokens=?, hard_tokens=? WHERE id=?`,
			c.Objective, c.Node, string(c.Kind), c.Title, c.Body, string(c.Tier), requires, deps, acceptance, c.Base, string(c.Status), c.WaitReason, c.ReworkCount, legacy, c.Channel, c.Model, c.ReworkNote, c.AuthorFamily, c.Budget.SoftTokens, c.Budget.HardTokens, c.ID)
		if err := rowsAffected(res, err); err != nil {
			return err
		}
		if _, err := s.q(ctx).ExecContext(ctx, `DELETE FROM claims WHERE card_id=?`, c.ID); err != nil {
			return err
		}
		for i := range c.Claims {
			if err := s.insertClaim(ctx, &c.Claims[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) DeleteCard(ctx context.Context, id string) error {
	return s.WithTx(ctx, func(ctx context.Context) error {
		if _, err := s.q(ctx).ExecContext(ctx, `DELETE FROM claims WHERE card_id=?`, id); err != nil {
			return err
		}
		res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM cards WHERE id=?`, id)
		return rowsAffected(res, err)
	})
}

const cardSelect = `SELECT id, objective_id, node_id, kind, title, body, tier, requires, deps, acceptance, base, status, wait_reason, rework_count, legacy, channel, model, rework_note, author_family, soft_tokens, hard_tokens FROM cards`

func (s *Store) ListCards(ctx context.Context, objectiveID string) ([]model.Card, error) {
	q := cardSelect
	var args []any
	if objectiveID != "" {
		q += ` WHERE objective_id=?`
		args = append(args, objectiveID)
	}
	q += ` ORDER BY id`
	rows, err := s.q(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Card{}
	for rows.Next() {
		item, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		claims, err := s.claimsFor(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Claims = claims
	}
	return out, nil
}

func scanCard(sc scanner) (model.Card, error) {
	var c model.Card
	var kind, tier, requires, deps, acceptance, status, legacy string
	if err := sc.Scan(&c.ID, &c.Objective, &c.Node, &kind, &c.Title, &c.Body, &tier, &requires, &deps, &acceptance, &c.Base, &status, &c.WaitReason, &c.ReworkCount, &legacy, &c.Channel, &c.Model, &c.ReworkNote, &c.AuthorFamily, &c.Budget.SoftTokens, &c.Budget.HardTokens); err != nil {
		return c, err
	}
	c.Kind = model.CardKind(kind)
	c.Tier = model.Tier(tier)
	c.Status = model.CardStatus(status)
	if err := parseJSON(requires, &c.Requires); err != nil {
		return c, err
	}
	if err := parseJSON(deps, &c.Deps); err != nil {
		return c, err
	}
	if err := parseJSON(acceptance, &c.Acceptance); err != nil {
		return c, err
	}
	if err := parseJSON(legacy, &c.Legacy); err != nil {
		return c, err
	}
	c.Requires = normStrings(c.Requires)
	c.Deps = normStrings(c.Deps)
	c.Acceptance = normStrings(c.Acceptance)
	c.Claims = []model.Claim{}
	return c, nil
}
