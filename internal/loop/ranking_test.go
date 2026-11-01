package loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	})
}

func absorbRankingText(t *testing.T, db *store.Store, text string) {
	t.Helper()
	card, err := db.GetCard(context.Background(), "rank")
	if err != nil {
		t.Fatal(err)
	}
	handled, err := (Deps{Store: db}).absorbDelegated(context.Background(), card, text)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("ranking absorption did not handle the proposal")
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
