package e2e

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/sched"
)

const faultSeed int64 = 20261102

// TestFaults injects every fault class into one seeded multi-card batch.
// After each card is terminal, further ticks must leave stored status and events unchanged.
func TestFaults(t *testing.T) {
	rng := rand.New(rand.NewSource(faultSeed))
	classes := []string{"transport", "truncated", "quota", "auth", "serve-restart"}
	rng.Shuffle(len(classes), func(i, j int) { classes[i], classes[j] = classes[j], classes[i] })
	phrases := []string{"dial tcp: connection refused", "i/o timeout", "connection reset by peer"}
	rng.Shuffle(len(phrases), func(i, j int) { phrases[i], phrases[j] = phrases[j], phrases[i] })
	phrases = phrases[:2]
	t.Logf("batch cards=%d seed=%d classes=%s", len(classes), faultSeed, strings.Join(classes, ","))
	for _, class := range classes {
		t.Logf("fault class %s", class)
	}
	if len(classes) < 2 {
		t.Fatal("batch needs more than one card")
	}

	h := New(t)
	t.Setenv("CARDEX_HOME", h.Home)
	runner := sched.New(h.Home)
	runner.Script = func(card model.Card) ([]byte, bool) {
		if card.Kind == model.KindReview {
			return []byte(scenarioReviewScript), true
		}
		return nil, false
	}
	h.Sched = runner
	stopLater(t, h)
	_, obj := bindProject(t, h, "true")
	installFaultChannels(t, h)

	type item struct {
		id, class, channel string
	}
	var batch []item
	var injected []string
	for i, class := range classes {
		id := "c" + strconv.Itoa(i)
		channel := "ch-" + class
		body := faultBody(class, phrases, "batch-"+id+".go")
		card := model.Card{
			ID: id, Objective: obj, Kind: model.KindDev, Title: class, Tier: model.TierT2,
			Status: model.CardReady, Channel: channel, Acceptance: []string{"true"},
			Claims: []model.Claim{{Path: "batch-" + id + ".go"}},
			Body:   body,
		}
		if err := h.DB.CreateCard(context.Background(), &card); err != nil {
			t.Fatal(err)
		}
		batch = append(batch, item{id: id, class: class, channel: channel})
		injected = append(injected, id)
		t.Logf("batch card %s class %s channel %s", id, class, channel)
	}

	serveID := ""
	for _, card := range batch {
		if card.class == "serve-restart" {
			serveID = card.id
		}
	}
	if serveID == "" {
		t.Fatal("batch missing serve-restart")
	}
	faultUntil(t, h, func() bool {
		if cardOf(t, h, serveID).Status != model.CardRunning || !attemptLive(t, h, serveID) {
			return false
		}
		for _, card := range batch {
			if card.class == "serve-restart" {
				continue
			}
			if cardOf(t, h, card.id).Status != model.CardMerged {
				return false
			}
		}
		return true
	}, 90*time.Second)
	for _, card := range batch {
		switch card.class {
		case "transport":
			assertTransport(t, h, card.id, phrases)
		case "truncated":
			assertTruncated(t, h, card.id)
		case "quota":
			assertQuota(t, h, card.id)
		case "auth":
			assertAuth(t, h, card.id)
		}
	}

	restartServe(t, h, serveID)
	assertServeRestart(t, h, serveID)

	before := faultRead(t, h)
	const extra = 3
	t.Logf("extra ticks after terminal n=%d", extra)
	for i := 0; i < extra; i++ {
		if err := h.Sched.Tick(context.Background()); err != nil {
			t.Fatalf("extra tick %d: %v\n%s", i, err, diag(h))
		}
		mid := faultRead(t, h)
		for id, status := range before.cards {
			if status == model.CardMerged && mid.cards[id] != model.CardMerged {
				t.Fatalf("card %s left merged during extra tick %d: %s", id, i, mid.cards[id])
			}
		}
	}
	after := faultRead(t, h)
	t.Logf("reread card status %s", after.statusText())
	t.Logf("reread events %d", len(after.events))
	assertFaultIdempotent(t, h, before, after, injected)
}

