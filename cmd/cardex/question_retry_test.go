package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/question"
	"github.com/OttoPrua/Cardex/internal/store"
)

const (
	retryQuestionID  = "q-retry"
	retryCardID      = "c-retry"
	retryObjectiveID = "o-retry"
)

type questionRetryFixture struct {
	obj          model.Objective
	card         model.Card
	q            model.Question
	last         model.Attempt
	offer        model.CardRetryOffer
	skipQuestion bool
}

func seedQuestionRetry(t *testing.T, change func(*questionRetryFixture)) (string, *store.Store) {
	t.Helper()
	home := t.TempDir()
	db := openBriefDB(t, home)
	ctx := t.Context()
	f := questionRetryFixture{
		obj:   model.Objective{ID: retryObjectiveID, Project: "p-retry", Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe},
		card:  model.Card{ID: retryCardID, Objective: retryObjectiveID, Kind: model.KindDev, Status: model.CardNeedsInput, WaitQuestionID: retryQuestionID, Channel: "synthetic-channel", Model: "synthetic-model"},
		q:     model.Question{ID: retryQuestionID, Objective: retryObjectiveID, Scope: retryCardID, ScopeKind: "card", Kind: model.QuestionCustom, Text: "no output after switching channel\n- synthetic diagnostic", Options: []string{"approve", "reject"}},
		offer: model.CardRetryOffer{Card: retryCardID, Attempt: "a-last", Reason: model.CardRetryNoOutput},
		last:  model.Attempt{ID: "a-last", Card: retryCardID, EndedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Outcome: model.OutcomeNoOutput, Usage: model.Usage{Tokens: 10}},
	}
	if change != nil {
		change(&f)
	}
	for _, err := range []error{db.CreateProject(ctx, &model.Project{ID: f.obj.Project, RepoPath: home}), db.CreateObjective(ctx, &f.obj), db.CreateCard(ctx, &f.card), db.CreateAttempt(ctx, &f.last)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !f.skipQuestion {
		if err := db.CreateQuestion(ctx, &f.q); err != nil {
			t.Fatal(err)
		}
		if f.offer.Reason != "" {
			payload, err := json.Marshal(f.offer)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.AppendEvent(ctx, &model.Event{Entity: "question:" + f.q.ID, Type: model.EventCardRetryOffered, Data: payload}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return home, db
}

func TestQuestionAnswerRetryCLI(t *testing.T) {
	home, db := seedQuestionRetry(t, nil)
	ctx := t.Context()
	code, out, errText := runMain(t, "--home", home, "--json", "question", "answer", retryQuestionID, "approve", "--retry")
	if code != 0 || errText != "" {
		t.Fatalf("retry: code=%d, %s", code, errText)
	}
	var result question.RetryResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || result != (question.RetryResult{Question: retryQuestionID, Answer: "approve", Card: retryCardID, Status: model.CardReady}) {
		t.Fatalf("JSON=%q parsed=%+v err=%v", out, result, err)
	}
	c := mustGetCard(t, db, retryCardID)
	if c.Status != model.CardReady || c.WaitQuestionID != "" || c.Channel != "synthetic-channel" || c.Model != "synthetic-model" {
		t.Fatalf("unexpected card update: %+v", c)
	}
	q, err := db.GetQuestion(ctx, retryQuestionID)
	if err != nil || q.Answer != "approve" {
		t.Fatalf("answer=%q err=%v", q.Answer, err)
	}
	for _, event := range []struct{ entity, kind string }{{"objective:" + retryObjectiveID, model.EventObjectiveDecision}, {"card:" + retryCardID, "question_retry"}} {
		events, err := db.ListEvents(ctx, event.entity)
		if err != nil || len(events) != 1 || events[0].Type != event.kind || !strings.Contains(string(events[0].Data), retryQuestionID) {
			t.Fatalf("audit %s: %+v, %v", event.entity, events, err)
		}
	}
	if len(mustAttempts(t, db, retryCardID)) != 1 {
		t.Fatal("answer must not start another attempt")
	}
	legacyHome, _ := seedQuestionRetry(t, nil)
	oldAnswer := mustHome(t, legacyHome, "--json", "question", "answer", retryQuestionID, "approve")
	oldRetry := mustHome(t, legacyHome, "--json", "card", "retry", retryCardID)
	t.Logf("synthetic CLI stages: 2 calls / %d JSON bytes -> 1 call / %d JSON bytes", len(oldAnswer)+len(oldRetry), len(out))
	code, _, errText = runMain(t, "--home", home, "question", "answer", "--retry", retryQuestionID, "approve")
	if code != 1 || !strings.Contains(errText, "already answered") {
		t.Fatalf("retry replay: code=%d, %q", code, errText)
	}
	if got, _ := db.ListEvents(ctx, "card:"+retryCardID); len(got) != 1 {
		t.Fatal("retry replay created an extra event")
	}
}

func TestQuestionAnswerRetryTransientExhaustion(t *testing.T) {
	home, _ := seedQuestionRetry(t, func(f *questionRetryFixture) {
		f.q.Text = "no other channel is available"
		f.last.Outcome = model.OutcomeTimeout
		f.offer.Reason = model.CardRetryChannelsExhausted
	})
	code, out, errText := runMain(t, "--home", home, "question", "answer", "--retry", retryQuestionID, "approve")
	if code != 0 || out != retryQuestionID+"\tapprove\t"+retryCardID+"\tready\n" || errText != "" {
		t.Fatalf("retry: code=%d, out=%q, err=%q", code, out, errText)
	}
}

func TestQuestionAnswerRetryRefusesUnsafeStates(t *testing.T) {
	tests := []struct {
		name    string
		option  string
		reason  string
		edit    func(*questionRetryFixture)
		prepare func(*testing.T, *store.Store)
	}{
		{name: "negative option", option: "reject", reason: "stored approve"},
		{name: "already rejected", reason: "already answered", edit: func(f *questionRetryFixture) { f.q.Answer = "reject" }},
		{name: "not a stored option", reason: "stored approve", edit: func(f *questionRetryFixture) { f.q.Options = []string{"reject"} }},
		{name: "freeform answer", reason: "stored approve", edit: func(f *questionRetryFixture) { f.q.Options = nil }},
		{name: "node question", reason: "not a card retry", edit: func(f *questionRetryFixture) { f.q.ScopeKind = "node"; f.q.Kind = model.QuestionReopen }},
		{name: "unknown current link", reason: "current retry blocker", edit: func(f *questionRetryFixture) { f.card.WaitQuestionID = "" }},
		{name: "stale current link", reason: "current retry blocker", edit: func(f *questionRetryFixture) { f.card.WaitQuestionID = "q-new" }},
		{name: "other objective", reason: "current retry blocker", edit: func(f *questionRetryFixture) { f.q.Objective = "o-other" }},
		{name: "review card", reason: "current retry blocker", edit: func(f *questionRetryFixture) { f.card.Kind = model.KindReview }},
		{name: "canceled card", reason: "current retry blocker", edit: func(f *questionRetryFixture) { f.card.Status = model.CardCanceled }},
		{name: "imported history", reason: "current retry blocker", edit: func(f *questionRetryFixture) { f.card.Legacy.Status = "failed" }},
		{name: "separate wait reason", reason: "current retry blocker", edit: func(f *questionRetryFixture) { f.card.WaitReason = "review required" }},
		{name: "paused objective", reason: "not active", edit: func(f *questionRetryFixture) { f.obj.Status = model.ObjectivePaused }},
		{name: "unfinished attempt", reason: "no settled", edit: func(f *questionRetryFixture) { f.last.Outcome = ""; f.last.EndedAt = time.Time{} }},
		{name: "authorization outcome", reason: "scheduler did not offer", edit: func(f *questionRetryFixture) {
			f.q.Text = "no other channel is available"
			f.last.Outcome = model.OutcomeAuthFailed
		}},
		{name: "permission outcome", reason: "scheduler did not offer", edit: func(f *questionRetryFixture) { f.last.Outcome = model.OutcomePermissionDenied }},
		{name: "quota outcome", reason: "scheduler did not offer", edit: func(f *questionRetryFixture) { f.last.Outcome = model.OutcomeQuotaHit }},
		{name: "review constraint", reason: "scheduler did not offer", edit: func(f *questionRetryFixture) { f.q.Text = "review hold unmet"; f.offer.Reason = "" }},
		{name: "matching description cannot offer a retry", reason: "scheduler did not offer", edit: func(f *questionRetryFixture) { f.offer.Reason = "" }},
		{name: "different attempt cannot use old offer", reason: "scheduler did not offer", edit: func(f *questionRetryFixture) { f.offer.Attempt = "a-previous" }},
		{name: "different card cannot use another offer", reason: "scheduler did not offer", edit: func(f *questionRetryFixture) { f.offer.Card = "other" }},
		{name: "later attempt makes the offer stale", reason: "scheduler did not offer", prepare: func(t *testing.T, db *store.Store) {
			if err := db.CreateAttempt(t.Context(), &model.Attempt{ID: "a-newer", Card: retryCardID, Index: 1, EndedAt: time.Now(), Outcome: model.OutcomeNoOutput}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "permission error inside generic outcome", reason: "scheduler did not offer", edit: func(f *questionRetryFixture) { f.last.ErrorClass = model.ErrorPermissionDenied }},
		{name: "auth error inside generic outcome", reason: "scheduler did not offer", edit: func(f *questionRetryFixture) { f.last.ErrorClass = model.ErrorAuthFailed }},
		{name: "budget question", reason: "not a card retry", edit: func(f *questionRetryFixture) { f.q.Kind = model.QuestionBudget }},
		{name: "objective budget used across cards", reason: "hard token budget", edit: func(f *questionRetryFixture) { f.obj.Budget.HardTokens = 16 }, prepare: func(t *testing.T, db *store.Store) {
			if err := db.CreateCard(t.Context(), &model.Card{ID: "side-card", Objective: retryObjectiveID, Status: model.CardDone, Kind: model.KindDev}); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateAttempt(t.Context(), &model.Attempt{ID: "side-attempt", Card: "side-card", Outcome: model.OutcomePassed, Usage: model.Usage{Tokens: 6}}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "objective budget pending", reason: "pending budget", prepare: func(t *testing.T, db *store.Store) {
			if err := db.CreateQuestion(t.Context(), &model.Question{ID: "q-budget", Objective: retryObjectiveID, Scope: retryObjectiveID, ScopeKind: "objective", Kind: model.QuestionBudget}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "another objective's question rolls back both writes", reason: "another pending", prepare: func(t *testing.T, db *store.Store) {
			if err := db.CreateQuestion(t.Context(), &model.Question{ID: "q-other", Objective: "o-other", Scope: retryCardID, ScopeKind: "card", Kind: model.QuestionCustom, Text: "synthetic review"}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "reopened node", reason: "not on a frozen node", edit: func(f *questionRetryFixture) { f.card.Node = "n-retry" }, prepare: func(t *testing.T, db *store.Store) {
			if err := db.CreateNode(t.Context(), &model.DesignNode{ID: "n-retry", Objective: retryObjectiveID, Layer: model.LayerModule, Status: model.NodeReopened}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, db := seedQuestionRetry(t, tt.edit)
			ctx := t.Context()
			if tt.prepare != nil {
				tt.prepare(t, db)
			}
			before := mustGetCard(t, db, retryCardID)
			qBefore, err := db.GetQuestion(ctx, retryQuestionID)
			if err != nil {
				t.Fatal(err)
			}
			option := tt.option
			if option == "" {
				option = "approve"
			}
			beforeEvents, err := db.ListEvents(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			code, out, errText := runMain(t, "--home", home, "question", "answer", retryQuestionID, option, "--retry")
			if code != 1 || out != "" || !strings.Contains(errText, tt.reason) {
				t.Fatalf("code=%d out=%q err=%q", code, out, errText)
			}
			after := mustGetCard(t, db, retryCardID)
			qAfter, err := db.GetQuestion(ctx, retryQuestionID)
			if err != nil {
				t.Fatal(err)
			}
			if before.Status != after.Status || before.WaitReason != after.WaitReason || before.WaitQuestionID != after.WaitQuestionID || qBefore.Answer != qAfter.Answer {
				t.Fatal("rejected retry modified state")
			}
			events, err := db.ListEvents(ctx, "")
			if err != nil || !reflect.DeepEqual(beforeEvents, events) {
				t.Fatalf("rejected retry left events: %+v, %v", events, err)
			}
		})
	}
}

func TestQuestionAnswerWithoutRetryDoesNotRequeue(t *testing.T) {
	home, db := seedQuestionRetry(t, nil)
	code, out, errText := runMain(t, "--home", home, "question", "answer", retryQuestionID, "reject")
	if code != 0 || errText != "" || out != retryQuestionID+"\treject\n" {
		t.Fatalf("plain answer: code=%d out=%q err=%q", code, out, errText)
	}
	q, err := db.GetQuestion(t.Context(), retryQuestionID)
	if err != nil || q.Answer != "reject" {
		t.Fatalf("answer=%q err=%v", q.Answer, err)
	}
	c := mustGetCard(t, db, retryCardID)
	if c.Status != model.CardNeedsInput || c.WaitQuestionID != retryQuestionID {
		t.Fatalf("plain answer requeued: %+v", c)
	}
}

func TestQuestionAnswerRetryFromSchedulerOffer(t *testing.T) {
	for _, reason := range []model.CardRetryReason{model.CardRetryNoOutput, model.CardRetryChannelsExhausted} {
		t.Run(string(reason), func(t *testing.T) {
			home, db := seedQuestionRetry(t, func(f *questionRetryFixture) {
				f.skipQuestion = true
				f.card.Status = model.CardHarvesting
				if reason == model.CardRetryNoOutput {
					f.last.Index = 1
				}
			})
			t.Setenv("CARDEX_HOME", home)
			ctx := t.Context()
			if reason == model.CardRetryNoOutput {
				earlier := model.Attempt{ID: "a-earlier", Card: retryCardID, Index: 0, EndedAt: time.Now(), Outcome: model.OutcomeNoOutput}
				if err := db.CreateAttempt(ctx, &earlier); err != nil {
					t.Fatal(err)
				}
				if err := db.AppendEvent(ctx, &model.Event{Entity: "attempt:" + earlier.ID, Type: "dev_disposed"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := loop.DevStep(ctx, retryObjectiveID); err != nil {
				t.Fatal(err)
			}
			card := mustGetCard(t, db, retryCardID)
			if card.Status != model.CardNeedsInput || card.WaitQuestionID == "" {
				t.Fatalf("scheduler did not create blocker: %+v", card)
			}
			events, err := db.ListEvents(ctx, "question:"+card.WaitQuestionID)
			if err != nil || len(events) != 1 || events[0].Type != model.EventCardRetryOffered {
				t.Fatalf("retry offer: %+v err=%v", events, err)
			}
			var got model.CardRetryOffer
			if err := json.Unmarshal(events[0].Data, &got); err != nil || got != (model.CardRetryOffer{Card: retryCardID, Attempt: "a-last", Reason: reason}) {
				t.Fatalf("retry offer: %+v err=%v", got, err)
			}
			count := len(mustAttempts(t, db, retryCardID))
			code, out, errText := runMain(t, "--home", home, "question", "answer", card.WaitQuestionID, "approve", "--retry")
			if code != 0 || errText != "" || !strings.HasSuffix(out, "\tready\n") {
				t.Fatalf("code=%d out=%q err=%q", code, out, errText)
			}
			if got := mustGetCard(t, db, retryCardID); got.Status != model.CardReady || got.WaitQuestionID != "" {
				t.Fatalf("not requeued: %+v", got)
			}
			if len(mustAttempts(t, db, retryCardID)) != count {
				t.Fatal("answer must not start an executor")
			}
		})
	}
}

func TestQuestionAnswerRetryProducerRejectsMisleadingQuestion(t *testing.T) {
	for _, name := range []string{"prior question", "permission error"} {
		t.Run(name, func(t *testing.T) {
			home, db := seedQuestionRetry(t, func(f *questionRetryFixture) {
				f.skipQuestion = name == "permission error"
				f.offer.Reason = ""
				f.card.Status = model.CardHarvesting
				if name == "permission error" {
					f.last.ErrorClass = model.ErrorPermissionDenied
				}
			})
			t.Setenv("CARDEX_HOME", home)
			ctx := t.Context()
			if err := loop.DevStep(ctx, retryObjectiveID); err != nil {
				t.Fatal(err)
			}
			card := mustGetCard(t, db, retryCardID)
			if card.Status != model.CardNeedsInput || card.WaitQuestionID == "" {
				t.Fatalf("scheduler did not create blocker: %+v", card)
			}
			events, err := db.ListEvents(ctx, "question:"+card.WaitQuestionID)
			if err != nil || len(events) != 0 {
				t.Fatalf("unsafe retry offer: %+v err=%v", events, err)
			}
			code, out, errText := runMain(t, "--home", home, "question", "answer", card.WaitQuestionID, "approve", "--retry")
			if code != 1 || out != "" || !strings.Contains(errText, "scheduler did not offer") {
				t.Fatalf("code=%d out=%q err=%q", code, out, errText)
			}
		})
	}
}

func TestQuestionAnswerEscapesLiteralRetryOption(t *testing.T) {
	home, db := seedQuestionRetry(t, func(f *questionRetryFixture) { f.q.Options = []string{"--retry"} })
	code, out, errText := runMain(t, "--home", home, "question", "answer", "--", retryQuestionID, "--retry")
	if code != 0 || out != retryQuestionID+"\t--retry\n" || errText != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errText)
	}
	if got := mustGetCard(t, db, retryCardID); got.Status != model.CardNeedsInput {
		t.Fatalf("literal option requeued: %+v", got)
	}
}
