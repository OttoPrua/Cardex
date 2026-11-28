package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/OttoPrua/Cardex/internal/model"
)

// ReviewProgress is an informational projection, not an authorization or
// merge check. Verdict is nonempty only for an attributable current receipt.
type ReviewProgress struct {
	Phase         string           `json:"phase"`
	Label         string           `json:"label"`
	ReviewCardID  string           `json:"review_card_id,omitempty"`
	Relation      string           `json:"relation,omitempty"`
	Mode          string           `json:"mode,omitempty"`
	Tier          string           `json:"tier,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	Verdict       string           `json:"verdict,omitempty"`
	ReviewStatus  model.CardStatus `json:"review_status,omitempty"`
	RouteReason   string           `json:"route_reason,omitempty"`
	RouteHint     string           `json:"route_hint,omitempty"`
	AttemptID     string           `json:"review_attempt_id,omitempty"`
	ActualChannel string           `json:"actual_channel,omitempty"`
	ActualModel   string           `json:"actual_model,omitempty"`
	ReviewedBase  string           `json:"reviewed_base,omitempty"`
}

var reviewPhaseLabel = map[string]string{
	"not_triggered": "未到复核触发条件", "skipped": "明确跳过",
	"planned": "已规划", "reused": "已复用", "waiting": "等待",
	"paused": "暂停", "no_route": "无可用路由", "started": "已启动",
	"absorbed": "已吸收", "needs_input": "等你决定", "closed": "已结束",
	"unknown": "未知",
}

func (p ReviewProgress) phase(phase string) ReviewProgress {
	p.Phase, p.Label = phase, reviewPhaseLabel[phase]
	return p
}

// Line is a compact stable display; whitespace inside stored data cannot
// inject a new tab-delimited field or line.
func (p ReviewProgress) Line() string {
	fields := []string{"review_progress", "phase=" + p.Phase, "label=" + p.Label}
	for _, item := range [][2]string{
		{"review_card_id", p.ReviewCardID}, {"relation", p.Relation}, {"mode", p.Mode}, {"tier", p.Tier},
		{"reason", p.Reason}, {"verdict", p.Verdict}, {"review_status", string(p.ReviewStatus)},
		{"route_reason", p.RouteReason}, {"route_hint", p.RouteHint}, {"attempt", p.AttemptID},
		{"actual_channel", p.ActualChannel}, {"actual_model", p.ActualModel}, {"reviewed_base", p.ReviewedBase},
	} {
		if item[1] != "" {
			fields = append(fields, item[0]+"="+reviewProgressText(item[1]))
		}
	}
	return strings.Join(fields, "\t")
}

func reviewProgressText(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		if b.Len() >= 192 {
			break
		}
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}

type reviewReceipt struct {
	Card           string `json:"card"`
	Review         string `json:"review"`
	From           string `json:"from"`
	ReviewCard     string `json:"review_card_id"`
	ReviewAttempt  string `json:"review_attempt_id"`
	Mode           string `json:"mode"`
	Tier           string `json:"tier"`
	TierConvention string `json:"tier_convention"`
	Reason         string `json:"reason"`
	Hint           string `json:"hint"`
	Verdict        string `json:"verdict"`
}

type reviewProgressEvent struct {
	model.Event
	reviewReceipt
}

func progressReceipt(e model.Event) (reviewProgressEvent, bool) {
	out := reviewProgressEvent{Event: e}
	if json.Unmarshal(e.Data, &out.reviewReceipt) != nil || e.TS.IsZero() {
		return out, false
	}
	return out, true
}

func progressEvents(events []model.Event) []reviewProgressEvent {
	var out []reviewProgressEvent
	for _, e := range events {
		if parsed, ok := progressReceipt(e); ok {
			out = append(out, parsed)
		}
	}
	// RFC3339Nano text can sort fractions ahead of a whole UTC second.
	// Use parsed times, with stored event ID as a deterministic tie-breaker.
	slices.SortStableFunc(out, func(a, b reviewProgressEvent) int {
		if n := a.TS.Compare(b.TS); n != 0 {
			return n
		}
		// Escalation and previous verdict can share one scheduler tick. An
		// escalation supersedes other plans in that tick independent of ID.
		if (a.Type == "review_escalated") != (b.Type == "review_escalated") {
			if a.Type == "review_escalated" {
				return 1
			}
			return -1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

type reviewObservation struct {
	card         model.Card
	attempt      model.Attempt
	linked       bool
	pinned       bool
	uncertainPin bool
}

func (o reviewObservation) pinnedReuse(author model.Attempt, plan reviewProgressEvent) bool {
	return plan.Type == "review_reused" && plan.Review == o.card.ID && o.pinned &&
		author.HeadCommit != "" && o.attempt.BaseCommit == author.HeadCommit
}

// attemptIsCurrent is the shared freshness and commit check for both attempt
// metadata and verdicts; a late receipt cannot make a pre-plan attempt current.
func (o reviewObservation) attemptIsCurrent(subject string, author model.Attempt, plan reviewProgressEvent) bool {
	a := o.attempt
	if a.ID == "" || a.StartedAt.IsZero() || o.card.Status == model.CardReady {
		return false
	}
	if o.card.ID != subject && author.HeadCommit != "" && a.BaseCommit != author.HeadCommit {
		return false
	}
	return !a.StartedAt.Before(plan.TS) || o.pinnedReuse(author, plan)
}

func (s *Store) ReviewProgress(ctx context.Context, cardID string) (out ReviewProgress, err error) {
	err = s.WithTx(ctx, func(ctx context.Context) error {
		var returnErr error
		out, returnErr = s.reviewProgress(ctx, cardID)
		return returnErr
	})
	return out, err
}

func (s *Store) reviewProgress(ctx context.Context, cardID string) (ReviewProgress, error) {
	parent, err := s.CardWaitState(ctx, cardID)
	if err != nil {
		return ReviewProgress{}, err
	}
	if parent.Status == model.CardCanceled {
		return (ReviewProgress{}).phase("closed"), nil
	}
	author, err := s.latestReviewProgressAttempt(ctx, cardID)
	if err != nil {
		return ReviewProgress{}, err
	}
	parentEvents, err := s.ListEvents(ctx, "card:"+cardID)
	if err != nil {
		return ReviewProgress{}, err
	}
	events := progressEvents(parentEvents)
	var plan reviewProgressEvent
	var currentNamed string
	for _, e := range events {
		if (e.Card != "" && e.Card != cardID) || e.TS.Before(author.StartedAt) {
			continue
		}
		switch e.Type {
		case "review_planned", "review_reused", "review_escalated":
			if e.Review != "" {
				plan, currentNamed = e, e.Review
			}
		case "review_skipped":
			plan, currentNamed = e, ""
		case "review_hold_unmet":
			if parent.Status == model.CardNeedsInput {
				plan, currentNamed = e, ""
			}
		}
	}
	tier, _ := model.TierFromConvention(model.Tier(plan.Tier), plan.TierConvention)
	progress := ReviewProgress{
		Mode: reviewProgressText(plan.Mode), Tier: string(tier),
		Reason: reviewProgressText(plan.Reason),
	}
	switch plan.Type {
	case "review_skipped":
		return progress.phase("skipped"), nil
	case "review_hold_unmet":
		return progress.phase("needs_input"), nil
	case "review_planned":
		progress.Relation = "planned"
	case "review_reused":
		progress.Relation = "reused"
	case "review_escalated":
		progress.Relation = "escalated"
	}

	possible, err := s.loadReviewProgressCandidates(ctx, cardID, currentNamed)
	if err != nil {
		return ReviewProgress{}, err
	}
	var current reviewObservation
	var fallback []reviewObservation
	var ambiguous bool
	for _, item := range possible {
		obs, err := s.observeReview(ctx, cardID, item)
		if err != nil {
			return ReviewProgress{}, err
		}
		// A review attempt launched for an earlier author attempt cannot
		// become the current review of the new candidate.
		if obs.card.ID != cardID && !obs.attempt.StartedAt.IsZero() && obs.attempt.StartedAt.Before(author.StartedAt) &&
			!obs.pinnedReuse(author, plan) {
			// A current plan can explicitly reuse the card with an old
			// completed attempt; show its queue state without that attempt.
			if obs.card.ID != currentNamed || !reviewQueued(obs.card.Status) {
				continue
			}
			obs.attempt = model.Attempt{}
		}
		if obs.uncertainPin {
			ambiguous = true
			continue
		}
		if !obs.linked {
			continue
		}
		if obs.card.ID == currentNamed {
			current = obs
			continue
		}
		if currentNamed == "" && !author.StartedAt.IsZero() && obs.attempt.ID == "" && obs.card.ID != cardID {
			// Manual queued cards have no reliable creation timestamp.
			continue
		}
		fallback = append(fallback, obs)
	}
	if currentNamed != "" && current.card.ID == "" {
		return progress.phase("unknown"), nil
	}
	if currentNamed == "" {
		if ambiguous {
			return progress.phase("unknown"), nil
		}
		var active []reviewObservation
		for _, obs := range fallback {
			if reviewQueued(obs.card.Status) || obs.card.Status == model.CardRunning || obs.card.Status == model.CardHarvesting {
				active = append(active, obs)
			}
		}
		if len(active) != 0 {
			fallback = active
		}
		switch len(fallback) {
		case 0:
			return progress.phase("not_triggered"), nil
		case 1:
			current = fallback[0]
		default:
			return progress.phase("unknown"), nil
		}
	} else if current.card.Status == model.CardCanceled && plan.Type == "review_escalated" {
		// A canceled escalation may be followed by a retry of the previous
		// review. Require a newer attempt, not an older verdict on that card.
		for _, obs := range fallback {
			if obs.card.ID == plan.From && obs.attempt.StartedAt.After(plan.TS) && obs.card.Status != model.CardCanceled {
				if current.card.ID != currentNamed {
					return (ReviewProgress{}).phase("unknown"), nil
				}
				current, progress = obs, ReviewProgress{}
			}
		}
	}

	progress.ReviewCardID, progress.ReviewStatus = current.card.ID, current.card.Status
	a := current.attempt
	if current.attemptIsCurrent(cardID, author, plan) {
		if (current.card.Status == model.CardDone || current.card.Status == model.CardMerged) &&
			(current.card.ID == cardID || (parent.Status != model.CardRunning && parent.Status != model.CardHarvesting)) {
			progress.Verdict = currentReviewVerdict(events, cardID, a, current.card, plan)
		}
		// Dependencies describe the card now, not what a past attempt
		// reviewed. For legacy unpinned attempts an exact settled receipt
		// can corroborate attribution; mutable dependencies alone cannot.
		if current.pinned || progress.Verdict != "" {
			progress.AttemptID = a.ID
			progress.ActualChannel, progress.ActualModel = reviewProgressText(a.Route.Channel), reviewProgressText(a.Route.Model)
			progress.ReviewedBase = reviewProgressText(a.BaseCommit)
		}
	}
	switch current.card.Status {
	case model.CardCanceled, model.CardFailed:
		return progress.phase("closed"), nil
	case model.CardPaused:
		return progress.phase("paused"), nil
	case model.CardNeedsInput:
		return progress.phase("needs_input"), nil
	case model.CardWaiting:
		return progress.phase("waiting"), nil
	case model.CardRunning, model.CardHarvesting:
		if progress.AttemptID != "" && !a.StartedAt.IsZero() &&
			(current.card.Status == model.CardRunning && a.PID > 0 && a.EndedAt.IsZero() ||
				current.card.Status == model.CardHarvesting && !a.EndedAt.IsZero()) {
			return progress.phase("started"), nil
		}
		return progress.phase("unknown"), nil
	case model.CardDone, model.CardMerged:
		if progress.Verdict != "" {
			return progress.phase("absorbed"), nil
		}
		return progress.phase("unknown"), nil
	case model.CardReady:
		routeEvents, err := s.ListEvents(ctx, "card:"+current.card.ID)
		if err != nil {
			return ReviewProgress{}, err
		}
		for _, e := range slices.Backward(progressEvents(routeEvents)) {
			if e.Type != "route_no_candidate" || e.TS.Before(plan.TS) || (e.Card != "" && e.Card != current.card.ID) {
				continue
			}
			if a.ID == "" || !a.EndedAt.IsZero() && a.EndedAt.Before(e.TS) {
				progress.RouteReason, progress.RouteHint = reviewProgressText(e.Reason), reviewProgressText(e.Hint)
				return progress.phase("no_route"), nil
			}
			break
		}
		if progress.Relation == "reused" {
			return progress.phase("reused"), nil
		}
		return progress.phase("planned"), nil
	default:
		return progress.phase("unknown"), nil
	}
}

func reviewQueued(status model.CardStatus) bool {
	switch status {
	case model.CardReady, model.CardPaused, model.CardNeedsInput, model.CardWaiting:
		return true
	default:
		return false
	}
}

func currentReviewVerdict(events []reviewProgressEvent, subject string, attempt model.Attempt, review model.Card, plan reviewProgressEvent) string {
	if attempt.Outcome != model.OutcomePassed || attempt.EndedAt.IsZero() || attempt.EndedAt.Before(attempt.StartedAt) {
		return ""
	}
	for _, e := range slices.Backward(events) {
		if e.Type != "review_verdict" || e.ReviewCard != review.ID || e.ReviewAttempt != attempt.ID ||
			(e.Card != "" && e.Card != subject) || e.TS.Before(attempt.EndedAt) || e.TS.Before(plan.TS) {
			continue
		}
		switch e.Verdict {
		case "pass", "rework", "blocked", "escalate":
			return e.Verdict
		default:
			return ""
		}
	}
	return ""
}

func (s *Store) latestReviewProgressAttempt(ctx context.Context, cardID string) (model.Attempt, error) {
	var a model.Attempt
	var started, ended string
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id, card_id, pid, started_at, ended_at, outcome, channel, model, base_commit, head_commit
		FROM attempts WHERE card_id=? ORDER BY attempt_index DESC, id DESC LIMIT 1`, cardID).
		Scan(&a.ID, &a.Card, &a.PID, &started, &ended, &a.Outcome, &a.Route.Channel, &a.Route.Model, &a.BaseCommit, &a.HeadCommit)
	if errors.Is(err, sql.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	if a.StartedAt, err = parseTime(started); err != nil {
		return a, err
	}
	a.EndedAt, err = parseTime(ended)
	return a, err
}

