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
