package store

import (
	"context"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
)

// RecordStrongFakeReceipt accepts only an opaque receipt produced by the
// actual locally owned Fake subprocess for this lease, not attempt text.
func (s *Store) RecordStrongFakeReceipt(ctx context.Context, want StrongIntent, lease string, sealed *runner.StrongFakeReceipt) error {
	r := sealed.Record()
	if !model.StrongDigestKey(lease) || r.Challenge != lease || r.Attempt != want.Attempt || !model.StrongDigestKey(r.BundleSHA) {
		return model.ErrStrongArtifact
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		in, err := s.strongIntent(ctx, want.Key, want.Slot)
		if err != nil || !sameStrongIntent(in, want) || (in.State != strongStarting && in.State != strongStarted) || in.Route.Runner != string(model.RunnerFake) || in.Route.T0 {
			return model.ErrStrongPlan
		}
		var active, pin string
		if err := s.q(ctx).QueryRowContext(ctx, `SELECT lease,custody_pin FROM strong_run_intents WHERE intent_id=?`, in.ID).Scan(&active, &pin); err != nil || active != lease {
			return model.ErrStrongArtifact
		}
		reg, err := s.strongProducer(ctx, in.Attempt)
		if err != nil || reg.Worktree != r.WorkDir || reg.Card != in.Card {
			return model.ErrStrongArtifact
		}
		var claimed int
		claim, err := s.captureClaim(ctx, reg, &claimed)
		if err != nil || claimed != 1 || strongWorkflowCapturePin(claim) != pin || claim.BundleSHA != r.BundleSHA {
			return model.ErrStrongArtifact
		}
		_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO strong_fake_execution(intent_id,attempt_id,bundle_sha,custody_pin,exit_code,tokens,usage_source,usage_confidence)
		 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(intent_id) DO NOTHING`, in.ID, in.Attempt, r.BundleSHA, pin, r.Exit, r.Usage.Tokens, r.Usage.Source, r.Usage.Confidence)
		if err != nil {
			return err
		}
		exit, usage, err := s.strongMeasuredUsage(ctx, in, pin)
		if err != nil || exit != r.Exit || usage != r.Usage {
			return model.ErrStrongArtifact
		}
		return nil
	})
}

func (s *Store) strongMeasuredUsage(ctx context.Context, in StrongIntent, pin string) (int, model.Usage, error) {
	var exit int
	var usage model.Usage
	err := s.q(ctx).QueryRowContext(ctx, `SELECT exit_code,tokens,usage_source,usage_confidence FROM strong_fake_execution
	  WHERE intent_id=? AND attempt_id=? AND custody_pin=?`, in.ID, in.Attempt, pin).Scan(&exit, &usage.Tokens, &usage.Source, &usage.Confidence)
	return exit, usage, err
}
