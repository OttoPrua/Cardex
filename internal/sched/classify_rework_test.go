package sched

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/store"
)

func TestClassifyRunnerSamplesKeepTier(t *testing.T) {
	answer := "TIER: T3\nREASON: complex"
	decoy := "TIER: T1\nREASON: trailing"
	samples := []struct {
		name   string
		runner model.RunnerName
		body   string
	}{
		{
			name:   "claude",
			runner: model.RunnerClaude,
			body:   `{"type":"result","result":"TIER: T3\nREASON: complex"}` + "\n" + decoy + "\n",
		},
		{
			name:   "codex",
			runner: model.RunnerCodex,
			body: "TIER: T1\nREASON: leading\n" +
				`{"type":"item.completed","item":{"type":"reasoning","text":"TIER: T1\nREASON: thought"}}` + "\n" +
				`{"type":"item.completed","item":{"type":"agent_message","text":"TIER: T3\nREASON: complex"}}` + "\n" +
				decoy + "\n",
		},
		{
			name:   "grok",
			runner: model.RunnerGrok,
			body: `{"type":"text","data":"TIER: T3\nREASON: complex"}` + "\n" +
				`{"type":"thought","data":"TIER: T1\nREASON: thought"}` + "\n" +
				`{"type":"plan","data":"TIER: T1\nREASON: plan"}` + "\n" +
				decoy + "\n",
		},
		{
			name:   "grok fragments",
			runner: model.RunnerGrok,
			body: `{"type":"text","data":"TIER: T"}` + "\n" +
				`{"type":"text","data":"3\nREASON: complex"}` + "\n" +
				decoy + "\n",
		},
		{
			name:   "cursor",
			runner: model.RunnerCursor,
			body:   `{"type":"result","result":"TIER: T3\nREASON: complex"}` + "\n" + decoy + "\n",
		},
		{
			name:   "kimi",
			runner: model.RunnerKimi,
			body:   `{"role":"assistant","content":"TIER: T3\nREASON: complex"}` + "\n" + decoy + "\n",
		},
		{
			name:   "agy",
			runner: model.RunnerAGY,
			body:   `{"status":"SUCCESS","response":"TIER: T3\nREASON: complex"}` + "\n" + decoy + "\n",
		},
		{
			name:   "opencode",
			runner: model.RunnerOpenCode,
			body:   `{"type":"text","part":{"text":"TIER: T3\nREASON: complex"}}` + "\n" + decoy + "\n",
		},
	}
	for _, sample := range samples {
		t.Run(sample.name, func(t *testing.T) {
			home := classifyCLIHome(t, sample.runner, "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' "+shellQuote(sample.body)+"\nexit 0\n")
			db := mustOpen(t, home)
			card := readyDevCard(t, db)
			if err := New(home).Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := mustCard(t, db, card.ID)
			if got.Tier != model.TierT3 {
				t.Fatalf("tier %s, want T3 from %s", got.Tier, answer)
			}
			if n := classifyErrors(t, db, card.ID); n != 0 {
				t.Fatalf("classify_error events %d", n)
			}
			attempts, err := db.ListAttempts(context.Background(), "cls-"+card.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 1 {
				t.Fatalf("classify attempts %+v", attempts)
			}
			if attempts[0].Outcome == "" || attempts[0].Outcome == model.OutcomePassed {
				t.Fatalf("classifier attempt outcome %q", attempts[0].Outcome)
			}
		})
	}
}

func TestClassifyAttemptIsNotHarvestedAsPassed(t *testing.T) {
	answer := "TIER: T3\nREASON: complex"
	home := classifyCLIHome(t, model.RunnerClaude, "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' "+shellQuote(`{"type":"result","result":"TIER: T3\nREASON: complex"}`)+"\nexit 0\n")
	db := mustOpen(t, home)
	card := readyDevCard(t, db)
	if err := New(home).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mustCard(t, db, card.ID).Tier != model.TierT3 {
		t.Fatalf("tier %s, want T3 from %s", mustCard(t, db, card.ID).Tier, answer)
	}
	attempts, err := db.ListAttempts(context.Background(), "cls-"+card.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("classify attempts %+v %v", attempts, err)
	}
	// The call stores no_output itself. Clearing it is the window harvest
	// must still recognize as the classification attempt.
	attempts[0].Outcome = ""
	if err := db.UpdateAttempt(context.Background(), &attempts[0]); err != nil {
		t.Fatal(err)
	}
	if err := New(home).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAttempt(context.Background(), attempts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome == "" || got.Outcome == model.OutcomePassed {
		t.Fatalf("classifier attempt outcome %q", got.Outcome)
	}
	cards, err := db.ListCards(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range cards {
		if item.Kind == model.KindReview {
			t.Fatalf("classifier attempt entered review: %+v", item)
		}
	}
}

func TestClassifyNonzeroExitUsesKindDefault(t *testing.T) {
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' " + shellQuote(`{"type":"result","result":"TIER: T1\nREASON: complex"}`) + "\nexit 1\n"
	home := classifyCLIHome(t, model.RunnerClaude, script)
	db := mustOpen(t, home)
	card := readyDevCard(t, db)
	if err := New(home).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := mustCard(t, db, card.ID)
	if got.Tier != model.DefaultTier(model.KindDev) {
		t.Fatalf("tier %s, want kind default", got.Tier)
	}
	if n := classifyErrors(t, db, card.ID); n == 0 {
		t.Fatal("missing classify_error")
	}
}

func TestClassifyTimeoutThenNextTick(t *testing.T) {
	script := `#!/bin/sh
prompt=$(cat)
case "$prompt" in
  *"Classify this Cardex card"*)
    printf '%s\n' '{"type":"result","result":"TIER: T1\nREASON: complex"}'
    /bin/sleep 45
    ;;
esac
exit 0
`
	home := classifyCLIHome(t, model.RunnerClaude, script)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(WithLog(ctx, ioDiscard{}), home, "127.0.0.1:0")
	}()
	deadline := time.Now().Add(50 * time.Second)
	for {
		raw, _ := os.ReadFile(filepath.Join(home, "serve-loop.log"))
		if strings.Count(string(raw), "iteration ") >= 2 {
			cancel()
			break
		}
		select {
		case err := <-errCh:
			cancel()
			t.Fatalf("serve exited before two ticks: %v\n%s", err, raw)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("timed out waiting for two ticks\n%s", raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	db := mustOpen(t, home)
	got := mustCard(t, db, "c")
	if got.Tier != model.DefaultTier(model.KindDev) {
		t.Fatalf("tier %s, want kind default after timeout", got.Tier)
	}
	if n := classifyErrors(t, db, "c"); n == 0 {
		t.Fatal("missing classify_error after timeout")
	}
	attempts, err := db.ListAttempts(context.Background(), "cls-c")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("classify attempts %+v", attempts)
	}
	item := attempts[0]
	if item.Exit == nil || item.EndedAt.IsZero() || item.Outcome == "" {
		t.Fatalf("classify attempt was not persisted: exit %v ended %v outcome %q", item.Exit, item.EndedAt, item.Outcome)
	}
	if item.Outcome == model.OutcomePassed {
		t.Fatal("classify attempt was harvested as passed")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func classifyCLIHome(t *testing.T, runner model.RunnerName, script string) string {
	t.Helper()
	home, db, repo := newHome(t)
	_, objID := seedRepo(t, db, repo, "true")
	bin := t.TempDir()
	path := filepath.Join(bin, classifyExecutable(runner))
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	ch := model.Channel{
		ID: "classifier", Runner: runner, Family: "classifier",
		Health: model.HealthOK, Concurrency: 1, Models: []string{"m"},
	}
	ctx := context.Background()
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
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return home
}

func readyDevCard(t *testing.T, db *store.Store) model.Card {
	t.Helper()
	card, err := db.GetCard(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func classifyExecutable(runner model.RunnerName) string {
	if runner == model.RunnerCursor {
		return "cursor-agent"
	}
	return string(runner)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func mustOpen(t *testing.T, home string) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
