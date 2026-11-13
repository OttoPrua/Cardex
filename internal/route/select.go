package route

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/quota"
)

// Select follows design §7.1: pin, tier, candidates, filters, pace order, structured reason.
func Select(ctx context.Context, req Request) (model.Route, model.RouteReason, error) {
	if err := ctx.Err(); err != nil {
		return model.Route{}, emptyReason(), err
	}
	now := req.Now.UTC()
	if req.Now.IsZero() {
		now = time.Now().UTC()
	}
	tier := resolveTier(req)
	if channelID, modelName, pinned := explicitPin(req); pinned && !objectivePinConflicts(req, channelID, modelName) {
		ch, chosen, err := matchPinned(req.Channels, channelID, modelName)
		if err != nil {
			return model.Route{}, emptyReason(), err
		}
		est, _ := fetchEst(ctx, req.Quota, ch.ID)
		cand := model.RouteCandidate{
			Channel: ch.ID, Model: chosen, Family: ch.Family, Tier: tier, Remaining: est.Ratio,
		}
		return model.Route{Channel: ch.ID, Model: chosen}, model.RouteReason{
			Candidates: []model.RouteCandidate{cand},
			Filtered:   []model.FilteredCandidate{},
			Scores:     []model.RouteScore{{Channel: ch.ID, Model: chosen, Score: 0}},
		}, nil
	}

	kept, filtered := rankTier(ctx, req, tier, now)
	if len(kept) == 0 {
		if next, ok := upOne(tier); ok {
			more, extra := rankTier(ctx, req, next, now)
			filtered = append(filtered, extra...)
			kept = more
		}
	}

	reason := emptyReason()
	reason.Filtered = filtered
	if reason.Filtered == nil {
		reason.Filtered = []model.FilteredCandidate{}
	}
	if len(kept) == 0 {
		return model.Route{}, reason, ErrNoCandidate
	}
	for _, item := range kept {
		reason.Candidates = append(reason.Candidates, model.RouteCandidate{
			Channel:    item.ch.ID,
			Model:      item.modelName,
			Family:     item.family,
			Tier:       item.tier,
			Preference: item.pref,
			Remaining:  item.est.Ratio,
		})
		reason.Scores = append(reason.Scores, model.RouteScore{
			Channel: item.ch.ID,
			Model:   item.modelName,
			Score:   item.pace,
		})
	}
	win := kept[0]
	return model.Route{Channel: win.ch.ID, Model: win.modelName}, reason, nil
}

type ranked struct {
	cand
	demoted bool
	pace    float64
}

// rankTier filters one tier and orders whoever remains. Borrow looks at this
// result, so a tier full of unusable candidates does not count as available.
func rankTier(ctx context.Context, req Request, tier model.Tier, now time.Time) ([]ranked, []model.FilteredCandidate) {
	cands, filtered := collect(ctx, req, tier)
	kept := make([]ranked, 0, len(cands))
	for _, c := range cands {
		if reason := rejectReason(c, req, now); reason != "" {
			filtered = append(filtered, model.FilteredCandidate{Channel: c.ch.ID, Model: c.modelName, Reason: reason})
			continue
		}
		item := ranked{cand: c}
		if c.known {
			item.demoted = overUsed(c.est, 90)
			item.pace = paceOf(c.est.Ratio, c.est.Reset, now)
		}
		kept = append(kept, item)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if a.pref != b.pref {
			return a.pref < b.pref
		}
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.known != b.known {
			return a.known
		}
		// SourceGroup is a group key, like preference: demotion and Pace
		// compare only after two candidates share that group.
		if a.ch.SourceGroup != b.ch.SourceGroup {
			return a.ch.SourceGroup < b.ch.SourceGroup
		}
		if a.demoted != b.demoted {
			return !a.demoted
		}
		if a.pace != b.pace {
			return a.pace > b.pace
		}
		if a.ch.ID != b.ch.ID {
			return a.ch.ID < b.ch.ID
		}
		return a.modelName < b.modelName
	})
	return kept, filtered
}

type cand struct {
	ch        model.Channel
	modelName string
	family    string
	tier      model.Tier
	pref      int
	est       quota.Estimate
	known     bool
}

func explicitPin(req Request) (channelID, modelName string, pinned bool) {
	if req.Card.Channel != "" || req.Card.Model != "" {
		return req.Card.Channel, req.Card.Model, true
	}
	if req.Objective.Channel != "" || req.Objective.Model != "" {
		return req.Objective.Channel, req.Objective.Model, true
	}
	return "", "", false
}

