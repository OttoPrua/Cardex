package store

import (
	"context"
	"fmt"

	"cardex/internal/model"
)

func prepareChannel(c *model.Channel) error {
	if c == nil {
		return fmt.Errorf("nil channel")
	}
	id, err := ensureID(c.ID, "ch")
	if err != nil {
		return err
	}
	c.ID = id
	if !c.Runner.Valid() {
		return invalid("runner", c.Runner)
	}
	if !c.Tier.Valid() {
		return invalid("tier", c.Tier)
	}
	if !c.Health.Valid() {
		return invalid("health", c.Health)
	}
	c.Models = normStrings(c.Models)
	if c.QuotaWindows == nil {
		c.QuotaWindows = []model.QuotaWindow{}
	}
	return nil
}

func (s *Store) CreateChannel(ctx context.Context, c *model.Channel) error {
	if err := prepareChannel(c); err != nil {
		return err
	}
	models, err := mustJSON(c.Models)
	if err != nil {
		return err
	}
	windows, err := mustJSON(c.QuotaWindows)
	if err != nil {
		return err
	}
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO channels(id, runner, engine_profile, source_group, family, tier, models, concurrency, quota_windows, redline, health, last_error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, string(c.Runner), c.EngineProfile, c.SourceGroup, c.Family, string(c.Tier), models, c.Concurrency, windows, c.Redline, string(c.Health), c.LastError)
	return err
}

func (s *Store) GetChannel(ctx context.Context, id string) (model.Channel, error) {
	row := s.q(ctx).QueryRowContext(ctx, channelSelect+` WHERE id=?`, id)
	return scanChannel(row)
}

func (s *Store) UpdateChannel(ctx context.Context, c *model.Channel) error {
	if c == nil || c.ID == "" {
		return fmt.Errorf("channel id required")
	}
	if !c.Runner.Valid() || !c.Tier.Valid() || !c.Health.Valid() {
		return fmt.Errorf("invalid channel enum")
	}
	c.Models = normStrings(c.Models)
	if c.QuotaWindows == nil {
		c.QuotaWindows = []model.QuotaWindow{}
	}
	models, err := mustJSON(c.Models)
	if err != nil {
		return err
	}
	windows, err := mustJSON(c.QuotaWindows)
	if err != nil {
		return err
	}
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE channels SET runner=?, engine_profile=?, source_group=?, family=?, tier=?, models=?, concurrency=?, quota_windows=?, redline=?, health=?, last_error=? WHERE id=?`,
		string(c.Runner), c.EngineProfile, c.SourceGroup, c.Family, string(c.Tier), models, c.Concurrency, windows, c.Redline, string(c.Health), c.LastError, c.ID)
	return rowsAffected(res, err)
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM channels WHERE id=?`, id)
	return rowsAffected(res, err)
}

const channelSelect = `SELECT id, runner, engine_profile, source_group, family, tier, models, concurrency, quota_windows, redline, health, last_error FROM channels`

func (s *Store) ListChannels(ctx context.Context) ([]model.Channel, error) {
	rows, err := s.q(ctx).QueryContext(ctx, channelSelect+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Channel{}
	for rows.Next() {
		item, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanChannel(sc scanner) (model.Channel, error) {
	var c model.Channel
	var runner, tier, models, windows, health string
	if err := sc.Scan(&c.ID, &runner, &c.EngineProfile, &c.SourceGroup, &c.Family, &tier, &models, &c.Concurrency, &windows, &c.Redline, &health, &c.LastError); err != nil {
		return c, err
	}
	c.Runner = model.RunnerName(runner)
	c.Tier = model.Tier(tier)
	c.Health = model.Health(health)
	if err := parseJSON(models, &c.Models); err != nil {
		return c, err
	}
	if err := parseJSON(windows, &c.QuotaWindows); err != nil {
		return c, err
	}
	c.Models = normStrings(c.Models)
	if c.QuotaWindows == nil {
		c.QuotaWindows = []model.QuotaWindow{}
	}
	return c, nil
}
