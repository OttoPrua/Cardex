package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
)

const MaxStrongInputs = 16

// StrongMaterial is the no-lineage projection visible to prompt assembly.
type StrongMaterial struct {
	Alias      string
	Body       []byte
	References []string
}

// SaveStrongArtifact binds one complete canonical harvest receipt to the exact
// successful ledger attempt selected by the trusted stage controller.
func (s *Store) SaveStrongArtifact(ctx context.Context, sealed *strongcustody.Result) (model.StrongArtifactRef, error) {
	capture := sealed.Record()
	reg := capture.Producer
	source := reg.Source
	if !reg.Valid() || !model.StrongResultMarkdown(capture.Body) {
		return model.StrongArtifactRef{}, model.ErrStrongArtifact
	}
	body := bytes.Clone(capture.Body)
	if capture.Origin != model.StrongResultFileOrigin || capture.Format != model.StrongResultV1 || capture.SHA != model.StrongArtifactDigest(body) || capture.Size != len(body) {
		return model.StrongArtifactRef{}, model.ErrStrongArtifact
	}
	refs := capture.References
	if len(refs) == 0 {
		refs = nil
	}
	if len(refs) > MaxStrongInputs || (len(refs) > 0 && source.Role != model.StrongPeer) {
		return model.StrongArtifactRef{}, model.ErrStrongArtifact
	}
	serialized := capture.ReferenceBytes
	var parsed []string
	if len(serialized) > 1024 || (len(serialized) > 0 && json.Unmarshal(serialized, &parsed) != nil) || !slices.Equal(parsed, refs) {
		return model.StrongArtifactRef{}, model.ErrStrongArtifact
	}
	ref := model.StrongArtifactRef{Run: source.Run, Stage: source.Stage, Attempt: reg.Attempt, Origin: model.StrongResultFileOrigin, Format: model.StrongResultV1, SHA: model.StrongArtifactDigest(body), Size: len(body)}
	referencesSHA := model.StrongArtifactDigest(serialized)
	ref.EnvelopeSHA = strongEnvelope(reg, capture.Claim, ref, referencesSHA)
	err := s.WithWriteTx(ctx, func(ctx context.Context) error {
		if err := s.checkStrongProducer(ctx, reg, &capture.Claim); err != nil {
			return model.ErrStrongArtifact
		}
		rendered, err := assembleStrongBundle(ctx, s, source, capture.Claim.TaskSHA)
		if err != nil || model.StrongArtifactDigest(rendered) != capture.Claim.BundleSHA {
			return model.ErrStrongArtifact
		}

		a, err := s.GetAttempt(ctx, ref.Attempt)
		if err != nil || a.Card != reg.Card || a.Worktree != reg.Worktree || !passedStrongAttempt(a) {
			return model.ErrStrongArtifact
		}
		if source.Role == model.StrongPeer {
			peerInputs, err := s.readStrongBindings(ctx, source)
			if err != nil {
				return model.ErrStrongArtifact
			}
			seenAliases := make(map[string]bool, len(refs))
			for _, alias := range refs {
				if seenAliases[alias] || peerAliasIndex(alias, len(peerInputs)) < 0 {
					return model.ErrStrongArtifact
				}
				seenAliases[alias] = true
			}
		}
		_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO strong_artifacts
			(run_id,stage_id,attempt_id,card_id,role,seat,origin,format,sha,size,body,references_json,references_sha,envelope_sha)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(attempt_id) DO NOTHING`, ref.Run, ref.Stage, ref.Attempt, reg.Card, source.Role, source.Seat, ref.Origin, ref.Format, ref.SHA, ref.Size, body, string(serialized), referencesSHA, ref.EnvelopeSHA)
		if err != nil {
			return err
		}
		storedSource, storedBody, storedRefs, err := s.readStrongArtifact(ctx, ref)
		if err != nil || storedSource != source || !bytes.Equal(storedBody, body) || !slices.Equal(storedRefs, refs) {
			return model.ErrStrongArtifact
		}
		return nil
	})
	if err != nil {
		return model.StrongArtifactRef{}, err
	}
	return ref, nil
}

func passedStrongAttempt(a model.Attempt) bool {
	return a.Outcome == model.OutcomePassed && !a.EndedAt.IsZero() && a.Exit != nil && *a.Exit == 0
}

func (s *Store) readStrongArtifact(ctx context.Context, ref model.StrongArtifactRef) (model.StrongStage, []byte, []string, error) {
	var source model.StrongStage
	var card, refsJSON, refsSHA string
	var got model.StrongArtifactRef
	var body []byte
	err := s.q(ctx).QueryRowContext(ctx, `SELECT run_id,stage_id,attempt_id,card_id,role,seat,origin,format,sha,size,
		CASE WHEN length(body) BETWEEN 1 AND ? THEN body END, CASE WHEN length(references_json) <= 1024 THEN references_json END, references_sha, envelope_sha FROM strong_artifacts
		WHERE run_id=? AND stage_id=? AND attempt_id=?`, model.StrongResultMax, ref.Run, ref.Stage, ref.Attempt).
		Scan(&got.Run, &got.Stage, &got.Attempt, &card, &source.Role, &source.Seat, &got.Origin, &got.Format, &got.SHA, &got.Size, &body, &refsJSON, &refsSHA, &got.EnvelopeSHA)
	source.Run, source.Stage = got.Run, got.Stage
	if err != nil || !source.Valid() || got != ref || ref.Origin != model.StrongResultFileOrigin || ref.Format != model.StrongResultV1 ||
		!model.StrongResultMarkdown(body) || len(body) != ref.Size || model.StrongArtifactDigest(body) != ref.SHA {
		return model.StrongStage{}, nil, nil, model.ErrStrongArtifact
	}
	reg, err := s.strongProducer(ctx, ref.Attempt)
	if err != nil || reg.Source != source || reg.Card != card {
		return model.StrongStage{}, nil, nil, model.ErrStrongArtifact
	}
	var claimed int
	claim, err := s.captureClaim(ctx, reg, &claimed)
	if err != nil || claimed != 1 || strongEnvelope(reg, claim, ref, refsSHA) != ref.EnvelopeSHA || s.checkStrongProducer(ctx, reg, &claim) != nil {
		return model.StrongStage{}, nil, nil, model.ErrStrongArtifact
	}
	a, err := s.GetAttempt(ctx, ref.Attempt)
	if err != nil || a.Card != card || a.Worktree != reg.Worktree || !passedStrongAttempt(a) {
		return model.StrongStage{}, nil, nil, model.ErrStrongArtifact
	}
	var refs []string
	if model.StrongArtifactDigest([]byte(refsJSON)) != refsSHA || (refsJSON != "" && json.Unmarshal([]byte(refsJSON), &refs) != nil) || len(refs) > MaxStrongInputs ||
		(len(refs) > 0 && source.Role != model.StrongPeer) {
		return model.StrongStage{}, nil, nil, model.ErrStrongArtifact
	}
	return source, body, refs, nil
}

func allowedStrongBinding(target, source model.StrongStage) bool {
	if !target.Valid() || !source.Valid() || target.Run != source.Run || target.Stage == source.Stage {
		return false
	}
	switch target.Role {
	case model.StrongPeer:
		return source.Role == model.StrongBlind && source.Seat != target.Seat
	case model.StrongSynthesis:
		return source.Role == model.StrongBlind || source.Role == model.StrongPeer
	case model.StrongRecheck:
		return source.Role == model.StrongSynthesis
	default:
		return false
	}
}

// BindStrongInputs records one immutable, ordered manifest. Inserting the
// complete scope in a single SQL statement prevents a partial binding even
// when this operation is called inside a caller-owned transaction.
func (s *Store) BindStrongInputs(ctx context.Context, target model.StrongStage, refs []model.StrongArtifactRef) (string, error) {
	if !target.Valid() || len(refs) == 0 || len(refs) > MaxStrongInputs {
		return "", model.ErrStrongArtifact
	}
	// Compute from the approved ordered request, not from a later read of
	// the mutable database: the trusted controller retains this receipt.
	body, err := json.Marshal(refs)
	if err != nil || len(body) > maxStrongAllowlist {
		return "", model.ErrStrongArtifact
	}
	expected := model.StrongArtifactDigest(body)
	err = s.WithWriteTx(ctx, func(ctx context.Context) error {
		var consumed int
		err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM strong_producers WHERE run_id=? AND stage_id=? AND claimed=1)`, target.Run, target.Stage).Scan(&consumed)
		if err != nil || consumed != 0 {
			return model.ErrStrongArtifact
		}

		seen := make(map[string]bool, len(refs))
		for _, ref := range refs {
			source, _, _, err := s.readStrongArtifact(ctx, ref)
			if err != nil || seen[source.Stage] || !allowedStrongBinding(target, source) {
				return model.ErrStrongArtifact
			}
			seen[source.Stage] = true
		}
		_, err = s.q(ctx).ExecContext(ctx, `INSERT INTO strong_input_bindings(run_id,target_stage,role,target_seat,allowlist_json,allowlist_sha)
			VALUES(?,?,?,?,?,?) ON CONFLICT(run_id,target_stage) DO NOTHING`,
			target.Run, target.Stage, target.Role, target.Seat, string(body), expected)
		if err != nil {
			return err
		}
		got, stored, err := s.readStrongManifest(ctx, target)
		if err != nil || stored != expected || !slices.Equal(got, refs) {
			return model.ErrStrongArtifact
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return expected, nil
}

const maxStrongAllowlist = 12 << 10

func (s *Store) readStrongBindings(ctx context.Context, target model.StrongStage) ([]model.StrongArtifactRef, error) {
	refs, _, err := s.readStrongManifest(ctx, target)
	return refs, err
}

func (s *Store) currentStrongManifestSHA(ctx context.Context, target model.StrongStage) (string, error) {
	if target.Role == model.StrongBlind || target.Role == model.StrongEvidence {
		var exists int
		err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM strong_input_bindings WHERE run_id=? AND target_stage=?)`, target.Run, target.Stage).Scan(&exists)
		if err != nil || exists != 0 {
			return "", model.ErrStrongArtifact
		}
		return model.StrongEmptyManifestSHA(), nil
	}
	_, sha, err := s.readStrongManifest(ctx, target)
	return sha, err
}

func (s *Store) readStrongManifest(ctx context.Context, target model.StrongStage) ([]model.StrongArtifactRef, string, error) {
	var body, sha string
	err := s.q(ctx).QueryRowContext(ctx, `SELECT CASE WHEN length(allowlist_json) <= ? THEN allowlist_json END, allowlist_sha
		FROM strong_input_bindings WHERE run_id=? AND target_stage=? AND role=? AND target_seat=?`,
		maxStrongAllowlist, target.Run, target.Stage, target.Role, target.Seat).Scan(&body, &sha)
	var refs []model.StrongArtifactRef
	if err != nil || model.StrongArtifactDigest([]byte(body)) != sha || json.Unmarshal([]byte(body), &refs) != nil || len(refs) == 0 || len(refs) > MaxStrongInputs {
		return nil, "", model.ErrStrongArtifact
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref.Run != target.Run || !model.StrongKey(ref.Stage) || !model.StrongKey(ref.Attempt) || seen[ref.Stage] {
			return nil, "", model.ErrStrongArtifact
		}
		seen[ref.Stage] = true
	}
	return refs, sha, nil
}

// StrongInputs projects only aliases and bytes for one recipient. It never
// accepts caller-controlled filesystem paths or a runner-supplied selection.
func (s *Store) StrongInputs(ctx context.Context, target model.StrongStage) ([]StrongMaterial, error) {
	if !target.Valid() {
		return nil, model.ErrStrongArtifact
	}
	if target.Role == model.StrongBlind || target.Role == model.StrongEvidence {
		return nil, nil
	}
	var materials []StrongMaterial
	err := s.WithTx(ctx, func(ctx context.Context) error {
		refs, err := s.readStrongBindings(ctx, target)
		if err != nil {
			return err
		}
		materials = make([]StrongMaterial, len(refs))
		total := 0
		for i, ref := range refs {
			if ref.Size <= 0 || ref.Size > model.StrongResultMax || total+ref.Size > model.StrongBundleMax {
				return model.ErrStrongArtifact
			}
			total += ref.Size
			source, body, citations, err := s.readStrongArtifact(ctx, ref)
			if err != nil || !allowedStrongBinding(target, source) {
				return model.ErrStrongArtifact
			}
			materials[i] = StrongMaterial{Alias: strongAlias(target.Role, i+1), Body: body}
			if target.Role != model.StrongSynthesis || len(citations) == 0 {
				continue
			}
			peerInputs, err := s.readStrongBindings(ctx, source)
			if err != nil {
				return err
			}
			for _, citation := range citations {
				iPeer := peerAliasIndex(citation, len(peerInputs))
				if iPeer < 0 {
					return model.ErrStrongArtifact
				}
				j := slices.Index(refs, peerInputs[iPeer])
				if j < 0 {
					return model.ErrStrongArtifact
				}
				materials[i].References = append(materials[i].References, strongAlias(target.Role, j+1))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return materials, nil
}

// SaveStrongTask pins the already approved task once per run. ST2 supplies the
// expected digest from its separately approved plan; ST1 does not grant it.
func (s *Store) SaveStrongTask(ctx context.Context, run, sha string, task []byte) error {
	if !model.StrongKey(run) || !model.StrongResultBody(task) || model.StrongArtifactDigest(task) != sha {
		return model.ErrStrongArtifact
	}
	return s.WithWriteTx(ctx, func(ctx context.Context) error {
		_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO strong_tasks(run_id,sha,size,body) VALUES(?,?,?,?) ON CONFLICT(run_id) DO NOTHING`, run, sha, len(task), task)
		if err != nil {
			return err
		}
		got, err := s.StrongTask(ctx, run, sha)
		if err != nil || !bytes.Equal(got, task) {
			return model.ErrStrongArtifact
		}
		return nil
	})
}