// objectivePinConflicts is true only for an objective pin: the card names no
// channel and no model, and either this review card's author family is the
// pin's family, or the pinned selection does not satisfy Card.Requires.
// A found pin with no model name has an empty capability set. A pin that
// cannot be matched is not a conflict by itself.
func objectivePinConflicts(req Request, channelID, modelName string) bool {
	if req.Card.Channel != "" || req.Card.Model != "" {
		return false
	}
	ch, chosen, found := findPinTarget(req.Channels, channelID, modelName)
	// A model-only pin with no healthy provider stays on the pin path.
	// matchPinned returns ErrPinnedUnavailable instead of the candidate pool.
	if channelID == "" && found && ch.Health != model.HealthOK {
		return false
	}
	modelToUse := modelName
	if modelToUse == "" {
		modelToUse = chosen
	}
	family := ""
	var caps []string
	knownModel := false
	if modelToUse != "" {
		for _, row := range req.Positioning {
			if row.Model != modelToUse {
				continue
			}
			family = row.Family
			caps = row.Capabilities
			knownModel = true
			break
		}
	}
	if family == "" && found {
		family = ch.Family
	}
	// A found pin is a known selection even when it names no model. No model
	// name, or a name absent from the positioning table, has an empty
	// capability set.
	if !knownModel && found {
		knownModel = true
	}
	if req.Card.Kind == model.KindReview && family != "" && family == authorFamily(req) {
		return true
	}
	if knownModel && !hasCaps(caps, req.Card.Requires) {
		return true
	}
	return false
}

// findPinTarget locates the channel and model a pin would use. A named
// channel is returned as listed; matchPinned still applies health. A
// model-only pin uses the first healthy provider, or the first provider
// when none are healthy, so family and Requires see the channel matchPinned
// would return.
func findPinTarget(channels []model.Channel, channelID, modelName string) (model.Channel, string, bool) {
	if channelID != "" {
		for _, ch := range channels {
			if ch.ID != channelID {
				continue
			}
			chosen := modelName
			if chosen == "" && len(ch.Models) > 0 {
				chosen = ch.Models[0]
			}
			return ch, chosen, true
		}
		return model.Channel{}, modelName, false
	}
	var fallback model.Channel
	found := false
	for _, ch := range channels {
		if modelName == "" || !offers(ch, modelName) {
			continue
		}
		if ch.Health == model.HealthOK {
			return ch, modelName, true
		}
		if !found {
			fallback = ch
			found = true
		}
	}
	if found {
		return fallback, modelName, true
	}
	return model.Channel{}, modelName, false
}

func matchPinned(channels []model.Channel, channelID, modelName string) (model.Channel, string, error) {
	if channelID != "" {
		for _, ch := range channels {
			if ch.ID != channelID {
				continue
			}
			if ch.Health != model.HealthOK {
				return model.Channel{}, "", ErrPinnedUnavailable
			}
			chosen := modelName
			if chosen == "" && len(ch.Models) > 0 {
				chosen = ch.Models[0]
			}
			return ch, chosen, nil
		}
		return model.Channel{}, "", ErrPinnedUnavailable
	}
	var fallback model.Channel
	found := false
	for _, ch := range channels {
		if !offers(ch, modelName) {
			continue
		}
		if ch.Health == model.HealthOK {
			return ch, modelName, nil
		}
		if !found {
			fallback = ch
			found = true
		}
	}
	if found && fallback.Health != model.HealthOK {
		return model.Channel{}, "", ErrPinnedUnavailable
	}
	return model.Channel{}, "", ErrPinnedUnavailable
}

func resolveTier(req Request) model.Tier {
	if req.Card.Tier.Valid() && req.Card.Tier != model.TierNone {
		return req.Card.Tier
	}
	if req.Classified.Valid() && req.Classified != model.TierNone {
		return req.Classified
	}
	return model.DefaultTier(req.Card.Kind)
}

func upOne(t model.Tier) (model.Tier, bool) {
	switch t {
	case model.TierT1:
		return model.TierT2, true
	case model.TierT2:
		return model.TierT3, true
	default:
		return "", false
	}
}

func authorFamily(req Request) string {
	if req.AuthorFamily != "" {
		return req.AuthorFamily
	}
	return req.Card.AuthorFamily
}

