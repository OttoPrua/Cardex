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
	"cardex/internal/quota"
	"cardex/internal/route"
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
	f := newDev(t, model.ModeManual, "file:origin", model.Budget{})
	a := f.channel("ch-a")
	b := f.channel("ch-b")
	card := f.card("c", a.ID, model.KindDev, model.CardHarvesting, "")
	f.attempt(card, 0, model.OutcomeNoOutput, a.ID, "", 0)
	f.step()
	got := f.mustCard("c")
	if got.Channel != b.ID || got.Status != model.CardReady || len(f.questions()) != 0 {
		t.Fatalf("switch %+v questions %+v", got, f.questions())
	}
	f.attempt(got, 1, model.OutcomeNoOutput, b.ID, "", 0)
	f.step()
	got = f.mustCard("c")
	if got.Channel != b.ID || got.Status != model.CardNeedsInput || len(f.questions()) != 1 || f.questions()[0].Answer != "" {
		t.Fatalf("ask %+v questions %+v", got, f.questions())
	}
	f.step()
	if f.mustCard("c").Channel != b.ID || len(f.questions()) != 1 {
		t.Fatal("second step changed the no-output disposition")
	}
}

func TestS08(t *testing.T) {
	// 撞额度，渠道封锁，换渠道从卡分支续做。
	f := newDev(t, model.ModeManual, "file:origin", model.Budget{})
	a := f.channel("ch-a")
	b := f.channel("ch-b")
	reset := time.Date(2026, 12, 1, 3, 4, 5, 0, time.UTC)
	card := f.card("c", a.ID, model.KindDev, model.CardHarvesting, "")
	attempt := f.attempt(card, 0, model.OutcomeQuotaHit, a.ID, "quota reset "+reset.Format(time.RFC3339), 0)
	f.step()
	got := f.mustCard("c")
	if got.Channel != b.ID || got.Status != model.CardReady || got.Base != attempt.Branch {
		t.Fatalf("continue %+v", got)
	}
	blocked, err := f.h.DB.GetChannel(f.ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Health != model.HealthOK || len(blocked.QuotaWindows) != 1 || blocked.QuotaWindows[0].Remaining != 0 || !blocked.QuotaWindows[0].ResetAt.Equal(reset) {
		t.Fatalf("channel %+v", blocked)
	}
	led := quota.New()
	if err := loop.RestoreQuota(f.ctx, led, f.events("")); err != nil {
		t.Fatal(err)
	}
	rem, err := led.Remaining(f.ctx, a.ID)
	if err != nil || !rem.Blocked {
		t.Fatalf("remaining %+v %v", rem, err)
	}
	if len(f.events("quota_block")) != 1 {
		t.Fatalf("events %+v", f.events("quota_block"))
	}
	f.step()
	if f.mustCard("c").Channel != b.ID || len(f.events("quota_block")) != 1 {
		t.Fatal("second step blocked or switched again")
	}
}

func TestS09(t *testing.T) {
	// 认证失败，渠道标记不健康并推送通知，卡换渠道。
	f := newDev(t, model.ModeManual, "lark:ou_s09", model.Budget{})
	dir := f.noticeDir()
	a := f.channel("ch-a")
	b := f.channel("ch-b")
	card := f.card("c", a.ID, model.KindDev, model.CardHarvesting, "")
	attempt := f.attempt(card, 0, model.OutcomeAuthFailed, a.ID, "token rejected", 0)
	f.step()
	got := f.mustCard("c")
	if got.Channel != b.ID || got.Base != attempt.Branch || got.Status != model.CardReady {
		t.Fatalf("switch %+v", got)
	}
	ch, err := f.h.DB.GetChannel(f.ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Health != model.HealthAuthFailed {
		t.Fatalf("health %s", ch.Health)
	}
	body := noticeBody(t, dir, "auth_failed")
	if !strings.Contains(body, f.origin) || !strings.Contains(body, `"destination": "`+f.origin) || !strings.Contains(body, `"adapter": "file"`) {
		t.Fatalf("notice %s", body)
	}
	f.step()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || f.mustCard("c").Channel != b.ID {
		t.Fatal("second step notified or switched again")
	}
}

func TestS10(t *testing.T) {
	// 拒答，不换渠道，派待拍板问题。
	f := newDev(t, model.ModeManual, "file:origin", model.Budget{})
	a := f.channel("ch-a")
	f.channel("ch-b")
	card := f.card("c", a.ID, model.KindDev, model.CardHarvesting, "")
	f.attempt(card, 0, model.OutcomeRefusal, a.ID, "", 0)
	f.step()
	got := f.mustCard("c")
	qs := f.questions()
	if got.Channel != a.ID || got.Status != model.CardNeedsInput || len(qs) != 1 || qs[0].Kind != model.QuestionRefusal || qs[0].Answer != "" {
		t.Fatalf("card %+v questions %+v", got, qs)
	}
	f.step()
	if f.mustCard("c").Channel != a.ID || len(f.questions()) != 1 {
		t.Fatal("second step switched or asked again")
	}
}

func TestS11(t *testing.T) {
	// 结果格式乱，派整理卡。
	f := newDev(t, model.ModeManual, "file:origin", model.Budget{})
	a := f.channel("ch-a")
	card := f.card("c", a.ID, model.KindDev, model.CardHarvesting, "")
	f.attempt(card, 0, model.OutcomeResultUnparsable, a.ID, "", 0)
	f.step()
	norms := f.cardsKind(model.KindNormalize)
	got := f.mustCard("c")
	if len(norms) != 1 || !devHasDep(norms[0], "c") || got.Status != model.CardWaiting || got.WaitReason != "normalize:"+norms[0].ID {
		t.Fatalf("card %+v norms %+v", got, norms)
	}
	f.step()
	if len(f.cardsKind(model.KindNormalize)) != 1 || f.mustCard("c").Status != model.CardWaiting {
		t.Fatal("second step created another normalize card")
	}
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
	f := newDev(t, model.ModeAuto, "lark:ou_test", model.Budget{})
	dir := f.noticeDir()
	ch := f.channel("ch-a")
	f.card("sib", ch.ID, model.KindDev, model.CardReady, "node")
	f.node("other")
	f.card("else", ch.ID, model.KindDev, model.CardReady, "other")
	card := f.card("dev", ch.ID, model.KindDev, model.CardHarvesting, "node")
	f.attempt(card, 0, model.OutcomeTestsFailed, ch.ID, "alpha", 0)
	f.attempt(card, 1, model.OutcomeTestsFailed, ch.ID, "beta", 0)
	f.attempt(card, 2, model.OutcomeTestsFailed, ch.ID, "gamma", 0)
	f.step()
	if f.mustCard("sib").Status != model.CardPaused || f.mustCard("else").Status != model.CardReady {
		t.Fatalf("sib %s else %s", f.mustCard("sib").Status, f.mustCard("else").Status)
	}
	var breaker *model.Question
	for i := range f.questions() {
		q := f.questions()[i]
		if q.Kind == model.QuestionBreaker {
			breaker = &q
		}
	}
	if breaker == nil || breaker.Scope != "node" || breaker.Answer != "" || len(breaker.Deliveries) != 1 || breaker.Deliveries[0].Adapter != "file" || breaker.Deliveries[0].Destination != f.origin {
		t.Fatalf("questions %+v", f.questions())
	}
	body := noticeBody(t, dir, "breaker")
	if !strings.Contains(body, f.origin) || !strings.Contains(body, `"destination": "`+f.origin) || !strings.Contains(body, `"adapter": "file"`) {
		t.Fatalf("notice %s", body)
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	f.step()
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || f.mustCard("else").Status != model.CardReady || f.mustCard("sib").Status != model.CardPaused {
		t.Fatal("second step delivered the breaker again")
	}
}

func TestS16(t *testing.T) {
	// 目标预算触顶，暂停目标。
	f := newDev(t, model.ModeManual, "file:origin", model.Budget{SoftTokens: 1000, HardTokens: 40})
	ch := f.channel("ch-a")
	card := f.card("spike", ch.ID, model.KindSpike, model.CardHarvesting, "")
	f.attempt(card, 0, model.OutcomePassed, ch.ID, "", 40)
	f.step()
	obj, err := f.h.DB.GetObjective(f.ctx, "obj")
	if err != nil {
		t.Fatal(err)
	}
	var budget int
	for _, q := range f.questions() {
		if q.Kind == model.QuestionBudget && q.Scope == "obj" && q.Answer == "" {
			budget++
		}
		if q.Kind == model.QuestionDone {
			t.Fatal("hard stop opened a done question")
		}
	}
	if obj.Status != model.ObjectivePaused || budget != 1 {
		t.Fatalf("status %s questions %+v", obj.Status, f.questions())
	}
	f.step()
	obj, err = f.h.DB.GetObjective(f.ctx, "obj")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Status != model.ObjectivePaused || len(f.questions()) != 1 {
		t.Fatal("second step changed the hard stop")
	}
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

type devFix struct {
	t      *testing.T
	h      *Harness
	ctx    context.Context
	origin string
	notes  string
}

func newDev(t *testing.T, mode model.ObjectiveMode, origin string, budget model.Budget) *devFix {
	t.Helper()
	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	ctx := context.Background()
	project := &model.Project{ID: "p", RepoPath: h.Repo}
	if err := h.DB.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	obj := &model.Objective{
		ID: "obj", Project: project.ID, Title: "e2e",
		Mode: mode, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive,
		Origin: origin, IntegrationBranch: "integration", Budget: budget,
	}
	if err := h.DB.CreateObjective(ctx, obj); err != nil {
		t.Fatal(err)
	}
	node := &model.DesignNode{ID: "node", Objective: obj.ID, Layer: model.LayerDetail, Status: model.NodeFrozen}
	if err := h.DB.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	return &devFix{t: t, h: h, ctx: ctx, origin: origin}
}

func (f *devFix) noticeDir() string {
	f.t.Helper()
	dir := f.t.TempDir()
	f.notes = dir
	f.ctx = loop.WithNotifier(f.ctx, loop.FileNotifier{Dir: dir})
	return dir
}

func (f *devFix) channel(id string) model.Channel {
	f.t.Helper()
	ch := &model.Channel{
		ID: id, Runner: model.RunnerFake, Family: id, Tier: model.TierT2,
		Health: model.HealthOK, Models: []string{"fake"}, Concurrency: 1, SourceGroup: id,
		QuotaWindows: []model.QuotaWindow{{
			Name: "5h", ResetRule: "rolling", LimitTokens: 100,
			ResetAt: time.Now().Add(2 * time.Hour).UTC(), Remaining: 1,
		}},
	}
	if err := f.h.DB.CreateChannel(f.ctx, ch); err != nil {
		f.t.Fatal(err)
	}
	return *ch
}

func (f *devFix) node(id string) {
	f.t.Helper()
	node := &model.DesignNode{ID: id, Objective: "obj", Layer: model.LayerModule, Status: model.NodeFrozen}
	if err := f.h.DB.CreateNode(f.ctx, node); err != nil {
		f.t.Fatal(err)
	}
}

func (f *devFix) card(id, channel string, kind model.CardKind, status model.CardStatus, node string) model.Card {
	f.t.Helper()
	if node == "" {
		node = "node"
	}
	card := &model.Card{
		ID: id, Objective: "obj", Node: node, Kind: kind, Title: id,
		Tier: model.DefaultTier(kind), Status: status, Channel: channel,
		Budget: model.Budget{SoftTokens: 1, HardTokens: 1},
	}
	if err := f.h.DB.CreateCard(f.ctx, card); err != nil {
		f.t.Fatal(err)
	}
	return *card
}

func (f *devFix) attempt(card model.Card, index int, outcome model.Outcome, channel, excerpt string, tokens int64) model.Attempt {
	f.t.Helper()
	attempt := &model.Attempt{
		ID: card.ID + "-" + string(rune('0'+index)), Card: card.ID, Index: index,
		Route:        model.Route{Channel: channel, Model: "fake"},
		Outcome:      outcome,
		ErrorClass:   model.ErrorClass(outcome),
		ErrorExcerpt: excerpt,
		EndedAt:      time.Now().UTC(),
		Branch:       "cardex/obj/c/" + card.ID,
		Usage:        model.Usage{Tokens: tokens},
	}
	if err := f.h.DB.CreateAttempt(f.ctx, attempt); err != nil {
		f.t.Fatal(err)
	}
	return *attempt
}

func (f *devFix) step() {
	f.t.Helper()
	f.t.Setenv("CARDEX_HOME", f.h.Home)
	if err := loop.DevStep(f.ctx, "obj"); err != nil {
		f.t.Fatal(err)
	}
}

func (f *devFix) mustCard(id string) model.Card {
	f.t.Helper()
	card, err := f.h.DB.GetCard(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return card
}

func (f *devFix) questions() []model.Question {
	f.t.Helper()
	questions, err := f.h.DB.ListQuestions(f.ctx, "obj")
	if err != nil {
		f.t.Fatal(err)
	}
	return questions
}

func (f *devFix) events(typ string) []model.Event {
	f.t.Helper()
	events, err := f.h.DB.ListEvents(f.ctx, "")
	if err != nil {
		f.t.Fatal(err)
	}
	if typ == "" {
		return events
	}
	var out []model.Event
	for _, event := range events {
		if event.Type == typ {
			out = append(out, event)
		}
	}
	return out
}

func (f *devFix) cardsKind(kind model.CardKind) []model.Card {
	f.t.Helper()
	cards, err := f.h.DB.ListCards(f.ctx, "obj")
	if err != nil {
		f.t.Fatal(err)
	}
	var out []model.Card
	for _, card := range cards {
		if card.Kind == kind {
			out = append(out, card)
		}
	}
	return out
}

func devHasDep(card model.Card, id string) bool {
	for _, dep := range card.Deps {
		if dep == id {
			return true
		}
	}
	return false
}

func noticeBody(t *testing.T, dir, kind string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), `"kind": "`+kind+`"`) {
			return string(body)
		}
	}
	t.Fatalf("no %s notice in %s", kind, dir)
	return ""
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
