package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

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
	if c.Status != model.CardNeedsInput {
		c.WaitQuestionID = ""
	}
	switch c.Sandbox {
	case "", "on", "off":
	default:
		return invalid("card sandbox", c.Sandbox)
	}
	if err := normReview("card review", &c.Review); err != nil {
		return err
	}
	c.Requires = normStrings(c.Requires)
	c.Deps = normStrings(c.Deps)
	if c.ID == strictRuntimeGuard {
		return fmt.Errorf("reserved card id %q", c.ID)
	}
	for _, id := range c.Deps {
		if strings.TrimSpace(id) == strictRuntimeGuard {
			return fmt.Errorf("reserved dependency id %q", id)
		}
	}
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
		s.noteStatus(ctx, *c)
		return nil
	})
}

func (s *Store) insertCard(ctx context.Context, c *model.Card) error {
	requires, err := mustJSON(c.Requires)
	if err != nil {
		return err
	}
	deps, err := mustJSON(storedDependencies(*c))
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
	_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO cards(id, objective_id, node_id, kind, title, body, tier, requires, deps, acceptance, base, status, wait_reason, rework_count, legacy, channel, model, rework_note, author_family, soft_tokens, hard_tokens, sandbox, review, changed_at, wait_question_id, strict_deps) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Objective, c.Node, string(c.Kind), c.Title, c.Body, wireTier(c.Tier), requires, deps, acceptance, c.Base, string(c.Status), c.WaitReason, c.ReworkCount, legacy, c.Channel, c.Model, c.ReworkNote, c.AuthorFamily, c.Budget.SoftTokens, c.Budget.HardTokens, c.Sandbox, c.Review, formatTime(time.Now()), c.WaitQuestionID, c.StrictDeps)
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

