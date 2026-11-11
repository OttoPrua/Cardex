package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestDisposeRanking(t *testing.T) {
	valid := `{"entries":[{"model":"corrected-model","family":"newfam","tiers":["T2"],"capabilities":["web"],"preference":1,"理由":"修正"}]}`
	rows := []struct {
		name         string
		outcome      model.Outcome
		class        model.ErrorClass
		evidence     string
		text         string
		status       model.CardStatus
		wantProposal int
		wantQuestion int
		later        bool
	}{
		{
			name: "passed", outcome: model.OutcomePassed, class: model.ErrorPassed,
			evidence: "acceptance=ok", text: valid, status: model.CardReady,
			wantProposal: 1, wantQuestion: 1,
		},
		{
			name: "later attempt", later: true, status: model.CardReady,
		},
		{
			name: "acceptance failed", outcome: model.OutcomeTestsFailed, class: model.ErrorTestsFailed,
			evidence: "acceptance=failed", text: valid, status: model.CardReady,
		},
		{
			name: "stopped", outcome: model.OutcomePassed, class: model.ErrorPassed,
			evidence: "acceptance=ok", text: valid, status: model.CardFailed,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			home, db := rankingHome(t)
			seedRanking(t, db, "old-model")
			ctx := context.Background()
			if row.later {
				disposeRankingText(t, home, db, "a-rank", model.OutcomeResultUnparsable, model.ErrorResultUnparsable, "acceptance=ok", "not a proposal")
				if proposalCount(t) != 0 {
					t.Fatalf("garbage proposals %d", proposalCount(t))
				}
				questions := mustQuestions(t, db)
				if len(questions) != 1 || !strings.Contains(questions[0].Text, "待复核") || !strings.Contains(questions[0].Text, rankingMark) {
					t.Fatalf("review question %+v", questions)
				}
				if err := db.CreateAttempt(ctx, &model.Attempt{ID: "a-next", Card: "rank"}); err != nil {
					t.Fatal(err)
				}
				disposeRankingText(t, home, db, "a-next", model.OutcomePassed, model.ErrorPassed, "acceptance=ok", valid)
				if proposalCount(t) != 1 {
					t.Fatalf("corrected proposals %d", proposalCount(t))
				}
				saved := mustProposals(t)
				if len(saved) != 1 || !strings.Contains(saved[0].Diff, "corrected-model") {
					t.Fatalf("proposal %+v", saved)
				}
				questions = mustQuestions(t, db)
				if len(questions) != 2 {
					t.Fatalf("questions %d", len(questions))
				}
				var corrected bool
				for _, q := range questions {
					if strings.Contains(q.Text, "corrected-model") && strings.Contains(q.Text, rankingMark) {
						corrected = true
					}
				}
				if !corrected {
					t.Fatalf("corrected question missing %+v", questions)
				}
				t.Log("later attempt stored the corrected proposal")
				return
			}
			if row.status != model.CardReady {
				card, err := db.GetCard(ctx, "rank")
				if err != nil {
					t.Fatal(err)
				}
				card.Status = row.status
				if err := db.UpdateCard(ctx, &card); err != nil {
					t.Fatal(err)
				}
			}
			disposeRankingText(t, home, db, "a-rank", row.outcome, row.class, row.evidence, row.text)
			if proposalCount(t) != row.wantProposal {
				t.Fatalf("proposals %d", proposalCount(t))
			}
			if len(mustQuestions(t, db)) != row.wantQuestion {
				t.Fatalf("questions %+v", mustQuestions(t, db))
			}
			if row.wantProposal == 1 {
				disposeRankingText(t, home, db, "a-rank", row.outcome, row.class, row.evidence, row.text)
				if proposalCount(t) != 1 || len(mustQuestions(t, db)) != 1 {
					t.Fatalf("repeat proposals %d questions %d", proposalCount(t), len(mustQuestions(t, db)))
				}
				t.Log("same attempt added neither a proposal nor a question")
			}
		})
	}
}

