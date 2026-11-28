package loop

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
)

func TestReviewModeResolution(t *testing.T) {
	cases := []struct {
		card, obj, proj string
		want            model.ReviewMode
	}{
		{"", "", "", model.ReviewFull},
		{"", "", "check", model.ReviewCheck},
		{"", "off", "check", model.ReviewOff},
		{"review", "off", "check", model.ReviewFull},
		{"auto", "off", "check", model.ReviewFull},
	}
	for _, tc := range cases {
		got := reviewModeFor(model.Card{Review: tc.card}, model.Objective{Review: tc.obj}, model.Project{Review: tc.proj})
		if got != tc.want {
			t.Fatalf("%+v: got %s", tc, got)
		}
	}
}

func TestPlanReviewTier(t *testing.T) {
	dev := model.Card{Kind: model.KindDev, Tier: model.TierT2}
	proj := model.Project{ForbiddenPaths: []string{"deploy/"}}
	cases := []struct {
		name string
		card model.Card
		stat *diffStat
		mode model.ReviewMode
		tier model.Tier
	}{
		{"no diff", dev, nil, model.ReviewFull, model.TierT1},
		{"small", dev, &diffStat{Files: 2, Lines: 30, Paths: []string{"a.go", "b.go"}}, model.ReviewFull, model.TierT3},
		{"medium keeps author", dev, &diffStat{Files: 5, Lines: 200, Paths: []string{"a.go"}}, model.ReviewFull, model.TierT2},
		{"large", dev, &diffStat{Files: 30, Lines: 900, Paths: []string{"a.go"}}, model.ReviewFull, model.TierT1},
		{"risky small", dev, &diffStat{Files: 1, Lines: 5, Paths: []string{"internal/sandbox/profile.go"}}, model.ReviewFull, model.TierT1},
		{"forbidden", dev, &diffStat{Files: 1, Lines: 5, Paths: []string{"deploy/run.sh"}}, model.ReviewFull, model.TierT1},
		{"check", model.Card{Kind: model.KindDev, Review: "check"}, nil, model.ReviewCheck, model.TierT3},
		{"off", model.Card{Kind: model.KindDev, Review: "off"}, nil, model.ReviewOff, ""},
	}
	for _, tc := range cases {
		got := planReview(tc.card, model.Objective{}, proj, tc.stat)
		if got.Mode != tc.mode || got.Tier != tc.tier {
			t.Fatalf("%s: got %+v", tc.name, got)
		}
	}
}

func TestParseNumstat(t *testing.T) {
	stat := parseNumstat("10\t2\ta.go\n-\t-\timg.png\n\n")
	if stat.Files != 2 || stat.Lines != 112 || len(stat.Paths) != 2 {
		t.Fatalf("%+v", stat)
	}
}

func TestReviewOfNewVerdicts(t *testing.T) {
	cases := map[string]string{
		"## 复核结论\nblocked 环境起不来\n":   "blocked",
		"## 复核结论\nescalate 跨模块没把握\n": "escalate",
		"## 复核结论\nmissing 缺报告\n":     "rework",
	}
	for raw, want := range cases {
		got, _, ok := ReviewOf(raw)
		if !ok || got != want {
			t.Fatalf("%q: %s %v", raw, got, ok)
		}
	}
}

func TestPlannedReviewDropsOff(t *testing.T) {
	for in, want := range map[string]string{"check": "check", " Review ": "review", "off": "", "x": ""} {
		if got := plannedReview(in); got != want {
			t.Fatalf("%q: %q", in, got)
		}
	}
}

func disposePassedDev(t *testing.T, f *fx, id string) {
	t.Helper()
	exit := 0
	attempt := model.Attempt{ID: "a-" + id, Card: id, Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC(), Route: model.Route{Channel: "codex"}}
	if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	card, _ := f.db.GetCard(f.ctx, id)
	ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
	if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewOffMergesWithoutReview(t *testing.T) {
	f := newFx(t, model.ModeManual)
	f.obj.Review = "off"
	if err := f.db.UpdateObjective(f.ctx, &f.obj); err != nil {
		t.Fatal(err)
	}
	mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardRunning})
	disposePassedDev(t, f, "dev")
	if got := rvCardStatus(t, f, "dev"); got != model.CardMergeQueued {
		t.Fatalf("status %s, want merge_queued", got)
	}
	if n := len(reviewDeps(t, f, "dev")); n != 0 {
		t.Fatalf("%d review cards", n)
	}
	if !hasCardEvent(t, f, "dev", "review_skipped") {
		t.Fatal("missing review_skipped")
	}
}

