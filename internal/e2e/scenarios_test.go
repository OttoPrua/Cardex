package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"cardex/internal/board"
	"cardex/internal/classify"
	"cardex/internal/harvest"
	"cardex/internal/host"
	"cardex/internal/loop"
	"cardex/internal/merge"
	"cardex/internal/model"
	"cardex/internal/question"
	"cardex/internal/quota"
	"cardex/internal/route"
	"cardex/internal/sched"
	"cardex/internal/store"
	"cardex/internal/workspace"
)

func TestS01(t *testing.T) {
	// 单卡闭环，经过复核和合并。
	t.Skip("R4-D")
}

func TestS02(t *testing.T) {
	// 同一文件、不同函数的两张卡并行，都能合并。
	h := New(t)
	useLaneSched(t, h)
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
	useLaneSched(t, h)
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
	useLaneSched(t, h)
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
	useLaneSched(t, h)
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
	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	ctx := t.Context()
	obj := &model.Objective{Title: "S12", Mode: model.ModeAuto, FreezeMode: model.FreezeSafe, Status: model.ObjectiveActive, Origin: "codex:s12"}
	h.NoError(h.DB.CreateObjective(ctx, obj))
	node := &model.DesignNode{Objective: obj.ID, Layer: model.LayerModule, Status: model.NodeDrafting}
	h.NoError(h.DB.CreateNode(ctx, node))
	done := &model.Card{
		Objective: obj.ID, Node: node.ID, Kind: model.KindDesign, Title: "设计",
		Body: "请设计这个模块\n", Tier: model.TierT3, Status: model.CardDone,
	}
	h.NoError(h.DB.CreateCard(ctx, done))
	e2ePlant(t, h, done.ID, "## 可冻结\n是\n")
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
		done := &model.Card{
			Objective: obj.ID, Node: id, Kind: model.KindDesign, Title: "设计",
			Body: "请设计这个节点\n", Tier: model.TierT3, Status: model.CardDone,
		}
		h.NoError(h.DB.CreateCard(ctx, done))
		e2ePlant(t, h, done.ID, "## 可冻结\n是\n")
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
	done := &model.Card{
		Objective: obj.ID, Node: node.ID, Kind: model.KindDev, Title: "已完成",
		Body: "实现已经完成\n", Tier: model.TierT2, Status: model.CardDone,
	}
	h.NoError(h.DB.CreateCard(ctx, done))
	e2ePlant(t, h, done.ID, "## 设计问题\n接口不一致\n")
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
	h := New(t)
	useLaneSched(t, h)
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
	pos := model.Positioning{
		ID: "pos-" + ch.ID, Model: "model-a", Family: "alpha",
		Tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, Preference: 1,
	}
	if err := h.DB.CreatePositioning(ctx, &pos); err != nil {
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
	useLaneSched(t, h)
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
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.CreateProject(ctx, &model.Project{ID: "p-s22", RepoPath: home}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "o-live", Project: "p-s22", Title: "live work", Status: model.ObjectiveActive,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateObjective(ctx, &model.Objective{
		ID: "o-hist", Project: "p-s22", Title: "old work", Status: model.ObjectiveDone,
		Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCard(ctx, &model.Card{
		ID: "c-live", Objective: "o-live", Title: "live card", Kind: model.KindDev, Status: model.CardReady,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCard(ctx, &model.Card{
		ID: "c-hist-s22", Objective: "o-hist", Title: "imported", Kind: model.KindDev, Status: model.CardDone,
		Legacy: model.Legacy{Status: "done", SourcePath: "v1/c-hist-s22"},
	}); err != nil {
		t.Fatal(err)
	}
	h := board.Handler()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, path := range []string{"/", "/api/work"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s: got %d, want 405", method, path, rec.Code)
			}
		}
	}
	for _, path := range []string{"/", "/api/work"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: got %d body %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "c-hist-s22") {
			t.Fatalf("GET %s contains historical card", path)
		}
		if !strings.Contains(rec.Body.String(), "c-live") {
			t.Fatalf("GET %s missing current card", path)
		}
	}
}

func TestS23(t *testing.T) {
	// 定位任务产生提案，提案生成问题，批准后新卡使用新定位。
	runRankingProductionPath(t)
}

func TestRankingDefaultEntry(t *testing.T) {
	// 缺省每周入队、卡上的 web、来源和三项快照。调度 tick 本身不入队。
	t.Run("no config file", func(t *testing.T) { runRankingProductionPath(t) })
	t.Run("config without schedules", func(t *testing.T) {
		assertRankingEnqueued(t, "{}\n", true, false, nil)
	})
	t.Run("schedules already set", func(t *testing.T) {
		assertRankingEnqueued(t, `{"schedules":[]}`+"\n", false, false, nil)
	})
	t.Run("ranking_sources replaces the list", func(t *testing.T) {
		assertRankingEnqueued(t, `{"ranking_sources":["自定义榜A","自定义榜B"]}`+"\n", true, false, []string{"自定义榜A", "自定义榜B"})
	})
	t.Run("tick does not enqueue", func(t *testing.T) {
		assertRankingEnqueued(t, "", true, true, nil)
	})
}

func runRankingProductionPath(t *testing.T) {
	t.Helper()
	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	useDefaultRunner(t, h)
	stopLater(t, h)
	ctx := context.Background()
	now := time.Now()
	obj, oldCh, oldModel, newModel := seedRankingWorld(t, h, true)
	if err := loop.ApplyDefaultSchedule(ctx, h.DB, h.Home, now); err != nil {
		t.Fatal(err)
	}
	rank, ok := findRankingCard(t, h, obj)
	if !ok {
		t.Fatal("default ranking card missing")
	}
	if !cardRequires(rank, "web") {
		t.Fatalf("ranking requires %+v", rank.Requires)
	}
	if rank.ID != loop.DefaultRankingCardID(now) {
		t.Fatalf("ranking id %s", rank.ID)
	}
	material, err := loop.RankingMaterial(h.Home)
	if err != nil {
		t.Fatal(err)
	}
	assertDefaultRankingPrompt(t, material)
	plantRankingExecutor(t, h, rank.ID, newModel)
	mustTick(t, h, func() bool {
		_, _, ok := endedRankingAttempt(t, h, rank.ID)
		return ok
	}, 30*time.Second)
	attempt, resultPath, ok := endedRankingAttempt(t, h, rank.ID)
	if !ok {
		t.Fatal("fake ranking did not write a result file")
	}
	t.Logf("fake-run wrote result file %s", resultPath)
	rank, err = h.DB.GetCard(ctx, rank.ID)
	if err != nil {
		t.Fatal(err)
	}
	output, _ := os.ReadFile(filepath.Join(h.Home, "attempts", attempt.ID, "output.log"))
	exitCode := 1
	if attempt.Exit != nil {
		exitCode = *attempt.Exit
	}
	res, err := harvest.Harvest(ctx, harvest.Input{
		Attempt:    attempt,
		Card:       rank,
		Exit:       exitCode,
		Output:     output,
		ErrorClass: attempt.ErrorClass,
		Repo:       attempt.Worktree,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if res.ResultText != string(raw) {
		t.Fatalf("harvested text %q want file %q", res.ResultText, raw)
	}
	if !strings.Contains(res.ResultText, newModel) {
		t.Fatalf("harvested text missing %s: %s", newModel, res.ResultText)
	}
	if err := loop.AbsorbHarvestedRanking(ctx, loop.Deps{Store: h.DB}, rank, loop.RankingHarvest{
		AttemptID:  attempt.ID,
		Outcome:    res.Outcome,
		ErrorClass: res.ErrorClass,
		Acceptance: harvestAcceptance(res.Evidence),
		ResultText: res.ResultText,
	}); err != nil {
		t.Fatal(err)
	}
	questions, err := h.DB.ListQuestions(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 1 {
		t.Fatalf("questions %+v", questions)
	}
	t.Logf("one-question %s from harvested ranking result", questions[0].ID)
	asked := questions[0]
	if !strings.Contains(asked.Text, "新出现") || !strings.Contains(asked.Text, newModel) {
		t.Fatalf("question %s", asked.Text)
	}
	if !strings.Contains(asked.Text, "+ "+newModel) {
		t.Fatalf("question missing diff %s", asked.Text)
	}
	if models := positionModels(t, h); len(models) != 1 || models[0] != oldModel {
		t.Fatalf("table changed before approval %v", models)
	}
	if len(asked.Options) != 1 || asked.Options[0] != "批准" {
		t.Fatalf("options %+v", asked.Options)
	}
	if err := h.Sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	repeated, err := h.DB.ListQuestions(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeated) != 1 || repeated[0].ID != asked.ID {
		t.Fatalf("second tick questions %+v", repeated)
	}
	t.Log("second-tick after absorb created no extra proposal or question")
	keptRoute := model.Route{Channel: "keep-ch", Model: "keep-model"}
	if _, err := question.New(h.DB, nil).Answer(ctx, asked.ID, asked.Options[0]); err != nil {
		t.Fatal(err)
	}
	if err := loop.FollowRanking(ctx, h.DB); err != nil {
		t.Fatal(err)
	}
	t.Log("not waiting for absorbAttempt ranking dispatch")
	next := model.Card{
		ID: "c-next", Objective: obj, Kind: model.KindDev, Tier: model.TierT2,
		Title: "next", Status: model.CardReady, Acceptance: []string{"true"},
		Body: fakeScript("", nil, nil),
	}
	if err := h.DB.CreateCard(ctx, &next); err != nil {
		t.Fatal(err)
	}
	mustTick(t, h, func() bool {
		rows, err := h.DB.ListPositioning(ctx)
		if err != nil || len(rows) != 1 || rows[0].Model != newModel {
			return false
		}
		attempts := attemptsOf(t, h, next.ID)
		return len(attempts) == 1 && attempts[0].Route.Model == newModel
	}, 30*time.Second)
	rows, err := h.DB.ListPositioning(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Model != newModel || !tierHas(rows[0].Tiers, model.TierT2) {
		t.Fatalf("approved positioning %+v", rows)
	}
	if got := attemptsOf(t, h, "c-keep"); len(got) != 1 || got[0].Route != keptRoute {
		t.Fatalf("kept route %+v", got)
	}
	rankAttempts := attemptsOf(t, h, rank.ID)
	if len(rankAttempts) != 1 || rankAttempts[0].Route.Channel != oldCh.ID || rankAttempts[0].Route.Model != oldModel {
		t.Fatalf("ranking route %+v", rankAttempts)
	}
	got := attemptsOf(t, h, next.ID)[0]
	if got.Route.Model != newModel || got.Route.Channel == "" {
		t.Fatalf("new route %+v", got.Route)
	}
	t.Logf("new-route model=%s", got.Route.Model)
	t.Logf("previously stored route unchanged channel=%s model=%s", keptRoute.Channel, keptRoute.Model)
}

func assertRankingEnqueued(t *testing.T, config string, wantCard, manual bool, sources []string) {
	t.Helper()
	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	useDefaultRunner(t, h)
	stopLater(t, h)
	ctx := context.Background()
	var obj string
	if len(sources) > 0 {
		obj, _, _, _ = seedRankingWorld(t, h, !manual)
	} else {
		var id string
		_, id = bindProject(t, h, "true")
		obj = id
		if !manual {
			setObjectiveAuto(t, h, obj)
		}
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(h.Home, "config.json"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if manual {
		if err := h.Sched.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if _, ok := findRankingCard(t, h, obj); ok {
			t.Fatal("tick enqueued a ranking card")
		}
	}
	if err := loop.ApplyDefaultSchedule(ctx, h.DB, h.Home, time.Now()); err != nil {
		t.Fatal(err)
	}
	if sources != nil {
		found, ok := findRankingCard(t, h, obj)
		if !ok {
			t.Fatal("ranking card missing")
		}
		if !cardRequires(found, "web") {
			t.Fatalf("requires %+v", found.Requires)
		}
		text, err := loop.RankingMaterial(h.Home)
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range sources {
			if !strings.Contains(text, source) {
				t.Fatalf("prompt missing %s\n%s", source, text)
			}
		}
		if strings.Contains(text, "SWE-bench 官方榜") {
			t.Fatalf("default sources remained\n%s", text)
		}
		assertRankingSnapshots(t, text)
		return
	}
	found, ok := findRankingCard(t, h, obj)
	if wantCard != ok {
		t.Fatalf("ranking card present=%v want %v id %s", ok, wantCard, found.ID)
	}
	if wantCard && !cardRequires(found, "web") {
		t.Fatalf("requires %+v", found.Requires)
	}
	if !wantCard {
		for _, card := range cardsOf(t, h, obj) {
			if card.Kind == model.KindRanking {
				t.Fatalf("default ranking card %s", card.ID)
			}
		}
	}
}

func useDefaultRunner(t *testing.T, h *Harness) {
	t.Helper()
	r := sched.New(h.Home)
	if r.Dispose != nil {
		t.Fatal("sched.New installed Dispose")
	}
	t.Log("production runner Dispose is nil and is not called")
	h.Sched = r
}

func setObjectiveAuto(t *testing.T, h *Harness, id string) {
	t.Helper()
	obj, err := h.DB.GetObjective(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	obj.Mode = model.ModeAuto
	if err := h.DB.UpdateObjective(context.Background(), &obj); err != nil {
		t.Fatal(err)
	}
}

func seedRankingWorld(t *testing.T, h *Harness, auto bool) (obj string, oldCh model.Channel, oldModel, newModel string) {
	t.Helper()
	_, obj = bindProject(t, h, "true")
	if auto {
		setObjectiveAuto(t, h, obj)
	}
	oldModel = "old-selectable"
	newModel = "new-approved"
	oldCh = readyChannel(t, h, "oldfam", oldModel)
	readyChannel(t, h, "newfam", newModel)
	if err := h.DB.CreatePositioning(context.Background(), &model.Positioning{
		ID: "pos-old", Model: oldModel, Family: "oldfam",
		Tiers:        []model.Tier{model.TierT1, model.TierT2, model.TierT3},
		Capabilities: []string{"web"}, Preference: 1,
	}); err != nil {
		t.Fatal(err)
	}
	keep := model.Card{
		ID: "c-keep", Objective: obj, Kind: model.KindDev, Tier: model.TierT2,
		Title: "keep", Status: model.CardDone,
	}
	if err := h.DB.CreateCard(context.Background(), &keep); err != nil {
		t.Fatal(err)
	}
	if err := h.DB.CreateAttempt(context.Background(), &model.Attempt{
		ID: "a-keep", Card: keep.ID, Route: model.Route{Channel: "keep-ch", Model: "keep-model"},
		Outcome: model.OutcomePassed, EndedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	// The dev collaborator already disposed this attempt. A second auto tick
	// must not open another review for it.
	if err := h.DB.CreateEvent(context.Background(), &model.Event{
		Entity: "attempt:a-keep", Type: "dev_disposed",
	}); err != nil {
		t.Fatal(err)
	}
	return obj, oldCh, oldModel, newModel
}

func plantRankingExecutor(t *testing.T, h *Harness, cardID, newModel string) {
	t.Helper()
	proposal := `{"entries":[{"model":"` + newModel + `","family":"newfam","tiers":["T2"],"capabilities":["web"],"preference":1,"理由":"新榜"}]}`
	script, err := json.Marshal(map[string]any{
		"attempts": map[string]any{
			"0": map[string]any{"result": proposal, "exit_code": 0},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.Home, "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cardID+".json"), script, 0o644); err != nil {
		t.Fatal(err)
	}
}

func endedRankingAttempt(t *testing.T, h *Harness, cardID string) (model.Attempt, string, bool) {
	t.Helper()
	for _, attempt := range attemptsOf(t, h, cardID) {
		if attempt.Worktree == "" {
			continue
		}
		path := filepath.Join(attempt.Worktree, ".cardex-result.md")
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if attempt.EndedAt.IsZero() && attempt.Exit == nil {
			continue
		}
		if host.LiveHost(attempt) {
			continue
		}
		return attempt, path, true
	}
	return model.Attempt{}, "", false
}

func harvestAcceptance(evidence string) string {
	const key = "acceptance="
	i := strings.Index(evidence, key)
	if i < 0 {
		return ""
	}
	rest := evidence[i+len(key):]
	if sp := strings.IndexAny(rest, " \t"); sp >= 0 {
		rest = rest[:sp]
	}
	return rest
}

func findRankingCard(t *testing.T, h *Harness, obj string) (model.Card, bool) {
	t.Helper()
	for _, card := range cardsOf(t, h, obj) {
		if card.Kind == model.KindRanking {
			return card, true
		}
	}
	return model.Card{}, false
}

func cardRequires(card model.Card, want string) bool {
	for _, req := range card.Requires {
		if req == want {
			return true
		}
	}
	return false
}

func attemptPrompt(t *testing.T, h *Harness, cardID string) string {
	t.Helper()
	attempts := attemptsOf(t, h, cardID)
	if len(attempts) == 0 {
		t.Fatalf("no attempt for %s", cardID)
	}
	path := attempts[0].PromptPath
	if path == "" {
		path = filepath.Join(h.Home, "attempts", attempts[0].ID, "prompt.txt")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func assertDefaultRankingPrompt(t *testing.T, text string) {
	t.Helper()
	for _, source := range []string{
		"SWE-bench 官方榜", "Aider 多语言编程榜", "LMArena WebDev",
		"LiveBench 编程类", "Terminal-Bench", "Artificial Analysis 编程指数",
	} {
		if !strings.Contains(text, source) {
			t.Fatalf("prompt missing %s\n%s", source, text)
		}
	}
	assertRankingSnapshots(t, text)
}

func assertRankingSnapshots(t *testing.T, text string) {
	t.Helper()
	for _, part := range []string{"## 当前定位表", "## 各渠道可用的模型清单", "## 成绩单摘要", "样本不足"} {
		if !strings.Contains(text, part) {
			t.Fatalf("prompt missing %s\n%s", part, text)
		}
	}
}

func readyChannel(t *testing.T, h *Harness, family, modelName string) model.Channel {
	t.Helper()
	ch, err := h.RegisterFakeChannel(family, model.TierT2, RemainingWindow{
		Ratio: 0.8, Reset: time.Now().Add(24 * time.Hour), Confidence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ch.Models = []string{modelName}
	ch.Concurrency = 4
	ch.Redline = 0
	ch.SourceGroup = family
	if err := h.DB.UpdateChannel(context.Background(), &ch); err != nil {
		t.Fatal(err)
	}
	return ch
}

func positionModels(t *testing.T, h *Harness) []string {
	t.Helper()
	rows, err := h.DB.ListPositioning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Model)
	}
	return out
}

func tierHas(tiers []model.Tier, want model.Tier) bool {
	for _, tier := range tiers {
		if tier == want {
			return true
		}
	}
	return false
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

func useLaneSched(t *testing.T, h *Harness) {
	t.Helper()
	r := sched.New(h.Home)
	r.Dispose = scenarioDispose(h.DB)
	h.Sched = r
}

const scenarioReviewScript = `{
  "attempts": {
    "0": {
      "write_files": [
        {"path": ".cardex-result.md", "content": "## 复核结论\npass\n"}
      ],
      "exit_code": 0
    }
  }
}`

func scenarioDispose(db *store.Store) func(context.Context, *model.Card, harvest.Result) error {
	return func(ctx context.Context, card *model.Card, res harvest.Result) error {
		if card.Kind == model.KindReview {
			subjectID := ""
			if len(card.Deps) > 0 {
				subjectID = card.Deps[0]
			}
			if res.Outcome == model.OutcomePassed && subjectID != "" {
				switch scenarioReviewWord(res.ResultText) {
				case "rework":
					subject, err := db.GetCard(ctx, subjectID)
					if err != nil {
						return err
					}
					subject.Status = model.CardReady
					subject.ReworkCount++
					subject.ReworkNote = strings.TrimSpace(res.ResultText)
					if err := db.UpdateCard(ctx, &subject); err != nil {
						return err
					}
				case "pass":
					if err := merge.New(db).Enqueue(ctx, subjectID); err != nil {
						return err
					}
				default:
					card.Status = model.CardNeedsInput
					return nil
				}
				card.Status = model.CardDone
				return nil
			}
			card.Status = model.CardNeedsInput
			return nil
		}
		if res.Outcome == model.OutcomePassed && scenarioMerges(card.Kind) {
			id := "rv-" + card.ID
			existing, err := db.GetCard(ctx, id)
			switch {
			case err == nil && (existing.Status == model.CardDone || existing.Status == model.CardMerged || existing.Status == model.CardCanceled || existing.Status == model.CardFailed):
				return nil
			case err == nil:
			case errors.Is(err, sql.ErrNoRows):
				review := model.Card{
					ID: id, Objective: card.Objective, Node: card.Node, Kind: model.KindReview,
					Title: "review " + card.Title, Body: scenarioReviewScript, Tier: model.TierT3,
					Status: model.CardReady, Deps: []string{card.ID}, AuthorFamily: card.AuthorFamily,
				}
				if err := db.CreateCard(ctx, &review); err != nil {
					return err
				}
			default:
				return err
			}
			card.Status = model.CardReviewing
			return nil
		}
		if res.Outcome == model.OutcomePassed {
			card.Status = model.CardDone
			return nil
		}
		if res.Outcome == model.OutcomeTestsFailed {
			card.Status = model.CardReady
			card.ReworkCount++
			card.ReworkNote = res.Excerpt
			return nil
		}
		card.Status = model.CardNeedsInput
		if card.WaitReason == "" {
			card.WaitReason = string(res.Outcome)
		}
		return nil
	}
}

func scenarioReviewWord(text string) string {
	for _, field := range strings.Fields(strings.ToLower(text)) {
		field = strings.Trim(field, "`*_.,;:()[]\"'")
		if field == "rework" || field == "pass" {
			return field
		}
	}
	return ""
}

func scenarioMerges(kind model.CardKind) bool {
	switch kind {
	case model.KindDev, model.KindRepair, model.KindRebase, model.KindSkeleton, model.KindBacklog:
		return true
	default:
		return false
	}
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
