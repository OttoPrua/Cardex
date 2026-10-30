package harvest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cardex/internal/model"
)

func TestHarvestTolerantResult(t *testing.T) {
	samples := []string{
		"##  结 论\ndone\n",
		"##\tBACKLOG\nitem\n",
		"## 新卡\n```json\n[{\"key\": \"a\",}]\n```\n",
		"## 新卡\n{\n  \"key\": \"a\", /* note */\n  \"kind\": \"dev\", // tail\n}\n",
		"## 复核 结论\nPASS\n",
		"##开放问题\n一个问题\n",
	}
	for _, sample := range samples {
		dir, head := newRepo(t)
		if err := os.WriteFile(filepath.Join(dir, ".cardex-result.md"), []byte(sample), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDesign,
		}, 0, nil, ""))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != model.OutcomePassed {
			t.Fatalf("sample %q outcome %s evidence %s", sample, res.Outcome, res.Evidence)
		}
	}
}

func TestHarvestMalformedResults(t *testing.T) {
	samples := []string{
		"",
		"hello world\n",
		"## 新卡\n{\n",
		"## 新卡\n{\"a\":}\n",
		"## 新卡\n{\"a\": 1, \"b\": [}\n",
		"## 新卡\nnot-json\n",
		"## 复核结论\nmaybe\n",
		"## 复核结论\n",
		"## 未知标题\ntext\n",
		"## 新卡\n[\n",
		"## 新卡\n{\"a\": \"unterminated}\n",
		"\x00\x01\x02\n",
		"## 新卡\n```json\n{\"a\":}\n```\n",
		"## 新卡\ntrue\n",
		"## 新卡\n{\"a\":1}{\"b\":2}\n",
		"### 只是三级\nno known section\n",
	}
	if len(samples) <= 10 {
		t.Fatalf("need more than ten malformed samples, got %d", len(samples))
	}
	for _, sample := range samples {
		dir, head := newRepo(t)
		if err := os.WriteFile(filepath.Join(dir, ".cardex-result.md"), []byte(sample), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDesign,
		}, 0, nil, ""))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != model.OutcomeResultUnparsable {
			t.Fatalf("sample %q outcome %s evidence %s", sample, res.Outcome, res.Evidence)
		}
		if res.ResultText != sample {
			t.Fatalf("result text %q want %q", res.ResultText, sample)
		}
	}
}

func TestHarvestOutcomes(t *testing.T) {
	t.Run("passed code", func(t *testing.T) {
		dir, head := newRepo(t)
		write(t, dir, "b.go", "package a\n\nfunc B() {}\n")
		res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDev, Acceptance: []string{"true"},
		}, 1, []byte("{\"type\":\"assistant\"}\n{\"type\":\"partial\""), ""))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != model.OutcomePassed {
			t.Fatalf("outcome %s evidence %s", res.Outcome, res.Evidence)
		}
	})

	t.Run("passed result file", func(t *testing.T) {
		dir, head := newRepo(t)
		write(t, dir, ".cardex-result.md", "## 结论\n设计完成\n")
		res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDesign,
		}, 0, nil, ""))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != model.OutcomePassed {
			t.Fatalf("outcome %s evidence %s", res.Outcome, res.Evidence)
		}
	})

	t.Run("tests_failed", func(t *testing.T) {
		dir, head := newRepo(t)
		write(t, dir, "b.go", "package a\n\nfunc B() {}\n")
		res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDev, Acceptance: []string{"exit 1"},
		}, 0, nil, ""))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != model.OutcomeTestsFailed {
			t.Fatalf("outcome %s evidence %s", res.Outcome, res.Evidence)
		}
	})

	t.Run("no_output", func(t *testing.T) {
		dir, head := newRepo(t)
		res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDev,
		}, 0, nil, ""))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != model.OutcomeNoOutput {
			t.Fatalf("outcome %s evidence %s", res.Outcome, res.Evidence)
		}
	})

	t.Run("acceptance failure yields runner class", func(t *testing.T) {
		dir, head := newRepo(t)
		write(t, dir, "b.go", "package a\n\nfunc B() {}\n")
		res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDev, Acceptance: []string{"exit 1"},
		}, 1, nil, model.ErrorTransport))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != model.OutcomeTransport {
			t.Fatalf("outcome %s evidence %s", res.Outcome, res.Evidence)
		}
	})

	classes := []model.ErrorClass{
		model.ErrorQuotaHit,
		model.ErrorAuthFailed,
		model.ErrorTransport,
		model.ErrorTimeout,
		model.ErrorPermissionDenied,
		model.ErrorRefusal,
		model.ErrorBudgetLimited,
		model.ErrorViolation,
	}
	for _, class := range classes {
		t.Run(string(class), func(t *testing.T) {
			dir, head := newRepo(t)
			res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
				ID: "c", Kind: model.KindDev,
			}, 1, nil, class))
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != model.Outcome(class) {
				t.Fatalf("outcome %s want %s evidence %s", res.Outcome, class, res.Evidence)
			}
		})
	}

	t.Run("violation overrides workspace pass", func(t *testing.T) {
		dir, head := newRepo(t)
		write(t, dir, "b.go", "package a\n\nfunc B() {}\n")
		res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
			ID: "c", Kind: model.KindDev, Acceptance: []string{"true"},
		}, 0, nil, model.ErrorViolation))
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != model.OutcomeViolation {
			t.Fatalf("outcome %s evidence %s", res.Outcome, res.Evidence)
		}
	})
}