func disposeRankingText(t *testing.T, home string, db *store.Store, attemptID string, outcome model.Outcome, class model.ErrorClass, evidence, text string) {
	t.Helper()
	card, err := db.GetCard(context.Background(), "rank")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithStore(WithAttempt(WithHome(context.Background(), home), attemptID), db)
	if err := Dispose(ctx, &card, harvest.Result{
		Outcome: outcome, ErrorClass: class, Evidence: evidence, ResultText: text,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRankingHarvestDisposition(t *testing.T) {
	valid := `{"entries":[{"model":"corrected-model","family":"newfam","tiers":["T2"],"capabilities":["web"],"preference":1,"理由":"修正"}]}`
	rows := []struct {
		name     string
		outcome  model.Outcome
		class    model.ErrorClass
		evidence string
		text     string
		absorb   bool
	}{
		{name: "passed", outcome: model.OutcomePassed, class: model.ErrorPassed, evidence: "acceptance=ok", text: valid, absorb: true},
		{name: "format result_unparsable", outcome: model.OutcomeResultUnparsable, class: model.ErrorResultUnparsable, evidence: "acceptance=ok", text: valid, absorb: true},
		{name: "tests_failed", outcome: model.OutcomeTestsFailed, class: model.ErrorTestsFailed, evidence: "acceptance=failed", text: valid},
		{name: "refusal", outcome: model.OutcomeRefusal, class: model.ErrorRefusal, evidence: "acceptance=ok", text: valid},
		{name: "violation", outcome: model.OutcomeViolation, class: model.ErrorViolation, text: valid},
		{name: "budget_limited", outcome: model.OutcomeBudgetLimited, class: model.ErrorBudgetLimited, text: valid},
		{name: "acceptance failed", outcome: model.OutcomePassed, class: model.ErrorPassed, evidence: "acceptance=failed", text: valid},
		{name: "acceptance timeout", outcome: model.OutcomePassed, class: model.ErrorPassed, evidence: "acceptance=timeout", text: valid},
		{name: "violation class", outcome: model.OutcomePassed, class: model.ErrorViolation, evidence: "acceptance=ok", text: valid},
		{name: "no_output", outcome: model.OutcomeNoOutput, class: model.ErrorNoOutput, text: valid},
		{name: "permission_denied", outcome: model.OutcomePermissionDenied, class: model.ErrorPermissionDenied, text: valid},
		{name: "transport", outcome: model.OutcomeTransport, class: model.ErrorTransport, text: valid},
		{name: "timeout", outcome: model.OutcomeTimeout, class: model.ErrorTimeout, text: valid},
		{name: "quota_hit", outcome: model.OutcomeQuotaHit, class: model.ErrorQuotaHit, text: valid},
		{name: "auth_failed", outcome: model.OutcomeAuthFailed, class: model.ErrorAuthFailed, text: valid},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			home, db := rankingHome(t)
			seedRanking(t, db, "old-model")
			ctx := context.Background()
			ch := model.Channel{
				ID: "ch-old", Runner: model.RunnerFake, Family: "oldfam", SourceGroup: "oldfam",
				Models: []string{"old-route"}, Concurrency: 4, Health: model.HealthOK, Tier: model.TierT2,
			}
			if err := db.CreateChannel(ctx, &ch); err != nil {
				t.Fatal(err)
			}
			card, err := db.GetCard(ctx, "rank")
			if err != nil {
				t.Fatal(err)
			}
			card.Status = model.CardRunning
			if err := db.UpdateCard(ctx, &card); err != nil {
				t.Fatal(err)
			}
			disposeRankingText(t, home, db, "a-rank", row.outcome, row.class, row.evidence, row.text)
			if !attemptHasDisposed(t, db, "a-rank") {
				t.Fatal("missing dev_disposed")
			}
			got, err := db.GetCard(ctx, "rank")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status == model.CardRunning || got.Status == model.CardWaiting {
				t.Fatalf("status %s", got.Status)
			}
			questions := mustQuestions(t, db)
			if row.absorb {
				if proposalCount(t) != 1 || len(questions) != 1 {
					t.Fatalf("proposals %d questions %+v", proposalCount(t), questions)
				}
				if got.Status != model.CardDone {
					t.Fatalf("status %s", got.Status)
				}
			} else if !rankingFailureSettled(got, questions) {
				t.Fatalf("not reworked or questioned %+v questions %+v", got, questions)
			}
			if proposalCount(t) != 0 && !row.absorb {
				t.Fatalf("failure stored a proposal")
			}
			if err := devStep(ctx, "obj"); err != nil {
				t.Fatal(err)
			}
			if err := devStep(ctx, "obj"); err != nil {
				t.Fatal(err)
			}
			cards, err := db.ListCards(ctx, "obj")
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range cards {
				if item.Kind == model.KindNormalize {
					t.Fatalf("normalize card %+v", item)
				}
			}
			after, err := db.GetCard(ctx, "rank")
			if err != nil {
				t.Fatal(err)
			}
			if after.Status == model.CardWaiting {
				t.Fatalf("second pass moved ranking to waiting: %s", after.WaitReason)
			}
			disposeRankingText(t, home, db, "a-rank", row.outcome, row.class, row.evidence, row.text)
			if row.absorb && (proposalCount(t) != 1 || len(mustQuestions(t, db)) != 1) {
				t.Fatalf("second dispose proposals %d questions %d", proposalCount(t), len(mustQuestions(t, db)))
			}
		})
	}
}

func rankingFailureSettled(card model.Card, questions []model.Question) bool {
	if card.Status == model.CardRunning || card.Status == model.CardWaiting {
		return false
	}
	if card.ReworkCount > 0 || card.Status == model.CardReady || card.Status == model.CardNeedsInput {
		for _, q := range questions {
			if q.Scope == card.ID && strings.TrimSpace(q.Answer) == "" {
				return true
			}
		}
		return card.ReworkCount > 0 || card.Status == model.CardReady || card.Status == model.CardNeedsInput
	}
	return false
}

func attemptHasDisposed(t *testing.T, db *store.Store, attemptID string) bool {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "attempt:"+attemptID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "dev_disposed" {
			return true
		}
	}
	return false
}