func collect(ctx context.Context, req Request, tier model.Tier) ([]cand, []model.FilteredCandidate) {
	author := authorFamily(req)
	review := req.Card.Kind == model.KindReview
	var cands []cand
	var filtered []model.FilteredCandidate
	for _, row := range req.Positioning {
		if row.Model == "" || !tierListed(row.Tiers, tier) || !hasCaps(row.Capabilities, req.Card.Requires) {
			continue
		}
		for _, ch := range req.Channels {
			if !offers(ch, row.Model) {
				continue
			}
			fam := row.Family
			if fam == "" {
				fam = ch.Family
			}
			if review && fam == author {
				filtered = append(filtered, model.FilteredCandidate{Channel: ch.ID, Model: row.Model, Reason: ReasonFamily})
				continue
			}
			est, known := fetchEst(ctx, req.Quota, ch.ID)
			cands = append(cands, cand{
				ch: ch, modelName: row.Model, family: fam, tier: tier, pref: row.Preference, est: est, known: known,
			})
		}
	}
	return cands, filtered
}

func rejectReason(c cand, req Request, now time.Time) string {
	if c.ch.Health != model.HealthOK {
		return ReasonHealth
	}
	inUse := 0
	if req.InUse != nil {
		inUse = req.InUse[c.ch.ID]
	}
	if c.ch.Concurrency > 0 && inUse >= c.ch.Concurrency {
		return ReasonConcurrency
	}
	if c.known && belowRedline(c.est, c.ch.Redline) {
		return ReasonRedline
	}
	if c.est.Blocked && !c.est.Reset.IsZero() && now.Before(c.est.Reset) {
		return ReasonBlocked
	}
	if c.known && overUsed(c.est, 98) {
		return ReasonCutoff
	}
	if !c.known && excludeUnknown(req) {
		return ReasonUnknownQuota
	}
	return ""
}

func excludeUnknown(req Request) bool {
	return strings.EqualFold(strings.TrimSpace(req.UnknownQuota), "exclude")
}

func offers(ch model.Channel, modelName string) bool {
	for _, name := range ch.Models {
		if name == modelName {
			return true
		}
	}
	return false
}

func tierListed(tiers []model.Tier, want model.Tier) bool {
	for _, t := range tiers {
		if t == want {
			return true
		}
	}
	return false
}

// hostProvided are card requirements the host meets itself, for any model:
// "browser" means the host starts a Chrome for the attempt. They are not
// model capabilities and do not narrow routing.
var hostProvided = map[string]struct{}{"browser": {}}

func hasCaps(have, need []string) bool {
	if len(need) == 0 {
		return true
	}
	set := map[string]struct{}{}
	for _, item := range have {
		set[item] = struct{}{}
	}
	for _, item := range need {
		if _, host := hostProvided[item]; host {
			continue
		}
		if _, ok := set[item]; !ok {
			return false
		}
	}
	return true
}

func fetchEst(ctx context.Context, q Quotas, id string) (quota.Estimate, bool) {
	if q == nil {
		return quota.Estimate{Unknown: true}, false
	}
	est, err := q.Remaining(ctx, id)
	if err != nil || est.Unknown || (est.LimitTokens <= 0 && est.LimitRequests <= 0) {
		if err != nil {
			return quota.Estimate{Unknown: true}, false
		}
		return est, false
	}
	return est, true
}

func paceOf(ratio float64, reset, now time.Time) float64 {
	hours := reset.Sub(now).Hours()
	if hours < 1 {
		hours = 1
	}
	return ratio / hours
}

func overUsed(est quota.Estimate, pct int64) bool {
	hit := false
	if est.LimitTokens > 0 {
		hit = est.UsedTokens*100 > est.LimitTokens*pct
	}
	if est.LimitRequests > 0 && est.UsedRequests*100 > est.LimitRequests*pct {
		hit = true
	}
	if est.LimitTokens <= 0 && est.LimitRequests <= 0 {
		return (1 - est.Ratio) > float64(pct)/100+1e-9
	}
	return hit
}

func belowRedline(est quota.Estimate, redline float64) bool {
	if est.Unknown {
		return false
	}
	ppm := int64(math.Round(redline * 1_000_000))
	hit := false
	have := false
	if est.LimitTokens > 0 {
		have = true
		remain := est.LimitTokens - est.UsedTokens
		if remain < 0 || remain*1_000_000 <= est.LimitTokens*ppm {
			hit = true
		}
	}
	if est.LimitRequests > 0 {
		have = true
		remain := est.LimitRequests - est.UsedRequests
		if remain < 0 || remain*1_000_000 <= est.LimitRequests*ppm {
			hit = true
		}
	}
	if !have {
		return est.Ratio <= redline
	}
	return hit
}

func emptyReason() model.RouteReason {
	return model.RouteReason{
		Candidates: []model.RouteCandidate{},
		Filtered:   []model.FilteredCandidate{},
		Scores:     []model.RouteScore{},
	}
}