func faultBody(class string, phrases []string, path string) string {
	switch class {
	case "transport":
		return faultScript(map[string]any{
			"0": faultDiagnostic(phrases[0]),
			"1": faultDiagnostic(phrases[1]),
			"2": faultPassFile(path),
		})
	case "truncated":
		return faultScript(map[string]any{
			"0": map[string]any{
				"truncated_events": true,
				"write_files":      []map[string]string{{"path": path, "content": "package fixture\n"}},
				"exit_code":        0,
			},
		})
	case "quota":
		reset := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
		return faultScript(map[string]any{
			"0": map[string]any{"quota_hit": map[string]string{"reset": reset}, "exit_code": 1},
			"1": faultPassFile(path),
		})
	case "auth":
		return faultScript(map[string]any{
			"0": map[string]any{"auth_failure": true, "exit_code": 1},
			"1": faultPassFile(path),
		})
	case "serve-restart":
		return faultScript(map[string]any{
			"0": map[string]any{"hold": "5m", "exit_code": 0},
			"1": map[string]any{"silent": true},
		})
	default:
		panic("unknown fault class " + class)
	}
}

func installFaultChannels(t *testing.T, h *Harness) {
	t.Helper()
	ctx := context.Background()
	ids := []string{"ch-transport", "ch-truncated", "ch-quota", "ch-auth", "ch-serve-restart", "ch-spare"}
	for _, id := range ids {
		family := "fam-" + strings.TrimPrefix(id, "ch-")
		modelName := "model-" + strings.TrimPrefix(id, "ch-")
		ch := model.Channel{
			ID: id, Runner: model.RunnerFake, Family: family, SourceGroup: family,
			Models: []string{modelName}, Concurrency: 4, Health: model.HealthOK, Tier: model.TierT2,
			Redline: 0,
			QuotaWindows: []model.QuotaWindow{{
				Name: "5h", ResetRule: "rolling", ResetAt: time.Now().Add(24 * time.Hour),
				LimitTokens: 100, UsedTokens: 20, Remaining: 0.8, Confidence: 1,
			}},
		}
		if err := h.DB.CreateChannel(ctx, &ch); err != nil {
			t.Fatal(err)
		}
		pos := model.Positioning{
			ID: "pos-" + id, Model: modelName, Family: family,
			Tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, Preference: 1,
		}
		if err := h.DB.CreatePositioning(ctx, &pos); err != nil {
			t.Fatal(err)
		}
	}
}

func assertTransport(t *testing.T, h *Harness, id string, phrases []string) {
	t.Helper()
	attempts := attemptsByIndex(t, h, id)
	if len(attempts) < 3 {
		t.Fatalf("transport attempts %d\n%s", len(attempts), diag(h))
	}
	if attempts[0].Route.Channel == "" || attempts[0].Route.Channel != attempts[1].Route.Channel {
		t.Fatalf("transport retries left the channel: %+v %+v", attempts[0].Route, attempts[1].Route)
	}
	var seen []string
	for _, attempt := range attempts {
		text := attemptOutput(h, attempt.ID)
		for _, phrase := range phrases {
			if strings.Contains(text, phrase) {
				seen = append(seen, phrase)
			}
		}
	}
	if !containsAll(seen, phrases) {
		t.Fatalf("transport phrases %v, saw %v\n%s", phrases, seen, diag(h))
	}
}

func assertTruncated(t *testing.T, h *Harness, id string) {
	t.Helper()
	text := ""
	for _, attempt := range attemptsOf(t, h, id) {
		text += attemptOutput(h, attempt.ID)
	}
	if !strings.Contains(text, `"partial":true`) && !strings.Contains(text, "partial") {
		t.Fatalf("truncated stream missing\n%s", text)
	}
}

