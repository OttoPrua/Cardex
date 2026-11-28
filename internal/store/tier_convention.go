package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// TierConventionSchema activates semantic T1 normalization in the application.
// The on-disk wire spelling never changes: a v12 binary still interprets it in
// its original convention even after a new binary writes to this database.
// Schemas 13 and 14 are reserved for D1 and ST1, respectively.
const TierConventionSchema = 15

var ErrTierUpgradeRequired = errors.New("tier upgrade required: drain the old scheduler, then run cardex migrate tiers --yes")

// TierConventionActive checks the exact typed activation and migration
// receipt together. A numeric maximum, a bare receipt and a stray table all
// fail to establish the convention on their own.
func (s *Store) TierConventionActive(ctx context.Context) (bool, error) {
	return tierConventionActive(ctx, s.q(ctx))
}

func tierConventionActive(ctx context.Context, q execer) (bool, error) {
	var present bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema
		WHERE type='table' AND name='tier_semantics_activation')`).Scan(&present); err != nil || !present {
		return false, err
	}
	var active bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM tier_semantics_activation a JOIN schema_migrations m ON m.version=a.schema_version
		WHERE a.singleton=1 AND typeof(a.schema_version)='integer'
		AND a.schema_version=? AND a.convention=?
	)`, TierConventionSchema, model.TierConvention).Scan(&active)
	return active, err
}

func recordTierActivation(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tier_semantics_activation (
		singleton INTEGER NOT NULL PRIMARY KEY CHECK(singleton=1),
		schema_version INTEGER NOT NULL,
		convention TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO tier_semantics_activation(singleton,schema_version,convention)
		VALUES (1,?,?) ON CONFLICT(singleton) DO NOTHING`, TierConventionSchema, model.TierConvention); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
		TierConventionSchema, formatTime(time.Now())); err != nil {
		return err
	}
	active, err := tierConventionActive(ctx, conn)
	if err != nil {
		return err
	}
	if !active {
		return errors.New("tier activation has an incompatible receipt")
	}
	return nil
}

func validateTierUpgrade(ctx context.Context, conn *sql.Conn) error {
	var pending bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM attempts WHERE started_at <> '' AND ended_at = ''
		UNION ALL SELECT 1 FROM cards WHERE status IN ('running', 'harvesting')
		UNION ALL
		SELECT 1 FROM attempts a JOIN cards c ON c.id=a.card_id
		WHERE c.kind IN ('plan','replan','normalize','ranking')
		AND (
			(a.outcome='' AND (a.ended_at<>'' OR a.exited<>0))
			OR (a.outcome IN ('passed','result_unparsable')
				AND NOT EXISTS (SELECT 1 FROM events e WHERE e.entity='attempt:'||a.id
					AND e.event_type='dev_disposed'))
		)
	)`).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return errors.New("drain running attempts and let the old scheduler consume finished planning or ranking results before migrating tier labels")
	}
	// Disposing an ambiguous plan can record dev_disposed while scheduling a
	// normalizer that still reads the old result. Unlike its source attempt,
	// the ready normalizer has no outcome or running record yet.
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM cards n JOIN cards source
			ON source.id=CASE WHEN json_valid(n.deps) THEN json_extract(n.deps, '$[0]') END
		WHERE n.kind='normalize' AND source.kind IN ('design','plan','replan')
		AND n.status NOT IN ('done','merged','failed','canceled')
	)`).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return errors.New("pending legacy normalization of a design, plan, or replan: finish or cancel it in the old runtime before migrating tier labels")
	}
	for _, field := range []struct {
		table, column string
		validate      func(string) error
	}{
		{"cards", "tier", func(s string) error { _, err := model.TranslateLegacyTier(model.Tier(s)); return err }},
		{"channels", "tier", func(s string) error { _, err := model.TranslateLegacyTier(model.Tier(s)); return err }},
		{"positioning", "tiers", func(s string) error {
			var values []model.Tier
			if err := json.Unmarshal([]byte(s), &values); err != nil {
				return errors.New("malformed positioning tiers")
			}
			_, err := translateTiers(values)
			return err
		}},
		{"attempts", "route_reason", func(s string) error {
			var reason model.RouteReason
			if err := json.Unmarshal([]byte(s), &reason); err != nil {
				return errors.New("malformed route reason")
			}
			_, err := translateRouteTiers(reason)
			return err
		}},
	} {
		rows, err := conn.QueryContext(ctx, "SELECT "+field.column+" FROM "+field.table)
		if err != nil {
			return err
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				_ = rows.Close()
				return err
			}
			if err := field.validate(value); err != nil {
				_ = rows.Close()
				return fmt.Errorf("%s: invalid legacy tier field: %w", field.table, err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	holds, err := conn.QueryContext(ctx, `SELECT body FROM cards WHERE kind='review' AND instr(body, ?) > 0`, model.ReviewHoldMarker)
	if err != nil {
		return err
	}
	defer holds.Close()
	for holds.Next() {
		var body string
		if err := holds.Scan(&body); err != nil {
			return err
		}
		if _, _, _, _, err := model.ParseReviewHold(body); err != nil {
			return fmt.Errorf("review hold: %w", err)
		}
	}
	return holds.Err()
}

// wireTier is called only after an explicit Valid check at card/channel writes.
func wireTier(t model.Tier) string {
	w, _ := model.TranslateLegacyTier(t)
	return string(w)
}

// The mapping is its own inverse. Copy input slices and optional dispatch
// records so marshaling them never changes what a caller passed to the store.
func translateTiers(in []model.Tier) ([]model.Tier, error) {
	out := slices.Clone(in)
	for i, raw := range out {
		next, err := model.TranslateLegacyTier(raw)
		if err != nil {
			return nil, err
		}
		if next == model.TierNone {
			return nil, errors.New("empty positioning tier")
		}
		out[i] = next
	}
	return out, nil
}

func translateRouteTiers(in model.RouteReason) (model.RouteReason, error) {
	out := in
	out.Candidates = slices.Clone(in.Candidates)
	var err error
	for i, candidate := range out.Candidates {
		out.Candidates[i].Tier, err = model.TranslateLegacyTier(candidate.Tier)
		if err != nil {
			return model.RouteReason{}, err
		}
	}
	if in.Dispatch != nil {
		d := *in.Dispatch
		if d.Tier, err = model.TranslateLegacyTier(d.Tier); err != nil {
			return model.RouteReason{}, err
		}
		if d.ActualTier, err = model.TranslateLegacyTier(d.ActualTier); err != nil {
			return model.RouteReason{}, err
		}
		out.Dispatch = &d
	}
	return out, nil
}

func marshalRouteReason(in model.RouteReason) (string, error) {
	wire, err := translateRouteTiers(in)
	if err != nil {
		return "", err
	}
	return mustJSON(wire)
}
