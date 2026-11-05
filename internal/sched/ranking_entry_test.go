package sched

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cardex/internal/loop"
	"cardex/internal/model"
	"cardex/internal/positioning"
	"cardex/internal/question"
	"cardex/internal/store"
)

func TestRankingScheduleTick(t *testing.T) {
	explicit := `{"schedules":[{"name":"weekly","every":"168h","card_template":{"kind":"ranking","title":"Rank","requires":["web","goal_mode"]}}]}`
	rows := []struct {
		name     string
		config   *string
		want     bool
		requires []string
		explicit bool
	}{
		{name: "no config.json", want: true, requires: []string{"web"}},
		{name: "no schedules key", config: strPtr("{}\n"), want: true, requires: []string{"web"}},
		{name: "empty schedules", config: strPtr("{\"schedules\":[]}\n"), want: false},
		{name: "explicit requires", config: strPtr(explicit), want: true, requires: []string{"web", "goal_mode"}, explicit: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			t.Setenv("CARDEX_HOME", home)
			_, obj := seedRepo(t, db, repo, "true")
			if row.config != nil {
				if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(*row.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			r := New(home)
			r.Launch = func(context.Context, string, string) (int, error) { return 7, nil }
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			found := rankingCards(t, db, obj)
			if row.want != (len(found) == 1) {
				t.Fatalf("ranking cards %+v want present %v", found, row.want)
			}
			if row.want {
				if found[0].Kind != model.KindRanking || !sameStrings(found[0].Requires, row.requires) {
					t.Fatalf("card kind %s requires %+v", found[0].Kind, found[0].Requires)
				}
				if row.explicit && found[0].ID == loop.DefaultRankingCardID(time.Now()) {
					t.Fatalf("explicit card used the default id %s", found[0].ID)
				}
				if !row.explicit && found[0].ID != loop.DefaultRankingCardID(time.Now()) {
					t.Fatalf("default id %s", found[0].ID)
				}
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			again := rankingCards(t, db, obj)
			if len(again) != len(found) {
				t.Fatalf("second tick in the same slot cards %d -> %+v", len(found), again)
			}
			t.Logf("second tick same slot kept %d ranking card", len(again))
		})
	}
}

func TestRankingPromptFromBuild(t *testing.T) {
	rows := []struct {
		name      string
		config    string
		ranking   bool
		want      []string
		absent    []string
		snapshots bool
	}{
		{
			name: "default sources", ranking: true, snapshots: true,
			want: []string{"SWE-bench 官方榜", "old-selectable", "model-a"},
		},
		{
			name: "ranking_sources replaces the list", ranking: true, snapshots: true,
			config: `{"ranking_sources":["自定义榜A","自定义榜B"]}` + "\n",
			want:   []string{"自定义榜A", "自定义榜B", "old-selectable", "model-a"},
			absent: []string{"SWE-bench 官方榜"},
		},
		{
			name: "dev prompt", ranking: false,
			absent: []string{"SWE-bench 官方榜", "## 当前定位表", "## 各渠道可用的模型清单", "## 成绩单摘要"},
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			t.Setenv("CARDEX_HOME", home)
			_, objID := seedRepo(t, db, repo, "true")
			if row.config != "" {
				if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(row.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			seedWebRoute(t, db)
			dev := model.Card{
				ID: "dev", Objective: objID, Kind: model.KindDev, Title: "dev",
				Tier: model.TierT2, Status: model.CardReady, Body: "implement the card",
			}
			if err := db.CreateCard(ctx, &dev); err != nil {
				t.Fatal(err)
			}
			r := New(home)
			r.Launch = func(context.Context, string, string) (int, error) { return 7, nil }
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			cardID := "dev"
			if row.ranking {
				found := rankingCards(t, db, objID)
				if len(found) != 1 {
					t.Fatalf("ranking cards %+v", found)
				}
				cardID = found[0].ID
			}
			text := attemptPrompt(t, home, db, cardID)
			if !strings.Contains(text, "<!-- cardex-card -->") {
				t.Fatalf("prompt was not built\n%s", text)
			}
			for _, part := range row.want {
				if !strings.Contains(text, part) {
					t.Fatalf("prompt missing %s\n%s", part, text)
				}
			}
			if row.snapshots {
				for _, part := range []string{"## 当前定位表", "## 各渠道可用的模型清单", "## 成绩单摘要"} {
					if !strings.Contains(text, part) {
						t.Fatalf("prompt missing %s\n%s", part, text)
					}
				}
			}
			for _, part := range row.absent {
				if strings.Contains(text, part) {
					t.Fatalf("prompt contains %s\n%s", part, text)
				}
			}
		})
	}
}

func TestRankingApproveHook(t *testing.T) {
	rows := []struct {
		name    string
		answer  string
		approve bool
	}{
		{name: "批准", answer: "批准", approve: true},
		{name: "other answer", answer: "驳回", approve: false},
		{name: "stored route", answer: "批准", approve: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, q, kept := seedRankingApproval(t)
			got, err := question.New(db, nil).Answer(ctx, q.ID, row.answer)
			if err != nil {
				t.Fatal(err)
			}
			if got.Answer != row.answer {
				t.Fatalf("answer %q", got.Answer)
			}
			empty, err := question.New(db, nil).Answer(ctx, q.ID, "")
			if err == nil || empty.Answer != row.answer {
				t.Fatalf("empty answer err %v stored %q", err, empty.Answer)
			}
			if positioningHas(t, db, "approved-model") {
				t.Fatal("answer updated the positioning table")
			}
			r := New(home)
			r.Launch = func(context.Context, string, string) (int, error) { return 7, nil }
			if err := r.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if positioningHas(t, db, "approved-model") != row.approve {
				t.Fatalf("after tick approved=%v want %v", positioningHas(t, db, "approved-model"), row.approve)
			}
			wantModel := "old-model"
			if row.approve {
				wantModel = "approved-model"
			}
			attempts, err := db.ListAttempts(ctx, "c-next")
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 1 || attempts[0].Route.Model != wantModel {
				t.Fatalf("dispatched route %+v want %s", attempts, wantModel)
			}
			attempt, err := db.GetAttempt(ctx, kept)
			if err != nil {
				t.Fatal(err)
			}
			if attempt.Route.Channel != "keep-ch" || attempt.Route.Model != "keep-model" {
				t.Fatalf("stored route changed %+v", attempt.Route)
			}
			t.Logf("answer %s approve %v dispatched %s kept %+v", row.answer, row.approve, attempts[0].Route.Model, attempt.Route)
		})
	}
}

// TestNewKeepsRankingHookAndClassify builds the runner with New.
// A stored 批准 runs the hook New installed. Any other answer does not,
// even when a 批准 is already stored and FollowRanking would publish it.
func TestNewKeepsRankingHookAndClassify(t *testing.T) {
	rows := []struct {
		name    string
		answer  string
		approve bool
	}{
		{name: "批准", answer: "批准", approve: true},
		{name: "other answer", answer: "驳回", approve: false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, q, _ := seedRankingApproval(t)
			if !row.approve {
				stored, err := question.New(db, nil).Answer(ctx, q.ID, "批准")
				if err != nil {
					t.Fatal(err)
				}
				if stored.Answer != "批准" || positioningHas(t, db, "approved-model") {
					t.Fatalf("unhooked store answer %q approved %v", stored.Answer, positioningHas(t, db, "approved-model"))
				}
				q = model.Question{
					Objective: stored.Objective, Scope: "side", ScopeKind: "card", Kind: model.QuestionCustom,
					Text: "unrelated", Options: []string{row.answer},
				}
				if err := db.CreateQuestion(ctx, &q); err != nil {
					t.Fatal(err)
				}
			}
			r := New(home)
			if r.Classify == nil {
				t.Fatal("New left Classify nil")
			}
			if r.Dispose == nil {
				t.Fatal("New left Dispose nil")
			}
			got, err := r.Questions(db).Answer(ctx, q.ID, row.answer)
			if err != nil {
				t.Fatal(err)
			}
			if got.Answer != row.answer {
				t.Fatalf("stored answer %q", got.Answer)
			}
			if positioningHas(t, db, "approved-model") != row.approve {
				t.Fatalf("answer %q ran follow hook=%v", row.answer, !row.approve)
			}
		})
	}
}

func positioningHas(t *testing.T, db *store.Store, modelName string) bool {
	t.Helper()
	rows, err := db.ListPositioning(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range rows {
		if item.Model == modelName {
			return true
		}
	}
	return false
}

func TestDeliverDoesNotAbsorbRanking(t *testing.T) {
	body, err := os.ReadFile("loop.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (r *Runner) deliver(")
	if start < 0 {
		t.Fatal("deliver missing")
	}
	rest := src[start+1:]
	end := strings.Index(rest, "\nfunc ")
	if end < 0 {
		t.Fatal("deliver end missing")
	}
	fn := src[start : start+1+end]
	if strings.Contains(fn, "AbsorbHarvestedRanking") {
		t.Fatal("deliver calls AbsorbHarvestedRanking")
	}
	t.Log("deliver does not call AbsorbHarvestedRanking")
}

func seedRankingApproval(t *testing.T) (string, *store.Store, model.Question, string) {
	t.Helper()
	home, db, repo := newHome(t)
	t.Setenv("CARDEX_HOME", home)
	ctx := context.Background()
	_, objID := seedRepo(t, db, repo, "true")
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{\"schedules\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct{ id, modelName string }{
		{"ch-old", "old-model"},
		{"ch-new", "approved-model"},
	} {
		ch := model.Channel{
			ID: spec.id, Runner: model.RunnerFake, Family: "fam", SourceGroup: "fam",
			Models: []string{spec.modelName}, Concurrency: 4, Health: model.HealthOK, Tier: model.TierT2,
		}
		if err := db.CreateChannel(ctx, &ch); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreatePositioning(ctx, &model.Positioning{
		ID: "pos-old", Model: "old-model", Family: "fam",
		Tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, Preference: 1,
	}); err != nil {
		t.Fatal(err)
	}
	table, err := positioning.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	prop, err := table.Propose(ctx, []model.Positioning{{
		Model: "approved-model", Family: "fam", Tiers: []model.Tier{model.TierT2}, Preference: 1,
	}})
	if closeErr := table.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	q := model.Question{
		Objective: objID, Scope: "rank", ScopeKind: "card", Kind: model.QuestionCustom,
		Text:    "定位提案 " + prop.ID + "\n差异：\n+ approved-model\n",
		Options: []string{"批准", "驳回"},
	}
	if err := db.CreateQuestion(ctx, &q); err != nil {
		t.Fatal(err)
	}
	keep := model.Card{ID: "keep", Objective: objID, Kind: model.KindDev, Title: "keep", Status: model.CardDone}
	if err := db.CreateCard(ctx, &keep); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAttempt(ctx, &model.Attempt{
		ID: "a-keep", Card: keep.ID, Route: model.Route{Channel: "keep-ch", Model: "keep-model"},
		Outcome: model.OutcomePassed, EndedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	next := model.Card{
		ID: "c-next", Objective: objID, Kind: model.KindDev, Tier: model.TierT2,
		Title: "next", Status: model.CardReady, Acceptance: []string{"true"},
	}
	if err := db.CreateCard(ctx, &next); err != nil {
		t.Fatal(err)
	}
	return home, db, q, "a-keep"
}

func seedWebRoute(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	ch := model.Channel{
		ID: "ch", Runner: model.RunnerFake, Family: "alpha", SourceGroup: "alpha",
		Models: []string{"old-selectable", "model-a"}, Concurrency: 4, Health: model.HealthOK, Tier: model.TierT2,
	}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	pos := model.Positioning{
		ID: "pos", Model: "old-selectable", Family: "alpha",
		Tiers:        []model.Tier{model.TierT1, model.TierT2, model.TierT3},
		Capabilities: []string{"web"}, Preference: 1,
	}
	if err := db.CreatePositioning(ctx, &pos); err != nil {
		t.Fatal(err)
	}
}

func rankingCards(t *testing.T, db *store.Store, obj string) []model.Card {
	t.Helper()
	cards, err := db.ListCards(context.Background(), obj)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Card
	for _, card := range cards {
		if card.Kind == model.KindRanking {
			out = append(out, card)
		}
	}
	return out
}

func attemptPrompt(t *testing.T, home string, db *store.Store, cardID string) string {
	t.Helper()
	attempts, err := db.ListAttempts(context.Background(), cardID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) == 0 {
		t.Fatalf("no attempt for %s", cardID)
	}
	path := attempts[0].PromptPath
	if path == "" {
		path = filepath.Join(home, "attempts", attempts[0].ID, "prompt.txt")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func sameStrings(got, want []string) bool {
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

func strPtr(s string) *string { return &s }
