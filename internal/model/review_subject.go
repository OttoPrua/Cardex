package model

import "strings"

// ReviewSubjectEvent is recorded on attempt:<id> when that attempt is created.
// Harvest uses it so a card created later cannot receive the verdict.
const ReviewSubjectEvent = "review_subject"

// PinnedReviewSubject is the subject fixed when the attempt's worktree was
// chosen. Harvest must use this value and not resolve the subject again.
// Standalone means the verdict stays on the review card.
type PinnedReviewSubject struct {
	Subject    string `json:"subject"`
	Standalone bool   `json:"standalone"`
}

// ReviewSubject names the card a review reads.
// explicit is true when Deps[0] is set. Otherwise an id with an "rv-" prefix
// names the remainder. Callers that find no stored card with that remainder
// use this review's own id, the same as a review id without the prefix.
func ReviewSubject(card Card) (id string, explicit bool) {
	if len(card.Deps) > 0 {
		if dep := strings.TrimSpace(card.Deps[0]); dep != "" {
			return dep, true
		}
	}
	if strings.HasPrefix(card.ID, "rv-") {
		rest := strings.TrimPrefix(card.ID, "rv-")
		if rest != "" {
			return rest, false
		}
	}
	return card.ID, false
}
