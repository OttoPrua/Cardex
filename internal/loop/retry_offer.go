package loop

import (
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/route"
)

// blockOnRetryQuestion attaches provenance only to newly issued questions.
// The existing question-deduplication policy may reuse a pending custom
// question for a different failure; never authorize retries using that row.
func (st *step) blockOnRetryQuestion(card *model.Card, kind model.QuestionKind, text string, attempt model.Attempt, reason model.CardRetryReason) (string, error) {
	before := len(st.snap.questions)
	id, err := st.blockOnQuestion(card, false, kind, text)
	if err != nil || len(st.snap.questions) == before || kind != model.QuestionCustom || !card.Kind.CanRetryTransient() || !reason.Permits(attempt) {
		return id, err
	}
	if err := st.event("question:"+id, model.EventCardRetryOffered, model.CardRetryOffer{
		Card: card.ID, Attempt: attempt.ID, Reason: reason,
	}); err != nil {
		return id, err
	}
	auto := route.LatestAutoSelection(st.snap.events, card.ID)
	if auto.EventID != "" && auto.Channel == card.Channel && auto.Channel == attempt.Route.Channel &&
		!attempt.StartedAt.IsZero() && !attempt.StartedAt.Before(auto.At) {
		ev, err := route.FallbackRetryOffer(card.ID, route.FallbackRetryBlock{
			Question: id, Attempt: attempt.ID, AutoRoute: auto.EventID,
		})
		if err != nil {
			return id, err
		}
		return id, st.event(ev.Entity, ev.Type, ev.Data)
	}
	return id, nil
}
