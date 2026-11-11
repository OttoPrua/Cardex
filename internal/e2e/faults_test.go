package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/sched"
)

const faultSeed int64 = 20261102

// TestFaults injects every fault class into one seeded multi-card batch.
// Merge acceptance is counted from the command the fixture installs and the
// real merge path runs. Harvest runs of that command are a separate count.
// After the first successful merge the count is one, and later ticks do not
// increase it even when status and event ids stay the same.
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
	// Disposition reads loop.retry_backoff_seconds from this file. One second
	// keeps c4 on the same two-wait path inside the 90s window.
	cfg := []byte("{\"loop\":{\"retry_backoff_seconds\":1}}\n")
	if err := os.WriteFile(filepath.Join(h.Home, "config.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
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
	installFaultAccept(t, h)

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
			Status: model.CardReady, Channel: channel, Acceptance: []string{faultAcceptCommand(h, id)},
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
			if card.id != "c4" {
				t.Fatalf("seed %d transport card is %s", faultSeed, card.id)
			}
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
	beforeMerge, beforeHarvest := faultAcceptCounts(t, h)
	assertMergeCountedOnce(t, before, beforeMerge, beforeHarvest)
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
	afterMerge, _ := faultAcceptCounts(t, h)
	t.Logf("reread card status %s", after.statusText())
	t.Logf("reread events %d", len(after.events))
	t.Logf("merge acceptance before %v after %v", beforeMerge, afterMerge)
	assertFaultIdempotent(t, h, before, after, injected, beforeMerge, afterMerge)
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
	assertTransportBackoff(t, h, id, attempts)
}

// assertTransportBackoff checks the two production retry_backoff waits and
// that the third attempt did not start before the second wait's deadline.
func assertTransportBackoff(t *testing.T, h *Harness, id string, attempts []model.Attempt) {
	t.Helper()
	events, err := h.DB.ListEvents(context.Background(), "card:"+id)
	if err != nil {
		t.Fatal(err)
	}
	type wait struct {
		until time.Time
	}
	var waits []wait
	for _, event := range events {
		if event.Type != "retry_backoff" {
			continue
		}
		var data struct {
			Until string `json:"until"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatalf("backoff record %s: %v", event.ID, err)
		}
		until, err := time.Parse(time.RFC3339Nano, data.Until)
		if err != nil {
			t.Fatalf("backoff until %q: %v", data.Until, err)
		}
		if !until.After(event.TS) {
			t.Fatalf("backoff record %s until %s is not a wait after %s", event.ID, until.Format(time.RFC3339Nano), event.TS.Format(time.RFC3339Nano))
		}
		waits = append(waits, wait{until: until})
	}
	if len(waits) != 2 {
		t.Fatalf("transport backoff waits %d, want 2\n%s", len(waits), diag(h))
	}
	sort.Slice(waits, func(i, j int) bool { return waits[i].until.Before(waits[j].until) })
	third := attempts[2]
	if third.Index != 2 {
		t.Fatalf("third transport attempt index %d", third.Index)
	}
	if third.StartedAt.Before(waits[1].until) {
		t.Fatalf("third attempt started %s before second backoff deadline %s", third.StartedAt.Format(time.RFC3339Nano), waits[1].until.Format(time.RFC3339Nano))
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
	kinds  map[string]model.CardKind
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
		kinds:  map[string]model.CardKind{},
		events: events,
		byCard: map[string][]string{},
	}
	for _, card := range cards {
		snap.cards[card.ID] = card.Status
		snap.kinds[card.ID] = card.Kind
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

func assertFaultIdempotent(t *testing.T, h *Harness, before, after faultSnap, injected []string, beforeMerge, afterMerge map[string]int) {
	t.Helper()
	fuzzy := map[model.CardStatus]bool{
		model.CardReady: true, model.CardRunning: true, model.CardHarvesting: true,
		model.CardReviewing: true, model.CardMergeQueued: true,
	}
	merged := 0
	nonCodeDone := 0
	for id, status := range after.cards {
		kind := after.kinds[id]
		qs := questionsFor(t, h, id)
		// Scheduler enqueues sch-ranking- beside the batch. Production does not
		// count that card as unfinished, and it stays ready when web is absent.
		if kind == model.KindRanking && strings.HasPrefix(id, "sch-ranking-") && status == model.CardReady {
			if before.cards[id] != model.CardReady || strings.Join(before.byCard[id], ",") != strings.Join(after.byCard[id], ",") {
				t.Fatalf("ranking card %s changed during extra ticks: before %s %v after %s %v", id, before.cards[id], before.byCard[id], status, after.byCard[id])
			}
			continue
		}
		if fuzzy[status] {
			t.Fatalf("card %s %s still %s after reread\n%s", id, kind, status, diag(h))
		}
		if status == model.CardNeedsInput && len(qs) != 1 {
			t.Fatalf("needs_input card %s has %d questions\n%s", id, len(qs), diag(h))
		}
		if !faultSettled(status, kind, len(qs)) {
			t.Fatalf("card %s %s ended %s with %d questions\n%s", id, kind, status, len(qs), diag(h))
		}
		if status == model.CardDone && faultNonCode(kind) {
			nonCodeDone++
		}
		if status != model.CardMerged {
			continue
		}
		merged++
		if beforeMerge[id] != 1 || afterMerge[id] != 1 {
			t.Fatalf("card %s merge acceptance before %d after %d, want 1; events before %v after %v", id, beforeMerge[id], afterMerge[id], before.byCard[id], after.byCard[id])
		}
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
	if nonCodeDone == 0 {
		t.Fatal("no non-code card ended done")
	}
	for _, id := range injected {
		status := after.cards[id]
		if status == model.CardMerged {
			if afterMerge[id] != 1 {
				t.Fatalf("card %s merge acceptance %d, want 1", id, afterMerge[id])
			}
			continue
		}
		qs := questionsFor(t, h, id)
		if len(qs) != 1 {
			t.Fatalf("card %s %s has %d questions, want exactly one\n%s", id, status, len(qs), diag(h))
		}
	}
}

func assertMergeCountedOnce(t *testing.T, snap faultSnap, merge, harvest map[string]int) {
	t.Helper()
	for id, status := range snap.cards {
		if status != model.CardMerged {
			continue
		}
		if merge[id] != 1 {
			t.Fatalf("card %s merge acceptance %d, want 1; harvest %d is not the merge count", id, merge[id], harvest[id])
		}
		if harvest[id] < 1 {
			t.Fatalf("card %s has no harvest acceptance run to separate from merge", id)
		}
	}
}

func faultSettled(status model.CardStatus, kind model.CardKind, questions int) bool {
	switch status {
	case model.CardMerged:
		return true
	case model.CardDone:
		return faultNonCode(kind)
	case model.CardNeedsInput:
		return questions == 1
	case model.CardWaiting, model.CardPaused, model.CardFailed:
		return questions > 0
	default:
		return questions > 0
	}
}

func faultNonCode(kind model.CardKind) bool {
	switch kind {
	case model.KindDesign, model.KindResearch, model.KindPlan, model.KindReview,
		model.KindReplan, model.KindNormalize, model.KindRanking:
		return true
	default:
		return false
	}
}

func installFaultAccept(t *testing.T, h *Harness) {
	t.Helper()
	script := `#!/bin/sh
d=$(pwd)
k=harvest
case "$d" in
  *cardex-merge-*) k=merge ;;
esac
printf '%s %s\n' "$k" "$1" >> "$FAULT_ACCEPT_LOG"
`
	if err := os.WriteFile(faultAcceptScript(h), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func faultAcceptScript(h *Harness) string {
	return filepath.Join(h.Home, "fault-accept.sh")
}

func faultAcceptLog(h *Harness) string {
	return filepath.Join(h.Home, "fault-accept.log")
}

func faultAcceptCommand(h *Harness, cardID string) string {
	return fmt.Sprintf("FAULT_ACCEPT_LOG=%s sh %s %s", faultShQuote(faultAcceptLog(h)), faultShQuote(faultAcceptScript(h)), faultShQuote(cardID))
}

func faultShQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func faultAcceptCounts(t *testing.T, h *Harness) (merge, harvest map[string]int) {
	t.Helper()
	merge = map[string]int{}
	harvest = map[string]int{}
	b, err := os.ReadFile(faultAcceptLog(h))
	if os.IsNotExist(err) {
		return merge, harvest
	}
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return merge, harvest
	}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("accept log %q", line)
		}
		switch fields[0] {
		case "merge":
			merge[fields[1]]++
		case "harvest":
			harvest[fields[1]]++
		default:
			t.Fatalf("accept log kind %q", fields[0])
		}
	}
	return merge, harvest
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
	item, ok := takeE2E(attempt.ID)
	if !ok || item.pid != attempt.PID {
		return
	}
	signalE2E(item)
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
