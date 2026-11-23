package harvest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestCompareBaselineCategories(t *testing.T) {
	current := parseGoTestOutput(strings.Join([]string{
		"--- FAIL: TestNew",
		"ctx TestNew line 1",
		"ctx TestNew line 2",
		"ctx TestNew line 3",
		"ctx TestNew line 4",
		"ctx TestNew line 5",
		"ctx TestNew line 6",
		"ctx TestNew line 7",
		"ctx TestNew line 8",
		"ctx TestNew line 9",
		"ctx TestNew line 10",
		"ctx TestNew line 11",
		"ctx TestNew line 12",
		"--- FAIL: TestStay",
		"ctx TestStay line 1",
		"FAIL",
	}, "\n"))
	base := parsedTests{ok: true, names: []string{"TestGone", "TestStay"}, context: map[string]string{}}
	got := compareParsed(current, 1, base, 1, true)
	if got.Status != "compared" || got.Exit == nil || *got.Exit != 1 {
		t.Fatalf("baseline %+v", got)
	}
	if len(got.New) != 1 || got.New[0].Name != "TestNew" {
		t.Fatalf("new %+v", got.New)
	}
	lines := strings.Split(got.New[0].Context, "\n")
	if len(lines) != 10 || !strings.Contains(got.New[0].Context, "ctx TestNew line 9") || strings.Contains(got.New[0].Context, "line 10") || strings.Contains(got.New[0].Context, "TestStay") {
		t.Fatalf("context %q", got.New[0].Context)
	}
	if len(got.Gone) != 1 || got.Gone[0] != "TestGone" {
		t.Fatalf("gone %+v", got.Gone)
	}
	if len(got.Still) != 1 || got.Still[0] != "TestStay" {
		t.Fatalf("still %+v", got.Still)
	}

	unparsed := compareParsed(parseGoTestOutput("boom no markers\n"), 2, base, 7, true)
	if unparsed.Status != "unparsed" || unparsed.Exit == nil || *unparsed.Exit != 7 || unparsed.New != nil || unparsed.Gone != nil || unparsed.Still != nil {
		t.Fatalf("unparsed %+v", unparsed)
	}
	missed := compareParsed(current, 1, parsedTests{}, 0, false)
	if missed.Status != "unparsed" || missed.Exit != nil || missed.New != nil {
		t.Fatalf("not ran %+v", missed)
	}
}

func TestGoTestCommand(t *testing.T) {
	if !isGoTest("go test .") || !isGoTest("  go   test ./...") {
		t.Fatal("go test")
	}
	if isGoTest("echo go test") || isGoTest("go fmt") || isGoTest("gofmt") {
		t.Fatal("not go test")
	}
}

func TestRouteMatch(t *testing.T) {
	in := Input{ExpectedChannel: "cha", ExpectedModel: "model-a", ExpectedTier: "T3"}
	in.Attempt.RouteReason.Candidates = []model.RouteCandidate{{
		Channel: "cha", Model: "model-a", Tier: model.TierT3,
	}}
	if !routeMatches(in, "cha", "model-a") {
		t.Fatal("match")
	}
	if routeMatches(in, "chb", "model-a") || routeMatches(in, "cha", "other") {
		t.Fatal("channel or model mismatch")
	}
	in.Attempt.RouteReason.Candidates[0].Tier = model.TierT1
	if routeMatches(in, "cha", "model-a") {
		t.Fatal("tier below hold matched")
	}
	in.Attempt.RouteReason.Candidates = nil
	if !routeMatches(in, "cha", "model-a") {
		t.Fatal("missing tier info should stay matched")
	}
}

func TestParseReviewSummary(t *testing.T) {
	raw := reviewSample()
	got := parseReviewSummary(raw)
	if got.Verdict != "rework" {
		t.Fatalf("verdict %s", got.Verdict)
	}
	if len(got.P0) != 1 || got.P0[0].File != "internal/loop/dev.go" || got.P0[0].Line != 42 || got.P0[0].Text != "空指针会在启动时崩" {
		t.Fatalf("p0 %+v", got.P0)
	}
	if len(got.P1) != 1 || got.P1[0].File != "internal/sched/launch_route.go" || got.P1[0].Line != 12 || got.P1[0].Text != "档位被静默降低" {
		t.Fatalf("p1 %+v", got.P1)
	}
	if len(got.P2) != 1 || got.P2[0].File != "cmd/cardex/data.go" || got.P2[0].Line != 7 || got.P2[0].Text != "摘要缺耗时" {
		t.Fatalf("p2 %+v", got.P2)
	}
	if strings.Contains(got.P0[0].Text+got.P1[0].Text+got.P2[0].Text, "注释") {
		t.Fatal("p3 leaked")
	}
	if len(got.Tests) != 2 || got.Tests[0] != "TestBaselineCategories" || got.Tests[1] != "TestReviewHold" {
		t.Fatalf("tests %+v", got.Tests)
	}
	if !strings.Contains(raw, "P3 注释可以再短") || !strings.Contains(raw, "空指针会在启动时崩") {
		t.Fatal("original text changed")
	}
}

