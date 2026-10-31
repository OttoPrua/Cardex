package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"syscall"

	"cardex/internal/classify"
	"cardex/internal/harvest"
	"cardex/internal/host"
	"cardex/internal/model"
	"cardex/internal/quota"
	"cardex/internal/route"
	"cardex/internal/sched"
)

func TestS01(t *testing.T) {
	// 单卡闭环，经过复核和合并。
	t.Skip("R4-D")
}

func TestS02(t *testing.T) {
	// 同一文件、不同函数的两张卡并行，都能合并。
	h := New(t)
	stopLater(t, h)
	commitRepo(t, h.Repo, "answer.go", answerWithFuncs, "funcs")
	_, obj := bindProject(t, h, "true")
	installRoutes(t, h)
	mk := func(id, symbol, body string) {
		t.Helper()
		card := model.Card{
			ID: id, Objective: obj, Kind: model.KindDev, Title: id, Tier: model.TierT2,
			Status: model.CardReady, Acceptance: []string{"true"},
			Claims: []model.Claim{{Path: "answer.go", Symbol: symbol}},
			Body: fakeScript("8s", nil, []map[string]string{{
				"path": "answer.go", "name": symbol, "body": body,
			}}),
		}
		if err := h.DB.CreateCard(context.Background(), &card); err != nil {
			t.Fatal(err)
		}
	}
	mk("c-foo", "Foo", "\treturn 11")
	mk("c-bar", "Bar", "\treturn 22")
	mustTick(t, h, func() bool {
		return cardOf(t, h, "c-foo").Status == model.CardRunning &&
			cardOf(t, h, "c-bar").Status == model.CardRunning &&
			attemptLive(t, h, "c-foo") && attemptLive(t, h, "c-bar")
	}, 20*time.Second)
	if cardOf(t, h, "c-foo").Status == model.CardMerged || cardOf(t, h, "c-bar").Status == model.CardMerged {
		t.Fatalf("merged before both were running\n%s", diag(h))
	}
	fooPID := livePID(t, h, "c-foo")
	barPID := livePID(t, h, "c-bar")
	t.Logf("parallel running c-foo pid %d c-bar pid %d", fooPID, barPID)
	mustTick(t, h, func() bool {
		return cardOf(t, h, "c-foo").Status == model.CardMerged &&
			cardOf(t, h, "c-bar").Status == model.CardMerged
	}, 45*time.Second)
	branch := integrationBranch(t, h, obj)
	text := gitOut(t, h.Repo, "show", branch+":answer.go")
	if !strings.Contains(text, "return 11") || !strings.Contains(text, "return 22") {
		t.Fatalf("integration answer.go:\n%s", text)
	}
}

func TestS03(t *testing.T) {
	// 同一函数的两张卡，第二张等待后再跑。
	h := New(t)
	stopLater(t, h)
	commitRepo(t, h.Repo, "answer.go", answerWithFuncs, "funcs")
	_, obj := bindProject(t, h, "true")
	installRoutes(t, h)
	mk := func(id, body, hold string) {
		t.Helper()
		card := model.Card{
			ID: id, Objective: obj, Kind: model.KindDev, Title: id, Tier: model.TierT2,
			Status: model.CardReady, Acceptance: []string{"true"},
			Claims: []model.Claim{{Path: "answer.go", Symbol: "Foo"}},
			Body: fakeScript(hold, nil, []map[string]string{{
				"path": "answer.go", "name": "Foo", "body": body,
			}}),
		}
		if err := h.DB.CreateCard(context.Background(), &card); err != nil {
			t.Fatal(err)
		}
	}
	mk("c1", "\treturn 11", "5s")
	mk("c2", "\treturn 22", "1s")
	mustTick(t, h, func() bool {
		waiting := cardOf(t, h, "c2")
		return cardOf(t, h, "c1").Status == model.CardRunning && attemptLive(t, h, "c1") &&
			waiting.Status == model.CardWaiting &&
			strings.Contains(waiting.WaitReason, "c1") &&
			strings.Contains(waiting.WaitReason, "Foo") &&
			len(attemptsOf(t, h, "c2")) == 0
	}, 20*time.Second)
	t.Logf("c2 waiting: %s", cardOf(t, h, "c2").WaitReason)
	mustTick(t, h, func() bool {
		if cardOf(t, h, "c2").Status == model.CardWaiting {
			return false
		}
		for _, attempt := range attemptsOf(t, h, "c2") {
			if attempt.PID > 0 {
				return true
			}
		}
		return false
	}, 30*time.Second)
	if attemptsOf(t, h, "c2")[0].PID <= 0 {
		t.Fatalf("c2 did not run\n%s", diag(h))
	}
}

