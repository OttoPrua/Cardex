package loop

import (
	"context"
	"strings"
	"testing"

	"cardex/internal/harvest"
	"cardex/internal/model"
	"cardex/internal/store"
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
