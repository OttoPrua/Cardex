package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cardex/internal/classify"
	"cardex/internal/harvest"
	"cardex/internal/host"
	"cardex/internal/loop"
	"cardex/internal/model"
	"cardex/internal/question"
	"cardex/internal/quota"
	"cardex/internal/route"
	"cardex/internal/workspace"
)

func TestS01(t *testing.T) {
	// 单卡闭环，经过复核和合并。
	t.Skip("R4-D")
}

func TestS02(t *testing.T) {
	// 同一文件、不同函数的两张卡并行，都能合并。
	t.Skip("R3-C")
}

func TestS03(t *testing.T) {
	// 同一函数的两张卡，第二张等待后再跑。
	t.Skip("R3-C")
}

func TestS04(t *testing.T) {
	// 未声明的重叠改动产生冲突，派变基卡后合并。
	t.Skip("R3-C")
}

func TestS05(t *testing.T) {
	// 合并后测试变红，派修复卡。
	t.Skip("R3-C")
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
	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	ctx := t.Context()
	obj := &model.Objective{Title: "S12", Mode: model.ModeAuto, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive, Origin: "codex:s12"}
	h.NoError(h.DB.CreateObjective(ctx, obj))
	node := &model.DesignNode{Objective: obj.ID, Layer: model.LayerModule, Status: model.NodeDrafting}
	h.NoError(h.DB.CreateNode(ctx, node))
	h.NoError(h.DB.CreateCard(ctx, &model.Card{
		Objective: obj.ID, Node: node.ID, Kind: model.KindDesign, Title: "设计",
		Body: "## 可冻结\n是\n", Tier: model.TierT3, Status: model.CardDone,
	}))
	eng := loop.New(h.DB, workspace.Git{}, quietDeliver{})
	h.NoError(eng.Step(ctx, obj.ID))
	got := e2eNode(t, h, node.ID)
	h.Equal(got.Status, model.NodeFreezable)
	fq := e2eFreezeQuestion(t, h, obj.ID, node.ID)
	svc := question.New(h.DB, quietDeliver{})
	if _, err := svc.Answer(ctx, fq.ID, ""); err == nil {
		t.Fatal("empty answer")
	}
	h.NoError(eng.Step(ctx, obj.ID))
	h.Equal(e2eNode(t, h, node.ID).Status, model.NodeFreezable)
	if _, err := svc.Answer(ctx, fq.ID, fq.Options[0]); err != nil {
		t.Fatal(err)
	}
	h.NoError(eng.Step(ctx, obj.ID))
	h.Equal(e2eNode(t, h, node.ID).Status, model.NodeFrozen)
	if !e2eHasKind(t, h, obj.ID, node.ID, model.KindPlan) {
		t.Fatal("frozen module has no plan card")
	}
}

func TestS13(t *testing.T) {
	// fast 模式下非方向层自动冻结，方向层仍要 Otto 确认。
	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	ctx := t.Context()
	obj := &model.Objective{Title: "S13", Mode: model.ModeAuto, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive, Origin: "codex:s13"}
	h.NoError(h.DB.CreateObjective(ctx, obj))
	direction := &model.DesignNode{Objective: obj.ID, Layer: model.LayerDirection, Status: model.NodeDrafting}
	module := &model.DesignNode{Objective: obj.ID, Layer: model.LayerModule, Status: model.NodeDrafting}
	h.NoError(h.DB.CreateNode(ctx, direction))
	h.NoError(h.DB.CreateNode(ctx, module))
	for _, id := range []string{direction.ID, module.ID} {
		h.NoError(h.DB.CreateCard(ctx, &model.Card{
			Objective: obj.ID, Node: id, Kind: model.KindDesign, Title: "设计",
			Body: "## 可冻结\n是\n", Tier: model.TierT3, Status: model.CardDone,
		}))
	}
	eng := loop.New(h.DB, workspace.Git{}, quietDeliver{})
	h.NoError(eng.Step(ctx, obj.ID))
	h.Equal(e2eNode(t, h, direction.ID).Status, model.NodeFreezable)
	if e2eFreezeQuestion(t, h, obj.ID, direction.ID).ID == "" {
		t.Fatal("direction fast froze without a freeze question")
	}
	h.Equal(e2eNode(t, h, module.ID).Status, model.NodeFrozen)
	qs, err := h.DB.ListQuestions(ctx, obj.ID)
	h.NoError(err)
	for _, q := range qs {
		if q.Kind == model.QuestionFreeze && q.Scope == module.ID {
			t.Fatal("fast module created a freeze question")
		}
	}
	if !e2eHasKind(t, h, obj.ID, module.ID, model.KindPlan) {
		t.Fatal("fast module has no plan card")
	}
}

func TestS14(t *testing.T) {
	// 开发卡报告设计问题，经确认重开节点，下游卡暂停，派重规划卡。
	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	ctx := t.Context()
	obj := &model.Objective{Title: "S14", Mode: model.ModeAuto, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive, Origin: "codex:s14"}
	h.NoError(h.DB.CreateObjective(ctx, obj))
	node := &model.DesignNode{Objective: obj.ID, Layer: model.LayerModule, Status: model.NodeFrozen}
	h.NoError(h.DB.CreateNode(ctx, node))
	ready := &model.Card{Objective: obj.ID, Node: node.ID, Kind: model.KindDev, Title: "未开始", Body: "待做", Tier: model.TierT2, Status: model.CardReady}
	running := &model.Card{Objective: obj.ID, Node: node.ID, Kind: model.KindDev, Title: "进行中", Body: "在跑", Tier: model.TierT2, Status: model.CardRunning}
	h.NoError(h.DB.CreateCard(ctx, ready))
	h.NoError(h.DB.CreateCard(ctx, running))
	h.NoError(h.DB.CreateCard(ctx, &model.Card{
		Objective: obj.ID, Node: node.ID, Kind: model.KindDev, Title: "已完成",
		Body: "## 设计问题\n接口不一致\n", Tier: model.TierT2, Status: model.CardDone,
	}))
	eng := loop.New(h.DB, workspace.Git{}, quietDeliver{})
	h.NoError(eng.Step(ctx, obj.ID))
	if e2eNode(t, h, node.ID).Status == model.NodeReopened {
		t.Fatal("reopened before the question was answered")
	}
	reopen := e2eQuestionKind(t, h, obj.ID, node.ID, model.QuestionReopen)
	if reopen.ID == "" || len(reopen.Options) == 0 {
		t.Fatal("missing reopen question")
	}
	h.Equal(e2eCard(t, h, ready.ID).Status, model.CardReady)
	h.Equal(e2eCard(t, h, running.ID).Status, model.CardRunning)
	if _, err := question.New(h.DB, quietDeliver{}).Answer(ctx, reopen.ID, reopen.Options[0]); err != nil {
		t.Fatal(err)
	}
	h.NoError(eng.Step(ctx, obj.ID))
	h.Equal(e2eNode(t, h, node.ID).Status, model.NodeReopened)
	h.Equal(e2eCard(t, h, ready.ID).Status, model.CardPaused)
	h.Equal(e2eCard(t, h, running.ID).Status, model.CardRunning)
	if !e2eHasKind(t, h, obj.ID, node.ID, model.KindReplan) {
		t.Fatal("missing replan card")
	}
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
	t.Skip("R3-C")
}

func TestS20(t *testing.T) {
	// manual 目标不自动生成卡，但复核和合并照常。
	t.Skip("R3-C")
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
