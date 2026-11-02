package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"cardex/internal/model"
	"cardex/internal/sched"
)

// TestFaults injects fixed fake-executor failures into merge-path dev cards.
func TestFaults(t *testing.T) {
	t.Run("transport", testFaultTransport)
	t.Run("truncated", testFaultTruncated)
	t.Run("quota", testFaultQuota)
	t.Run("auth", testFaultAuth)
	t.Run("serve-restart", testFaultServeRestart)
}

func testFaultTransport(t *testing.T) {
	t.Log("transport")
	phrases := []string{"dial tcp: connection refused", "i/o timeout"}
	body := faultScript(map[string]any{
		"0": faultDiagnostic(phrases[0]),
		"1": faultDiagnostic(phrases[1]),
		"2": faultPassFile("extra.go"),
	})
	h, _ := faultHarness(t, body)
	watch := watchMerges(h)
	defer watch.stop()
	faultUntil(t, h, func() bool {
		return cardOf(t, h, "c1").Status == model.CardMerged
	}, 45*time.Second)
	attempts := attemptsByIndex(t, h, "c1")
	if len(attempts) < 3 {
		t.Fatalf("attempts %d\n%s", len(attempts), diag(h))
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
		t.Fatalf("transport phrases %v in logs, saw %v\n%s", phrases, seen, diag(h))
	}
	faultPost(t, h, watch)
}

func testFaultTruncated(t *testing.T) {
	t.Log("truncated")
	body := faultScript(map[string]any{
		"0": map[string]any{
			"truncated_events": true,
			"write_files":      []map[string]string{{"path": "extra.go", "content": "package fixture\n"}},
			"exit_code":        0,
		},
	})
	h, _ := faultHarness(t, body)
	watch := watchMerges(h)
	defer watch.stop()
	faultUntil(t, h, func() bool {
		return cardOf(t, h, "c1").Status == model.CardMerged
	}, 45*time.Second)
	text := ""
	for _, attempt := range attemptsOf(t, h, "c1") {
		text += attemptOutput(h, attempt.ID)
	}
	if !strings.Contains(text, `"partial":true`) && !strings.Contains(text, "partial") {
		t.Fatalf("truncated stream missing\n%s", text)
	}
	faultPost(t, h, watch)
}

func testFaultQuota(t *testing.T) {
	t.Log("quota")
	reset := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	body := faultScript(map[string]any{
		"0": map[string]any{"quota_hit": map[string]string{"reset": reset}, "exit_code": 1},
		"1": faultPassFile("extra.go"),
	})
	h, _ := faultHarness(t, body)
	watch := watchMerges(h)
	defer watch.stop()
	faultUntil(t, h, func() bool {
		return cardOf(t, h, "c1").Status == model.CardMerged
	}, 45*time.Second)
	attempts := attemptsByIndex(t, h, "c1")
	if len(attempts) < 2 || attempts[0].Outcome != model.OutcomeQuotaHit {
		t.Fatalf("quota attempts %+v\n%s", attempts, diag(h))
	}
	if attempts[0].Route.Channel == "" || attempts[0].Route.Channel == attempts[1].Route.Channel {
		t.Fatalf("quota stayed on %s", attempts[0].Route.Channel)
	}
	faultPost(t, h, watch)
}

func testFaultAuth(t *testing.T) {
	t.Log("auth")
	body := faultScript(map[string]any{
		"0": map[string]any{"auth_failure": true, "exit_code": 1},
		"1": faultPassFile("extra.go"),
	})
	h, _ := faultHarness(t, body)
	watch := watchMerges(h)
	defer watch.stop()
	faultUntil(t, h, func() bool {
		return cardOf(t, h, "c1").Status == model.CardMerged
	}, 45*time.Second)
	attempts := attemptsByIndex(t, h, "c1")
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
	faultPost(t, h, watch)
}

func testFaultServeRestart(t *testing.T) {
	t.Log("serve-restart")
	body := faultScript(map[string]any{
		"0": map[string]any{"hold": "30s", "exit_code": 0},
		"1": map[string]any{"silent": true},
	})
	h, _ := faultHarness(t, body)
	watch := watchMerges(h)
	defer watch.stop()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- sched.Serve(ctx, h.Home, "127.0.0.1:0") }()
	waitLive(t, h, "c1", 20*time.Second)
	for _, attempt := range attemptsOf(t, h, "c1") {
		killAttempt(h, attempt)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for _, attempt := range attemptsOf(t, h, "c1") {
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
		card := cardOf(t, h, "c1")
		return card.Status == model.CardNeedsInput && len(questionsFor(t, h, card.ID)) == 1
	}, 30*time.Second)
	questions := questionsFor(t, h, "c1")
	if len(questions) != 1 || !strings.Contains(questions[0].Text, "no output") {
		t.Fatalf("questions %+v\n%s", questions, diag(h))
	}
	if cardOf(t, h, "c1").Status == model.CardMerged {
		t.Fatalf("serve-restart merged the card\n%s", diag(h))
	}
	cancel2()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("second serve: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("second serve did not return")
	}
	faultPost(t, h, watch)
}

func faultHarness(t *testing.T, body string) (*Harness, string) {
	t.Helper()
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
	installRoutes(t, h)
	card := model.Card{
		ID: "c1", Objective: obj, Kind: model.KindDev, Title: "fault", Tier: model.TierT2,
		Status: model.CardReady, Acceptance: []string{"true"},
		Claims: []model.Claim{{Path: "extra.go"}},
		Body:   body,
	}
	if err := h.DB.CreateCard(context.Background(), &card); err != nil {
		t.Fatal(err)
	}
	return h, obj
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

func faultPost(t *testing.T, h *Harness, watch *mergeWatch) {
	t.Helper()
	fuzzy := map[model.CardStatus]bool{
		model.CardReady: true, model.CardRunning: true, model.CardHarvesting: true,
		model.CardReviewing: true, model.CardMergeQueued: true,
	}
	for _, card := range cardsOf(t, h, "") {
		if fuzzy[card.Status] {
			t.Fatalf("card %s still %s\n%s", card.ID, card.Status, diag(h))
		}
		if !scenarioMerges(card.Kind) {
			continue
		}
		if card.Status == model.CardMerged {
			continue
		}
		qs := questionsFor(t, h, card.ID)
		if len(qs) != 1 {
			t.Fatalf("card %s %s has %d questions\n%s", card.ID, card.Status, len(qs), diag(h))
		}
	}
	for id, n := range watch.counts() {
		if n > 1 {
			t.Fatalf("card %s merged %d times", id, n)
		}
	}
}

type mergeWatch struct {
	mu     sync.Mutex
	n      map[string]int
	last   map[string]model.CardStatus
	stopCh chan struct{}
	wg     sync.WaitGroup
}

func watchMerges(h *Harness) *mergeWatch {
	w := &mergeWatch{
		n:      map[string]int{},
		last:   map[string]model.CardStatus{},
		stopCh: make(chan struct{}),
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			select {
			case <-w.stopCh:
				return
			default:
			}
			cards, err := h.DB.ListCards(context.Background(), "")
			if err == nil {
				w.mu.Lock()
				for _, card := range cards {
					prev := w.last[card.ID]
					if card.Status == model.CardMerged && prev != model.CardMerged {
						w.n[card.ID]++
					}
					w.last[card.ID] = card.Status
				}
				w.mu.Unlock()
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	return w
}

func (w *mergeWatch) counts() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]int{}
	for id, n := range w.n {
		out[id] = n
	}
	return out
}

func (w *mergeWatch) stop() {
	close(w.stopCh)
	w.wg.Wait()
}
