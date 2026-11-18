package harvest

import (
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestDecideCrashWithoutChangesIsNotTestsFailed(t *testing.T) {
	cases := []struct {
		name string
		f    facts
		want model.Outcome
	}{
		{"crash no changes", facts{ProducesCode: true, Acceptance: acceptFailed, Exit: 1}, model.OutcomeNoOutput},
		{"crash no changes transport", facts{ProducesCode: true, Acceptance: acceptFailed, Exit: 1, Class: model.ErrorTransport}, model.OutcomeTransport},
		{"clean exit no changes", facts{ProducesCode: true, Acceptance: acceptFailed, Exit: 0}, model.OutcomeTestsFailed},
		{"crash with changes", facts{ProducesCode: true, HasChanges: true, Acceptance: acceptFailed, Exit: 1}, model.OutcomeTestsFailed},
	}
	for _, c := range cases {
		if got := decide(c.f); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestDecideReadOnlyCodeCardPassesOnAcceptance(t *testing.T) {
	base := facts{ProducesCode: true, AcceptRan: true, Acceptance: acceptOK, Result: resultValid}
	if got := decide(base); got != model.OutcomePassed {
		t.Fatalf("read-only dev card with passing acceptance: %s", got)
	}
	noAccept := base
	noAccept.AcceptRan = false
	if got := decide(noAccept); got != model.OutcomeNoOutput {
		t.Fatalf("no acceptance commands and no changes: %s", got)
	}
	noResult := base
	noResult.Result = resultMissing
	if got := decide(noResult); got != model.OutcomeNoOutput {
		t.Fatalf("no result file and no changes: %s", got)
	}
	crashed := base
	crashed.Exit = 1
	if got := decide(crashed); got != model.OutcomeNoOutput {
		t.Fatalf("nonzero exit without changes: %s", got)
	}
	quota := base
	quota.Class = model.ErrorQuotaHit
	if got := decide(quota); got != model.OutcomeQuotaHit {
		t.Fatalf("quota hit: %s", got)
	}
}