func TestS04(t *testing.T) {
	// 未声明的重叠改动产生冲突，派变基卡后合并。
	h := New(t)
	stopLater(t, h)
	commitRepo(t, h.Repo, "extra.txt", "base\n", "extra")
	_, obj := bindProject(t, h, "true")
	installRoutes(t, h)
	mk := func(id, content string) {
		t.Helper()
		card := model.Card{
			ID: id, Objective: obj, Kind: model.KindDev, Title: id, Tier: model.TierT2,
			Status: model.CardReady, Acceptance: []string{"true"},
			Body: fakeScript("", []map[string]string{{"path": "extra.txt", "content": content}}, nil),
		}
		if err := h.DB.CreateCard(context.Background(), &card); err != nil {
			t.Fatal(err)
		}
	}
	mk("c1", "from-c1\n")
	mk("c2", "from-c2\n")
	runnerOf(t, h).Script = func(card model.Card) ([]byte, bool) {
		if card.Kind != model.KindRebase {
			return nil, false
		}
		branch := integrationBranch(t, h, card.Objective)
		out, err := exec.Command("git", "-C", h.Repo, "show", branch+":extra.txt").Output()
		if err != nil || strings.TrimSpace(string(out)) == "" {
			out = []byte("resolved\n")
		}
		return fakeScriptRaw("", []map[string]string{{"path": "extra.txt", "content": string(out)}}, nil), true
	}
	var rebaseID string
	mustTick(t, h, func() bool {
		for _, card := range cardsOf(t, h, obj) {
			if card.Kind != model.KindRebase {
				continue
			}
			if !strings.Contains(card.Body, "extra.txt") || !strings.Contains(card.Body, "<<<<<<<") {
				t.Fatalf("rebase body missing conflict: %s", card.Body)
			}
			for _, attempt := range attemptsOf(t, h, card.ID) {
				if attempt.PID > 0 {
					rebaseID = card.ID
					return true
				}
			}
		}
		return false
	}, 40*time.Second)
	mustTick(t, h, func() bool {
		return cardOf(t, h, "c1").Status == model.CardMerged &&
			cardOf(t, h, "c2").Status == model.CardMerged
	}, 45*time.Second)
	if rebaseID == "" {
		t.Fatal("rebase was not dispatched")
	}
	text := gitOut(t, h.Repo, "show", integrationBranch(t, h, obj)+":extra.txt")
	if strings.TrimSpace(text) == "base" {
		t.Fatalf("integration did not take the edit: %q", text)
	}
}

func TestS05(t *testing.T) {
	// 合并后测试变红，派修复卡。
	h := New(t)
	stopLater(t, h)
	_, obj := bindProject(t, h, "./test.sh")
	installRoutes(t, h)
	before := gitOut(t, h.Repo, "rev-parse", "HEAD")
	card := model.Card{
		ID: "c1", Objective: obj, Kind: model.KindDev, Title: "c1", Tier: model.TierT2,
		Status: model.CardReady, Acceptance: []string{"true"},
		Body: fakeScript("", nil, []map[string]string{{
			"path": "answer.go", "name": "Answer", "body": "\treturn 2",
		}}),
	}
	if err := h.DB.CreateCard(context.Background(), &card); err != nil {
		t.Fatal(err)
	}
	runnerOf(t, h).Script = func(card model.Card) ([]byte, bool) {
		if card.Kind != model.KindRepair {
			return nil, false
		}
		return fakeScriptRaw("", []map[string]string{{
			"path":    "answer.go",
			"content": "package fixture\n\nfunc Answer() int {\n\treturn 1\n}\n",
		}}, nil), true
	}
	var repairID string
	mustTick(t, h, func() bool {
		for _, card := range cardsOf(t, h, obj) {
			if card.Kind != model.KindRepair {
				continue
			}
			if !strings.Contains(card.Body, "FAIL") && !strings.Contains(card.Body, "exit:") {
				t.Fatalf("repair body missing test failure: %s", card.Body)
			}
			for _, attempt := range attemptsOf(t, h, card.ID) {
				if attempt.PID > 0 {
					repairID = card.ID
					return true
				}
			}
		}
		return false
	}, 60*time.Second)
	mustTick(t, h, func() bool {
		return cardOf(t, h, "c1").Status == model.CardMerged
	}, 60*time.Second)
	if repairID == "" {
		t.Fatal("repair was not dispatched")
	}
	after := gitOut(t, h.Repo, "rev-parse", integrationBranch(t, h, obj))
	text := gitOut(t, h.Repo, "show", integrationBranch(t, h, obj)+":answer.go")
	if after == before || !strings.Contains(text, "return 1") || strings.Contains(text, "return 2") {
		t.Fatalf("head %s -> %s\n%s", before, after, text)
	}
}

