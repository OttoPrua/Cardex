package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
)

func TestStrongArtifactLineageRejectsCrossScopeAndTampering(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if !strongcustody.Supported() {
		t.Skip("strong custody unsupported")
	}
	exit := 0
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := model.Attempt{ID: "att-synthetic-one", Card: "card-synthetic-one", Worktree: dir}
	if err := db.CreateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	other := a
	other.ID = "att-synthetic-two"
	other.Index = 1
	other.Outcome = model.OutcomePassed
	other.EndedAt = time.Now()
	other.Exit = &exit
	if err := db.CreateAttempt(ctx, &other); err != nil {
		t.Fatal(err)
	}
	source := model.StrongStage{Run: "run-synthetic", Stage: "src-synthetic", Seat: "seat-1", Role: model.StrongBlind}
	target := model.StrongStage{Run: source.Run, Stage: "target-synthetic", Seat: "seat-2", Role: model.StrongPeer}
	if _, err := db.RegisterStrongProducer(ctx, source, other.ID); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("late ordinary registration: %v", err)
	}
	reg, err := db.RegisterStrongProducer(ctx, source, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := model.Attempt{ID: "att-stale-pending", Card: "card-stale", Worktree: dir}
	if err := db.CreateAttempt(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	staleReg, err := db.RegisterStrongProducer(ctx, source, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	live := stale
	live.StartedAt = time.Now()
	live.PID = 999
	if err := db.UpdateAttempt(ctx, &live); err != nil {
		t.Fatal(err)
	}
	if capture, err := strongcustody.Prepare(ctx, db, stale, staleReg, "", "", nil); capture != nil || !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("stale pending input accepted: %v", err)
	}

	wrongScope := source
	wrongScope.Stage = "forged-stage"
	if _, err := db.RegisterStrongProducer(ctx, wrongScope, a.ID); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("relabel producer: %v", err)
	}
	if _, err := db.SaveStrongArtifact(ctx, &strongcustody.Result{}); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("self-made seal: %v", err)
	}
	task := []byte("Synthetic purpose")
	taskSHA := model.StrongArtifactDigest(task)
	if err := db.SaveStrongTask(ctx, source.Run, taskSHA, task); err != nil {
		t.Fatal(err)
	}
	rendered, err := AssembleStrongBundle(ctx, db, source, taskSHA)
	if err != nil {
		t.Fatal(err)
	}
	if forged, err := strongcustody.Prepare(ctx, db, a, reg, taskSHA, model.StrongEmptyManifestSHA(), []byte("not the rendered bundle")); forged != nil || !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatal("claimed forged bundle")
	}

	c, err := strongcustody.Prepare(ctx, db, a, reg, taskSHA, model.StrongEmptyManifestSHA(), rendered)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if other, err := strongcustody.Prepare(ctx, db, a, reg, taskSHA, model.StrongEmptyManifestSHA(), rendered); other != nil || !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("second prepared custody handle: %v", err)
	}

	original := []byte("## 结论\r\nExact synthetic body and original item A.\r\n")
	if err := os.WriteFile(filepath.Join(dir, ".cardex-result.md"), original, 0600); err != nil {
		t.Fatal(err)
	}
	capture, err := c.Seal(ctx, a)
	if err != nil || capture == nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := db.SaveStrongArtifact(ctx, capture); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("unfinished ledger returned an artifact: %v", err)
	}
	a.Outcome = model.OutcomePassed
	a.EndedAt = time.Now()
	a.Exit = &exit
	if err := db.UpdateAttempt(ctx, &a); err != nil {
		t.Fatal(err)
	}
	ref, err := db.SaveStrongArtifact(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Size != len(original) || ref.SHA != model.StrongArtifactDigest(original) || ref.Origin != model.StrongResultFileOrigin || ref.Format != model.StrongResultV1 {
		t.Fatalf("%#v", ref)
	}
	if replay, err := db.SaveStrongArtifact(ctx, capture); err != nil || replay != ref {
		t.Fatalf("replay %v %v", replay, err)
	}
	for _, mutate := range []func(*model.StrongArtifactRef){
		func(r *model.StrongArtifactRef) { r.Run = "forged-run" },
		func(r *model.StrongArtifactRef) { r.Stage = "forged-stage" },
		func(r *model.StrongArtifactRef) { r.Attempt = other.ID },
		func(r *model.StrongArtifactRef) { r.Origin = "excerpt" },
		func(r *model.StrongArtifactRef) { r.Format = "future-format" },
		func(r *model.StrongArtifactRef) { r.Size-- },
		func(r *model.StrongArtifactRef) { r.EnvelopeSHA = model.StrongArtifactDigest([]byte("false envelope")) },
		func(r *model.StrongArtifactRef) { r.SHA = model.StrongArtifactDigest([]byte("forged")) },
	} {
		bad := ref
		mutate(&bad)
		if _, err := db.BindStrongInputs(ctx, target, []model.StrongArtifactRef{bad}); !errors.Is(err, model.ErrStrongArtifact) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}
	if _, err := db.BindStrongInputs(ctx, model.StrongStage{Run: source.Run, Stage: "self", Seat: source.Seat, Role: model.StrongPeer}, []model.StrongArtifactRef{ref}); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("self: %v", err)
	}
	wrong := target
	wrong.Run = "other-run"
	if _, err := db.BindStrongInputs(ctx, wrong, []model.StrongArtifactRef{ref}); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("cross-run: %v", err)
	}
	if _, err := db.BindStrongInputs(ctx, target, []model.StrongArtifactRef{ref, ref}); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := db.BindStrongInputs(ctx, target, []model.StrongArtifactRef{ref}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.StrongInputs(ctx, target)
	if err != nil || len(got) != 1 || got[0].Alias != "Material 01" || !bytes.Equal(got[0].Body, original) {
		t.Fatalf("%+v %v", got, err)
	}
	wrong = target
	wrong.Seat = "seat-3"
	if _, err := db.StrongInputs(ctx, wrong); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("wrong seat: %v", err)
	}
	var manifest string
	if err := db.q(ctx).QueryRowContext(ctx, `SELECT allowlist_json FROM strong_input_bindings WHERE run_id=? AND target_stage=?`, target.Run, target.Stage).Scan(&manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_input_bindings SET allowlist_json=allowlist_json||' ' WHERE run_id=? AND target_stage=?`, target.Run, target.Stage); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StrongInputs(ctx, target); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("tampered manifest: %v", err)
	}
	if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_input_bindings SET allowlist_json=? WHERE run_id=? AND target_stage=?`, manifest, target.Run, target.Stage); err != nil {
		t.Fatal(err)
	}

	// A rewritten metadata sidecar and matching local digest still violate
	// the independently accepted lineage envelope pinned by the binding.
	forgedRefs := `[]`
	if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_artifacts SET references_json=?,references_sha=? WHERE attempt_id=?`, forgedRefs, model.StrongArtifactDigest([]byte(forgedRefs)), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StrongInputs(ctx, target); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("sidecar resealed itself: %v", err)
	}
	if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_artifacts SET references_json='',references_sha=? WHERE attempt_id=?`, model.StrongArtifactDigest(nil), a.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.q(ctx).ExecContext(ctx, `UPDATE strong_artifacts SET body=? WHERE attempt_id=?`, bytes.Repeat([]byte{'x'}, len(original)), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StrongInputs(ctx, target); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("tampered body: %v", err)
	}
}
