package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

const (
	normalizeResolvedEvent  = "normalize_resolved"
	normalizeBlockedEvent   = "normalize_blocked"
	staleDesignAttemptEvent = "design_stale_attempt_ignored"
)

const (
	normalizeBadArtifact  = "unusable_design_result"
	normalizeBadControl   = "unclear_freeze_control"
	normalizeFailed       = "normalization_failed"
	normalizeInvalid      = "normalization_result_unusable"
	normalizeApplied      = "normalization_applied"
	normalizeStale        = "normalization_source_not_current"
	normalizePriorPending = "previous_normalization_still_pending"
	normalizeMissingLink  = "normalization_link_invalid_or_missing_card"
)

// IsNormalizationFailureWait tells display-only callers that the current
// normalization attempt may carry a more useful failure description.
func IsNormalizationFailureWait(wait string) bool {
	return strings.HasPrefix(wait, normalizeFailed+" (")
}

func (d Deps) noteStaleDesignAttempt(ctx context.Context, cardID, attemptID, currentID string, outcome model.Outcome) error {
	if attemptID == "" {
		return nil
	}
	raw, err := json.Marshal(map[string]string{"card": cardID, "attempt": attemptID, "current_attempt": currentID, "outcome": string(outcome)})
	if err != nil {
		return err
	}
	return d.noteEvent(ctx, attemptKey(attemptID), staleDesignAttemptEvent, raw)
}

type normalizationLink struct {
	SourceCard       string `json:"source_card,omitempty"`
	SourceAttempt    string `json:"source_attempt,omitempty"`
	Normalize        string `json:"normalize"`
	NormalizeAttempt string `json:"normalize_attempt,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

// normalizationSource uses exactly one recorded planning edge as authority.
// Old attempt-scoped events stored only "normalize"; the attempt's DB row
// itself provides lineage. No current-attempt guess is permitted.
func (d Deps) normalizationSource(ctx context.Context, cardID, normalizeID string) (string, bool, error) {
	attempts, err := d.Store.ListAttempts(ctx, cardID)
	if err != nil {
		return "", false, err
	}
	bound := ""
	count := 0
	for _, attempt := range attempts {
		linked, err := d.matchesNormalizationPlan(ctx, attemptKey(attempt.ID), cardID, attempt.ID, normalizeID)
		if err != nil {
			return "", false, err
		}
		if linked {
			count++
			bound = attempt.ID
		}
	}
	// This typed card event only covers the no-attempt compatibility path.
	if len(attempts) == 0 {
		linked, err := d.matchesNormalizationPlan(ctx, "card:"+cardID, cardID, "", normalizeID)
		return "", linked, err
	}
	return bound, count == 1, nil
}

func (d Deps) matchesNormalizationPlan(ctx context.Context, entity, cardID, attemptID, normalizeID string) (bool, error) {
	events, err := d.Store.ListEvents(ctx, entity)
	if err != nil {
		return false, err
	}
	matches := 0
	for _, event := range events {
		if event.Type != NormalizePlannedEvent {
			continue
		}
		var link normalizationLink
		if json.Unmarshal(event.Data, &link) != nil || link.Normalize != normalizeID {
			return false, nil
		}
		if (link.SourceCard != "" && link.SourceCard != cardID) ||
			(link.SourceAttempt != "" && link.SourceAttempt != attemptID) ||
			(attemptID == "" && link.SourceCard != cardID) {
			return false, nil
		}
		matches++
	}
	return matches == 1, nil
}

func normalizationPlanEntity(source, attempt string) string {
	if attempt != "" {
		return attemptKey(attempt)
	}
	return "card:" + source
}

func (d Deps) writeNormalizationPlan(ctx context.Context, link normalizationLink) error {
	entity := normalizationPlanEntity(link.SourceCard, link.SourceAttempt)
	raw, err := json.Marshal(link)
	if err != nil {
		return err
	}
	return d.noteEvent(ctx, entity, NormalizePlannedEvent, raw)
}

func (d Deps) noteNormalizeResolution(ctx context.Context, link normalizationLink) error {
	entity := "card:" + link.Normalize
	if link.NormalizeAttempt != "" {
		entity = attemptKey(link.NormalizeAttempt)
	}
	raw, err := json.Marshal(link)
	if err != nil {
		return err
	}
	return d.noteEvent(ctx, entity, normalizeResolvedEvent, raw)
}

func (d Deps) blockNormalization(ctx context.Context, source model.Card, link normalizationLink) error {
	fresh, err := d.Store.GetCard(ctx, source.ID)
	if err != nil {
		return err
	}
	fresh.Status = model.CardNeedsInput
	fresh.WaitReason = fmt.Sprintf("%s (source attempt %s; normalization %s)", link.Reason, link.SourceAttempt, link.Normalize)
	if err := d.Store.UpdateCard(ctx, &fresh); err != nil {
		return err
	}
	// A blocked disposition is consumed, but nothing was absorbed.
	entity := "card:" + source.ID
	if link.SourceAttempt != "" {
		entity = attemptKey(link.SourceAttempt)
	}
	raw, err := json.Marshal(link)
	if err != nil {
		return err
	}
	return d.noteEvent(ctx, entity, normalizeBlockedEvent, raw)
}

func normalizeResultUsable(source model.Card, text string, outcome model.Outcome) bool {
	if outcome == model.OutcomeResultUnparsable || judgeResultFreeze(text) == freezeUnclear {
		return false
	}
	if !designUnusable(text, outcome) {
		return true
	}
	// A plan and replan may produce the existing raw JSON card-list
	// protocol instead of Markdown sections.
	return (source.Kind == model.KindPlan || source.Kind == model.KindReplan) &&
		!ParseCardList(text, source.Node, nil).Bad
}

func (d Deps) pendingNormalization(ctx context.Context, objective, source string) (string, error) {
	cards, err := d.Store.ListCards(ctx, objective)
	if err != nil {
		return "", err
	}
	for _, c := range cards {
		if c.Kind == model.KindNormalize && len(c.Deps) > 0 && c.Deps[0] == source && !terminalCard(c.Status) {
			_, linked, err := d.normalizationSource(ctx, source, c.ID)
			if err != nil || linked {
				return c.ID, err
			}
		}
	}
	return "", nil
}
