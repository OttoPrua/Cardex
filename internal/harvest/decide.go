package harvest

import "cardex/internal/model"

type resultState int

const (
	resultMissing resultState = iota
	resultValid
	resultUnparsable
)

type acceptState int

const (
	acceptOK acceptState = iota
	acceptFailed
	acceptTimeout
)

type facts struct {
	ProducesCode bool
	HasChanges   bool
	Acceptance   acceptState
	Result       resultState
	Class        model.ErrorClass
}

// decide applies design §5. Violation wins. An acceptance timeout is timeout.
// A present result file that still cannot be parsed is result_unparsable.
// Acceptance failure is tests_failed unless the runner class is one of the
// retry/block classes. An explicit budget_limited class is not passed, even
// when a code card has changes and a passing acceptance or a non-code card
// has a valid result file. Passed otherwise has those two shapes. No changes
// and no valid result is no_output unless that runner class applies.
func decide(f facts) model.Outcome {
	if f.Class == model.ErrorViolation {
		return model.OutcomeViolation
	}
	if f.Acceptance == acceptTimeout {
		return model.OutcomeTimeout
	}
	if f.Result == resultUnparsable {
		return model.OutcomeResultUnparsable
	}
	if f.Acceptance == acceptFailed {
		if outcome, ok := runnerOutcome(f.Class); ok {
			return outcome
		}
		return model.OutcomeTestsFailed
	}
	if f.Class == model.ErrorBudgetLimited {
		return model.OutcomeBudgetLimited
	}
	if f.ProducesCode && f.HasChanges && f.Acceptance == acceptOK {
		return model.OutcomePassed
	}
	if !f.ProducesCode && f.Result == resultValid {
		return model.OutcomePassed
	}
	if !f.HasChanges && f.Result != resultValid {
		if outcome, ok := runnerOutcome(f.Class); ok {
			return outcome
		}
		return model.OutcomeNoOutput
	}
	if outcome, ok := runnerOutcome(f.Class); ok {
		return outcome
	}
	return model.OutcomeNoOutput
}

func runnerOutcome(class model.ErrorClass) (model.Outcome, bool) {
	switch class {
	case model.ErrorQuotaHit:
		return model.OutcomeQuotaHit, true
	case model.ErrorAuthFailed:
		return model.OutcomeAuthFailed, true
	case model.ErrorTransport:
		return model.OutcomeTransport, true
	case model.ErrorTimeout:
		return model.OutcomeTimeout, true
	case model.ErrorPermissionDenied:
		return model.OutcomePermissionDenied, true
	case model.ErrorRefusal:
		return model.OutcomeRefusal, true
	case model.ErrorBudgetLimited:
		return model.OutcomeBudgetLimited, true
	case model.ErrorViolation:
		return model.OutcomeViolation, true
	default:
		return "", false
	}
}

func producesCode(kind model.CardKind) bool {
	switch kind {
	case model.KindDesign, model.KindResearch, model.KindPlan, model.KindReview,
		model.KindReplan, model.KindNormalize, model.KindRanking:
		return false
	default:
		return true
	}
}
