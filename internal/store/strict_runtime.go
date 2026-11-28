package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

// An older scheduler must stop strict cards before even starting its separate
// classification attempt. Store a reserved, necessarily missing dependency in
// the legacy array, but don't expose or evaluate it in the current runtime.
const strictRuntimeGuard = "__cardex_strict_deps_current_runtime_required__"

func storedDependencies(c model.Card) []string {
	if !c.StrictDeps {
		return c.Deps
	}
	deps := append([]string(nil), c.Deps...)
	if len(deps) == 0 {
		// Never consume the review/repair/rebase/normalize subject slot.
		deps = append(deps, "")
	}
	return append(deps, strictRuntimeGuard)
}

func readStrictDependencies(c *model.Card) {
	if !c.StrictDeps {
		return
	}
	deps := c.Deps[:0]
	for _, id := range c.Deps {
		if id != strictRuntimeGuard {
			deps = append(deps, id)
		}
	}
	if len(deps) == 1 && deps[0] == "" {
		deps = deps[:0]
	}
	c.Deps = deps
}

func strictRuntimeFenceMissing(ctx context.Context, conn *sql.Conn) (bool, error) {
	var missing bool
	err := conn.QueryRowContext(ctx, `SELECT
		NOT EXISTS (SELECT 1 FROM pragma_table_info('cards') WHERE name='strict_deps')
		OR NOT EXISTS (SELECT 1 FROM pragma_table_info('attempts') WHERE name='strict_runtime_rev')
		OR (SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name IN
			('strict_card_insert','strict_card_update','strict_attempt_insert','strict_attempt_update')) <> 4`).Scan(&missing)
	return missing, err
}

func installStrictRuntimeFence(ctx context.Context, conn *sql.Conn) error {
	var strict int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cards') WHERE name='strict_deps'`).Scan(&strict); err != nil {
		return err
	}
	if strict == 0 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE cards ADD COLUMN strict_deps INTEGER NOT NULL DEFAULT 0 CHECK (strict_deps IN (0, 1))`); err != nil {
			return err
		}
	}
	var epoch int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('attempts') WHERE name='strict_runtime_rev'`).Scan(&epoch); err != nil {
		return err
	}
	if epoch == 0 {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE attempts ADD COLUMN strict_runtime_rev INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	var reserved int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM cards WHERE id=?`, strictRuntimeGuard).Scan(&reserved); err != nil {
		return err
	}
	if reserved != 0 {
		return fmt.Errorf("reserved strict runtime guard id already exists")
	}
	// The hypothetical standalone v12 already stored strict cards with plain
	// arrays. Add the guard before installing the constraints.
	if _, err := conn.ExecContext(ctx, `UPDATE cards SET deps=json_insert(
		CASE WHEN coalesce(json_array_length(deps),0)=0 THEN '[""]' ELSE deps END, '$[#]', ?)
		WHERE strict_deps=1 AND NOT EXISTS (SELECT 1 FROM json_each(cards.deps) WHERE value=?)`,
		strictRuntimeGuard, strictRuntimeGuard); err != nil {
		return err
	}
	// Only grandfather an already claimed v12 attempt's first terminal
	// write, without enabling any legacy launch or PID change.
	for _, stmt := range []string{
		`CREATE TRIGGER IF NOT EXISTS strict_card_insert BEFORE INSERT ON cards
		WHEN NEW.id = 'RUNTIME_GUARD' OR (NEW.strict_deps=1 AND NOT EXISTS
			(SELECT 1 FROM json_each(NEW.deps) WHERE value='RUNTIME_GUARD'))
		BEGIN SELECT RAISE(ABORT,'strict dependency cards require the current runtime'); END`,
		`CREATE TRIGGER IF NOT EXISTS strict_card_update BEFORE UPDATE OF id,deps,strict_deps ON cards
		WHEN NEW.id = 'RUNTIME_GUARD' OR (NEW.strict_deps=1 AND NOT EXISTS
			(SELECT 1 FROM json_each(NEW.deps) WHERE value='RUNTIME_GUARD'))
		BEGIN SELECT RAISE(ABORT,'strict dependency cards require the current runtime'); END`,
		`CREATE TRIGGER IF NOT EXISTS strict_attempt_insert BEFORE INSERT ON attempts
		WHEN NEW.strict_runtime_rev<=0 AND EXISTS (SELECT 1 FROM cards WHERE id=NEW.card_id AND strict_deps=1)
		BEGIN SELECT RAISE(ABORT,'strict dependency attempts require the current runtime'); END`,
		`CREATE TRIGGER IF NOT EXISTS strict_attempt_update BEFORE UPDATE OF card_id,pid ON attempts
		WHEN NEW.strict_runtime_rev<=OLD.strict_runtime_rev
			AND EXISTS (SELECT 1 FROM cards WHERE id IN (OLD.card_id,NEW.card_id) AND strict_deps=1)
			AND NOT (OLD.strict_runtime_rev=0 AND OLD.pid>0 AND NEW.pid=OLD.pid
				AND NEW.card_id=OLD.card_id AND OLD.ended_at='' AND NEW.ended_at<>'')
		BEGIN SELECT RAISE(ABORT,'strict dependency attempts require the current runtime'); END`,
	} {
		if _, err := conn.ExecContext(ctx, strings.ReplaceAll(stmt, "RUNTIME_GUARD", strictRuntimeGuard)); err != nil {
			return err
		}
	}
	return nil
}
