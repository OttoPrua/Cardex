package model

// EventCardRetryOffered records that the scheduler offered an operator retry.
// The event entity is question:<id>; display text cannot grant retry eligibility.
const EventCardRetryOffered = "card_retry_offered"

type CardRetryReason string

const (
	CardRetryNoOutput          CardRetryReason = "no_output_after_switch"
	CardRetryChannelsExhausted CardRetryReason = "transient_channels_exhausted"
)

// CardRetryOffer ties a question to the particular settled failure it can retry.
type CardRetryOffer struct {
	Card    string          `json:"card"`
	Attempt string          `json:"attempt"`
	Reason  CardRetryReason `json:"reason"`
}

// CanRetryTransient excludes review/ranking and design decisions from operator
// retries. Those control flows must use their own decisions.
func (k CardKind) CanRetryTransient() bool {
	switch k {
	case KindDev, KindSkeleton, KindRepair, KindRebase, KindBacklog:
		return true
	default:
		return false
	}
}

// Permits rejects managed auth, quota, permissions, and other nontransient
// failures, even if they appear under a generic no-output outcome.
func (r CardRetryReason) Permits(a Attempt) bool {
	if a.ID == "" || a.EndedAt.IsZero() {
		return false
	}
	switch a.Outcome {
	case OutcomeNoOutput:
		return (r == CardRetryNoOutput || r == CardRetryChannelsExhausted) &&
			(a.ErrorClass == ErrorNone || a.ErrorClass == ErrorNoOutput || a.ErrorClass == ErrorPassed)
	case OutcomeTransport:
		return r == CardRetryChannelsExhausted && (a.ErrorClass == ErrorNone || a.ErrorClass == ErrorTransport)
	case OutcomeTimeout:
		return r == CardRetryChannelsExhausted && (a.ErrorClass == ErrorNone || a.ErrorClass == ErrorTimeout)
	default:
		return false
	}
}
