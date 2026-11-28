package harvest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
)

func TestStrongHarvestOriginalNotEvidenceExcerpt(t *testing.T) {
	if !strongcustody.Supported() {
		t.Skip("strong custody unsupported")
	}
	ctx := context.Background()
	body := "\uFEFF## 结论\r\n" + strings.Repeat("x", evidenceLimit) + "\r\nOriginal item A.\r\n"
	t.Run("sealed before acceptance replaces both output files", func(t *testing.T) {
		db, in := strongHarvestFixture(t)
		dir := in.Attempt.Worktree
		write(t, dir, ".cardex-result.md", body)
		refRaw := " [ \"Material 01\" ]\n"
		write(t, dir, ".cardex-strong-references.json", refRaw)
		in.Card.Acceptance = []string{`printf '## 结论\nreplacement\n' > .cardex-result.md; printf '["Material 99"]' > .cardex-strong-references.json`}
		res, err := Harvest(ctx, in)
		seal := res.StrongResult.Record()
		if err != nil || res.StrongResult == nil || res.Outcome != model.OutcomePassed || !bytes.Equal(seal.Body, []byte(body)) ||
			seal.Producer.Attempt != in.Attempt.ID || seal.Producer.Card != in.Card.ID || len(seal.References) != 1 || seal.References[0] != "Material 01" || string(seal.ReferenceBytes) != refRaw {
			t.Fatalf("outcome %s: %v", res.Outcome, err)
		}
		// A decoded copy is not an authorization or a way to change the seal.
		seal.Body[0] = 'X'
		seal.References[0] = "Material 99"
		if got := res.StrongResult.Record(); got.Body[0] == 'X' || got.References[0] != "Material 01" {
			t.Fatal("mutable seal")
		}
		blockedReg := seal.Producer
		blockedReg.Token = strings.Repeat("f", 64)
		if _, err := strongcustody.Prepare(ctx, db, in.Attempt, blockedReg, "", "", nil); !errors.Is(err, model.ErrStrongArtifact) {
			t.Fatal("forged custody accepted")
		}
	})
	for label, testBody := range map[string]string{"truncated": string(truncateEvidence([]byte(body))),
		"overflow":     "## 结论\n" + strings.Repeat("x", model.StrongResultMax),
		"wrong format": "## unrecognized\nnot a canonical result",
	} {
		t.Run(label, func(t *testing.T) {
			_, in := strongHarvestFixture(t)
			write(t, in.Attempt.Worktree, ".cardex-result.md", testBody)
			in.Card.Acceptance = []string{`printf 'ran' > unexpected-acceptance`}
			if _, err := Harvest(ctx, in); !errors.Is(err, model.ErrStrongArtifact) {
				t.Fatalf("expected rejection: %v", err)
			}
			if _, err := os.Stat(filepath.Join(in.Attempt.Worktree, "unexpected-acceptance")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ran acceptance before validating custody: %v", err)
			}
		})
	}
	t.Run("ordinary remains unchanged", func(t *testing.T) {
		dir, head := newRepo(t)
		write(t, dir, ".cardex-result.md", "## 结论\nordinary result")
		in := harvestInput(dir, head, model.Card{ID: "c-ordinary", Kind: model.KindDesign}, 0, nil, "")
		if res, err := Harvest(ctx, in); err != nil || res.StrongResult != nil || res.Outcome != model.OutcomePassed {
			t.Fatalf("%s %v", res.Outcome, err)
		}
	})
}

func strongHarvestFixture(t *testing.T) (*store.Store, Input) {
	t.Helper()
	ctx := context.Background()
	dir, head := newRepo(t)
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	in := harvestInput(dir, head, model.Card{ID: "synthetic-card", Kind: model.KindDesign}, 0, nil, "")
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateAttempt(ctx, &in.Attempt); err != nil {
		t.Fatal(err)
	}
	reg, err := db.RegisterStrongProducer(ctx, model.StrongStage{Run: "run-synthetic", Stage: "src-synthetic", Seat: "seat-synthetic", Role: model.StrongBlind}, in.Attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	task := []byte("Synthetic purpose")
	sha := model.StrongArtifactDigest(task)
	if err := db.SaveStrongTask(ctx, reg.Source.Run, sha, task); err != nil {
		t.Fatal(err)
	}
	rendered, err := store.AssembleStrongBundle(ctx, db, reg.Source, sha)
	if err != nil {
		t.Fatal(err)
	}
	in.CaptureStrongResult, err = strongcustody.Prepare(ctx, db, in.Attempt, reg, sha, model.StrongEmptyManifestSHA(), rendered)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.CaptureStrongResult.Close() })
	return db, in
}

func TestStrongSnapshotExactBareCommit(t *testing.T) {
	ctx := context.Background()
	repo, head := newRepo(t)
	original, err := os.ReadFile(filepath.Join(repo, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(t.TempDir(), "snapshot.git")
	runGit(t, repo, "clone", "--no-hardlinks", "--bare", repo, bare)
	write(t, repo, "a.go", "not committed approved evidence")
	got, err := ReadStrongSnapshot(ctx, bare, head, []string{"a.go"})
	if err != nil || len(got) != 1 || !bytes.Equal(got[0].Body, original) || got[0].SHA != model.StrongArtifactDigest(original) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, tc := range []struct {
		dir, commit string
		paths       []string
	}{
		{repo, head, []string{"a.go"}},
		{bare, "HEAD", []string{"a.go"}},
		{bare, head[:12], []string{"a.go"}},
		{bare, head, []string{"a.go", "missing"}},
		{bare, head, []string{"a.go", "a.go"}},
		{bare, head, []string{"../a.go"}},
		{bare, head, []string{":(glob)*"}},
	} {
		if got, err := ReadStrongSnapshot(ctx, tc.dir, tc.commit, tc.paths); err == nil || got != nil {
			t.Fatalf("allowed %+v: %+v %v", tc, got, err)
		}
	}
}

func TestStrongSnapshotBoundedOwnedGitChild(t *testing.T) {
	repo, _ := newRepo(t)
	large := strings.Repeat("bounded synthetic git output\n", 32<<10)
	write(t, repo, "large.txt", large)
	runGit(t, repo, "add", "large.txt")
	runGit(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "synthetic bounded fixture")
	blob := strings.TrimSpace(runGitOut(t, repo, "rev-parse", "HEAD:large.txt"))
	small := strings.TrimSpace(runGitOut(t, repo, "rev-parse", "HEAD:a.go"))
	bare := filepath.Join(t.TempDir(), "snapshot.git")
	runGit(t, repo, "clone", "--no-hardlinks", "--bare", repo, bare)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if got, err := strongGit(ctx, bare, 32, "cat-file", "blob", blob); got != nil || err == nil || !strings.Contains(err.Error(), "output limit") || ctx.Err() != nil {
		t.Fatalf("oversized Git child did not stop and reap before deadline: length=%d error=%v context=%v", len(got), err, ctx.Err())
	}
	expected, err := os.ReadFile(filepath.Join(repo, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := strongGit(ctx, bare, len(expected), "cat-file", "blob", small); err != nil || !bytes.Equal(got, expected) {
		t.Fatalf("exact positive after bounded overflow: length=%d error=%v", len(got), err)
	}
}
