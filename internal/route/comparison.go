package route

import (
	"context"
	"errors"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
)

var ErrStrictComparisonUnavailable = errors.New("strict comparison requires one available compatible channel and exact model")

func selectStrictComparison(ctx context.Context, req Request, tier model.Tier, now time.Time) (model.Route, model.RouteReason, error) {
	reason := emptyReason()
	card := req.Card
	if !card.ValidStrictComparison() {
		return model.Route{}, reason, ErrStrictComparisonUnavailable
	}
	for _, ch := range req.Channels {
		if ch.ID != card.Channel {
			continue
		}
		if ch.Runner != model.RunnerClaude || runner.CompatibleBindingID(ch.EngineProfile) == "" ||
			!offers(ch, card.Model) || req.AvoidChannels[ch.ID] {
			return model.Route{}, reason, ErrStrictComparisonUnavailable
		}
		est, known := fetchEst(ctx, req.Quota, ch.ID)
		if why := rejectReason(cand{ch: ch, est: est, known: known}, req, now); why != "" {
			reason.Filtered = append(reason.Filtered, model.FilteredCandidate{Channel: ch.ID, Model: card.Model, Reason: why})
			return model.Route{}, reason, ErrStrictComparisonUnavailable
		}
		reason.Candidates = append(reason.Candidates, model.RouteCandidate{
			Channel: ch.ID, Model: card.Model, Family: ch.Family, Tier: tier, Remaining: est.Ratio,
		})
		return model.Route{Channel: ch.ID, Model: card.Model}, reason, nil
	}
	return model.Route{}, reason, ErrStrictComparisonUnavailable
}