func TestReviewCheckOpensT1Check(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardRunning, Review: "check"})
	disposePassedDev(t, f, "dev")
	reviews := reviewDeps(t, f, "dev")
	if len(reviews) != 1 {
		t.Fatalf("%d review cards", len(reviews))
	}
	rv := reviews[0]
	if rv.Tier != model.TierT3 || rv.Review != "check" {
		t.Fatalf("review %+v", rv)
	}
}

func TestReviewAutoWithoutDiffIsT3(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardRunning})
	disposePassedDev(t, f, "dev")
	reviews := reviewDeps(t, f, "dev")
	if len(reviews) != 1 {
		t.Fatalf("%d review cards", len(reviews))
	}
	rv := reviews[0]
	if rv.Tier != model.TierT1 || rv.Review != "review" {
		t.Fatalf("review %+v", rv)
	}
}

func disposeReviewText(t *testing.T, f *fx, id, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.home, ".cardex-result.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	exit := 0
	attempt := model.Attempt{ID: "a-" + id, Card: id, Worktree: f.home, Exit: &exit, EndedAt: time.Now().UTC()}
	if err := f.db.CreateAttempt(f.ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	card, _ := f.db.GetCard(f.ctx, id)
	ctx := WithStore(WithAttempt(WithHome(f.ctx, f.home), attempt.ID), f.db)
	if err := Dispose(ctx, &card, harvest.Result{Outcome: model.OutcomePassed, ResultText: text}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewBlockedAsksWithoutRework(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardReviewing})
	mkCard(t, f, model.Card{ID: "rv", Kind: model.KindReview, Tier: model.TierT2, Status: model.CardRunning, Deps: []string{"dev"}})
	disposeReviewText(t, f, "rv", "## 复核结论\nblocked 测试环境起不来\n")
	dev, _ := f.db.GetCard(f.ctx, "dev")
	if dev.Status != model.CardNeedsInput || dev.ReworkCount != 0 {
		t.Fatalf("dev %s rework %d", dev.Status, dev.ReworkCount)
	}
	if got := rvCardStatus(t, f, "rv"); got != model.CardDone {
		t.Fatalf("review %s", got)
	}
}

func TestReviewEscalateOpensNextTier(t *testing.T) {
	f := newFx(t, model.ModeManual)
	mkCard(t, f, model.Card{ID: "dev", Kind: model.KindDev, Status: model.CardReviewing})
	mkCard(t, f, model.Card{ID: "rv", Kind: model.KindReview, Tier: model.TierT3, Status: model.CardRunning, Deps: []string{"dev"}, Review: "review"})
	disposeReviewText(t, f, "rv", "## 复核结论\nescalate 并发部分没把握\n")
	dev, _ := f.db.GetCard(f.ctx, "dev")
	if dev.Status != model.CardReviewing || dev.ReworkCount != 0 {
		t.Fatalf("dev %s rework %d", dev.Status, dev.ReworkCount)
	}
	var next *model.Card
	for _, c := range reviewDeps(t, f, "dev") {
		if c.ID != "rv" {
			next = &c
		}
	}
	if next == nil || next.Tier != model.TierT2 || next.Status != model.CardReady {
		t.Fatalf("next review %+v", next)
	}
}

func TestAttemptDiffReadsGit(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=c", "-c", "user.email=c@c.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("commit", "-am", "head")
	head := run("rev-parse", "HEAD")
	stat := attemptDiff(context.Background(), model.Project{RepoPath: repo}, model.Attempt{BaseCommit: base, HeadCommit: head})
	if stat == nil || stat.Files != 1 || stat.Lines != 2 {
		t.Fatalf("stat %+v", stat)
	}
	plan := planReview(model.Card{Kind: model.KindDev, Tier: model.TierT2}, model.Objective{}, model.Project{}, stat)
	if plan.Tier != model.TierT3 {
		t.Fatalf("plan %+v", plan)
	}
	if attemptDiff(context.Background(), model.Project{RepoPath: repo}, model.Attempt{BaseCommit: "deadbeef", HeadCommit: head}) != nil {
		t.Fatal("bad base should be unreadable")
	}
}
