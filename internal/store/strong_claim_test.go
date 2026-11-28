package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
)

func strongClaimFixture(t *testing.T) (context.Context, *Store, string) {
	t.Helper()
	if !strongcustody.Supported() {
		t.Skip("strong custody unsupported")
	}
	ctx := context.Background()
	db := openDB(t)
	task := []byte("Synthetic pinned task")
	sha := model.StrongArtifactDigest(task)
	if err := db.SaveStrongTask(ctx, "run-synthetic", sha, task); err != nil {
		t.Fatal(err)
	}
	return ctx, db, sha
}

func strongPending(t *testing.T, db *Store, ctx context.Context, stage model.StrongStage, n string) (model.Attempt, model.StrongProducerRegistration) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := model.Attempt{ID: "attempt-" + n, Card: "card-" + n, Worktree: dir}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	reg, err := db.RegisterStrongProducer(ctx, stage, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a, reg
}

func strongReady(t *testing.T, db *Store, ctx context.Context, a model.Attempt, reg model.StrongProducerRegistration, task, manifest string) *strongcustody.Capture {
	t.Helper()
	body, err := AssembleStrongBundle(ctx, db, reg.Source, task)
	if err != nil {
		t.Fatal(err)
	}
	c, err := strongcustody.Prepare(ctx, db, a, reg, task, manifest, body)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	exact := bytes.Clone(body)
	body[0] = 'X'
	first := c.LaunchInput()
	if !bytes.Equal(first, exact) {
		t.Fatal("prepare retained mutable caller input")
	}
	first[0] = 'X'
	if !bytes.Equal(c.LaunchInput(), exact) {
		t.Fatal("capture exposed mutable launch input")
	}
	return c
}