func assertQuota(t *testing.T, h *Harness, id string) {
	t.Helper()
	attempts := attemptsByIndex(t, h, id)
	if len(attempts) < 2 || attempts[0].Outcome != model.OutcomeQuotaHit {
		t.Fatalf("quota attempts %+v\n%s", attempts, diag(h))
	}
	if attempts[0].Route.Channel == "" || attempts[0].Route.Channel == attempts[1].Route.Channel {
		t.Fatalf("quota stayed on %s", attempts[0].Route.Channel)
	}
}

func assertAuth(t *testing.T, h *Harness, id string) {
	t.Helper()
	attempts := attemptsByIndex(t, h, id)
	if len(attempts) < 2 || attempts[0].Outcome != model.OutcomeAuthFailed {
		t.Fatalf("auth attempts %+v\n%s", attempts, diag(h))
	}
	if attempts[0].Route.Channel == "" || attempts[0].Route.Channel == attempts[1].Route.Channel {
		t.Fatalf("auth stayed on %s", attempts[0].Route.Channel)
	}
	ch, err := h.DB.GetChannel(context.Background(), attempts[0].Route.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Health != model.HealthAuthFailed {
		t.Fatalf("channel health %s", ch.Health)
	}
}

func restartServe(t *testing.T, h *Harness, id string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- sched.Serve(ctx, h.Home, "127.0.0.1:0") }()
	waitLive(t, h, id, 20*time.Second)
	for _, attempt := range attemptsOf(t, h, id) {
		killAttempt(h, attempt)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for _, attempt := range attemptsOf(t, h, id) {
			if processAlive(attempt.PID) {
				alive = true
				killAttempt(h, attempt)
			}
		}
		if !alive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("first serve: %v\n%s", err, diag(h))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("first serve did not return")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { errCh <- sched.Serve(ctx2, h.Home, "127.0.0.1:0") }()
	waitCard(t, h, func() bool {
		card := cardOf(t, h, id)
		return card.Status == model.CardNeedsInput && len(questionsFor(t, h, card.ID)) == 1
	}, 30*time.Second)
	cancel2()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("second serve: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("second serve did not return")
	}
}

func assertServeRestart(t *testing.T, h *Harness, id string) {
	t.Helper()
	questions := questionsFor(t, h, id)
	if len(questions) != 1 || !strings.Contains(questions[0].Text, "no output") {
		t.Fatalf("questions %+v\n%s", questions, diag(h))
	}
	if cardOf(t, h, id).Status == model.CardMerged {
		t.Fatalf("serve-restart merged the card\n%s", diag(h))
	}
}

type faultSnap struct {
	cards  map[string]model.CardStatus
	events []model.Event
	byCard map[string][]string
}

func (s faultSnap) statusText() string {
	ids := make([]string, 0, len(s.cards))
	for id := range s.cards {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(id)
		b.WriteByte('=')
		b.WriteString(string(s.cards[id]))
	}
	return b.String()
}

func faultRead(t *testing.T, h *Harness) faultSnap {
	t.Helper()
	ctx := context.Background()
	cards, err := h.DB.ListCards(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	events, err := h.DB.ListEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := h.DB.ListAttempts(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	byAttempt := map[string]string{}
	for _, attempt := range attempts {
		byAttempt[attempt.ID] = attempt.Card
	}
	snap := faultSnap{
		cards:  map[string]model.CardStatus{},
		events: events,
		byCard: map[string][]string{},
	}
	for _, card := range cards {
		snap.cards[card.ID] = card.Status
	}
	for _, event := range events {
		cardID := ""
		if strings.HasPrefix(event.Entity, "card:") {
			cardID = strings.TrimPrefix(event.Entity, "card:")
		}
		if strings.HasPrefix(event.Entity, "attempt:") {
			cardID = byAttempt[strings.TrimPrefix(event.Entity, "attempt:")]
		}
		if cardID == "" {
			var data struct {
				Card string `json:"card"`
			}
			_ = json.Unmarshal(event.Data, &data)
			cardID = data.Card
		}
		if cardID == "" {
			continue
		}
		snap.byCard[cardID] = append(snap.byCard[cardID], event.ID)
	}
	return snap
}

func assertFaultIdempotent(t *testing.T, h *Harness, before, after faultSnap, injected []string) {
	t.Helper()
	fuzzy := map[model.CardStatus]bool{
		model.CardReady: true, model.CardRunning: true, model.CardHarvesting: true,
		model.CardReviewing: true, model.CardMergeQueued: true,
	}
	merged := 0
	for id, status := range after.cards {
		if fuzzy[status] {
			t.Fatalf("card %s still %s after reread\n%s", id, status, diag(h))
		}
		if status == model.CardNeedsInput {
			qs := questionsFor(t, h, id)
			if len(qs) != 1 {
				t.Fatalf("needs_input card %s has %d questions\n%s", id, len(qs), diag(h))
			}
		}
		if status != model.CardMerged {
			continue
		}
		merged++
		events := after.byCard[id]
		if len(events) == 0 {
			t.Fatalf("merged card %s has an empty event counter; that is not proof", id)
		}
		if before.cards[id] != model.CardMerged {
			t.Fatalf("card %s was not merged before extra ticks: %s", id, before.cards[id])
		}
		if strings.Join(before.byCard[id], ",") != strings.Join(events, ",") {
			t.Fatalf("card %s events changed after extra ticks: before %v after %v", id, before.byCard[id], events)
		}
	}
	if merged == 0 {
		t.Fatal("no merged card in the reread; empty merge counter is not proof")
	}
	for _, id := range injected {
		status := after.cards[id]
		if status == model.CardMerged {
			continue
		}
		qs := questionsFor(t, h, id)
		if len(qs) != 1 {
			t.Fatalf("card %s %s has %d questions, want exactly one\n%s", id, status, len(qs), diag(h))
		}
	}
}

func faultScript(attempts map[string]any) string {
	raw, err := json.Marshal(map[string]any{"attempts": attempts})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func faultDiagnostic(phrase string) map[string]any {
	encoded, err := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"message": phrase},
	})
	if err != nil {
		panic(err)
	}
	return map[string]any{
		"output":    "error: " + phrase + "\n" + string(encoded) + "\n",
		"exit_code": 1,
	}
}

func faultPassFile(path string) map[string]any {
	return map[string]any{
		"write_files": []map[string]string{{"path": path, "content": "package fixture\n"}},
		"exit_code":   0,
	}
}

func faultUntil(t *testing.T, h *Harness, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		if err := h.Sched.Tick(context.Background()); err != nil {
			if strings.Contains(err.Error(), "database is locked") {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			t.Fatalf("tick: %v\n%s", err, diag(h))
		}
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout\n%s", diag(h))
}

func waitLive(t *testing.T, h *Harness, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cardOf(t, h, id).Status == model.CardRunning && attemptLive(t, h, id) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("host did not stay up\n%s", diag(h))
}

func waitCard(t *testing.T, h *Harness, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("condition not met\n%s", diag(h))
}

func killAttempt(h *Harness, attempt model.Attempt) {
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

func attemptsByIndex(t *testing.T, h *Harness, cardID string) []model.Attempt {
	t.Helper()
	attempts := attemptsOf(t, h, cardID)
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].Index < attempts[j].Index })
	return attempts
}

func attemptOutput(h *Harness, id string) string {
	b, err := os.ReadFile(filepath.Join(h.Home, "attempts", id, "output.log"))
	if err != nil {
		return ""
	}
	return string(b)
}

func containsAll(got, want []string) bool {
	have := map[string]bool{}
	for _, item := range got {
		have[item] = true
	}
	for _, item := range want {
		if !have[item] {
			return false
		}
	}
	return true
}

func questionsFor(t *testing.T, h *Harness, cardID string) []model.Question {
	t.Helper()
	all := questionsOf(t, h, "")
	var out []model.Question
	for _, q := range all {
		if q.Scope == cardID {
			out = append(out, q)
		}
	}
	return out
}
