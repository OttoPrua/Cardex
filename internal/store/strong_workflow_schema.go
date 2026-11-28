package store

import (
	"context"

	"github.com/OttoPrua/Cardex/internal/model"
)

// strongWorkflowPrototypeVersion is reserved only. The official global
// migration must run after the actual T1 v15 migration, never skip it.
const strongWorkflowPrototypeVersion = 16

var strongWorkflowPrototypeDDL = []string{
	`CREATE TABLE IF NOT EXISTS strong_run_plans (
		run_id TEXT NOT NULL, revision INTEGER NOT NULL, plan_sha TEXT NOT NULL, plan_json BLOB NOT NULL,
		status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
		approval_id TEXT NOT NULL DEFAULT '', approver TEXT NOT NULL DEFAULT '', approved_at TEXT NOT NULL DEFAULT '',
		reserved INTEGER NOT NULL DEFAULT 0 CHECK(reserved>=0), spent INTEGER NOT NULL DEFAULT 0 CHECK(spent>=0),
		PRIMARY KEY(run_id,revision), UNIQUE(run_id,plan_sha)
	)`,
	`CREATE TABLE IF NOT EXISTS strong_run_evidence (
		run_id TEXT NOT NULL, revision INTEGER NOT NULL, plan_sha TEXT NOT NULL,
		evidence_sha TEXT NOT NULL, record BLOB NOT NULL,
		PRIMARY KEY(run_id,revision)
	)`,
	`CREATE TABLE IF NOT EXISTS strong_run_slots (
		run_id TEXT NOT NULL, revision INTEGER NOT NULL, slot TEXT NOT NULL, ordinal INTEGER NOT NULL,
		state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', condition TEXT NOT NULL DEFAULT '',
		condition_reason TEXT NOT NULL DEFAULT '', condition_focus TEXT NOT NULL DEFAULT '', condition_sha TEXT NOT NULL DEFAULT '', condition_actor TEXT NOT NULL DEFAULT '',
		accepted_ref BLOB NOT NULL DEFAULT '', accepted_attempt TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(run_id,revision,slot)
	)`,
	`CREATE TABLE IF NOT EXISTS strong_run_intents (
		run_id TEXT NOT NULL, revision INTEGER NOT NULL, slot TEXT NOT NULL, intent_id TEXT NOT NULL UNIQUE,
		attempt_id TEXT NOT NULL UNIQUE, card_id TEXT NOT NULL, state TEXT NOT NULL,
		route_json BLOB NOT NULL, reserved INTEGER NOT NULL CHECK(reserved>0), measured INTEGER NOT NULL DEFAULT 0,
		lease TEXT NOT NULL DEFAULT '', custody_pin TEXT NOT NULL DEFAULT '', t0_approver TEXT NOT NULL DEFAULT '', t0_id TEXT NOT NULL DEFAULT '',
		usage_source TEXT NOT NULL DEFAULT '', usage_confidence REAL NOT NULL DEFAULT 0,
		PRIMARY KEY(run_id,revision,slot)
	)`,
	`CREATE TABLE IF NOT EXISTS strong_fake_execution (
		intent_id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL UNIQUE, bundle_sha TEXT NOT NULL,
		custody_pin TEXT NOT NULL, exit_code INTEGER NOT NULL, tokens INTEGER NOT NULL,
		usage_source TEXT NOT NULL, usage_confidence REAL NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS strong_approval_receipt_once ON strong_run_plans(approval_id) WHERE approval_id<>''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS strong_t0_receipt_once ON strong_run_intents(t0_id) WHERE t0_id<>''`,
}

// InstallStrongWorkflowPrototype is explicit for isolated synthetic tests.
// It is not called by Open, the CLI, the ordinary scheduler or production.
func (s *Store) InstallStrongWorkflowPrototype(ctx context.Context) error {
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		// Installing the isolated prototype is never a shortcut for T1.
		// It deliberately leaves the global database version at typed v15.
		var version int
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil || version != strongWorkflowPrototypeVersion-1 {
			return model.ErrStrongPlan
		}
		active, err := s.TierConventionActive(ctx)
		if err != nil || !active {
			return model.ErrStrongPlan
		}
		for _, q := range strongWorkflowPrototypeDDL {
			if _, err := s.q(ctx).ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
}
