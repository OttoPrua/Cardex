package sched

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/prompt"
	"github.com/OttoPrua/Cardex/internal/store"
)

// TestLaunchUnusableChannel drives Tick from sched.New.
// A stored channel that is missing, not ok, or blocked until a future reset
// is not launched again. A healthy unblocked explicit channel stays pinned.
// Empty, preset, failed, and unparseable classification stay on that same path.
func TestLaunchUnusableChannel(t *testing.T) {
	t1 := func(context.Context, string, string) (string, error) {
		return "TIER: T1\nREASON: small change\n", nil
	}
	bad := func(context.Context, string, string) (string, error) {
		return "", errors.New("classifier down")
	}
	junk := func(context.Context, string, string) (string, error) {
		return "not a tier\n", nil
	}
	cases := []struct {
		name         string
		classify     string
		tier         model.Tier
		call         func(context.Context, string, string) (string, error)
		wantTier     model.Tier
		wantCalls    int
		cardChannel  string
		cardModel    string
		objChannel   string
		objModel     string
		wantModel    string
		specs        []pinChannel
		block        string
		blockOnCard  bool
		wantChannel  string
		wantQuestion bool
		branch       bool
		backlog      bool
		released     bool
		reason       string
		pinChannel   string
		pinModel     string
	}{
		{
			name: "quota hit opens another channel",
			specs: []pinChannel{
				{id: "cha", modelName: "model-a", pref: 0},
				{id: "chb", modelName: "model-b", pref: 9},
			},
			cardChannel: "cha", block: "cha", blockOnCard: true, branch: true, wantChannel: "chb",
			released: true, reason: "blocked", pinChannel: "cha",
		},
		{
			name: "unhealthy pin opens another channel",
			specs: []pinChannel{
				{id: "cha", modelName: "model-a", health: model.HealthAuthFailed},
				{id: "chb", modelName: "model-b"},
			},
			cardChannel: "cha", wantChannel: "chb",
			released: true, reason: "health", pinChannel: "cha",
		},
		{
			name:        "missing pin opens another channel",
			specs:       []pinChannel{{id: "chb", modelName: "model-b"}},
			cardChannel: "gone", wantChannel: "chb",
			released: true, reason: "missing", pinChannel: "gone",
		},
		{
			name: "blocked objective pin opens another channel",
			specs: []pinChannel{
				{id: "cha", modelName: "model-a", pref: 0},
				{id: "chb", modelName: "model-b", pref: 9},
			},
			objChannel: "cha", block: "cha", wantChannel: "chb",
			released: true, reason: "blocked", pinChannel: "cha",
		},
		{
			name: "blocked model pin opens another channel",
			specs: []pinChannel{
				{id: "cha", modelName: "model-a", pref: 0},
				{id: "chb", modelName: "model-b", pref: 9},
			},
			cardModel: "model-a", block: "cha", wantChannel: "chb",
			released: true, reason: "blocked", pinModel: "model-a",
		},
		{
			name: "blocked pin and no candidate asks once",
			specs: []pinChannel{
				{id: "cha", modelName: "model-a"},
			},
			cardChannel: "cha", block: "cha", blockOnCard: true, wantQuestion: true, reason: "blocked",
		},
		{
			name: "unhealthy pin and no candidate asks once",
			specs: []pinChannel{
				{id: "cha", modelName: "model-a", health: model.HealthAuthFailed},
			},
			cardChannel: "cha", wantQuestion: true, reason: "health",
		},
		{
			name:        "missing pin and no candidate asks once",
			cardChannel: "gone", wantQuestion: true, reason: "missing",
		},
		{
			name: "missing card pin does not inherit a below-red-line objective channel",
			specs: []pinChannel{
				{id: "chb", modelName: "model-b", pref: 0, redline: 0.5, remain: 0.1, hasRemain: true},
				{id: "chc", modelName: "model-c", pref: 1, remain: 0.9, hasRemain: true},
			},
			cardChannel: "cha", objChannel: "chb", wantChannel: "chc", wantModel: "model-c",
			released: true, reason: "missing", pinChannel: "cha",
		},
		{
			name: "missing card pin and no candidate above the red line asks once",
			specs: []pinChannel{
				{id: "chb", modelName: "model-b", pref: 0, redline: 0.5, remain: 0.1, hasRemain: true},
			},
			cardChannel: "cha", objChannel: "chb", wantQuestion: true, reason: "missing",
		},
		{
			name: "blocked carrier does not drop a model another carrier can run",
			specs: []pinChannel{
				{id: "cha", modelName: "model-m", pref: 1},
				{id: "chb", modelName: "model-m", pref: 5},
				{id: "chc", modelName: "model-n", pref: 0},
			},
			cardModel: "model-m", block: "cha", wantChannel: "chb", wantModel: "model-m",
		},
		{
			name: "blocked carrier does not drop an objective model another carrier can run",
			specs: []pinChannel{
				{id: "cha", modelName: "model-m", pref: 1},
				{id: "chb", modelName: "model-m", pref: 5},
				{id: "chc", modelName: "model-n", pref: 0},
			},
			objModel: "model-m", block: "cha", wantChannel: "chb", wantModel: "model-m",
		},
		{
			name: "below red line pin stays on that channel",
			specs: []pinChannel{
				{id: "cha", modelName: "model-a", pref: 9, redline: 0.5, remain: 0.1, hasRemain: true},
				{id: "chb", modelName: "model-b", pref: 0, remain: 0.9, hasRemain: true},
			},
			cardChannel: "cha", wantChannel: "cha",
		},
		{
			name: "blocked backlog pin is not rewritten",
			specs: []pinChannel{
				{id: "cha", modelName: "model-a"},
				{id: "chb", modelName: "model-b", remain: 0.9, hasRemain: true, soon: true},
			},
			cardChannel: "cha", block: "cha", blockOnCard: true, backlog: true,
		},
		{
			name: "healthy pin stays on that channel",
			specs: []pinChannel{
				{id: "cha", modelName: "model-a", pref: 9},
				{id: "chb", modelName: "model-b", pref: 0},
			},
			cardChannel: "cha", wantChannel: "cha",
		},
		{
			name: "healthy objective pin stays on that channel",
			specs: []pinChannel{
				{id: "cha", modelName: "model-a", pref: 9},
				{id: "chb", modelName: "model-b", pref: 0},
			},
			objChannel: "cha", wantChannel: "cha",
		},
		{name: "empty tier", classify: "empty", call: t1, wantTier: model.TierT1, wantCalls: 1},
		{name: "preset tier", classify: "preset", tier: model.TierT3, call: t1, wantTier: model.TierT3},
		{name: "failed call", classify: "failed", call: bad, wantTier: model.DefaultTier(model.KindDev), wantCalls: 1},
		{name: "unparseable call", classify: "junk", call: junk, wantTier: model.DefaultTier(model.KindDev), wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			home, db, repo := newHome(t)
			_, obj := seedRepo(t, db, repo, "true")
			if tc.classify != "" {
				seedRoutes(t, db)
				addClassifierChannel(t, db)
				runClassifyLaunch(t, ctx, home, db, obj, tc.tier, tc.call, tc.wantTier, tc.wantCalls)
				return
			}
			for _, spec := range tc.specs {
				addPinChannel(t, db, spec)
			}
			if tc.objChannel != "" || tc.objModel != "" {
				got, err := db.GetObjective(ctx, obj)
				if err != nil {
					t.Fatal(err)
				}
				got.Channel = tc.objChannel
				got.Model = tc.objModel
				if err := db.UpdateObjective(ctx, &got); err != nil {
					t.Fatal(err)
				}
			}
			cardID := "c1"
			kind := model.KindDev
			if tc.backlog {
				kind = model.KindBacklog
			}
			card := model.Card{
				ID: cardID, Objective: obj, Kind: kind, Title: tc.name, Tier: model.TierT2,
				Status: model.CardReady, Channel: tc.cardChannel, Model: tc.cardModel,
				Acceptance: []string{"true"},
				Body:       `{"attempts":{"0":{"exit_code":0,"output":"ok\n"},"1":{"exit_code":0,"output":"ok\n"}}}`,
			}
			if err := db.CreateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			if tc.block != "" {
				blockTarget := cardID
				if !tc.blockOnCard {
					blockTarget = "spent"
					spent := model.Card{
						ID: blockTarget, Objective: obj, Kind: model.KindDev, Title: "spent",
						Tier: model.TierT2, Status: model.CardDone,
					}
					if err := db.CreateCard(ctx, &spent); err != nil {
						t.Fatal(err)
					}
				}
				seedQuotaBlock(t, db, blockTarget, tc.block)
			}
			if tc.branch {
				branch(t, repo, obj, cardID, "note.txt", "package fixture\n")
			}
			before, err := db.ListAttempts(ctx, cardID)
			if err != nil {
				t.Fatal(err)
			}
			r := New(home)
			r.Prompt = func(context.Context, prompt.Input) (string, error) { return "PROMPT", nil }
			stopLaunchHosts(t, r)
			if err := r.Tick(ctx); err != nil {
				t.Fatal(readLog(home, err))
			}
			if tc.backlog {
				got, err := db.GetCard(ctx, cardID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != model.CardReady || got.Channel != tc.cardChannel || got.Model != tc.cardModel || got.WaitReason != "" {
					t.Fatalf("backlog rewritten %+v", got)
				}
				after, err := db.ListAttempts(ctx, cardID)
				if err != nil {
					t.Fatal(err)
				}
				if len(after) != len(before) {
					t.Fatalf("backlog attempts %d, want %d", len(after), len(before))
				}
				assertObjectiveUntouched(t, ctx, db, obj, tc.objChannel, tc.objModel)
				assertNoQuestion(t, ctx, db, obj, cardID)
				assertNoPinEvent(t, ctx, db, cardID)
				t.Logf("backlog left on today's path; objective Channel and Model unchanged")
				return
			}
			if tc.wantQuestion {
				if err := r.Tick(ctx); err != nil {
					t.Fatal(readLog(home, err))
				}
				assertOneQuestion(t, ctx, db, obj, cardID, tc.reason)
				got, err := db.GetCard(ctx, cardID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != model.CardNeedsInput || got.WaitReason != "" {
					t.Fatalf("status %s wait %q", got.Status, got.WaitReason)
				}
				if got.Channel != tc.cardChannel || got.Model != tc.cardModel {
					t.Fatalf("question rewrote channel %s model %s", got.Channel, got.Model)
				}
				after, err := db.ListAttempts(ctx, cardID)
				if err != nil {
					t.Fatal(err)
				}
				if len(after) != len(before) {
					t.Fatalf("attempts %d, want the original %d", len(after), len(before))
				}
				got.Status = model.CardReady
				if err := db.UpdateCard(ctx, &got); err != nil {
					t.Fatal(err)
				}
				if err := r.Tick(ctx); err != nil {
					t.Fatal(readLog(home, err))
				}
				assertOneQuestion(t, ctx, db, obj, cardID, tc.reason)
				again, err := db.GetCard(ctx, cardID)
				if err != nil {
					t.Fatal(err)
				}
				if again.Status != model.CardNeedsInput {
					t.Fatalf("second launch status %s", again.Status)
				}
				after, err = db.ListAttempts(ctx, cardID)
				if err != nil {
					t.Fatal(err)
				}
				if len(after) != len(before) {
					t.Fatalf("second launch attempts %d, want %d", len(after), len(before))
				}
				assertObjectiveUntouched(t, ctx, db, obj, tc.objChannel, tc.objModel)
				assertNoPinEvent(t, ctx, db, cardID)
				t.Logf("%s exactly one question reason %s; no new attempt; status %s not failed; objective Channel and Model unchanged", pinLogKind(tc.name), tc.reason, again.Status)
				return
			}
			after, err := db.ListAttempts(ctx, cardID)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before)+1 {
				t.Fatalf("attempts %d, want a later attempt after %d", len(after), len(before))
			}
			opened := newestAttempt(after)
			if opened.Route.Channel != tc.wantChannel {
				t.Fatalf("channel %s, want %s", opened.Route.Channel, tc.wantChannel)
			}
			if tc.wantModel != "" && opened.Route.Model != tc.wantModel {
				t.Fatalf("model %s, want %s", opened.Route.Model, tc.wantModel)
			}
			got, err := db.GetCard(ctx, cardID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status == model.CardReady && got.WaitReason == "" && len(after) == len(before) {
				t.Fatalf("card stayed ready with no newer attempt")
			}
			if got.Status == model.CardFailed {
				t.Fatalf("status %s", got.Status)
			}
			assertObjectiveUntouched(t, ctx, db, obj, tc.objChannel, tc.objModel)
			if tc.released {
				if got.Channel != tc.wantChannel || got.Model != opened.Route.Model || got.Model == "" {
					t.Fatalf("writeback channel %s model %s route %+v", got.Channel, got.Model, opened.Route)
				}
				assertPinEvent(t, ctx, db, cardID, tc.pinChannel, tc.pinModel, tc.wantChannel, opened.Route.Model, tc.reason)
				t.Logf("%s switched channel original pin %s/%s new route %s/%s reason %s; objective Channel and Model unchanged", pinLogKind(tc.name), tc.pinChannel, tc.pinModel, got.Channel, got.Model, tc.reason)
			} else {
				if got.Channel != tc.cardChannel || got.Model != tc.cardModel {
					t.Fatalf("pin changed to %s/%s", got.Channel, got.Model)
				}
				assertNoPinEvent(t, ctx, db, cardID)
				if strings.Contains(tc.name, "below red") {
					t.Logf("below-red-line pin not released; objective Channel and Model unchanged")
				} else {
					t.Logf("%s pin unchanged; objective Channel and Model unchanged", pinLogKind(tc.name))
				}
			}
			if tc.branch {
				tip := rev(t, repo, "github.com/OttoPrua/Cardex/"+obj+"/c/"+cardID)
				if opened.BaseCommit != tip {
					t.Fatalf("base %s, card branch %s", opened.BaseCommit, tip)
				}
			}
		})
	}
}

func pinLogKind(name string) string {
	switch {
	case strings.Contains(name, "quota"):
		return "quota-block"
	case strings.Contains(name, "below red"):
		return "below-red-line"
	case strings.Contains(name, "unhealthy"):
		return "unhealthy"
	case strings.Contains(name, "healthy"):
		return "healthy"
	case strings.Contains(name, "missing"):
		return "missing-channel"
	case strings.Contains(name, "objective"):
		return "objective-pin"
	case strings.Contains(name, "model"):
		return "model-pin"
	default:
		return name
	}
}

type pinChannel struct {
	id, modelName string
	pref          int
	health        model.Health
	redline       float64
	remain        float64
	hasRemain     bool
	soon          bool
}

func addPinChannel(t *testing.T, db *store.Store, spec pinChannel) {
	t.Helper()
	health := spec.health
	if health == "" {
		health = model.HealthOK
	}
	ch := model.Channel{
		ID: spec.id, Runner: model.RunnerFake, Family: spec.id, SourceGroup: spec.id,
		Models: []string{spec.modelName}, Concurrency: 4, Health: health, Tier: model.TierT2,
		Redline: spec.redline,
	}
	if spec.hasRemain {
		reset := time.Now().Add(24 * time.Hour)
		if spec.soon {
			reset = time.Now().Add(30 * time.Minute)
		}
		ch.QuotaWindows = []model.QuotaWindow{windowWithRemaining("5h", reset, spec.remain)}
	}
	if err := db.CreateChannel(context.Background(), &ch); err != nil {
		t.Fatal(err)
	}
	pos := model.Positioning{
		ID: "pos-" + spec.id, Model: spec.modelName, Family: spec.id,
		Tiers: []model.Tier{model.TierT1, model.TierT2, model.TierT3}, Preference: spec.pref,
	}
	if err := db.CreatePositioning(context.Background(), &pos); err != nil {
		t.Fatal(err)
	}
}

func seedQuotaBlock(t *testing.T, db *store.Store, cardID, channelID string) {
	t.Helper()
	reset := time.Now().Add(2 * time.Hour).UTC()
	exit := 1
	att := model.Attempt{
		ID: "prior-" + channelID, Card: cardID, Index: 0,
		Route:        model.Route{Channel: channelID, Model: "model-" + channelID},
		StartedAt:    reset.Add(-3 * time.Hour),
		EndedAt:      reset.Add(-2*time.Hour - time.Minute),
		Exit:         &exit,
		Outcome:      model.OutcomeQuotaHit,
		ErrorClass:   model.ErrorQuotaHit,
		ErrorExcerpt: "quota hit reset at " + reset.Format(time.RFC3339),
		PID:          0,
	}
	if err := db.CreateAttempt(context.Background(), &att); err != nil {
		t.Fatal(err)
	}
}

func newestAttempt(attempts []model.Attempt) model.Attempt {
	var best model.Attempt
	seen := false
	for _, attempt := range attempts {
		if !seen || attempt.Index > best.Index || (attempt.Index == best.Index && attempt.ID > best.ID) {
			best = attempt
			seen = true
		}
	}
	return best
}

func assertOneQuestion(t *testing.T, ctx context.Context, db *store.Store, obj, cardID, reason string) {
	t.Helper()
	questions, err := db.ListQuestions(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, question := range questions {
		if question.Scope != cardID {
			continue
		}
		n++
		if question.Text == "" || !strings.Contains(question.Text, reason) {
			t.Fatalf("question %q, want reason %s", question.Text, reason)
		}
	}
	if n != 1 {
		t.Fatalf("questions %d, want 1", n)
	}
}

func assertNoQuestion(t *testing.T, ctx context.Context, db *store.Store, obj, cardID string) {
	t.Helper()
	questions, err := db.ListQuestions(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range questions {
		if question.Scope == cardID {
			t.Fatalf("unexpected question %q", question.Text)
		}
	}
}

func assertObjectiveUntouched(t *testing.T, ctx context.Context, db *store.Store, obj, channel, modelName string) {
	t.Helper()
	got, err := db.GetObjective(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	if got.Channel != channel || got.Model != modelName {
		t.Fatalf("objective channel %q model %q, want %q %q", got.Channel, got.Model, channel, modelName)
	}
}

func assertPinEvent(t *testing.T, ctx context.Context, db *store.Store, cardID, pinChannel, pinModel, channel, modelName, reason string) {
	t.Helper()
	events := pinEvents(t, ctx, db, cardID)
	if len(events) != 1 {
		t.Fatalf("pin events %d, want 1", len(events))
	}
	var data struct {
		PinChannel string `json:"pin_channel"`
		PinModel   string `json:"pin_model"`
		Channel    string `json:"channel"`
		Model      string `json:"model"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(events[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.PinChannel != pinChannel || data.PinModel != pinModel || data.Channel != channel || data.Model != modelName || data.Reason != reason {
		t.Fatalf("event %+v", data)
	}
	text := string(events[0].Data)
	for _, part := range []string{pinChannel, pinModel, channel, modelName, reason} {
		if part != "" && !strings.Contains(text, part) {
			t.Fatalf("event text %s missing %s", text, part)
		}
	}
}

func assertNoPinEvent(t *testing.T, ctx context.Context, db *store.Store, cardID string) {
	t.Helper()
	if events := pinEvents(t, ctx, db, cardID); len(events) != 0 {
		t.Fatalf("pin events %+v", events)
	}
}

func pinEvents(t *testing.T, ctx context.Context, db *store.Store, cardID string) []model.Event {
	t.Helper()
	events, err := db.ListEvents(ctx, "card:"+cardID)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Event
	for _, event := range events {
		if event.Type == eventPinReleased {
			out = append(out, event)
		}
	}
	return out
}

func runClassifyLaunch(t *testing.T, ctx context.Context, home string, db *store.Store, obj string, tier model.Tier, call func(context.Context, string, string) (string, error), want model.Tier, wantCalls int) {
	t.Helper()
	calls := 0
	var wrapped func(context.Context, string, string) (string, error)
	if call != nil {
		inner := call
		wrapped = func(ctx context.Context, channelID, promptText string) (string, error) {
			calls++
			if channelID != classifierChannelID || promptText == "" {
				t.Errorf("call channel %q prompt %q", channelID, promptText)
			}
			return inner(ctx, channelID, promptText)
		}
	}
	card := model.Card{
		ID: "c1", Objective: obj, Kind: model.KindDev, Title: "classify", Tier: tier,
		Status: model.CardReady,
		Body:   `{"attempts":{"0":{"exit_code":0,"output":"ok\n"}}}`,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Classify = wrapped
	r.Prompt = func(context.Context, prompt.Input) (string, error) { return "PROMPT", nil }
	stopLaunchHosts(t, r)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	got, err := db.GetCard(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != want || got.Tier == model.TierNone {
		t.Fatalf("stored tier %q, want %q", got.Tier, want)
	}
	if calls != wantCalls {
		t.Fatalf("Call count %d, want %d", calls, wantCalls)
	}
	attempts, err := db.ListAttempts(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Route.Channel == "" {
		t.Fatalf("attempts %+v", attempts)
	}
	for _, cand := range attempts[0].RouteReason.Candidates {
		if cand.Tier != want {
			t.Fatalf("candidate tier %q, want %q", cand.Tier, want)
		}
	}
	if len(attempts[0].RouteReason.Candidates) == 0 {
		t.Fatal("route reason has no candidate")
	}
}

func stopLaunchHosts(t *testing.T, r *Runner) {
	t.Helper()
	trackLaunch(t, r)
}
