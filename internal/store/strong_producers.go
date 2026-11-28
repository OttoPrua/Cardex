package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/OttoPrua/Cardex/internal/model"
)

// RegisterStrongProducer is a controller-only prelaunch operation. It cannot
// relabel a previously started/completed ordinary attempt into strong lineage.
func (s *Store) RegisterStrongProducer(ctx context.Context, source model.StrongStage, attemptID string) (model.StrongProducerRegistration, error) {
	if !source.Valid() || !model.StrongKey(attemptID) {
		return model.StrongProducerRegistration{}, model.ErrStrongArtifact
	}
	var reg model.StrongProducerRegistration
	err := s.WithWriteTx(ctx, func(ctx context.Context) error {
		a, err := s.GetAttempt(ctx, attemptID)
		if err != nil || !pendingStrongAttempt(a) {
			return model.ErrStrongArtifact
		}

		reg, err = s.strongProducer(ctx, attemptID)
		if err == nil {
			if reg.Source != source || reg.Card != a.Card || reg.Worktree != a.Worktree {
				return model.ErrStrongArtifact
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var entropy [32]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return err
		}
		reg = model.StrongProducerRegistration{Source: source, Attempt: attemptID, Card: a.Card, Worktree: a.Worktree, Token: hex.EncodeToString(entropy[:])}
		_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO strong_producers
			(attempt_id,card_id,worktree,run_id,stage_id,role,seat,token) VALUES(?,?,?,?,?,?,?,?)`,
			reg.Attempt, reg.Card, reg.Worktree, source.Run, source.Stage, source.Role, source.Seat, reg.Token)
		return err
	})
	if err != nil {
		return model.StrongProducerRegistration{}, err
	}
	return reg, nil
}

func (s *Store) strongProducer(ctx context.Context, attemptID string) (model.StrongProducerRegistration, error) {
	var reg model.StrongProducerRegistration
	err := s.q(ctx).QueryRowContext(ctx, `SELECT attempt_id,card_id,worktree,run_id,stage_id,role,seat,token FROM strong_producers WHERE attempt_id=?`, attemptID).
		Scan(&reg.Attempt, &reg.Card, &reg.Worktree, &reg.Source.Run, &reg.Source.Stage, &reg.Source.Role, &reg.Source.Seat, &reg.Token)
	if err != nil {
		return model.StrongProducerRegistration{}, err
	}
	if !reg.Valid() {
		return model.StrongProducerRegistration{}, model.ErrStrongArtifact
	}
	return reg, nil
}

// The accepted ref independently pins both byte streams and the producer,
// including its run, stage, attempt, card, role, seat and custody registration.
func strongEnvelope(reg model.StrongProducerRegistration, claim model.StrongCaptureClaim, ref model.StrongArtifactRef, referencesSHA string) string {
	ref.EnvelopeSHA = ""
	body, _ := json.Marshal(struct {
		Version       int
		Producer      model.StrongProducerRegistration
		Claim         model.StrongCaptureClaim
		Artifact      model.StrongArtifactRef
		ReferencesSHA string
	}{1, reg, claim, ref, referencesSHA})
	return model.StrongArtifactDigest(body)
}

func pendingStrongAttempt(a model.Attempt) bool {
	return model.StrongKey(a.Card) && a.EndedAt.IsZero() && a.Exit == nil && a.Outcome == "" && a.PID == 0 &&
		a.StartedAt.IsZero() && filepath.IsAbs(a.Worktree) && filepath.Clean(a.Worktree) == a.Worktree
}

// CheckStrongProducer verifies an exact committed claim, or the independently
// durable pending state before claiming. Never emit a handle inside a caller tx.
func (s *Store) CheckStrongProducer(ctx context.Context, reg model.StrongProducerRegistration, expect *model.StrongCaptureClaim) error {
	if s == nil || !reg.Valid() {
		return model.ErrStrongArtifact
	}
	if expect == nil {
		if _, nested := ctx.Value(txKey{}).(execer); nested {
			return model.ErrStrongArtifact
		}
	}
	return s.checkStrongProducer(ctx, reg, expect)
}

func (s *Store) checkStrongProducer(ctx context.Context, reg model.StrongProducerRegistration, expect *model.StrongCaptureClaim) error {
	got, err := s.strongProducer(ctx, reg.Attempt)
	if err != nil || got != reg {
		return model.ErrStrongArtifact
	}
	var claimed int
	current, err := s.captureClaim(ctx, reg, &claimed)
	if err != nil {
		return model.ErrStrongArtifact
	}
	a, err := s.GetAttempt(ctx, reg.Attempt)
	if err != nil || a.Card != reg.Card || a.Worktree != reg.Worktree {
		return model.ErrStrongArtifact
	}
	if expect == nil {
		if claimed != 0 || !pendingStrongAttempt(a) {
			return model.ErrStrongArtifact
		}
		return nil
	}
	if claimed != 1 || !expect.Valid() || *expect != current {
		return model.ErrStrongArtifact
	}
	pin, err := s.currentStrongManifestSHA(ctx, reg.Source)
	if err != nil || pin != expect.ManifestSHA {
		return model.ErrStrongArtifact
	}
	return nil
}

func (s *Store) captureClaim(ctx context.Context, reg model.StrongProducerRegistration, claimed *int) (model.StrongCaptureClaim, error) {
	var claim model.StrongCaptureClaim
	err := s.q(ctx).QueryRowContext(ctx, `SELECT claimed,claim_token,manifest_sha,bundle_sha,task_sha FROM strong_producers WHERE attempt_id=? AND token=?`, reg.Attempt, reg.Token).
		Scan(claimed, &claim.Token, &claim.ManifestSHA, &claim.BundleSHA, &claim.TaskSHA)
	return claim, err
}

// ClaimStrongProducer returns only after its private transaction commits. It
// computes the actual deterministic bundle and requires the caller-staged
// bytes to match exactly, including the peer's ordered aliases.
func (s *Store) ClaimStrongProducer(ctx context.Context, reg model.StrongProducerRegistration, taskSHA, approvedManifestSHA string, rendered []byte) (model.StrongCaptureClaim, error) {
	if _, nested := ctx.Value(txKey{}).(execer); nested || !reg.Valid() || !model.StrongDigestKey(taskSHA) || !model.StrongDigestKey(approvedManifestSHA) || len(rendered) == 0 || len(rendered) > model.StrongBundleMax {
		return model.StrongCaptureClaim{}, model.ErrStrongArtifact
	}
	var claim model.StrongCaptureClaim
	err := s.WithWriteTx(ctx, func(ctx context.Context) error {
		if err := s.checkStrongProducer(ctx, reg, nil); err != nil {
			return err
		}
		manifest, err := s.currentStrongManifestSHA(ctx, reg.Source)
		if err != nil || manifest != approvedManifestSHA {
			return model.ErrStrongArtifact
		}
		canonical, err := assembleStrongBundle(ctx, s, reg.Source, taskSHA)
		if err != nil || !bytes.Equal(canonical, rendered) {
			return model.ErrStrongArtifact
		}
		var entropy [32]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return err
		}
		claim = model.StrongCaptureClaim{Token: hex.EncodeToString(entropy[:]), ManifestSHA: manifest, BundleSHA: model.StrongArtifactDigest(canonical), TaskSHA: taskSHA}
		res, err := s.q(ctx).ExecContext(ctx, `UPDATE strong_producers SET claimed=1,claim_token=?,manifest_sha=?,bundle_sha=?,task_sha=?
		WHERE attempt_id=? AND token=? AND claimed=0`, claim.Token, claim.ManifestSHA, claim.BundleSHA, claim.TaskSHA, reg.Attempt, reg.Token)
		if err := rowsAffected(res, err); err != nil {
			return model.ErrStrongArtifact
		}
		return nil
	})
	if err != nil {
		return model.StrongCaptureClaim{}, err
	}
	return claim, nil
}
