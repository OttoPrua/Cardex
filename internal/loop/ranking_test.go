package loop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cardex/internal/harvest"
	"cardex/internal/model"
	"cardex/internal/prompt"
	"cardex/internal/store"
)

func TestRankingMaterialAndDefaultSchedule(t *testing.T) {
	text, err := RankingMaterial("")
	if err != nil {
		t.Fatal(err)
	}
	preface, err := prompt.Preface(model.KindRanking)
	if err != nil {
		t.Fatal(err)
	}
	if text != preface {
		t.Fatal("empty home changed the ranking instructions")
	}
	for _, phrase := range []string{
		"web", ".cardex-result.md", "当前定位表", "各渠道可用的模型清单", "成绩单摘要",
		"SWE-bench 官方榜", "Aider 多语言编程榜", "LMArena WebDev", "LiveBench 编程类",
		"Terminal-Bench", "Artificial Analysis 编程指数",
		"model", "family", "tiers", "capabilities", "preference", "理由", "新出现",
	} {
		if !strings.Contains(text, phrase) {
			t.Fatalf("instructions missing %q", phrase)
		}
	}

	home := t.TempDir()
	body, err := json.Marshal(map[string]any{"ranking_sources": []string{"Only This Board"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	replaced, err := RankingMaterial(home)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(replaced, "Only This Board") {
		t.Fatal("override source missing")
	}
	if strings.Contains(replaced, "SWE-bench 官方榜") || strings.Contains(replaced, "Terminal-Bench") {
		t.Fatalf("default source survived\n%s", replaced)
	}
	for _, phrase := range []string{"web", ".cardex-result.md", "当前定位表", "各渠道可用的模型清单", "成绩单摘要", "model", "family", "tiers", "capabilities", "preference", "理由", "新出现"} {
		if !strings.Contains(replaced, phrase) {
			t.Fatalf("override dropped %q", phrase)
		}
	}

	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "config.json"), []byte(`{"loop":{"rework_limit":2}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, err := DefaultSchedule(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Kind != model.KindRanking || tasks[0].Every != 168*time.Hour || !hasWeb(tasks[0].Requires) {
		t.Fatalf("default schedule %+v", tasks)
	}
	withSched := t.TempDir()
	if err := os.WriteFile(filepath.Join(withSched, "config.json"), []byte(`{"schedules":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks, err = DefaultSchedule(withSched)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("user schedules %+v %v", tasks, err)
	}
}

func TestRankingPromptSnapshots(t *testing.T) {
	defaults := []string{
		"SWE-bench 官方榜", "Aider 多语言编程榜", "LMArena WebDev",
		"LiveBench 编程类", "Terminal-Bench", "Artificial Analysis 编程指数",
	}
	cases := []struct {
		name      string
		sources   []string
		schedules bool
	}{
		{name: "defaults"},
		{name: "override", sources: []string{"Only Alpha Board", "Only Beta Board"}},
		{name: "schedules", schedules: true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			token := 4200 + i
			posModel := "pos-" + tc.name
			chModel := "listed-" + tc.name
			scoreModel := "score-" + tc.name
			plantRankingHome(t, home, posModel, "fam-"+tc.name, "ch-"+tc.name, chModel, scoreModel, int64(token), tc.sources, tc.schedules)
			text, err := RankingMaterial(home)
			if err != nil {
				t.Fatal(err)
			}
			for _, piece := range []string{
				"## 当前定位表", "## 各渠道可用的模型清单", "## 成绩单摘要",
				"model=" + posModel, "family=fam-" + tc.name, "tiers=T2", "capabilities=web", "preference=3",
				"channel=ch-" + tc.name, "models=" + chModel,
				"model=" + scoreModel, "kind=dev", "tier=T2",
				"复核通过率=0", "合入率=1", "平均返工=0",
				"每张合入token=" + strconv.Itoa(token), "样本=1", "样本不足",
			} {
				if !strings.Contains(text, piece) {
					t.Fatalf("prompt missing %q\n%s", piece, text)
				}
			}
			if tc.sources != nil {
				t.Log("source override plus the three snapshots")
			}
			if tc.sources == nil {
				for _, source := range defaults {
					if !strings.Contains(text, source) {
						t.Fatalf("default source %q missing", source)
					}
				}
			} else {
				for _, source := range tc.sources {
					if !strings.Contains(text, "- "+source) {
						t.Fatalf("override %q missing\n%s", source, text)
					}
				}
				for _, source := range defaults {
					if strings.Contains(text, source) {
						t.Fatalf("default source %q survived", source)
					}
				}
			}
			tasks, err := DefaultSchedule(home)
			if err != nil {
				t.Fatal(err)
			}
			if tc.schedules {
				if len(tasks) != 0 {
					t.Fatalf("set schedules still defaulted %+v", tasks)
				}
				return
			}
			if len(tasks) != 1 || tasks[0].Kind != model.KindRanking || tasks[0].Every != 168*time.Hour || !hasWeb(tasks[0].Requires) {
				t.Fatalf("default schedule %+v", tasks)
			}
		})
	}
}

func plantRankingHome(t *testing.T, home, posModel, family, channelID, channelModel, scoreModel string, tokens int64, sources []string, schedules bool) {
	t.Helper()
	ctx := context.Background()
	top := map[string]any{}
	if sources != nil {
		top["ranking_sources"] = sources
	}
	if schedules {
		top["schedules"] = []any{}
	}
	if len(top) > 0 {
		body, err := json.Marshal(top)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "config.json"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreatePositioning(ctx, &model.Positioning{
		ID: "pos-" + posModel, Model: posModel, Family: family,
		Tiers: []model.Tier{model.TierT2}, Capabilities: []string{"web"}, Preference: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateChannel(ctx, &model.Channel{
		ID: channelID, Runner: model.RunnerFake, Family: family, Tier: model.TierT2,
		Health: model.HealthOK, Models: []string{channelModel}, Concurrency: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCard(ctx, &model.Card{
		ID: "score-" + scoreModel, Kind: model.KindDev, Tier: model.TierT2,
		Title: scoreModel, Status: model.CardMerged,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "att-" + scoreModel, Card: "score-" + scoreModel,
		Route: model.Route{Channel: channelID, Model: scoreModel},
		Usage: model.Usage{Tokens: tokens},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRankingAbsorbLooseAndApprove(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		_, db := rankingHome(t)
		seedRanking(t, db, "old-model")
		before := mustAttempt(t, db, "a-keep")
		text := "前言不是 JSON\n" +
			`{"entries":[{"model":"kept-model","family":"fam","tiers":["T2","nope"],"capabilities":["web"],"preference":3,"理由":"因为榜"},{"model":"partial"}]}` +
			"\n尾部 { 坏了\n"
		if strings.Contains(text, "##") {
			t.Fatal("proposal fixture has a known section title")
		}
		absorbRankingText(t, db, text)
		questions := mustQuestions(t, db)
		if len(questions) != 1 || !strings.Contains(questions[0].Text, "待复核") {
			t.Fatalf("question %+v", questions)
		}
		if !strings.Contains(questions[0].Text, "新出现") || !strings.Contains(questions[0].Text, "因为榜") || !strings.Contains(questions[0].Text, "+ kept-model") {
			t.Fatalf("question text %s", questions[0].Text)
		}
		saved := mustProposals(t)
		if len(saved) != 1 {
			t.Fatalf("proposals %d", len(saved))
		}
		kept := proposalEntry(t, saved[0].Entries, "kept-model")
		if kept.Family != "fam" || kept.Preference != 3 || !hasTier(kept.Tiers, "T2") || !hasTier(kept.Tiers, "nope") || !hasWeb(kept.Capabilities) {
			t.Fatalf("kept entry %+v", kept)
		}
		partial := proposalEntry(t, saved[0].Entries, "partial")
		if partial.Family != "" || partial.Preference != 0 {
			t.Fatalf("partial entry %+v", partial)
		}
		if models := positioningModels(t, db); len(models) != 1 || models[0] != "old-model" {
			t.Fatalf("table changed %v", models)
		}
		if got := mustAttempt(t, db, "a-keep"); got.Route != before.Route {
			t.Fatalf("route %v want %v", got.Route, before.Route)
		}
	})

	t.Run("garbage", func(t *testing.T) {
		_, db := rankingHome(t)
		seedRanking(t, db, "old-model")
		absorbRankingText(t, db, "{")
		questions := mustQuestions(t, db)
		if len(questions) != 1 || !strings.Contains(questions[0].Text, "待复核") {
			t.Fatalf("question %+v", questions)
		}
		if _, err := os.Stat(filepath.Join(os.Getenv("CARDEX_HOME"), "positioning", "proposals")); !os.IsNotExist(err) {
			t.Fatalf("garbage still proposed: %v", err)
		}
		if models := positioningModels(t, db); len(models) != 1 || models[0] != "old-model" {
			t.Fatalf("table changed %v", models)
		}
	})

	t.Run("untitled", func(t *testing.T) {
		_, db := rankingHome(t)
		seedRanking(t, db, "old-model")
		before := mustAttempt(t, db, "a-keep")
		rankBefore := mustAttempt(t, db, "a-rank")
		text := "{\"model\":\"plain-model\",\"family\":\"plainfam\",\"tiers\":[\"T2\"],\"capabilities\":[\"web\"],\"preference\":2,\"理由\":\"无标题\"}"
		if strings.Contains(text, "##") {
			t.Fatal("proposal fixture has a known section title")
		}
		absorbRankingText(t, db, text)
		questions := mustQuestions(t, db)
		if len(questions) != 1 || strings.Contains(questions[0].Text, "待复核") {
			t.Fatalf("question %+v", questions)
		}
		if !strings.Contains(questions[0].Text, "新出现") || !strings.Contains(questions[0].Text, "plain-model") || !strings.Contains(questions[0].Text, "+ plain-model") {
			t.Fatalf("question text %s", questions[0].Text)
		}
		saved := mustProposals(t)
		if len(saved) != 1 || saved[0].Status == "approved" {
			t.Fatalf("published early %+v", saved)
		}
		if models := positioningModels(t, db); len(models) != 1 || models[0] != "old-model" {
			t.Fatalf("table changed %v", models)
		}
		if got := mustAttempt(t, db, "a-keep"); got.Route != before.Route {
			t.Fatalf("keep route %v want %v", got.Route, before.Route)
		}
		if got := mustAttempt(t, db, "a-rank"); got.Route != rankBefore.Route {
			t.Fatalf("ranking route %v want %v", got.Route, rankBefore.Route)
		}
	})

	t.Run("approve", func(t *testing.T) {
		_, db := rankingHome(t)
		seedRanking(t, db, "old-model")
		before := mustAttempt(t, db, "a-keep")
		rankBefore := mustAttempt(t, db, "a-rank")
		text := `{"entries":[{"model":"new-model","family":"newfam","tiers":["T2"],"capabilities":["web"],"preference":1,"理由":"榜单"}]}`
		if strings.Contains(text, "##") {
			t.Fatal("proposal fixture has a known section title")
		}
		absorbRankingText(t, db, text)
		questions := mustQuestions(t, db)
		if len(questions) != 1 {
			t.Fatalf("questions %+v", questions)
		}
		q := questions[0]
		if !strings.Contains(q.Text, "+ new-model") || !strings.Contains(q.Text, "新出现") || !strings.Contains(q.Text, "new-model") || strings.Contains(q.Text, "待复核") {
			t.Fatalf("question text %s", q.Text)
		}
		if models := positioningModels(t, db); len(models) != 1 || models[0] != "old-model" {
			t.Fatalf("published early %v", models)
		}
		if len(q.Options) != 1 {
			t.Fatalf("options %+v", q.Options)
		}
		if _, err := (Deps{Store: db}).questions().Answer(context.Background(), q.ID, q.Options[0]); err != nil {
			t.Fatal(err)
		}
		if err := FollowRanking(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		rows, err := db.ListPositioning(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Model != "new-model" || rows[0].Family != "newfam" || rows[0].Preference != 1 || !hasTier(rows[0].Tiers, model.TierT2) || !hasWeb(rows[0].Capabilities) {
			t.Fatalf("approved %+v", rows)
		}
		if got := mustAttempt(t, db, "a-keep"); got.Route != before.Route {
			t.Fatalf("keep route %v want %v", got.Route, before.Route)
		}
		if got := mustAttempt(t, db, "a-rank"); got.Route != rankBefore.Route {
			t.Fatalf("ranking route %v want %v", got.Route, rankBefore.Route)
		}
		if q.Options[0] != "批准" {
			t.Fatalf("options %+v", q.Options)
		}
		t.Log("「批准」 without rewriting a stored route")
	})
}

func TestRankingScheduleRequires(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	explicitWeb := ScheduleTask{Name: "configured", Kind: model.KindRanking, Every: week, Requires: []string{"web"}}
	explicitFull := ScheduleTask{Name: "configured", Kind: model.KindRanking, Every: week, Requires: []string{"web", "tools"}}
	cases := []struct {
		name     string
		config   string
		tasks    []ScheduleTask
		wantCard bool
		wantReq  []string
	}{
		{name: "default weekly", wantCard: true, wantReq: []string{"web"}},
		{name: "missing schedules key", config: "{}\n", wantCard: true, wantReq: []string{"web"}},
		{name: "explicit web", tasks: []ScheduleTask{explicitWeb}, wantCard: true, wantReq: []string{"web"}},
		{name: "explicit web and tools", tasks: []ScheduleTask{explicitFull}, wantCard: true, wantReq: []string{"web", "tools"}},
		{name: "schedules key suppresses default", config: `{"schedules":[]}` + "\n", wantCard: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, db := rankingHome(t)
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(tc.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			obj := &model.Objective{ID: "obj", Title: "ranking", Mode: model.ModeManual, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive}
			if err := db.CreateObjective(ctx, obj); err != nil {
				t.Fatal(err)
			}
			tasks := tc.tasks
			if tasks == nil {
				var err error
				tasks, err = DefaultSchedule(home)
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, task := range tasks {
				built := ScheduleCard(obj.ID, "preview-"+task.Name, task)
				if !sameRequires(built.Requires, task.Requires) {
					t.Fatalf("ScheduleCard requires %+v want %+v", built.Requires, task.Requires)
				}
			}
			var err error
			if tc.tasks != nil {
				err = EnqueueRankingSchedule(ctx, db, obj.ID, tc.tasks, now)
			} else {
				err = ApplyDefaultSchedule(ctx, db, home, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			cards, err := db.ListCards(ctx, obj.ID)
			if err != nil {
				t.Fatal(err)
			}
			var found []model.Card
			for _, card := range cards {
				if card.Kind == model.KindRanking {
					found = append(found, card)
				}
			}
			if tc.wantCard != (len(found) == 1) {
				t.Fatalf("ranking cards %+v want one=%v", found, tc.wantCard)
			}
			if !tc.wantCard {
				return
			}
			if !sameRequires(found[0].Requires, tc.wantReq) {
				t.Fatalf("stored requires %+v want %+v", found[0].Requires, tc.wantReq)
			}
			if tc.tasks == nil {
				t.Log("missing schedules key")
			}
			if hasWeb(found[0].Requires) && tc.tasks != nil {
				t.Logf("explicit web requires kept: %v", found[0].Requires)
			}
		})
	}
}

func TestRankingAbsorbHonorsHarvest(t *testing.T) {
	text := `{"entries":[{"model":"new-model","family":"newfam","tiers":["T2"],"capabilities":["web"],"preference":1,"理由":"榜单"}]}`
	if strings.Contains(text, "##") {
		t.Fatal("proposal fixture has a known section title")
	}
	cases := []struct {
		name       string
		outcome    model.Outcome
		class      model.ErrorClass
		acceptance string
		status     model.CardStatus
		wantAbsorb bool
	}{
		{name: "violation", outcome: model.OutcomeViolation, class: model.ErrorViolation, status: model.CardReady},
		{name: "violation class", outcome: model.OutcomePassed, class: model.ErrorViolation, acceptance: "passed", status: model.CardReady},
		{name: "budget_limited", outcome: model.OutcomeBudgetLimited, class: model.ErrorBudgetLimited, status: model.CardReady},
		{name: "tests_failed", outcome: model.OutcomeTestsFailed, class: model.ErrorTestsFailed, acceptance: "failed", status: model.CardReady},
		{name: "acceptance failed", outcome: model.OutcomePassed, class: model.ErrorPassed, acceptance: "failed", status: model.CardReady},
		{name: "stopped failed", outcome: model.OutcomePassed, class: model.ErrorPassed, acceptance: "passed", status: model.CardFailed},
		{name: "stopped canceled", outcome: model.OutcomePassed, class: model.ErrorPassed, acceptance: "passed", status: model.CardCanceled},
		{name: "result_unparsable", outcome: model.OutcomeResultUnparsable, class: model.ErrorResultUnparsable, acceptance: "passed", status: model.CardReady, wantAbsorb: true},
		{name: "passed", outcome: model.OutcomePassed, class: model.ErrorPassed, acceptance: "passed", status: model.CardReady, wantAbsorb: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, db := rankingHome(t)
			seedRanking(t, db, "old-model")
			ctx := context.Background()
			card, err := db.GetCard(ctx, "rank")
			if err != nil {
				t.Fatal(err)
			}
			card.Status = tc.status
			if err := db.UpdateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			before := mustAttempt(t, db, "a-rank")
			harvest := RankingHarvest{
				AttemptID:  before.ID,
				Outcome:    tc.outcome,
				ErrorClass: tc.class,
				Acceptance: tc.acceptance,
				ResultText: text,
			}
			if err := AbsorbHarvestedRanking(ctx, Deps{Store: db}, card, harvest); err != nil {
				t.Fatal(err)
			}
			if proposalCount(t) != 0 && !tc.wantAbsorb {
				t.Fatalf("proposal created for %s", tc.name)
			}
			gotCard, err := db.GetCard(ctx, "rank")
			if err != nil {
				t.Fatal(err)
			}
			gotAttempt := mustAttempt(t, db, "a-rank")
			if !tc.wantAbsorb {
				if proposalCount(t) != 0 {
					t.Fatalf("proposals %d", proposalCount(t))
				}
				if len(mustQuestions(t, db)) != 0 {
					t.Fatalf("questions %+v", mustQuestions(t, db))
				}
				if gotCard.Status != tc.status {
					t.Fatalf("status %s", gotCard.Status)
				}
				if gotAttempt.Outcome != "" || gotAttempt.Route != before.Route {
					t.Fatalf("attempt %+v", gotAttempt)
				}
				return
			}
			if proposalCount(t) != 1 {
				t.Fatalf("proposals %d", proposalCount(t))
			}
			if gotCard.Status != model.CardDone {
				t.Fatalf("status %s", gotCard.Status)
			}
			if gotAttempt.Outcome != tc.outcome {
				t.Fatalf("outcome %s want %s", gotAttempt.Outcome, tc.outcome)
			}
			if gotAttempt.Route != before.Route {
				t.Fatalf("route %v", gotAttempt.Route)
			}
			questions := mustQuestions(t, db)
			if len(questions) != 1 || !strings.Contains(questions[0].Text, "new-model") {
				t.Fatalf("question %+v", questions)
			}
			if err := AbsorbHarvestedRanking(ctx, Deps{Store: db}, gotCard, harvest); err != nil {
				t.Fatal(err)
			}
			if proposalCount(t) != 1 {
				t.Fatalf("second tick proposals %d", proposalCount(t))
			}
			if len(mustQuestions(t, db)) != 1 {
				t.Fatalf("second tick questions %d", len(mustQuestions(t, db)))
			}
			again := mustAttempt(t, db, "a-rank")
			if again.Outcome != tc.outcome || again.Route != before.Route {
				t.Fatalf("second tick attempt %+v", again)
			}
		})
	}
}

func TestRankingHarvestFormatStillAbsorbs(t *testing.T) {
	proposal := `{"entries":[{"model":"new-model","family":"newfam","tiers":["T2"],"capabilities":["web"],"preference":1,"理由":"榜单"}]}`
	prose := "说明文字还不是标题\n" + proposal + "\n以上是提案。\n"
	headed := "## 结论\n提案\n" + proposal + "\n"
	cases := []struct {
		name       string
		body       string
		class      model.ErrorClass
		acceptance []string
		status     model.CardStatus
		want       string
	}{
		{name: "untitled json", body: proposal, want: "absorb"},
		{name: "prose json", body: prose, want: "absorb"},
		{name: "passed", body: headed, want: "absorb"},
		{name: "garbage", body: "this is not a proposal\n", want: "review"},
		{name: "violation", body: headed, class: model.ErrorViolation, want: "skip"},
		{name: "budget_limited", body: headed, class: model.ErrorBudgetLimited, want: "skip"},
		{name: "acceptance failure", body: headed, acceptance: []string{"exit 1"}, want: "skip"},
		{name: "stopped failed", body: headed, status: model.CardFailed, want: "skip"},
		{name: "stopped canceled", body: headed, status: model.CardCanceled, want: "skip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name != "passed" && tc.want != "skip" && strings.Contains(tc.body, "##") {
				t.Fatal("result fixture has a known section title")
			}
			_, db := rankingHome(t)
			seedRanking(t, db, "old-model")
			ctx := context.Background()
			card, err := db.GetCard(ctx, "rank")
			if err != nil {
				t.Fatal(err)
			}
			before := mustAttempt(t, db, "a-rank")
			keepBefore := mustAttempt(t, db, "a-keep")
			work, head := rankingGitWorktree(t)
			if err := os.WriteFile(filepath.Join(work, ".cardex-result.md"), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			harvestCard := card
			harvestCard.Acceptance = tc.acceptance
			res, err := harvest.Harvest(ctx, harvest.Input{
				Attempt:    model.Attempt{ID: before.ID, Card: card.ID, Worktree: work, BaseCommit: head},
				Card:       harvestCard,
				Exit:       0,
				ErrorClass: tc.class,
				Repo:       work,
			})
			if err != nil {
				t.Fatal(err)
			}
			wantHarvest := model.OutcomeResultUnparsable
			switch {
			case tc.class == model.ErrorViolation:
				wantHarvest = model.OutcomeViolation
			case tc.class == model.ErrorBudgetLimited:
				wantHarvest = model.OutcomeBudgetLimited
			case len(tc.acceptance) > 0:
				wantHarvest = model.OutcomeTestsFailed
			case strings.Contains(tc.body, "## 结论"):
				wantHarvest = model.OutcomePassed
			}
			if res.Outcome != wantHarvest {
				t.Fatalf("harvest outcome %s want %s evidence %s", res.Outcome, wantHarvest, res.Evidence)
			}
			if !strings.Contains(tc.body, "##") && res.Outcome == model.OutcomePassed {
				t.Fatal("outcome was overwritten to passed")
			}
			if res.ResultText != tc.body {
				t.Fatalf("result text %q", res.ResultText)
			}
			acceptance := ""
			if strings.Contains(res.Evidence, "acceptance=failed") {
				acceptance = "failed"
			}
			got := RankingHarvest{
				AttemptID:  before.ID,
				Outcome:    res.Outcome,
				ErrorClass: res.ErrorClass,
				Acceptance: acceptance,
				ResultText: res.ResultText,
			}
			if tc.status != "" {
				card.Status = tc.status
				if err := db.UpdateCard(ctx, &card); err != nil {
					t.Fatal(err)
				}
			}
			if err := AbsorbHarvestedRanking(ctx, Deps{Store: db}, card, got); err != nil {
				t.Fatal(err)
			}
			gotCard, err := db.GetCard(ctx, "rank")
			if err != nil {
				t.Fatal(err)
			}
			gotAttempt := mustAttempt(t, db, "a-rank")
			keep := mustAttempt(t, db, "a-keep")
			if gotAttempt.Route != before.Route || keep.Route != keepBefore.Route {
				t.Fatalf("route changed attempt %+v keep %+v", gotAttempt.Route, keep.Route)
			}
			switch tc.want {
			case "skip":
				if proposalCount(t) != 0 || len(mustQuestions(t, db)) != 0 {
					t.Fatalf("absorbed proposals %d questions %+v", proposalCount(t), mustQuestions(t, db))
				}
				wantStatus := model.CardReady
				if tc.status != "" {
					wantStatus = tc.status
				}
				if gotCard.Status != wantStatus {
					t.Fatalf("status %s", gotCard.Status)
				}
				if gotAttempt.Outcome != "" {
					t.Fatalf("outcome %s", gotAttempt.Outcome)
				}
				if tc.status != "" {
					t.Log("stopped card not absorbed")
				} else {
					t.Logf("%s not absorbed", tc.name)
				}
			case "review":
				if proposalCount(t) != 0 {
					t.Fatalf("proposals %d", proposalCount(t))
				}
				if gotCard.Status != model.CardNeedsInput || gotCard.WaitReason != "待复核" {
					t.Fatalf("card %+v", gotCard)
				}
				questions := mustQuestions(t, db)
				if len(questions) != 1 || !strings.Contains(questions[0].Text, "待复核") {
					t.Fatalf("questions %+v", questions)
				}
				if gotAttempt.Outcome != res.Outcome {
					t.Fatalf("outcome %s want %s", gotAttempt.Outcome, res.Outcome)
				}
			case "absorb":
				if res.Outcome != model.OutcomeResultUnparsable && res.Outcome != model.OutcomePassed {
					t.Fatalf("outcome %s", res.Outcome)
				}
				if proposalCount(t) != 1 {
					t.Fatalf("proposals %d", proposalCount(t))
				}
				saved := mustProposals(t)
				if len(saved) != 1 || !strings.Contains(saved[0].Diff, "new-model") {
					t.Fatalf("proposal %+v", saved)
				}
				questions := mustQuestions(t, db)
				if len(questions) != 1 || !strings.Contains(questions[0].Text, "+ new-model") || !strings.Contains(questions[0].Text, "新出现") || !strings.Contains(questions[0].Text, "new-model") {
					t.Fatalf("question %+v", questions)
				}
				if gotCard.Status != model.CardDone {
					t.Fatalf("status %s", gotCard.Status)
				}
				if gotAttempt.Outcome != res.Outcome {
					t.Fatalf("outcome %s want %s", gotAttempt.Outcome, res.Outcome)
				}
				if tc.name == "untitled json" && res.Outcome == model.OutcomeResultUnparsable {
					t.Log("untitled result_unparsable JSON absorbed")
				}
			default:
				t.Fatalf("want %s", tc.want)
			}
			if err := AbsorbHarvestedRanking(ctx, Deps{Store: db}, gotCard, got); err != nil {
				t.Fatal(err)
			}
			if (tc.want == "absorb" && proposalCount(t) != 1) || (tc.want != "absorb" && proposalCount(t) != 0) {
				t.Fatalf("second absorb proposals %d", proposalCount(t))
			}
			wantQuestions := 0
			if tc.want != "skip" {
				wantQuestions = 1
			}
			if len(mustQuestions(t, db)) != wantQuestions {
				t.Fatalf("second absorb questions %d", len(mustQuestions(t, db)))
			}
			again := mustAttempt(t, db, "a-rank")
			if again.Outcome != gotAttempt.Outcome || again.Route != before.Route {
				t.Fatalf("second absorb attempt %+v", again)
			}
			fresh, err := db.GetCard(ctx, "rank")
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Status != gotCard.Status {
				t.Fatalf("second absorb status %s", fresh.Status)
			}
			t.Log("second absorb did not add a proposal or question")
		})
	}
}

func rankingGitWorktree(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wt := filepath.Join(root, "wt")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git(repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("ranking\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "README")
	git(repo, "-c", "user.name=cardex", "-c", "user.email=cardex@cardex.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "init")
	git(repo, "worktree", "add", "--detach", wt, "HEAD")
	head := strings.TrimSpace(git(wt, "rev-parse", "HEAD"))
	return wt, head
}

func TestRankingOpenDoesNotMaintain(t *testing.T) {
	home, db := rankingHome(t)
	ctx := context.Background()
	obj := &model.Objective{ID: "obj", Title: "ranking", Mode: model.ModeManual, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive}
	if err := db.CreateObjective(ctx, obj); err != nil {
		t.Fatal(err)
	}
	if err := db.CreatePositioning(ctx, &model.Positioning{
		ID: "pos-old", Model: "old-model", Family: "oldfam", Tiers: []model.Tier{model.TierT2}, Preference: 1,
	}); err != nil {
		t.Fatal(err)
	}
	card := &model.Card{
		ID: "rank-open", Objective: obj.ID, Kind: model.KindRanking, Tier: model.TierT2,
		Title: "rank", Status: model.CardReady,
	}
	if err := db.CreateCard(ctx, card); err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	work := t.TempDir()
	result := `{"entries":[{"model":"new-model","family":"newfam","tiers":["T2"],"capabilities":["web"],"preference":1,"理由":"榜单"}]}`
	if err := os.WriteFile(filepath.Join(work, ".cardex-result.md"), []byte(result), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "a-open", Card: card.ID, Route: model.Route{Channel: "ch-old", Model: "old-model"},
		Worktree: work, EndedAt: time.Now().UTC(), Exit: &exitCode,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateQuestion(ctx, &model.Question{
		ID: "q-open", Objective: obj.ID, Scope: card.ID, ScopeKind: "card",
		Kind: model.QuestionCustom, Text: "定位提案 prop-open\n", Options: []string{"批准"}, Answer: "批准",
	}); err != nil {
		t.Fatal(err)
	}
	propDir := filepath.Join(home, "positioning", "proposals")
	if err := os.MkdirAll(propDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"id": "prop-open", "status": "proposed",
		"entries": []map[string]any{{"model": "new-model", "family": "newfam", "tiers": []string{"T2"}, "capabilities": []string{"web"}, "preference": 1}},
		"diff":    "+ new-model\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(propDir, "prop-open.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	beforeCards, err := db.ListCards(ctx, obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	if _, err := opened.ListObjectives(ctx, ""); err != nil {
		t.Fatal(err)
	}
	cards, err := opened.ListCards(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened.ListQuestions(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.ListAttempts(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.ListPositioning(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.GetCard(ctx, card.ID); err != nil {
		t.Fatal(err)
	}
	if len(cards) != len(beforeCards) {
		t.Fatalf("cards %d want %d", len(cards), len(beforeCards))
	}
	for _, item := range cards {
		if item.ID == DefaultRankingCardID(time.Now()) {
			t.Fatalf("open enqueued %s", item.ID)
		}
		if item.ID == card.ID && item.Status != model.CardReady {
			t.Fatalf("open changed card %+v", item)
		}
	}
	attempt, err := opened.GetAttempt(ctx, "a-open")
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Outcome != "" {
		t.Fatalf("open stored outcome %s", attempt.Outcome)
	}
	questions, err := opened.ListQuestions(ctx, obj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 1 || questions[0].Answer != "批准" {
		t.Fatalf("questions %+v", questions)
	}
	raw, err := os.ReadFile(filepath.Join(propDir, "prop-open.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "proposed" {
		t.Fatalf("proposal status %s", saved.Status)
	}
	if models := positioningModels(t, opened); len(models) != 1 || models[0] != "old-model" {
		t.Fatalf("positioning %v", models)
	}
}

func absorbRankingText(t *testing.T, db *store.Store, text string) {
	t.Helper()
	card, err := db.GetCard(context.Background(), "rank")
	if err != nil {
		t.Fatal(err)
	}
	if err := AbsorbRanking(context.Background(), Deps{Store: db}, card, text); err != nil {
		t.Fatal(err)
	}
}

func rankingHome(t *testing.T) (string, *store.Store) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return home, db
}

func seedRanking(t *testing.T, db *store.Store, current string) {
	t.Helper()
	ctx := context.Background()
	obj := &model.Objective{ID: "obj", Title: "ranking", Mode: model.ModeManual, FreezeMode: model.FreezeFast, Status: model.ObjectiveActive}
	if err := db.CreateObjective(ctx, obj); err != nil {
		t.Fatal(err)
	}
	rank := &model.Card{ID: "rank", Objective: obj.ID, Kind: model.KindRanking, Tier: model.TierT2, Title: "rank", Status: model.CardReady, Requires: []string{"web"}}
	keep := &model.Card{ID: "keep", Objective: obj.ID, Kind: model.KindDev, Tier: model.TierT2, Title: "keep", Status: model.CardDone}
	if err := db.CreateCard(ctx, rank); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCard(ctx, keep); err != nil {
		t.Fatal(err)
	}
	ended := time.Now().UTC()
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "a-rank", Card: rank.ID, Route: model.Route{Channel: "ch-old", Model: "old-route"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "a-keep", Card: keep.ID, Route: model.Route{Channel: "keep-ch", Model: "keep-model"},
		Outcome: model.OutcomePassed, EndedAt: ended,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreatePositioning(ctx, &model.Positioning{
		ID: "pos-old", Model: current, Family: "oldfam", Tiers: []model.Tier{model.TierT2}, Preference: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func mustQuestions(t *testing.T, db *store.Store) []model.Question {
	t.Helper()
	questions, err := db.ListQuestions(context.Background(), "obj")
	if err != nil {
		t.Fatal(err)
	}
	return questions
}

func mustAttempt(t *testing.T, db *store.Store, id string) model.Attempt {
	t.Helper()
	attempt, err := db.GetAttempt(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func positioningModels(t *testing.T, db *store.Store) []string {
	t.Helper()
	rows, err := db.ListPositioning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Model)
	}
	return out
}

type savedProposal struct {
	ID      string              `json:"id"`
	Entries []model.Positioning `json:"entries"`
	Diff    string              `json:"diff"`
	Status  string              `json:"status"`
}

func mustProposals(t *testing.T) []savedProposal {
	t.Helper()
	dir := filepath.Join(os.Getenv("CARDEX_HOME"), "positioning", "proposals")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []savedProposal
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var saved savedProposal
		if err := json.Unmarshal(body, &saved); err != nil {
			t.Fatal(err)
		}
		out = append(out, saved)
	}
	return out
}

func proposalEntry(t *testing.T, entries []model.Positioning, name string) model.Positioning {
	t.Helper()
	for _, entry := range entries {
		if entry.Model == name {
			return entry
		}
	}
	t.Fatalf("missing proposal entry %s in %+v", name, entries)
	return model.Positioning{}
}

func hasTier(tiers []model.Tier, want model.Tier) bool {
	for _, tier := range tiers {
		if tier == want {
			return true
		}
	}
	return false
}

func hasWeb(requires []string) bool {
	for _, item := range requires {
		if item == "web" {
			return true
		}
	}
	return false
}

func sameRequires(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func proposalCount(t *testing.T) int {
	t.Helper()
	dir := filepath.Join(os.Getenv("CARDEX_HOME"), "positioning", "proposals")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			n++
		}
	}
	return n
}