func (s *Store) loadReviewProgressCandidates(ctx context.Context, subject, named string) ([]model.Card, error) {
	// Search explicit first dependencies, scheduler-style rv- names, the
	// standalone card itself, exact parent records, and pinned historical
	// subjects (including cross-objective reviews whose deps later changed).
	rows, err := s.q(ctx).QueryContext(ctx, `WITH pinned AS (
		SELECT a.card_id FROM events e JOIN attempts a ON a.id=substr(e.entity,9)
		WHERE e.event_type=? AND substr(e.entity,1,8)='attempt:'
			AND CASE WHEN json_valid(e.data) THEN json_extract(e.data,'$.subject') END=?
	)
	SELECT id, status, deps FROM cards
	WHERE kind=? AND (id IN (?,?,?) OR
		trim(CASE WHEN json_valid(deps) THEN json_extract(deps,'$[0]') END)=?
		OR id IN (SELECT card_id FROM pinned))
	ORDER BY id`, model.ReviewSubjectEvent, subject, model.KindReview, subject, "rv-"+subject, named, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Card
	for rows.Next() {
		c := model.Card{Kind: model.KindReview}
		var deps string
		if err := rows.Scan(&c.ID, &c.Status, &deps); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(deps), &c.Deps); err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) observeReview(ctx context.Context, subject string, review model.Card) (reviewObservation, error) {
	out := reviewObservation{card: review}
	a, err := s.latestReviewProgressAttempt(ctx, review.ID)
	if err != nil {
		return out, err
	}
	out.attempt = a
	if a.ID != "" {
		events, err := s.ListEvents(ctx, "attempt:"+a.ID)
		if err != nil {
			return out, err
		}
		var pin *model.PinnedReviewSubject
		for _, e := range events {
			if e.Type != model.ReviewSubjectEvent {
				continue
			}
			var p model.PinnedReviewSubject
			if json.Unmarshal(e.Data, &p) != nil || p.Subject == "" || pin != nil && p != *pin {
				out.uncertainPin = true
				return out, nil // inconsistent evidence must not fall back to mutable deps
			}
			pin = &p
		}
		if pin != nil {
			out.pinned = true
			out.linked = !pin.Standalone && pin.Subject == subject || pin.Standalone && subject == review.ID
			return out, nil
		}
	}
	id, explicit := model.ReviewSubject(review)
	out.linked = id == subject
	if !out.linked && !explicit && review.ID == subject {
		var exists int
		err := s.q(ctx).QueryRowContext(ctx, "SELECT 1 FROM cards WHERE id=?", id).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			out.linked = true // the scheduler treats an unresolved rv- name as standalone
		} else if err != nil {
			return out, err
		}
	}
	return out, nil
}