func (s *Store) StrongTask(ctx context.Context, run, expectedSHA string) ([]byte, error) {
	var body []byte
	var size int
	err := s.q(ctx).QueryRowContext(ctx, `SELECT size,CASE WHEN length(body) BETWEEN 1 AND ? THEN body END FROM strong_tasks
	WHERE run_id=? AND sha=?`, model.StrongResultMax, run, expectedSHA).Scan(&size, &body)
	if err != nil || !model.StrongKey(run) || !model.StrongResultBody(body) || size != len(body) || model.StrongArtifactDigest(body) != expectedSHA {
		return nil, model.ErrStrongArtifact
	}
	return body, nil
}

func strongAlias(role model.StrongRole, i int) string {
	if role == model.StrongSynthesis {
		return fmt.Sprintf("Source %02d", i)
	}
	return fmt.Sprintf("Material %02d", i)
}

func peerAliasIndex(alias string, count int) int {
	for i := 0; i < count; i++ {
		if strongAlias(model.StrongPeer, i+1) == alias {
			return i
		}
	}
	return -1
}

// CheckStrongAnonymity rejects known private key tokens in allowed free-form
// content. It is a preventable exact-ID check, not a claim that prose is
// impossible to attribute or arbitrary external identities are detectable.
func (s *Store) CheckStrongAnonymity(ctx context.Context, target model.StrongStage, approvedTaskSHA string, bodies ...[]byte) error {
	if !target.Valid() {
		return model.ErrStrongArtifact
	}
	keys := map[string]bool{target.Run: true, target.Stage: true, target.Seat: true, approvedTaskSHA: true}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT p.stage_id,p.attempt_id,p.card_id,p.seat,p.token,p.claim_token,p.manifest_sha,p.bundle_sha,COALESCE(a.sha,''),COALESCE(a.envelope_sha,''),COALESCE(a.references_sha,'')
		FROM strong_producers p LEFT JOIN strong_artifacts a ON a.attempt_id=p.attempt_id WHERE p.run_id=? LIMIT 513`, target.Run)
	if err != nil {
		return err
	}
	defer rows.Close()
	for n := 0; rows.Next(); n++ {
		if n >= 512 {
			return model.ErrStrongArtifact
		}
		var stage, attempt, card, seat, token, claim, manifest, bundle, sha, envelope, refs string
		if err := rows.Scan(&stage, &attempt, &card, &seat, &token, &claim, &manifest, &bundle, &sha, &envelope, &refs); err != nil {
			return err
		}
		for _, k := range []string{stage, attempt, card, seat, token, claim, manifest, bundle, sha, envelope, refs} {
			keys[k] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	delete(keys, "")
	isToken := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
	}
	for _, body := range bodies {
		start := -1
		for i, c := range body {
			if isToken(c) {
				if start < 0 {
					start = i
				}
				continue
			}
			if start >= 0 && keys[string(body[start:i])] {
				return model.ErrStrongArtifact
			}
			start = -1
		}
		if start >= 0 && keys[string(body[start:])] {
			return model.ErrStrongArtifact
		}
	}
	return nil
}
