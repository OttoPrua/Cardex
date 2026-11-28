//go:build linux || darwin

package strongcustody

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"golang.org/x/sys/unix"
)

type custodyVerifier struct {
	want model.StrongProducerRegistration
}

func (v custodyVerifier) CheckStrongProducer(ctx context.Context, got model.StrongProducerRegistration, _ *model.StrongCaptureClaim) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if got != v.want {
		return model.ErrStrongArtifact
	}
	return nil
}

func (v custodyVerifier) ClaimStrongProducer(ctx context.Context, got model.StrongProducerRegistration, _ string, manifest string, rendered []byte) (model.StrongCaptureClaim, error) {
	h := strings.Repeat("f", 64)
	return model.StrongCaptureClaim{Token: h, ManifestSHA: manifest, BundleSHA: model.StrongArtifactDigest(rendered), TaskSHA: h}, v.CheckStrongProducer(ctx, got, nil)
}

func strongWorkspaceFixture(t *testing.T) (model.Attempt, model.StrongProducerRegistration) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "parent", "work")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	a := model.Attempt{ID: "att-synthetic", Card: "card-synthetic", Worktree: dir}
	p := model.StrongProducerRegistration{Attempt: a.ID, Card: a.Card, Worktree: dir, Token: strings.Repeat("a", 64), Source: model.StrongStage{Run: "run-synthetic", Stage: "stage-synthetic", Seat: "seat-synthetic", Role: model.StrongBlind}}
	return a, p
}

func TestStrongCustodyRejectsSwappedAncestorsAndUnsafeOutputs(t *testing.T) {
	for _, test := range []string{"hardlink", "sidecar hardlink", "FIFO", "symlink", "workspace swap", "ancestor symlink", "ancestor rename with replacement"} {
		t.Run(test, func(t *testing.T) {
			a, p := strongWorkspaceFixture(t)
			ctx := context.Background()
			handle, err := Prepare(ctx, custodyVerifier{p}, a, p, "synthetic", model.StrongEmptyManifestSHA(), []byte("not production"))
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Close()
			file := filepath.Join(a.Worktree, strongResultName)
			raw := []byte("## 结论\nvalid synthetic bytes\n")
			put := func(path string, body []byte) {
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			checked := func(err error) {
				if err != nil {
					t.Fatal(err)
				}
			}
			switch test {
			case "hardlink", "sidecar hardlink":
				body := raw
				link := file
				if test == "sidecar hardlink" {
					put(file, raw)
					body = []byte(`["Material 01"]`)
					link = filepath.Join(a.Worktree, strongReferencesName)
				}
				other := filepath.Join(filepath.Dir(a.Worktree), "outside")
				put(other, body)
				checked(os.Link(other, link))
			case "FIFO":
				checked(unix.Mkfifo(file, 0600))
			case "symlink":
				other := filepath.Join(filepath.Dir(a.Worktree), "outside")
				put(other, raw)
				checked(os.Symlink(other, file))
			case "workspace swap":
				checked(os.Rename(a.Worktree, a.Worktree+"-stale"))
				checked(os.Mkdir(a.Worktree, 0700))
				put(file, raw)
			case "ancestor symlink", "ancestor rename with replacement":
				parent := filepath.Dir(a.Worktree)
				checked(os.Rename(parent, parent+"-stale"))
				if test == "ancestor symlink" {
					checked(os.Symlink(parent+"-stale", parent))
					if _, err := Prepare(ctx, custodyVerifier{p}, a, p, "synthetic", model.StrongEmptyManifestSHA(), []byte("not production")); !errors.Is(err, model.ErrStrongArtifact) {
						t.Fatal("prepare followed an ancestor symlink")
					}
				} else {
					checked(os.MkdirAll(a.Worktree, 0700))
				}
				put(file, raw)
			}
			done := make(chan error, 1)
			go func() {
				if got, err := handle.Seal(ctx, a); got != nil {
					done <- nil
				} else {
					done <- err
				}
			}()
			select {
			case err := <-done:
				if !errors.Is(err, model.ErrStrongArtifact) {
					t.Fatalf("unsafe output accepted: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("output read blocked")
			}
		})
	}
}

func TestStrongCustodyRejectsPreexistingAndReplay(t *testing.T) {
	a, p := strongWorkspaceFixture(t)
	ctx := context.Background()
	file := filepath.Join(a.Worktree, strongResultName)
	body := []byte("## 结论\nvalid synthetic bytes\n")
	if err := os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(ctx, custodyVerifier{p}, a, p, "synthetic", model.StrongEmptyManifestSHA(), []byte("not production")); !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatal("accepted existing file")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	h, err := Prepare(ctx, custodyVerifier{p}, a, p, "synthetic", model.StrongEmptyManifestSHA(), []byte("not production"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	sealed, err := h.Seal(ctx, a)
	if err != nil || sealed == nil {
		t.Fatalf("%v", err)
	}
	if again, err := h.Seal(ctx, a); again != nil || !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatal("custody handle reused")
	}
}