func TestHarvestWritesSummaryAndBaseline(t *testing.T) {
	useFakeGo(t)
	dir, base, head := twoCommitRepo(t)
	home := t.TempDir()
	res, err := Harvest(context.Background(), Input{
		Attempt: model.Attempt{
			ID: "att", Card: "c", Worktree: dir, BaseCommit: base,
			Route: model.Route{Channel: "cha", Model: "model-a"},
		},
		Card:            model.Card{ID: "c", Kind: model.KindDev, Acceptance: []string{"go test ."}},
		Exit:            1,
		Home:            home,
		Repo:            dir,
		Runner:          "fake",
		ExpectedChannel: "cha",
		ExpectedModel:   "model-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != model.OutcomeTestsFailed {
		t.Fatalf("outcome %s", res.Outcome)
	}
	sum := readSummary(t, home, "att")
	if sum.Runner != "fake" || sum.Channel != "cha" || sum.Model != "model-a" || !sum.Match {
		t.Fatalf("route %+v", sum)
	}
	if sum.BaseCommit != base || sum.HeadCommit != head {
		t.Fatalf("sha base %s head %s want %s %s", sum.BaseCommit, sum.HeadCommit, base, head)
	}
	if len(sum.Files) != 1 || sum.Files[0].Path != "fails.txt" || sum.Files[0].Added != 1 || sum.Files[0].Deleted != 1 {
		t.Fatalf("files %+v", sum.Files)
	}
	if len(sum.Acceptance) != 1 || sum.Acceptance[0].Command != "go test ." || sum.Acceptance[0].Exit != 1 || sum.Acceptance[0].DurationMS <= 0 {
		t.Fatalf("acceptance %+v", sum.Acceptance)
	}
	bl := sum.Acceptance[0].Baseline
	if bl == nil || bl.Status != "compared" || bl.Exit == nil || *bl.Exit != 1 {
		t.Fatalf("baseline %+v", bl)
	}
	if len(bl.New) != 1 || bl.New[0].Name != "TestNew" || strings.Count(bl.New[0].Context, "\n") != 9 || strings.Contains(bl.New[0].Context, "line 10") {
		t.Fatalf("new %+v", bl.New)
	}
	if len(bl.Gone) != 1 || bl.Gone[0] != "TestGone" || len(bl.Still) != 1 || bl.Still[0] != "TestStay" {
		t.Fatalf("gone %+v still %+v", bl.Gone, bl.Still)
	}
	log, err := os.ReadFile(filepath.Join(home, "attempts", "att", "acceptance.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), `command="go test ." exit=1 duration=`) {
		t.Fatalf("log %s", log)
	}

	cache, ok := baselineCachePath(home, base, "go test .")
	if !ok {
		t.Fatal("cache path")
	}
	body, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	var cached baselineCache
	if err := json.Unmarshal(body, &cached); err != nil {
		t.Fatal(err)
	}
	cached.Names = []string{"TestStay"}
	rewritten, err := json.Marshal(cached)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, append(rewritten, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Harvest(context.Background(), Input{
		Attempt:         model.Attempt{ID: "att2", Card: "c", Worktree: dir, BaseCommit: base},
		Card:            model.Card{ID: "c", Kind: model.KindDev, Acceptance: []string{"go test ."}},
		Home:            home,
		Repo:            dir,
		ExpectedChannel: "",
	}); err != nil {
		t.Fatal(err)
	}
	again := readSummary(t, home, "att2")
	bl = again.Acceptance[0].Baseline
	if bl == nil || bl.Status != "compared" || len(bl.New) != 1 || bl.New[0].Name != "TestNew" || len(bl.Gone) != 0 || len(bl.Still) != 1 {
		t.Fatalf("cached baseline %+v", bl)
	}
}

func TestHarvestUnparsedGoTest(t *testing.T) {
	useFakeGo(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	write(t, dir, "fails.txt", "UNPARSED\n")
	runGit(t, dir, "add", "fails.txt")
	runGit(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	base := strings.TrimSpace(runGitOut(t, dir, "rev-parse", "HEAD"))
	home := t.TempDir()
	if _, err := Harvest(context.Background(), Input{
		Attempt: model.Attempt{ID: "att", Card: "c", Worktree: dir, BaseCommit: base},
		Card:    model.Card{ID: "c", Kind: model.KindDev, Acceptance: []string{"go test ."}},
		Home:    home,
		Repo:    dir,
	}); err != nil {
		t.Fatal(err)
	}
	sum := readSummary(t, home, "att")
	bl := sum.Acceptance[0].Baseline
	if bl == nil || bl.Status != "unparsed" || bl.Exit == nil || *bl.Exit != 2 || bl.New != nil || bl.Gone != nil || bl.Still != nil {
		t.Fatalf("baseline %+v acceptance %+v", bl, sum.Acceptance[0])
	}
	if sum.Acceptance[0].Exit != 2 {
		t.Fatalf("exit %d", sum.Acceptance[0].Exit)
	}
}

func TestHarvestReviewSummaryKeepsOriginal(t *testing.T) {
	dir, head := newRepo(t)
	raw := reviewSample()
	write(t, dir, ".cardex-result.md", raw)
	home := t.TempDir()
	if _, err := Harvest(context.Background(), Input{
		Attempt: model.Attempt{
			ID: "rev-att", Card: "rev", Worktree: dir, BaseCommit: head,
			Route: model.Route{Channel: "cha", Model: "model-a"},
			RouteReason: model.RouteReason{Candidates: []model.RouteCandidate{{
				Channel: "cha", Model: "model-a", Tier: model.TierT2,
			}}},
		},
		Card:            model.Card{ID: "rev", Kind: model.KindReview, Tier: model.TierT3},
		Home:            home,
		Repo:            dir,
		Runner:          "fake",
		ExpectedChannel: "cha",
		ExpectedModel:   "model-a",
		ExpectedTier:    "T3",
	}); err != nil {
		t.Fatal(err)
	}
	kept, err := os.ReadFile(filepath.Join(dir, ".cardex-result.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != raw {
		t.Fatalf("worktree result changed\n%s", kept)
	}
	copied, err := os.ReadFile(filepath.Join(home, "attempts", "rev-att", "result.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != raw {
		t.Fatalf("copied result changed\n%s", copied)
	}
	sum := readSummary(t, home, "rev-att")
	if sum.Match {
		t.Fatal("T2 candidate matched a T3 review")
	}
	if sum.Review == nil || sum.Review.Verdict != "rework" || sum.Review.Source != "attempts/rev-att/result.md" {
		t.Fatalf("review %+v", sum.Review)
	}
	if len(sum.Review.P0) != 1 || len(sum.Review.P1) != 1 || len(sum.Review.P2) != 1 || len(sum.Review.Tests) != 2 {
		t.Fatalf("review %+v", sum.Review)
	}
}

func reviewSample() string {
	return strings.Join([]string{
		"## 复核结论",
		"rework",
		"P0 空指针会在启动时崩 internal/loop/dev.go:42",
		"P1 档位被静默降低 internal/sched/launch_route.go:12",
		"P2 摘要缺耗时 cmd/cardex/data.go:7",
		"P3 注释可以再短 docs/review-policy.md:3",
		"补测试：TestReviewHold",
		"",
		"## 测试",
		"TestBaselineCategories",
		"",
	}, "\n")
}

func readSummary(t *testing.T, home, id string) Summary {
	t.Helper()
	sum, err := LoadSummary(SummaryPath(home, id))
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func twoCommitRepo(t *testing.T) (dir, base, head string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	write(t, dir, "fails.txt", "TestGone TestStay\n")
	runGit(t, dir, "add", "fails.txt")
	runGit(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	base = strings.TrimSpace(runGitOut(t, dir, "rev-parse", "HEAD"))
	write(t, dir, "fails.txt", "TestNew TestStay\n")
	runGit(t, dir, "add", "fails.txt")
	runGit(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "head")
	head = strings.TrimSpace(runGitOut(t, dir, "rev-parse", "HEAD"))
	return dir, base, head
}

func useFakeGo(t *testing.T) {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module fake\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(fakeGoSource), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	exe := "go"
	if runtime.GOOS == "windows" {
		exe = "go.exe"
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(bin, exe), ".")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fake go: %v\n%s", err, out)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const fakeGoSource = `package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "test" {
		fmt.Fprintln(os.Stderr, "not test")
		os.Exit(2)
	}
	time.Sleep(30 * time.Millisecond)
	body, err := os.ReadFile("fails.txt")
	if err != nil {
		fmt.Println("boom")
		os.Exit(1)
	}
	text := strings.TrimSpace(string(body))
	if text == "UNPARSED" {
		fmt.Println("boom no markers")
		os.Exit(2)
	}
	if text == "" {
		fmt.Println("PASS")
		return
	}
	for _, name := range strings.Fields(text) {
		fmt.Printf("--- FAIL: %s\n", name)
		for i := 1; i <= 12; i++ {
			fmt.Printf("ctx %s line %d\n", name, i)
		}
	}
	fmt.Println("FAIL")
	os.Exit(1)
}
`