func TestS06(t *testing.T) {
	// 事件流不完整，但工作区有改动且测试通过，按 passed 处理。
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "add", "a.go")
	git("-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "init")
	headCmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	headOut, err := headCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := harvest.Harvest(context.Background(), harvest.Input{
		Attempt: model.Attempt{
			ID: "s06", Card: "card", Worktree: dir, BaseCommit: strings.TrimSpace(string(headOut)),
		},
		Card: model.Card{
			ID: "card", Kind: model.KindDev, Acceptance: []string{"true"},
		},
		Exit:   1,
		Output: []byte("{\"type\":\"assistant\",\"text\":\"hel"),
		Repo:   dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != model.OutcomePassed {
		t.Fatalf("outcome %s evidence %s", res.Outcome, res.Evidence)
	}
}

func TestS07(t *testing.T) {
	// 没有产出，换渠道重派一次，仍然没有就派待拍板问题。
	t.Skip("R3-B")
}

func TestS08(t *testing.T) {
	// 撞额度，渠道封锁，换渠道从卡分支续做。
	t.Skip("R3-B")
}

func TestS09(t *testing.T) {
	// 认证失败，渠道标记不健康并推送通知，卡换渠道。
	t.Skip("R3-B")
}

func TestS10(t *testing.T) {
	// 拒答，不换渠道，派待拍板问题。
	t.Skip("R3-B")
}

func TestS11(t *testing.T) {
	// 结果格式乱，派整理卡。
	t.Skip("R3-B")
}

func TestS12(t *testing.T) {
	// 设计期，safe 模式下 module 层冻结需要问题确认。
	t.Skip("R3-A")
}

func TestS13(t *testing.T) {
	// fast 模式下自动冻结。
	t.Skip("R3-A")
}

func TestS14(t *testing.T) {
	// 开发卡报告设计问题，经确认重开节点，下游卡暂停，派重规划卡。
	t.Skip("R3-A")
}

func TestS15(t *testing.T) {
	// 节点熔断，问题投递到 origin（用 file 适配器）。
	t.Skip("R3-B")
}

func TestS16(t *testing.T) {
	// 目标预算触顶，暂停目标。
	t.Skip("R3-B")
}

func TestS17(t *testing.T) {
	// 同层同源按 Pace 排序，低于红线的渠道被排除，显式指定不受红线限制。
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	led := quota.New()
	led.SetClock(func() time.Time { return now })
	mk := func(id, modelName string, used int64, redline float64) model.Channel {
		t.Helper()
		ch := model.Channel{
			ID: id, Runner: model.RunnerGrok, SourceGroup: "same", Family: "fam",
			Models: []string{modelName}, Concurrency: 2, Health: model.HealthOK, Redline: redline,
			QuotaWindows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", LimitTokens: 100, ResetAt: now,
			}},
		}
		led.Configure(ch)
		if err := led.Record(context.Background(), id, model.Usage{Tokens: used, Source: "meter"}, now.Add(-time.Minute), now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		return ch
	}
	fast := mk("fast", "fast-m", 20, 0)
	slow := mk("slow", "slow-m", 50, 0)
	low := mk("low", "low-m", 90, 0.2)
	channels := []model.Channel{slow, low, fast}
	pos := []model.Positioning{
		{Model: "fast-m", Family: "fam", Tiers: []model.Tier{model.TierT2}, Preference: 1},
		{Model: "slow-m", Family: "fam", Tiers: []model.Tier{model.TierT2}, Preference: 1},
		{Model: "low-m", Family: "fam", Tiers: []model.Tier{model.TierT2}, Preference: 1},
	}
	card := model.Card{Kind: model.KindDev, Tier: model.TierT2}
	rt, reason, err := route.Select(context.Background(), route.Request{
		Card: card, Channels: channels, Positioning: pos, Quota: led, Now: now,
	})
	if err != nil || rt.Channel != fast.ID {
		t.Fatalf("pace route %+v err %v", rt, err)
	}
	if len(reason.Candidates) != 2 || reason.Candidates[0].Channel != fast.ID || reason.Candidates[1].Channel != slow.ID {
		t.Fatalf("pace candidates %+v", reason.Candidates)
	}
	if len(reason.Filtered) != 1 || reason.Filtered[0].Channel != low.ID || reason.Filtered[0].Reason != route.ReasonRedline {
		t.Fatalf("redline %+v", reason.Filtered)
	}
	if len(reason.Scores) != 2 || reason.Scores[0].Score <= reason.Scores[1].Score {
		t.Fatalf("scores %+v", reason.Scores)
	}
	card.Channel = low.ID
	rt, reason, err = route.Select(context.Background(), route.Request{
		Card: card, Channels: channels, Positioning: pos, Quota: led, Now: now,
	})
	if err != nil || rt.Channel != low.ID || rt.Model != "low-m" {
		t.Fatalf("pin %+v err %v", rt, err)
	}
	if len(reason.Filtered) != 0 {
		t.Fatalf("pin applied redline %+v", reason.Filtered)
	}
}

func TestS18(t *testing.T) {
	// 分级成功写入 tier；分级超时退回规则。
	ctx := context.Background()
	card := model.Card{Kind: model.KindDev, Body: "classify this body"}
	res, err := classify.Classify(ctx, card, classify.Options{
		Channel: "classifier",
		Call: func(callCtx context.Context, channelID, prompt string) (string, error) {
			if channelID != "classifier" || prompt == "" {
				t.Fatalf("call %s %q", channelID, prompt)
			}
			if _, ok := callCtx.Deadline(); !ok {
				t.Fatal("no deadline")
			}
			return "TIER: T1\nREASON: small change\n", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	card.Tier = res.Tier
	if card.Tier != model.TierT1 || res.Source != classify.SourceModel {
		t.Fatalf("success %+v tier %s", res, card.Tier)
	}
	timed := model.Card{Kind: model.KindDev, Body: "a different body"}
	res, err = classify.Classify(ctx, timed, classify.Options{
		Channel: "classifier",
		Call: func(context.Context, string, string) (string, error) {
			return "", context.DeadlineExceeded
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	timed.Tier = res.Tier
	if timed.Tier != model.TierT2 || res.Source != classify.SourceRule {
		t.Fatalf("timeout %+v tier %s", res, timed.Tier)
	}
}

func TestS19(t *testing.T) {
	// 余量填充，临近重置的渠道拿到 backlog 卡。
	h := New(t)
	stopLater(t, h)
	ctx := context.Background()
	ch, err := h.RegisterFakeChannel("alpha", model.TierT1, RemainingWindow{
		Ratio: 0.9, Reset: time.Now().Add(30 * time.Minute), Confidence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch.Concurrency = 2
	ch.Redline = 0
	ch.Models = []string{"model-a"}
	if err := h.DB.UpdateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	_, obj := bindProject(t, h, "true")
	card := model.Card{
		ID: "back", Objective: obj, Kind: model.KindBacklog, Title: "back", Tier: model.TierT1,
		Status: model.CardReady,
		Body:   fakeScript("3s", []map[string]string{{"path": "b.txt", "content": "b\n"}}, nil),
	}
	if err := h.DB.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	mustTick(t, h, func() bool {
		return cardOf(t, h, "back").Status == model.CardRunning && attemptLive(t, h, "back")
	}, 20*time.Second)
	attempts := attemptsOf(t, h, "back")
	if len(attempts) != 1 || attempts[0].Route.Channel != ch.ID {
		t.Fatalf("backlog route %+v channel %s\n%s", attempts, ch.ID, diag(h))
	}
	if err := h.Sched.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again := attemptsOf(t, h, "back"); len(again) != 1 || again[0].ID != attempts[0].ID {
		t.Fatalf("second tick attempts %+v", again)
	}
}

func TestS20(t *testing.T) {
	// manual 目标不自动生成卡，但复核和合并照常。
	h := New(t)
	stopLater(t, h)
	commitRepo(t, h.Repo, "note.txt", "base\n", "note")
	_, obj := bindProject(t, h, "true")
	installRoutes(t, h)
	card := model.Card{
		ID: "dev", Objective: obj, Kind: model.KindDev, Title: "dev", Tier: model.TierT2,
		Status: model.CardReady, Acceptance: []string{"true"},
		Body: fakeScript("", []map[string]string{{"path": "note.txt", "content": "shipped\n"}}, nil),
	}
	if err := h.DB.CreateCard(context.Background(), &card); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runnerOf(t, h).Engine = func(_ context.Context, id string) error {
		calls++
		extra := model.Card{
			ID: "design-" + id, Objective: id, Kind: model.KindDesign, Title: "design",
			Tier: model.TierT3, Status: model.CardNeedsInput,
		}
		if _, err := h.DB.GetCard(context.Background(), extra.ID); err == nil {
			return nil
		}
		return h.DB.CreateCard(context.Background(), &extra)
	}
	mustTick(t, h, func() bool {
		dev, err := h.DB.GetCard(context.Background(), "dev")
		if err != nil || dev.Status != model.CardMerged {
			return false
		}
		review, err := h.DB.GetCard(context.Background(), "rv-dev")
		return err == nil && review.Status == model.CardDone
	}, 40*time.Second)
	if calls != 0 {
		t.Fatalf("manual engine calls %d", calls)
	}
	for _, card := range cardsOf(t, h, obj) {
		switch card.Kind {
		case model.KindDesign, model.KindPlan, model.KindReplan:
			t.Fatalf("manual grew %+v", card)
		}
	}
	text := gitOut(t, h.Repo, "show", integrationBranch(t, h, obj)+":note.txt")
	if !strings.Contains(text, "shipped") {
		t.Fatalf("note %q", text)
	}
}

func TestS21(t *testing.T) {
	// 导入 v1 数据，按映射规则落位。
	t.Skip("R4-B")
}

func TestS22(t *testing.T) {
	// 看板只接受 GET，默认视图不含历史。
	t.Skip("R4-A")
}

func TestS23(t *testing.T) {
	// 定位任务产生提案，提案生成问题，批准后新卡使用新定位。
	t.Skip("R4-C")
}

func TestS24(t *testing.T) {
	// 执行器写 worktree 以外的地方被沙箱拒绝（只在 macOS 上跑）。
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS only")
	}
	h := New(t)
	ctx := context.Background()
	ch := model.Channel{
		ID: "ch-s24", Runner: model.RunnerFake, Health: model.HealthOK, Tier: model.TierT2, Family: "fake",
	}
	if err := h.DB.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "c-s24", Kind: model.KindDev, Status: model.CardReady, Title: "s24", Objective: "o", Node: "n",
	}
	if err := h.DB.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(h.Home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := sandboxOutsideDir(t)
	target := filepath.Join(outside, "newfile")
	raw, err := json.Marshal(map[string]any{
		"attempts": map[string]any{
			"0": map[string]any{
				"outside_write": target,
				"write_files":   []map[string]string{{"path": "inside.txt", "content": "inside"}},
				"output":        "s24-done\n",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := "a-s24"
	scriptPath := filepath.Join(h.Home, "attempts", id, "script.json")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(h.Home, "prompt-s24.md")
	if err := os.WriteFile(prompt, []byte("prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempt := model.Attempt{
		ID: id, Card: card.ID, Index: 0,
		Route:      model.Route{Channel: ch.ID, Model: "fake"},
		Worktree:   work,
		PromptPath: prompt,
	}
	if err := h.DB.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.Home, "attempts", id, "hard-duration"), []byte("30s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := host.Run(ctx, h.Home, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("outside file: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(work, "inside.txt"))
	if err != nil || string(body) != "inside" {
		t.Fatalf("inside write %q %v", body, err)
	}
	logb, err := os.ReadFile(filepath.Join(h.Home, "attempts", id, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.DB.GetAttempt(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	evidence := strings.ToLower(string(logb) + "\n" + got.ErrorExcerpt)
	if !strings.Contains(evidence, "operation not permitted") && !strings.Contains(evidence, "deny file-write") {
		t.Fatalf("denial missing from evidence: log=%s excerpt=%s class=%s", logb, got.ErrorExcerpt, got.ErrorClass)
	}
	if strings.Contains(string(logb), "must be relative") || strings.Contains(string(logb), "escapes workdir") {
		t.Fatalf("denied by the in-process path guard: %s", logb)
	}
	if got.Outcome != "" {
		t.Fatalf("outcome %q", got.Outcome)
	}
	t.Logf("outside file absent; denial in evidence: %s", got.ErrorExcerpt)
}

const answerWithFuncs = "package fixture\n\nfunc Answer() int {\n\treturn 1\n}\n\nfunc Foo() int {\n\treturn 0\n}\n\nfunc Bar() int {\n\treturn 0\n}\n"

func bindProject(t *testing.T, h *Harness, testCmd string) (string, string) {
	t.Helper()
	ctx := context.Background()
	branch := gitOut(t, h.Repo, "rev-parse", "--abbrev-ref", "HEAD")
	proj := model.Project{
		ID: "p", RepoPath: h.Repo, WorktreeRoot: filepath.Join(h.Root, "wt"),
		TestCmd: testCmd, DesignDir: "docs/design/",
	}
	if err := h.DB.CreateProject(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	obj := model.Objective{
		ID: "obj", Project: proj.ID, Title: "obj", Mode: model.ModeManual, FreezeMode: model.FreezeFast,
		Status: model.ObjectiveActive, IntegrationBranch: branch,
	}
	if err := h.DB.CreateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	return proj.ID, obj.ID
}

func installRoutes(t *testing.T, h *Harness) {
	t.Helper()
	ctx := context.Background()
	for _, spec := range []struct{ family, modelName string }{
		{"alpha", "model-a"},
		{"beta", "model-b"},
	} {
		ch, err := h.RegisterFakeChannel(spec.family, model.TierT2, RemainingWindow{
			Ratio: 0.8, Reset: time.Now().Add(24 * time.Hour), Confidence: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		ch.Models = []string{spec.modelName}
		ch.Concurrency = 4
		ch.SourceGroup = spec.family
		ch.Redline = 0
		if err := h.DB.UpdateChannel(ctx, &ch); err != nil {
			t.Fatal(err)
		}
		pos := model.Positioning{
			ID: "pos-" + ch.ID, Model: spec.modelName, Family: spec.family,
			Tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, Preference: 1,
		}
		if err := h.DB.CreatePositioning(ctx, &pos); err != nil {
			t.Fatal(err)
		}
	}
}

func fakeScript(hold string, files, funcs []map[string]string) string {
	return string(fakeScriptRaw(hold, files, funcs))
}

func fakeScriptRaw(hold string, files, funcs []map[string]string) []byte {
	step := map[string]any{"exit_code": 0}
	if hold != "" {
		step["hold"] = hold
	}
	if len(files) > 0 {
		step["write_files"] = files
	}
	if len(funcs) > 0 {
		step["change_funcs"] = funcs
	}
	raw, err := json.Marshal(map[string]any{"attempts": map[string]any{"0": step}})
	if err != nil {
		panic(err)
	}
	return raw
}

func runnerOf(t *testing.T, h *Harness) *sched.Runner {
	t.Helper()
	r, ok := h.Sched.(*sched.Runner)
	if !ok {
		t.Fatalf("sched %T", h.Sched)
	}
	return r
}

func mustTick(t *testing.T, h *Harness, cond func() bool, timeout time.Duration) {
	t.Helper()
	if err := h.TickUntil(cond, timeout); err != nil {
		t.Fatalf("%v\n%s", err, diag(h))
	}
}

func cardOf(t *testing.T, h *Harness, id string) model.Card {
	t.Helper()
	card, err := h.DB.GetCard(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func cardsOf(t *testing.T, h *Harness, obj string) []model.Card {
	t.Helper()
	cards, err := h.DB.ListCards(context.Background(), obj)
	if err != nil {
		t.Fatal(err)
	}
	return cards
}

func attemptsOf(t *testing.T, h *Harness, cardID string) []model.Attempt {
	t.Helper()
	attempts, err := h.DB.ListAttempts(context.Background(), cardID)
	if err != nil {
		t.Fatal(err)
	}
	return attempts
}

func attemptLive(t *testing.T, h *Harness, cardID string) bool {
	t.Helper()
	for _, attempt := range attemptsOf(t, h, cardID) {
		if host.LiveHost(attempt) && processAlive(attempt.PID) {
			return true
		}
	}
	return false
}

func livePID(t *testing.T, h *Harness, cardID string) int {
	t.Helper()
	for _, attempt := range attemptsOf(t, h, cardID) {
		if host.LiveHost(attempt) && processAlive(attempt.PID) {
			return attempt.PID
		}
	}
	return 0
}

func integrationBranch(t *testing.T, h *Harness, obj string) string {
	t.Helper()
	got, err := h.DB.GetObjective(context.Background(), obj)
	if err != nil {
		t.Fatal(err)
	}
	return got.IntegrationBranch
}

func commitRepo(t *testing.T, repo, name, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "--", name)
	gitRun(t, repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", msg)
}

func gitRun(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func stopLater(t *testing.T, h *Harness) {
	t.Helper()
	t.Cleanup(func() { stopAttempts(h) })
}

func stopAttempts(h *Harness) {
	attempts, err := h.DB.ListAttempts(context.Background(), "")
	if err != nil {
		return
	}
	for _, attempt := range attempts {
		if b, err := os.ReadFile(filepath.Join(h.Home, "attempts", attempt.ID, "executor.pgid")); err == nil {
			if pgid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pgid > 0 {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
		}
		if attempt.PID > 0 {
			_ = syscall.Kill(-attempt.PID, syscall.SIGKILL)
			_ = syscall.Kill(attempt.PID, syscall.SIGKILL)
		}
	}
}

func diag(h *Harness) string {
	var b strings.Builder
	cards, _ := h.DB.ListCards(context.Background(), "")
	for _, card := range cards {
		fmt.Fprintf(&b, "card %s %s %s wait=%q\n", card.ID, card.Kind, card.Status, card.WaitReason)
	}
	attempts, _ := h.DB.ListAttempts(context.Background(), "")
	for _, attempt := range attempts {
		fmt.Fprintf(&b, "attempt %s card=%s pid=%d exit=%v ended=%s outcome=%s\n",
			attempt.ID, attempt.Card, attempt.PID, attempt.Exit, attempt.EndedAt.Format(time.RFC3339), attempt.Outcome)
		logb, err := os.ReadFile(filepath.Join(h.Home, "attempts", attempt.ID, "launcher.log"))
		if err == nil && len(logb) > 0 {
			text := string(logb)
			if len(text) > 800 {
				text = text[len(text)-800:]
			}
			fmt.Fprintf(&b, "launcher %s: %s\n", attempt.ID, text)
		}
	}
	return b.String()
}

// sandboxOutsideDir is outside TMPDIR. The seatbelt profile allows writes under TMPDIR,
// so a denial target created in t.TempDir would be permitted.
func sandboxOutsideDir(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	tmpReal, _ := filepath.EvalSymlinks(os.TempDir())
	baseReal, err := filepath.EvalSymlinks(base)
	if err != nil {
		baseReal = base
	}
	sep := string(os.PathSeparator)
	if baseReal == tmpReal || strings.HasPrefix(baseReal, tmpReal+sep) || strings.HasPrefix(tmpReal, baseReal+sep) {
		base = "/var/tmp"
	}
	dir, err := os.MkdirTemp(base, "cardex-s24-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
