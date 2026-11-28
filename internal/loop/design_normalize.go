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

func normalizationWaitReason(link normalizationLink) string {
	return fmt.Sprintf("%s (source attempt %s; normalization %s)", link.Reason, link.SourceAttempt, link.Normalize)
}

// normalizationMayApply distinguishes a source already applied from a source
// held by a failed earlier attempt on this same normalizer. The latter hold
// remains consumed for replay of the original artifact, but a newer, exactly
// linked normalizer attempt may resolve it if nobody changed the source wait.
func (d Deps) normalizationMayApply(ctx context.Context, source model.Card, link normalizationLink) (bool, error) {
	entity := normalizationPlanEntity(link.SourceCard, link.SourceAttempt)
	events, err := d.Store.ListEvents(ctx, entity)
	if err != nil {
		return false, err
	}
	var blocks []normalizationLink
	for _, event := range events {
		switch event.Type {
		case evAbsorbed:
			return false, nil
		case normalizeBlockedEvent:
			var old normalizationLink
			if json.Unmarshal(event.Data, &old) != nil ||
				old.SourceCard != link.SourceCard || old.SourceAttempt != link.SourceAttempt ||
				old.Normalize != link.Normalize || old.NormalizeAttempt == "" ||
				(old.Reason != normalizeFailed && old.Reason != normalizeInvalid) {
				return false, nil
			}
			blocks = append(blocks, old)
		}
	}
	if len(blocks) == 0 {
		return true, nil
	}
	if source.Status != model.CardNeedsInput || source.WaitQuestionID != "" || link.NormalizeAttempt == "" {
		return false, nil
	}
	attempts, err := d.Store.ListAttempts(ctx, link.Normalize)
	if err != nil {
		return false, err
	}
	current := currentAttempt(attempts)
	if current == nil || current.ID != link.NormalizeAttempt {
		return false, nil
	}
	indices := make(map[string]int, len(attempts))
	for _, attempt := range attempts {
		indices[attempt.ID] = attempt.Index
	}
	var latest normalizationLink
	var latestIndex int
	for _, old := range blocks {
		index, ok := indices[old.NormalizeAttempt]
		if !ok || index >= current.Index {
			return false, nil
		}
		if latest.NormalizeAttempt == "" || index > latestIndex {
			latest, latestIndex = old, index
		} else if index == latestIndex && old != latest {
			return false, nil
		}
	}
	return source.WaitReason == normalizationWaitReason(latest), nil
}

func (d Deps) blockNormalization(ctx context.Context, source model.Card, link normalizationLink) error {
	fresh, err := d.Store.GetCard(ctx, source.ID)
	if err != nil {
		return err
	}
	fresh.Status = model.CardNeedsInput
	fresh.WaitReason = normalizationWaitReason(link)
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
	if link.NormalizeAttempt != "" {
		// A new retry can fail for a different reason. Preserve the typed
		// evidence for every effective attempt, not just the first hold.
		return d.Store.AppendEvent(ctx, &model.Event{Entity: entity, Type: normalizeBlockedEvent, Data: raw})
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
