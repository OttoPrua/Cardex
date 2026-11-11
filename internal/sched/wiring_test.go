package sched

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
)

func TestScriptlessResearchIsNotLaunched(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	t.Setenv("CARDEX_HOME", home)
	_, objID := seedRepo(t, db, repo, "true")
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	obj.Mode = model.ModeAuto
	if err := db.UpdateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	seedRoutes(t, db)
	if err := New(home).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	cards, err := db.ListCards(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	var research *model.Card
	for i := range cards {
		if cards[i].Kind == model.KindResearch {
			research = &cards[i]
		}
	}
	if research == nil {
		t.Fatal("design did not create inventory")
	}
	attempts, err := db.ListAttempts(ctx, research.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 0 || research.Status != model.CardReady {
		t.Fatalf("research launched status=%s attempts=%d", research.Status, len(attempts))
	}
}

func TestClassifyFailureKeepsKindDefault(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, objID := seedRepo(t, db, repo, "true")
	card := model.Card{
		ID: "c", Objective: objID, Kind: model.KindDev, Title: "c",
		Status: model.CardReady, Body: "classify me",
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if err := New(home).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got := mustCard(t, db, "c")
	if got.Tier != model.DefaultTier(model.KindDev) {
		t.Fatalf("tier %s", got.Tier)
	}
	if n := classifyErrors(t, db, "c"); n != 1 {
		t.Fatalf("classify_error events %d", n)
	}
	if err := New(home).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := classifyErrors(t, db, "c"); n != 1 {
		t.Fatalf("second tick classify_error events %d", n)
	}
}

func TestClassifySkipsTieredCard(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, objID := seedRepo(t, db, repo, "true")
	card := model.Card{
		ID: "c", Objective: objID, Kind: model.KindDev, Title: "c",
		Tier: model.TierT1, Status: model.CardReady, Body: "already tiered",
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if err := New(home).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got := mustCard(t, db, "c")
	if got.Tier != model.TierT1 {
		t.Fatalf("tier changed to %s", got.Tier)
	}
	if n := classifyErrors(t, db, "c"); n != 0 {
		t.Fatalf("classify_error events %d", n)
	}
}

func TestClassifyUnparseableUsesHost(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, objID := seedRepo(t, db, repo, "true")
	ch := model.Channel{
		ID: "classifier", Runner: model.RunnerFake, Family: "classifier",
		Health: model.HealthDegraded, Concurrency: 1,
	}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "c", Objective: objID, Kind: model.KindDev, Title: "c",
		Status: model.CardReady, Body: "needs a tier",
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if err := New(home).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got := mustCard(t, db, "c")
	if got.Tier != model.TierT2 {
		t.Fatalf("tier %s", got.Tier)
	}
	if n := classifyErrors(t, db, "c"); n != 1 {
		t.Fatalf("classify_error events %d", n)
	}
	if _, err := db.GetCard(ctx, "cls-c"); err != nil {
		t.Fatalf("host classify attempt was not created: %v", err)
	}
}

func TestClassifyHostTierFromScript(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, objID := seedRepo(t, db, repo, "true")
	ch := model.Channel{
		ID: "classifier", Runner: model.RunnerFake, Family: "classifier",
		Health: model.HealthDegraded, Concurrency: 1,
	}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	script := []byte(`{"attempts":{"0":{"output":"TIER: T1\nREASON: small change\n","exit_code":0}}}`)
	if err := os.MkdirAll(filepath.Join(home, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "scripts", "cls-c.json"), script, 0o644); err != nil {
		t.Fatal(err)
	}
	card := model.Card{
		ID: "c", Objective: objID, Kind: model.KindDev, Title: "c",
		Status: model.CardReady, Body: "needs a tier",
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	if err := New(home).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got := mustCard(t, db, "c")
	if got.Tier != model.TierT1 {
		t.Fatalf("tier %s", got.Tier)
	}
	if n := classifyErrors(t, db, "c"); n != 0 {
		t.Fatalf("classify_error events %d", n)
	}
	attempts, err := db.ListAttempts(ctx, "cls-c")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("classify attempts %+v", attempts)
	}
	if !eventsOf(t, db, attempts[0].ID, "host_start") {
		t.Fatal("classifier host did not start")
	}
}

func classifyErrors(t *testing.T, db interface {
	ListEvents(context.Context, string) ([]model.Event, error)
}, cardID string) int {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "card:"+cardID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, event := range events {
		if event.Type == "classify_error" {
			n++
		}
	}
	return n
}

func TestDeliveryFailureRetries(t *testing.T) {
	ctx := context.Background()
	t.Setenv("GROK_HOME", t.TempDir())
	home, db, repo := newHome(t)
	_, objID := seedRepo(t, db, repo, "true")
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte("{\"schedules\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch := model.Channel{
		ID: "ch", Runner: model.RunnerFake, Family: "fam", SourceGroup: "sub1",
		Health: model.HealthOK, Models: []string{"m"},
	}
	if err := db.CreateChannel(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	obj.Origin = "hermes:prof/sess"
	obj.Channel = "ch"
	if err := db.UpdateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	card := model.Card{ID: "c", Objective: obj.ID, Kind: model.KindDev, Title: "c", Tier: model.TierT2, Status: model.CardDone}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"origin":"file:box"}`)
	if err := db.AppendEvent(ctx, &model.Event{Entity: "card:c", Type: "origin", Data: raw}); err != nil {
		t.Fatal(err)
	}
	q := model.Question{
		ID: "q1", Objective: obj.ID, Scope: card.ID, ScopeKind: "card",
		Kind: model.QuestionCustom, Text: "decide this",
	}
	if err := db.CreateQuestion(ctx, &q); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "inbox"), []byte("blocked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	r := New(home)
	r.Now = func() time.Time { return now }
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := deliveryErrors(t, db, q.ID); n != 1 {
		t.Fatalf("delivery_error events %d", n)
	}
	if err := os.Remove(filepath.Join(home, "inbox")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(notify.ReminderGap(0) + time.Second)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(home, "inbox")
	entries, err := os.ReadDir(inbox)
	if err != nil || len(entries) == 0 {
		t.Fatalf("inbox retry %v %v", entries, err)
	}
	body, err := os.ReadFile(filepath.Join(inbox, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "q1") || !strings.Contains(text, "file:box") {
		t.Fatalf("inbox %s", text)
	}
	if strings.Contains(text, "sub1") {
		t.Fatalf("inbox took the channel account %s", text)
	}
	hermesRoot := filepath.Join(home, "control", "manager-wake", "hermes-inbox")
	foundSub := false
	_ = filepath.Walk(hermesRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(b), "sub1") {
			foundSub = true
		}
		return nil
	})
	if foundSub {
		t.Fatal("channel source group was written as a hermes subscription")
	}
}

func deliveryErrors(t *testing.T, db interface {
	ListEvents(context.Context, string) ([]model.Event, error)
}, questionID string) int {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "question:"+questionID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, event := range events {
		if event.Type == "delivery_error" {
			n++
		}
	}
	return n
}