func strongSeal(t *testing.T, ctx context.Context, db *Store, a model.Attempt, c *strongcustody.Capture, refs ...string) *strongcustody.Result {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.Worktree, ".cardex-result.md"), []byte("## 结论\nExact answer.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if len(refs) > 0 {
		raw, err := json.Marshal(refs)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(a.Worktree, ".cardex-strong-references.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.Seal(ctx, a)
	if err != nil || s == nil {
		t.Fatalf("seal: %v", err)
	}
	exit := 0
	a.EndedAt = time.Now()
	a.Exit = &exit
	a.Outcome = model.OutcomePassed
	if err := db.UpdateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStrongClaimRejectsRollbackAndStaleInstance(t *testing.T) {
	ctx, db, task := strongClaimFixture(t)
	stage := model.StrongStage{Run: "run-synthetic", Stage: "blind-synthetic", Seat: "seat-one", Role: model.StrongBlind}
	a, reg := strongPending(t, db, ctx, stage, "rollback")
	body, err := AssembleStrongBundle(ctx, db, stage, task)
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("test rollback")
	err = db.WithWriteTx(ctx, func(tx context.Context) error {
		if c, err := strongcustody.Prepare(tx, db, a, reg, task, model.StrongEmptyManifestSHA(), body); c != nil || !errors.Is(err, model.ErrStrongArtifact) {
			t.Fatal("nested transaction emitted an uncommitted capture")
		}
		if claim, err := db.ClaimStrongProducer(tx, reg, task, model.StrongEmptyManifestSHA(), body); claim.Valid() || !errors.Is(err, model.ErrStrongArtifact) {
			t.Fatal("nested claim returned a committed-only token")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	h1 := strongReady(t, db, ctx, a, reg, task, model.StrongEmptyManifestSHA())
	var claimed int
	first, err := db.captureClaim(ctx, reg, &claimed)
	if err != nil || claimed != 1 || first.ManifestSHA != model.StrongArtifactDigest([]byte("[]")) {
		t.Fatalf("%+v %v", first, err)
	}
	// Simulate an interrupted older controller resetting its claim. This is
	// deliberate SQLite fault injection; production has no reclaim API.
	if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_producers SET claimed=0,claim_token='' WHERE attempt_id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	h2 := strongReady(t, db, ctx, a, reg, task, model.StrongEmptyManifestSHA())
	second, err := db.captureClaim(ctx, reg, &claimed)
	if err != nil || first.Token == second.Token {
		t.Fatalf("stale token revived: %v", err)
	}
	if s, err := h1.Seal(ctx, a); s != nil || !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("stale handle could seal: %v", err)
	}
	seal := strongSeal(t, ctx, db, a, h2)
	if seal.Record().Claim != second {
		t.Fatal("new seal did not use committed claim")
	}
	if _, err := db.SaveStrongArtifact(ctx, seal); err != nil {
		t.Fatal(err)
	}
	if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_producers SET claim_token=? WHERE attempt_id=?`, first.Token, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveStrongArtifact(ctx, seal); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("stale sealed instance could save: %v", err)
	}
}

func TestStrongClaimRejectsReorderedPeerManifest(t *testing.T) {
	ctx, db, task := strongClaimFixture(t)
	role := func(stage, seat string, role model.StrongRole) model.StrongStage {
		return model.StrongStage{Run: "run-synthetic", Stage: stage, Seat: seat, Role: role}
	}
	produce := func(stage model.StrongStage, name string) model.StrongArtifactRef {
		a, reg := strongPending(t, db, ctx, stage, name)
		seal := strongSeal(t, ctx, db, a, strongReady(t, db, ctx, a, reg, task, model.StrongEmptyManifestSHA()))
		r, err := db.SaveStrongArtifact(ctx, seal)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	one, two := produce(role("blind-one", "seat-one", model.StrongBlind), "first"), produce(role("blind-two", "seat-two", model.StrongBlind), "second")
	peer := role("peer-three", "seat-three", model.StrongPeer)
	original := []model.StrongArtifactRef{one, two}
	approved, err := db.BindStrongInputs(ctx, peer, original)
	if err != nil {
		t.Fatal(err)
	}
	a, reg := strongPending(t, db, ctx, peer, "peer")
	rewrite := func(order []model.StrongArtifactRef) string {
		raw, err := json.Marshal(order)
		if err != nil {
			t.Fatal(err)
		}
		sha := model.StrongArtifactDigest(raw)
		if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_input_bindings SET allowlist_json=?,allowlist_sha=? WHERE run_id=? AND target_stage=?`, string(raw), sha, peer.Run, peer.Stage); err != nil {
			t.Fatal(err)
		}
		return sha
	}
	if one.SHA != two.SHA || one.Attempt == two.Attempt || one.EnvelopeSHA == two.EnvelopeSHA {
		t.Fatal("fixture must use identical prose from distinct source lineages")
	}
	before, err := AssembleStrongBundle(ctx, db, peer, task)
	if err != nil {
		t.Fatal(err)
	}
	if tampered := rewrite([]model.StrongArtifactRef{two, one}); tampered == approved {
		t.Fatal("reordered manifest did not change its hash")
	}
	after, err := AssembleStrongBundle(ctx, db, peer, task)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("fixture needs indistinguishable rendered bytes: %v", err)
	}
	if premature, err := strongcustody.Prepare(ctx, db, a, reg, task, approved, after); premature != nil || !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("prelaunch reordered source identities could claim: %v", err)
	}
	if restored := rewrite(original); restored != approved {
		t.Fatal("original approved manifest was not restored")
	}
	h := strongReady(t, db, ctx, a, reg, task, approved)
	if _, err := db.BindStrongInputs(ctx, peer, original); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("allowed binding after claim: %v", err)
	}
	seal := strongSeal(t, ctx, db, a, h, "Material 01")
	newSHA := rewrite([]model.StrongArtifactRef{two, one})
	if _, err := db.SaveStrongArtifact(ctx, seal); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("rewritten manifest produced artifact: %v", err)
	}
	if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_producers SET manifest_sha=? WHERE attempt_id=?`, newSHA, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveStrongArtifact(ctx, seal); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("mutated adjacent pin accepted: %v", err)
	}
	oldSHA := rewrite(original)
	if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_producers SET manifest_sha=? WHERE attempt_id=?`, oldSHA, a.ID); err != nil {
		t.Fatal(err)
	}
	p, err := db.SaveStrongArtifact(ctx, seal)
	if err != nil {
		t.Fatal(err)
	}
	synth := role("synthesis", "", model.StrongSynthesis)
	if _, err := db.BindStrongInputs(ctx, synth, []model.StrongArtifactRef{p, two, one}); err != nil {
		t.Fatal(err)
	}
	if valid, err := db.StrongInputs(ctx, synth); err != nil || len(valid) != 3 || len(valid[0].References) != 1 || valid[0].References[0] != "Source 03" {
		t.Fatalf("incorrect positive: %+v %v", valid, err)
	}
	newSHA = rewrite([]model.StrongArtifactRef{two, one})
	if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_producers SET manifest_sha=? WHERE attempt_id=?`, newSHA, a.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := db.StrongInputs(ctx, synth); got != nil || !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("mutated peer meaning reached synthesis: %+v %v", got, err)
	}
}
