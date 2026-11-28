package store

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/strongevidence"
)

const strongEvidenceRecordMax = 3 << 20

func matchesStrongEvidence(p model.StrongPlan, record strongevidence.Record) bool {
	digest, err := record.Digest()
	if err != nil || digest != p.EvidenceSHA || record.Commit != p.EvidenceCommit || len(record.Files) != len(p.EvidencePaths) {
		return false
	}
	for i, f := range record.Files {
		if f.Path != p.EvidencePaths[i] {
			return false
		}
	}
	return true
}

// SaveStrongEvidence only accepts opaque resolver output for the exact
// preview. A result file or third-party echo cannot materialize evidence.
func (s *Store) SaveStrongEvidence(ctx context.Context, key StrongRunKey, snapshot *strongevidence.Snapshot) error {
	record := snapshot.Record()
	body, err := json.Marshal(record)
	if err != nil || len(body) > strongEvidenceRecordMax {
		return model.ErrStrongArtifact
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		p, v, err := s.strongPlan(ctx, key)
		if err != nil || v.Status != StrongAwaiting || !matchesStrongEvidence(p, record) {
			return model.ErrStrongArtifact
		}
		_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO strong_run_evidence(run_id,revision,plan_sha,evidence_sha,record)
		  VALUES(?,?,?,?,?) ON CONFLICT(run_id,revision) DO NOTHING`, key.Run, key.Revision, key.SHA, p.EvidenceSHA, body)
		if err != nil {
			return err
		}
		got, err := s.strongEvidence(ctx, p, key)
		gotBytes, _ := json.Marshal(got)
		if err != nil || !bytes.Equal(gotBytes, body) {
			return model.ErrStrongArtifact
		}
		return nil
	})
}

func (s *Store) strongEvidence(ctx context.Context, p model.StrongPlan, key StrongRunKey) (strongevidence.Record, error) {
	var r strongevidence.Record
	var b []byte
	err := s.q(ctx).QueryRowContext(ctx, `SELECT CASE WHEN length(record)<=? THEN record END FROM strong_run_evidence
	  WHERE run_id=? AND revision=? AND plan_sha=? AND evidence_sha=?`, strongEvidenceRecordMax, key.Run, key.Revision, key.SHA, p.EvidenceSHA).Scan(&b)
	if err != nil || json.Unmarshal(b, &r) != nil || !matchesStrongEvidence(p, r) {
		return r, model.ErrStrongArtifact
	}
	canonical, err := json.Marshal(r)
	if err != nil || !bytes.Equal(canonical, b) {
		return r, model.ErrStrongArtifact
	}
	return r, nil
}

func (s *Store) strongBundleEvidence(ctx context.Context, target model.StrongStage) ([]strongevidence.File, error) {
	if target.Role != model.StrongWorkflowEvidence && target.Role != model.StrongVerification && target.Role != model.StrongWorkflowReview && target.Role != model.StrongWorkflowRewrite {
		return nil, nil
	}
	prefix, name, ok := strings.Cut(target.Stage, "-")
	if !ok || !strings.HasPrefix(prefix, "v") {
		return nil, model.ErrStrongArtifact
	}
	revision, err := strconv.Atoi(strings.TrimPrefix(prefix, "v"))
	if err != nil || revision < 1 {
		return nil, model.ErrStrongArtifact
	}
	key := StrongRunKey{Run: target.Run, Revision: revision}
	if err := s.q(ctx).QueryRowContext(ctx, `SELECT plan_sha FROM strong_run_plans WHERE run_id=? AND revision=?`, target.Run, revision).Scan(&key.SHA); err != nil {
		return nil, model.ErrStrongArtifact
	}
	p, _, err := s.strongPlan(ctx, key)
	if err != nil {
		return nil, err
	}
	slot, ok := p.Slot(name)
	if !ok {
		return nil, model.ErrStrongArtifact
	}
	scope, ok := strongSource(key, slot)
	if !ok || scope != target {
		return nil, model.ErrStrongArtifact
	}
	d, err := s.strongEvidence(ctx, p, key)
	if err != nil {
		return nil, err
	}
	return d.Files, nil
}