// ExecutionDependencyBlocker checks the current stored statuses and returns the
// first unsatisfied execution dependency (not a review or repair subject).
func (s *Store) ExecutionDependencyBlocker(ctx context.Context, card model.Card) (string, error) {
	for _, id := range card.ExecutionDependencyIDs() {
		dep, err := s.GetCard(ctx, id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if reason := card.DependencyBlocker(id, dep); reason != "" {
			return reason, nil
		}
	}
	return "", nil
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
		deps, err := mustJSON(storedDependencies(*c))
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
		q := `UPDATE cards SET objective_id=?, node_id=?, kind=?, title=?, body=?, tier=?, requires=?, deps=?, acceptance=?, base=?, status=?, wait_reason=?, rework_count=?, legacy=?, channel=?, model=?, rework_note=?, author_family=?, soft_tokens=?, hard_tokens=?, review=?, wait_question_id=?, strict_deps=?,
			changed_at=CASE WHEN status<>? OR wait_reason<>? THEN ? ELSE changed_at END WHERE id=?`
		args := []any{c.Objective, c.Node, string(c.Kind), c.Title, c.Body, wireTier(c.Tier), requires, deps, acceptance, c.Base, string(c.Status), c.WaitReason, c.ReworkCount, legacy, c.Channel, c.Model, c.ReworkNote, c.AuthorFamily, c.Budget.SoftTokens, c.Budget.HardTokens, c.Review, c.WaitQuestionID, c.StrictDeps, string(c.Status), c.WaitReason, formatTime(time.Now()), c.ID}
		var transitioned bool
		if hasLifecycleStatus(c.Status) {
			res, err := s.q(ctx).ExecContext(ctx, q+` AND (status<>? OR wait_question_id<>?)`, append(args, c.Status, c.WaitQuestionID)...)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			transitioned = n != 0
		}
		if !transitioned {
			if err := rowsAffected(s.q(ctx).ExecContext(ctx, q, args...)); err != nil {
				return err
			}
		}
		if _, err := s.q(ctx).ExecContext(ctx, `DELETE FROM claims WHERE card_id=?`, c.ID); err != nil {
			return err
		}
		for i := range c.Claims {
			if err := s.insertClaim(ctx, &c.Claims[i]); err != nil {
				return err
			}
		}
		if transitioned {
			s.noteStatus(ctx, *c)
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

const cardSelect = `SELECT id, objective_id, node_id, kind, title, body, tier, requires, deps, acceptance, base, status, wait_reason, rework_count, legacy, channel, model, rework_note, author_family, soft_tokens, hard_tokens, sandbox, review, wait_question_id, strict_deps FROM cards`

func (s *Store) ListCards(ctx context.Context, objectiveID string) ([]model.Card, error) {
	return s.listCards(ctx, objectiveID, nil)
}

// ListCardsChangedSince includes card and attempt writes after since. For
// historical rows, the stored attempt times and status events provide evidence.
func (s *Store) ListCardsChangedSince(ctx context.Context, objectiveID string, since time.Time) ([]model.Card, error) {
	return s.listCards(ctx, objectiveID, &since)
}

func (s *Store) listCards(ctx context.Context, objectiveID string, since *time.Time) ([]model.Card, error) {
	var conditions []string
	var args []any
	if objectiveID != "" {
		conditions = append(conditions, `objective_id=?`)
		args = append(args, objectiveID)
	}
	if since != nil {
		// Stored times are UTC. Stripping Z makes the exact-second value sort
		// before a fractional value in the same second, at nanosecond precision.
		cutoff := strings.TrimSuffix(since.UTC().Format(time.RFC3339Nano), "Z")
		conditions = append(conditions, `(rtrim(cards.changed_at, 'Z') > ?
			OR EXISTS (SELECT 1 FROM attempts a WHERE a.card_id=cards.id AND (
				rtrim(a.changed_at, 'Z') > ? OR rtrim(a.started_at, 'Z') > ? OR rtrim(a.ended_at, 'Z') > ?))
			OR EXISTS (SELECT 1 FROM events e WHERE e.entity='card:' || cards.id
				AND e.event_type IN ('origin', 'card_created', 'card_status', 'card_marked_merged', 'merge_success') AND rtrim(e.ts, 'Z') > ?))`)
		args = append(args, cutoff, cutoff, cutoff, cutoff, cutoff)
	}
	q := cardSelect
	if len(conditions) > 0 {
		q += ` WHERE ` + strings.Join(conditions, ` AND `)
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
	if err := sc.Scan(&c.ID, &c.Objective, &c.Node, &kind, &c.Title, &c.Body, &tier, &requires, &deps, &acceptance, &c.Base, &status, &c.WaitReason, &c.ReworkCount, &legacy, &c.Channel, &c.Model, &c.ReworkNote, &c.AuthorFamily, &c.Budget.SoftTokens, &c.Budget.HardTokens, &c.Sandbox, &c.Review, &c.WaitQuestionID, &c.StrictDeps); err != nil {
		return c, err
	}
	c.Kind = model.CardKind(kind)
	var err error
	if c.Tier, err = model.TranslateLegacyTier(model.Tier(tier)); err != nil {
		return c, err
	}
	c.Status = model.CardStatus(status)
	if err := parseJSON(requires, &c.Requires); err != nil {
		return c, err
	}
	if err := parseJSON(deps, &c.Deps); err != nil {
		return c, err
	}
	readStrictDependencies(&c)
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

// SetUnassignedStrictTier only updates the tier of a still-strict, unassigned
// card. The scheduler validates the rest of the observed card under its
// surrounding writer transaction before calling this field-level CAS.
func (s *Store) SetUnassignedStrictTier(ctx context.Context, id string, tier model.Tier) (bool, error) {
	if tier == model.TierNone || !tier.Valid() {
		return false, invalid("tier", tier)
	}
	write, err := s.q(ctx).ExecContext(ctx, `UPDATE cards SET tier=? WHERE id=? AND strict_deps=1 AND tier=''`, wireTier(tier), id)
	if err != nil {
		return false, err
	}
	n, err := write.RowsAffected()
	return n == 1, err
}
