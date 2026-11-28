package prompt

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
)

func TestStrongBundleBlindExactAndPeerIsolated(t *testing.T) {
	if !strongcustody.Supported() {
		t.Skip("strong custody unsupported")
	}
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "strong.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	makeStage := func(name, seat string, role model.StrongRole) model.StrongStage {
		return model.StrongStage{Run: "run-INTERNAL-marker", Stage: name, Seat: seat, Role: role}
	}
	blind1 := makeStage("blind-INTERNAL-one", "seat-INTERNAL-one", model.StrongBlind)
	blind2 := makeStage("blind-INTERNAL-two", "seat-INTERNAL-two", model.StrongBlind)
	p1 := makeStage("review-INTERNAL-one", blind1.Seat, model.StrongPeer)
	p2 := makeStage("review-INTERNAL-two", blind2.Seat, model.StrongPeer)
	task := []byte("Explain A and B independently.\r\n")
	taskSHA := model.StrongArtifactDigest(task)
	if err := db.SaveStrongTask(ctx, blind1.Run, taskSHA, task); err != nil {
		t.Fatal(err)
	}
	tamper := []byte("Different task for second blind seat.")
	if err := db.SaveStrongTask(ctx, blind1.Run, model.StrongArtifactDigest(tamper), tamper); err == nil {
		t.Fatal("task changed for same run")
	}
	b1, err := AssembleStrongBundle(ctx, db, blind1, taskSHA)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := AssembleStrongBundle(ctx, db, blind2, taskSHA)
	if err != nil || !bytes.Equal(b1, b2) || bytes.Contains(b1, []byte("INTERNAL")) {
		t.Fatalf("%s %s %v", b1, b2, err)
	}
	pins := map[model.StrongStage]string{}
	export := func(source model.StrongStage, name, body string, references ...string) model.StrongArtifactRef {
		exit := 0
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		a := model.Attempt{ID: "att-INTERNAL-" + name, Card: "card-INTERNAL-" + name, Worktree: dir}
		if err := db.CreateAttempt(ctx, &a); err != nil {
			t.Fatal(err)
		}
		reg, err := db.RegisterStrongProducer(ctx, source, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := AssembleStrongBundle(ctx, db, source, taskSHA)
		if err != nil {
			t.Fatal(err)
		}
		pin := model.StrongEmptyManifestSHA()
		if source.Role != model.StrongBlind {
			pin = pins[source]
		}
		c, err := strongcustody.Prepare(ctx, db, a, reg, taskSHA, pin, rendered)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if err := os.WriteFile(filepath.Join(dir, ".cardex-result.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if len(references) > 0 {
			data, _ := json.Marshal(references)
			if err := os.WriteFile(filepath.Join(dir, ".cardex-strong-references.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		seal, err := c.Seal(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		a.Outcome = model.OutcomePassed
		a.Exit = &exit
		a.EndedAt = time.Now()
		if err := db.UpdateAttempt(ctx, &a); err != nil {
			t.Fatal(err)
		}
		r, err := db.SaveStrongArtifact(ctx, seal)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	one := "## 结论\r\nOriginal source numbered A.\r\n"
	two := "## 结论\nAnother source numbered B.\n"
	ref1, ref2 := export(blind1, "one", one), export(blind2, "two", two)
	for _, parm := range []struct {
		stage model.StrongStage
		ref   model.StrongArtifactRef
		want  string
	}{
		{p1, ref2, two}, {p2, ref1, one},
	} {
		pin, err := db.BindStrongInputs(ctx, parm.stage, []model.StrongArtifactRef{parm.ref})
		if err != nil {
			t.Fatal(err)
		}
		pins[parm.stage] = pin
		body, err := AssembleStrongBundle(ctx, db, parm.stage, taskSHA)
		var b StrongBundle
		if err != nil || json.Unmarshal(body, &b) != nil || bytes.Contains(body, []byte("INTERNAL")) ||
			len(b.Materials) != 1 || b.Materials[0].Body != parm.want || b.Materials[0].Reference != "Material 01" {
			t.Fatalf("%s: %v", body, err)
		}
	}
	reviewBody := "## 结论\nLeave the original item A. and literal label Material 01 as written.\n"
	review := export(p1, "review-one", reviewBody, "Material 01")
	synth := makeStage("synth-INTERNAL-one", "", model.StrongSynthesis)
	if _, err := db.BindStrongInputs(ctx, synth, []model.StrongArtifactRef{review, ref1, ref2}); err != nil {
		t.Fatal(err)
	}
	b, err := AssembleStrongBundle(ctx, db, synth, taskSHA)
	var bundle StrongBundle
	if err != nil || json.Unmarshal(b, &bundle) != nil || len(bundle.Materials) != 3 || bundle.Materials[0].Body != reviewBody ||
		bundle.Materials[0].Reference != "Source 01" || len(bundle.Materials[0].References) != 1 || bundle.Materials[0].References[0] != "Source 03" ||
		bytes.Contains(b, []byte("INTERNAL")) {
		t.Fatalf("bad anonymous citation projection: %s %v", b, err)
	}
	incomplete := makeStage("synth-INTERNAL-two", "", model.StrongSynthesis)
	if _, err := db.BindStrongInputs(ctx, incomplete, []model.StrongArtifactRef{review, ref1}); err != nil {
		t.Fatal(err)
	}
	if b, err := AssembleStrongBundle(ctx, db, incomplete, taskSHA); err == nil || b != nil {
		t.Fatal("synthesis read outside its allowlist")
	}

	leak := export(makeStage("blind-INTERNAL-leak", "seat-INTERNAL-leak", model.StrongBlind), "leak", "## 结论\nLeaked raw identifier: att-INTERNAL-two.")
	leakTarget := makeStage("review-INTERNAL-leak", "seat-else", model.StrongPeer)
	if _, err := db.BindStrongInputs(ctx, leakTarget, []model.StrongArtifactRef{leak}); err != nil {
		t.Fatal(err)
	}
	if b, err := AssembleStrongBundle(ctx, db, leakTarget, taskSHA); err == nil || b != nil {
		t.Fatal("private ID reached bundle")
	}

}