func TestHarvestClaimUpdateIsNotFailure(t *testing.T) {
	dir, head := newRepo(t)
	write(t, dir, "a.go", "package a\n\nfunc Foo() {}\n\nfunc Bar() { println(1) }\n")
	write(t, dir, "note.txt", "extra\n")
	res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
		ID: "c", Kind: model.KindDev, Acceptance: []string{"true"},
		Claims: []model.Claim{{Path: "a.go", Symbol: "Foo"}},
	}, 0, []byte("not-json"), ""))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != model.OutcomePassed {
		t.Fatalf("outcome %s evidence %s", res.Outcome, res.Evidence)
	}
	if !hasClaim(res.Claims, "a.go", "Bar") || !hasClaim(res.Claims, "note.txt", "") {
		t.Fatalf("claims %#v", res.Claims)
	}
}

func TestHarvestAcceptanceTimeout(t *testing.T) {
	dir, head := newRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := Harvest(ctx, harvestInput(dir, head, model.Card{
		ID: "c", Kind: model.KindDev, Acceptance: []string{"sleep 30"},
	}, 0, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != model.OutcomeTimeout {
		t.Fatalf("outcome %s evidence %s", res.Outcome, res.Evidence)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("acceptance hung for %s", time.Since(start))
	}
}

func TestHarvestBudgetLimitedNotPassed(t *testing.T) {
	tests := []struct {
		name       string
		kind       model.CardKind
		acceptance []string
		class      model.ErrorClass
		change     bool
		result     string
		timeout    bool
		want       model.Outcome
	}{
		{
			name:       "code changes and passing acceptance",
			kind:       model.KindDev,
			acceptance: []string{"true"},
			class:      model.ErrorBudgetLimited,
			change:     true,
			want:       model.OutcomeBudgetLimited,
		},
		{
			name:   "design card valid result",
			kind:   model.KindDesign,
			class:  model.ErrorBudgetLimited,
			result: "## 结论\n设计完成\n",
			want:   model.OutcomeBudgetLimited,
		},
		{
			name:       "violation still wins",
			kind:       model.KindDev,
			acceptance: []string{"true"},
			class:      model.ErrorViolation,
			change:     true,
			want:       model.OutcomeViolation,
		},
		{
			name:       "acceptance timeout still wins",
			kind:       model.KindDev,
			acceptance: []string{"sleep 30"},
			class:      model.ErrorBudgetLimited,
			change:     true,
			timeout:    true,
			want:       model.OutcomeTimeout,
		},
		{
			name:   "unparsable result still wins",
			kind:   model.KindDesign,
			class:  model.ErrorBudgetLimited,
			result: "hello world\n",
			want:   model.OutcomeResultUnparsable,
		},
		{
			name:       "incomplete events without budget_limited still pass",
			kind:       model.KindDev,
			acceptance: []string{"true"},
			change:     true,
			want:       model.OutcomePassed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, head := newRepo(t)
			if tc.change {
				write(t, dir, "b.go", "package a\n\nfunc B() {}\n")
			}
			if tc.result != "" {
				write(t, dir, ".cardex-result.md", tc.result)
			}
			ctx := context.Background()
			if tc.timeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
				defer cancel()
			}
			res, err := Harvest(ctx, harvestInput(dir, head, model.Card{
				ID: "c", Kind: tc.kind, Acceptance: tc.acceptance,
			}, 1, []byte("{\"type\":\"assistant\",\"text\":\"hel"), tc.class))
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != tc.want {
				t.Fatalf("outcome %s want %s evidence %s", res.Outcome, tc.want, res.Evidence)
			}
		})
	}
}

func harvestInput(dir, head string, card model.Card, exit int, output []byte, class model.ErrorClass) Input {
	return Input{
		Attempt:    model.Attempt{ID: "att", Card: card.ID, Worktree: dir, BaseCommit: head},
		Card:       card,
		Exit:       exit,
		Output:     output,
		ErrorClass: class,
		Repo:       dir,
	}
}

func hasClaim(claims []model.Claim, path, symbol string) bool {
	for _, c := range claims {
		if c.Path == path && c.Symbol == symbol {
			return true
		}
	}
	return false
}

func newRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	write(t, dir, "a.go", "package a\n\nfunc Foo() {}\n\nfunc Bar() {}\n")
	runGit(t, dir, "add", "a.go")
	runGit(t, dir, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "init")
	return dir, strings.TrimSpace(runGitOut(t, dir, "rev-parse", "HEAD"))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
