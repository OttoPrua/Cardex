package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
	"github.com/OttoPrua/Cardex/internal/quota"
	"github.com/OttoPrua/Cardex/internal/route"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

var clockRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

type harvested struct {
	cardID    string
	attemptID string
}

func (st *step) disposeAll() error {
	var todo []harvested
	for _, card := range st.snap.cards {
		for _, attempt := range st.snap.attempts[card.ID] {
			if attempt.Outcome == "" || st.snap.disposed(attempt.ID) {
				continue
			}
			todo = append(todo, harvested{cardID: card.ID, attemptID: attempt.ID})
		}
	}
	sort.Slice(todo, func(i, j int) bool {
		ai := st.attempt(todo[i].cardID, todo[i].attemptID)
		aj := st.attempt(todo[j].cardID, todo[j].attemptID)
		if ai.Index != aj.Index {
			return ai.Index < aj.Index
		}
		if ai.Card != aj.Card {
			return ai.Card < aj.Card
		}
		return ai.ID < aj.ID
	})
	for _, item := range todo {
		card, ok := st.snap.card(item.cardID)
		if !ok {
			continue
		}
		attempt := st.attempt(item.cardID, item.attemptID)
		if err := st.decideHarvest(card, attempt); err != nil {
			return err
		}
	}
	return nil
}

func (st *step) attempt(cardID, attemptID string) model.Attempt {
	for _, attempt := range st.snap.attempts[cardID] {
		if attempt.ID == attemptID {
			return attempt
		}
	}
	return model.Attempt{}
}

// decideHarvest is the one dev, review, and normalize disposition.
// Dispose and disposeAll both call it.
func (st *step) decideHarvest(card model.Card, attempt model.Attempt) error {
	if err := st.apply(card, attempt); err != nil {
		return err
	}
	if !designKind(card.Kind) || !designFailureOutcome(attempt.Outcome) {
		return nil
	}
	current := card
	if saved, ok := st.snap.card(card.ID); ok {
		current = saved
	}
	if err := finishDesignFailure(st.ctx, st.db, &current, attempt); err != nil {
		return err
	}
	st.snap.putCard(current)
	return nil
}

func (st *step) apply(card model.Card, attempt model.Attempt) error {
	// A violation voids the normalize attempt and leaves the original waiting.
	if card.Kind == model.KindNormalize && attempt.Outcome != "" && attempt.Outcome != model.OutcomePassed && attempt.Outcome != model.OutcomeViolation {
		if err := st.failNormalize(card); err != nil {
			return err
		}
		return st.dispose(attempt)
	}
	dec := Decide(card, attempt.Outcome, attempt.ErrorExcerpt, st.cfg.ReworkLimit, st.cfg.TransportRetries, st.budgetLeft(), historyBefore(st.snap.attempts[card.ID], attempt))
	if dec.QuestionText == noOutputQuestion {
		dec.QuestionText = noOutputDetail(st.snap.attempts[card.ID], attempt)
	}
	switch dec.Action {
	case ActionReview:
		if err := st.ensureReview(card, attempt); err != nil {
			return err
		}
	case ActionRework:
		card.ReworkCount = dec.ReworkCount
		if err := st.reschedule(card, attempt, dec); err != nil {
			return err
		}
		if err := st.noteRework(card, attempt, reworkSourceTests, false); err != nil {
			return err
		}
	case ActionQuestion, ActionVoid:
		card.Status = model.CardNeedsInput
		card.WaitReason = ""
		if err := st.saveCard(card); err != nil {
			return err
		}
		if err := st.openQuestion(dec.QuestionKind, card.ID, "card", dec.QuestionText, true); err != nil {
			return err
		}
		if dec.Action == ActionQuestion && reworkLimitQuestion(dec.QuestionText) {
			if err := st.noteRework(card, attempt, reworkSourceTests, true); err != nil {
				return err
			}
		}
		if dec.Action == ActionVoid {
			if err := st.event("attempt:"+attempt.ID, "attempt_voided", map[string]string{
				"branch": attempt.Branch,
				"card":   card.ID,
			}); err != nil {
				return err
			}
		}
	case ActionReschedule, ActionRetrySame, ActionContinue:
		if err := st.reschedule(card, attempt, dec); err != nil {
			return err
		}
	case ActionNormalize:
		if err := st.createNormalize(card); err != nil {
			return err
		}
	case ActionQuotaMove:
		ch, ok := st.snap.channel(attempt.Route.Channel)
		if !ok {
			return fmt.Errorf("quota_hit channel %s missing", attempt.Route.Channel)
		}
		if err := st.blockQuota(ch, attempt.ErrorExcerpt); err != nil {
			return err
		}
		if err := st.reschedule(card, attempt, dec); err != nil {
			return err
		}
	case ActionAuthMove:
		ch, ok := st.snap.channel(attempt.Route.Channel)
		if !ok {
			return fmt.Errorf("auth_failed channel %s missing", attempt.Route.Channel)
		}
		if err := st.markAuth(ch, attempt.ErrorExcerpt, attempt.ID); err != nil {
			return err
		}
		if err := st.reschedule(card, attempt, dec); err != nil {
			return err
		}
	case ActionUnavailableMove:
		ch, ok := st.snap.channel(attempt.Route.Channel)
		if !ok {
			return fmt.Errorf("unavailable channel %s missing", attempt.Route.Channel)
		}
		if err := st.markUnavailable(ch, attempt); err != nil {
			return err
		}
		if err := st.reschedule(card, attempt, dec); err != nil {
			return err
		}
	case ActionAbsorb:
		if err := st.absorb(card, attempt); err != nil {
			return err
		}
	default:
		if dec.NeedsInput {
			card.Status = model.CardNeedsInput
			if err := st.saveCard(card); err != nil {
				return err
			}
		}
	}
	return st.dispose(attempt)
}

func (st *step) budgetLeft() bool {
	hard := st.snap.obj.Budget.HardTokens
	if hard <= 0 {
		return true
	}
	return st.snap.spend() < hard
}

func historyBefore(attempts []model.Attempt, cur model.Attempt) History {
	earlier := make([]model.Attempt, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.ID == cur.ID {
			continue
		}
		if attempt.Index < cur.Index || (attempt.Index == cur.Index && attempt.ID < cur.ID) {
			earlier = append(earlier, attempt)
		}
	}
	sort.Slice(earlier, func(i, j int) bool {
		if earlier[i].Index != earlier[j].Index {
			return earlier[i].Index < earlier[j].Index
		}
		return earlier[i].ID < earlier[j].ID
	})
	var hist History
	for _, attempt := range earlier {
		if attempt.Outcome == model.OutcomeNoOutput {
			hist.NoOutput++
		}
		if attempt.Outcome == model.OutcomeBudgetLimited {
			hist.BudgetLimited++
		}
	}
	for i := len(earlier) - 1; i >= 0; i-- {
		attempt := earlier[i]
		if attempt.Route.Channel != cur.Route.Channel {
			break
		}
		if attempt.Outcome != model.OutcomeTransport && attempt.Outcome != model.OutcomeTimeout {
			break
		}
		hist.SameChannelRetries++
	}
	return hist
}

func (st *step) ensureReview(card model.Card, attempt model.Attempt) error {
	for _, other := range st.snap.cards {
		if other.Kind == model.KindReview && hasDep(other, card.ID) && !TerminalCard(other.Status) {
			return nil
		}
	}
	derived := st.reviewFamily(attempt)
	family := card.AuthorFamily
	if derived != "" && derived != card.AuthorFamily {
		card.AuthorFamily = derived
		family = derived
	}
	card.Status = model.CardReviewing
	if err := st.saveCard(card); err != nil {
		return err
	}
	review := model.Card{
		Objective:    card.Objective,
		Node:         card.Node,
		Kind:         model.KindReview,
		Title:        "review " + card.Title,
		Body:         "reviewer family must differ from " + family,
		Tier:         model.TierT3,
		Status:       model.CardReady,
		Deps:         []string{card.ID},
		AuthorFamily: family,
	}
	_, err := st.createCard(review)
	return err
}

// reviewFamily is the positioning family of the attempt's model.
// An empty family on that row falls back to the successful channel, as does a model with no row.
func (st *step) reviewFamily(attempt model.Attempt) string {
	name := strings.TrimSpace(attempt.Route.Model)
	if name != "" {
		for _, row := range st.snap.positioning {
			if row.Model != name {
				continue
			}
			if family := strings.TrimSpace(row.Family); family != "" {
				return family
			}
			break
		}
	}
	if ch, ok := st.snap.channel(attempt.Route.Channel); ok {
		return strings.TrimSpace(ch.Family)
	}
	return ""
}

func (st *step) createNormalize(card model.Card) error {
	for _, other := range st.snap.cards {
		if other.Kind == model.KindNormalize && hasDep(other, card.ID) && !TerminalCard(other.Status) {
			return nil
		}
	}
	created, err := st.createCard(model.Card{
		Objective: card.Objective,
		Node:      card.Node,
		Kind:      model.KindNormalize,
		Title:     "normalize " + card.Title,
		Tier:      model.TierT1,
		Status:    model.CardReady,
		Deps:      []string{card.ID},
	})
	if err != nil {
		return err
	}
	card.Status = model.CardWaiting
	card.WaitReason = "normalize:" + created.ID
	return st.saveCard(card)
}

func (st *step) failNormalize(card model.Card) error {
	if len(card.Deps) > 0 {
		if parent, ok := st.snap.card(card.Deps[0]); ok {
			parent.Status = model.CardNeedsInput
			parent.WaitReason = ""
			if err := st.saveCard(parent); err != nil {
				return err
			}
		}
	}
	card.Status = model.CardFailed
	return st.saveCard(card)
}

func (st *step) reschedule(card model.Card, attempt model.Attempt, dec Decision) error {
	if dec.UseBranch && attempt.Branch != "" {
		card.Base = attempt.Branch
	}
	card.WaitReason = ""
	if dec.SameChannel {
		if attempt.Route.Channel != "" {
			card.Channel = attempt.Route.Channel
		}
		// Ranking harvest must leave a settled status, so it does not take the dev backoff hold.
		if st.cfg.RetryBackoff > 0 && transportRetry(attempt.Outcome) && card.Kind != model.KindRanking {
			return st.holdBackoff(card)
		}
		card.Status = model.CardReady
		if err := st.saveCard(card); err != nil {
			return err
		}
		if transportRetry(attempt.Outcome) {
			return st.event("card:"+card.ID, "retry_backoff", map[string]string{
				"until":   st.now.UTC().Format(time.RFC3339Nano),
				"channel": card.Channel,
			})
		}
		return nil
	}
	if dec.OtherChannel {
		channelID, modelName, ok, err := st.nextRoute(card, attempt.Route.Channel)
		if err != nil {
			return err
		}
		if !ok {
			card.Status = model.CardNeedsInput
			if err := st.saveCard(card); err != nil {
				return err
			}
			return st.openQuestion(model.QuestionCustom, card.ID, "card", "no other channel is available", true)
		}
		pin := keptPin(st.snap.events, card)
		excluded := autoExcluded(st.snap.events, card.ID)
		if attempt.Route.Channel != "" {
			excluded[attempt.Route.Channel] = true
		}
		// The objective pin stays stored. This card's own later request
		// suppresses it when the recorded channel is one this card failed.
		objChannel := st.snap.obj.Channel
		objModel := st.snap.obj.Model
		modelPin := card.Model
		card.Model = ""
		card.Channel = channelID
		card.Status = model.CardReady
		if err := st.saveCard(card); err != nil {
			return err
		}
		return st.event("card:"+card.ID, "auto_route", map[string]string{
			"channel":           channelID,
			"model":             modelName,
			"exclude":           attempt.Route.Channel,
			"pin":               pin,
			"model_pin":         modelPin,
			"objective_channel": objChannel,
			"objective_model":   objModel,
		})
	}
	card.Status = model.CardReady
	return st.saveCard(card)
}

func transportRetry(outcome model.Outcome) bool {
	return outcome == model.OutcomeTransport || outcome == model.OutcomeTimeout
}

// holdBackoff keeps a same-channel transport or timeout retry off the ready
// queue until the backoff has elapsed. The first wait uses RetryBackoff.
// A later wait on the same card is twice that base. The deadline is the wait reason.
func (st *step) holdBackoff(card model.Card) error {
	wait := st.cfg.RetryBackoff
	if st.backoffHolds(card.ID) >= 1 {
		wait *= 2
	}
	until := st.now.UTC().Add(wait)
	card.Status = model.CardWaiting
	card.WaitReason = "backoff:" + until.Format(time.RFC3339Nano)
	if err := st.saveCard(card); err != nil {
		return err
	}
	return st.event("card:"+card.ID, "retry_backoff", map[string]string{
		"until":   until.Format(time.RFC3339Nano),
		"channel": card.Channel,
	})
}

// releaseBackoff promotes cards whose backoff deadline has arrived.
// It runs before harvest disposition, so a deadline written in this step stays held.
func (st *step) releaseBackoff() error {
	for _, card := range append([]model.Card{}, st.snap.cards...) {
		until, ok := backoffDeadline(card.WaitReason)
		if !ok || card.Status != model.CardWaiting || until.After(st.now) {
			continue
		}
		card.Status = model.CardReady
		card.WaitReason = ""
		if err := st.saveCard(card); err != nil {
			return err
		}
	}
	return nil
}

func (st *step) backoffHolds(cardID string) int {
	n := 0
	for _, event := range st.snap.events {
		if event.Type == "retry_backoff" && event.Entity == "card:"+cardID {
			n++
		}
	}
	return n
}

func backoffDeadline(reason string) (time.Time, bool) {
	const prefix = "backoff:"
	if !strings.HasPrefix(reason, prefix) {
		return time.Time{}, false
	}
	until, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(reason, prefix))
	if err != nil {
		return time.Time{}, false
	}
	return until, true
}

func (st *step) nextRoute(card model.Card, failed string) (string, string, bool, error) {
	req, err := routeRequestFrom(st.ctx, st.db, card, st.snap.obj, st.now, failed, true)
	if err != nil {
		return "", "", false, err
	}
	picked, _, err := route.Select(st.ctx, req)
	if err != nil || picked.Channel == "" || picked.Channel == failed {
		return "", "", false, nil
	}
	return picked.Channel, surfaceModel(picked.Model), true, nil
}

func rowsWithModel(rows []model.Positioning) []model.Positioning {
	out := make([]model.Positioning, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Model) == "" {
			continue
		}
		out = append(out, row)
	}
	return out
}

func (st *step) blockQuota(ch model.Channel, excerpt string) error {
	reset := resetTime(excerpt, ch, st.now)
	led := quota.New()
	led.SetClock(func() time.Time { return st.now })
	led.Configure(ch)
	if err := led.ObserveLimit(st.ctx, ch.ID, reset); err != nil {
		return err
	}
	est, err := led.Remaining(st.ctx, ch.ID)
	if err != nil {
		return err
	}
	if !est.Blocked {
		return fmt.Errorf("quota block did not apply to %s", ch.ID)
	}
	if len(ch.QuotaWindows) == 0 {
		ch.QuotaWindows = []model.QuotaWindow{{
			Name: "5h", ResetRule: "rolling", LimitTokens: 1, UsedTokens: 1,
		}}
	}
	for i := range ch.QuotaWindows {
		ch.QuotaWindows[i].ResetAt = reset
		ch.QuotaWindows[i].Remaining = 0
	}
	ch.LastError = "quota blocked until " + reset.Format(time.RFC3339Nano)
	if err := st.db.UpdateChannel(st.ctx, &ch); err != nil {
		return err
	}
	st.snap.putChannel(ch)
	return st.event("channel:"+ch.ID, "quota_block", map[string]string{
		"channel": ch.ID,
		"reset":   reset.Format(time.RFC3339Nano),
	})
}

func resetTime(excerpt string, ch model.Channel, now time.Time) time.Time {
	if match := clockRE.FindString(excerpt); match != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, match); err == nil {
			return parsed.UTC()
		}
	}
	for _, window := range ch.QuotaWindows {
		if !window.ResetAt.IsZero() && window.ResetAt.After(now) {
			return window.ResetAt.UTC()
		}
	}
	return now.UTC().Add(time.Hour)
}

func (st *step) markAuth(ch model.Channel, excerpt, attemptID string) error {
	ch.Health = model.HealthAuthFailed
	if excerpt != "" {
		ch.LastError = excerpt
	}
	if err := st.db.UpdateChannel(st.ctx, &ch); err != nil {
		return err
	}
	st.snap.putChannel(ch)
	text := "auth failed"
	if excerpt != "" {
		text += ": " + excerpt
	}
	if err := st.noticeEpisode("auth-"+ch.ID, "auth_failed", text, attemptID); err != nil {
		return err
	}
	return st.event("channel:"+ch.ID, "notify", map[string]string{
		"kind":    "auth_failed",
		"channel": ch.ID,
	})
}

// markUnavailable takes the channel out of automatic routing. Health
// degraded is the existing "do not auto-select" state; LastError keeps the
// model and the CLI's own words so doctor and the notice show the cause.
func (st *step) markUnavailable(ch model.Channel, attempt model.Attempt) error {
	excerpt := strings.TrimSpace(attempt.ErrorExcerpt)
	ch.Health = model.HealthDegraded
	ch.LastError = "unavailable: model " + attempt.Route.Model
	if excerpt != "" {
		ch.LastError += ": " + excerpt
	}
	if err := st.db.UpdateChannel(st.ctx, &ch); err != nil {
		return err
	}
	st.snap.putChannel(ch)
	text := fmt.Sprintf("channel %s unavailable (attempt %s): %s", ch.ID, attempt.ID, ch.LastError)
	if err := st.noticeEpisode("unavailable-"+ch.ID, "channel_unavailable", text, attempt.ID); err != nil {
		return err
	}
	return st.event("channel:"+ch.ID, "notify", map[string]string{
		"kind":    "channel_unavailable",
		"channel": ch.ID,
		"model":   attempt.Route.Model,
		"attempt": attempt.ID,
	})
}

func (st *step) openQuestion(kind model.QuestionKind, scope, scopeKind, text string, deliver bool) error {
	for _, question := range st.snap.questions {
		if question.Objective == st.snap.obj.ID && question.Kind == kind && question.Scope == scope && question.Answer == "" {
			return nil
		}
	}
	question := model.Question{
		Objective: st.snap.obj.ID,
		Scope:     scope,
		ScopeKind: scopeKind,
		Kind:      kind,
		Text:      text,
		Options:   []string{"approve", "reject"},
	}
	if deliver {
		question.Deliveries = []model.Delivery{{
			At:          st.now,
			Adapter:     "file",
			Destination: st.snap.obj.Origin,
		}}
	}
	if err := st.db.CreateQuestion(st.ctx, &question); err != nil {
		return err
	}
	st.snap.questions = append(st.snap.questions, question)
	if deliver {
		if err := st.notice(question.ID, string(kind), text); err != nil {
			return err
		}
	}
	return nil
}

// queueRedelivery sends notices whose delivery never succeeded for this fault.
// A notice already queued in this step is not added again.
// A successful delivery of an earlier fault does not retire a later one.
func (st *step) queueRedelivery() error {
	type dueNotice struct {
		kind      string
		text      string
		origin    string
		objective string
		episode   string
	}
	due := map[string]dueNotice{}
	for _, event := range st.snap.events {
		if event.Type != "notice_due" || !strings.HasPrefix(event.Entity, "notice:") {
			continue
		}
		id := strings.TrimPrefix(event.Entity, "notice:")
		var data struct {
			Kind      string `json:"kind"`
			Text      string `json:"text"`
			Origin    string `json:"origin"`
			Objective string `json:"objective"`
			Episode   string `json:"episode"`
		}
		if json.Unmarshal(event.Data, &data) != nil || !redeliverableKind(data.Kind) {
			continue
		}
		due[id] = dueNotice{
			kind: data.Kind, text: data.Text, origin: data.Origin,
			objective: data.Objective, episode: data.Episode,
		}
	}
	queued := map[string]bool{}
	for _, n := range st.notices {
		queued[n.QuestionID] = true
	}
	for id, item := range due {
		if id == "" || queued[id] || st.snap.episodeDelivered(id, item.episode) {
			continue
		}
		objective := item.objective
		if objective == "" {
			objective = st.snap.obj.ID
		}
		st.notices = append(st.notices, Notice{
			QuestionID: id,
			Origin:     st.noticeOrigin(item.origin, objective),
			Text:       item.text,
			Kind:       item.kind,
			Objective:  objective,
			Episode:    item.episode,
		})
	}
	return nil
}

func (st *step) notice(id, kind, text string) error {
	return st.noticeEpisode(id, kind, text, "")
}

// noticeEpisode records one fault. The same episode is not queued twice.
// A later episode reuses id and is still delivered after an earlier success.
func (st *step) noticeEpisode(id, kind, text, episode string) error {
	episode = strings.TrimSpace(episode)
	if id != "" {
		for _, n := range st.notices {
			if n.QuestionID == id && n.Episode == episode {
				return nil
			}
		}
		if redeliverableKind(kind) && st.snap.episodeDelivered(id, episode) {
			return nil
		}
	}
	rawOrigin := st.snap.obj.Origin
	st.notices = append(st.notices, Notice{
		QuestionID: id,
		Origin:     st.noticeOrigin(rawOrigin, st.snap.obj.ID),
		Text:       text,
		Kind:       kind,
		Objective:  st.snap.obj.ID,
		Episode:    episode,
	})
	if !redeliverableKind(kind) || id == "" {
		return nil
	}
	if current, ok := st.snap.dueEpisode(id); ok && current == episode {
		return nil
	}
	return st.event("notice:"+id, "notice_due", map[string]string{
		"kind":      kind,
		"text":      text,
		"origin":    rawOrigin,
		"objective": st.snap.obj.ID,
		"episode":   episode,
	})
}

// noticeOrigin fills an empty stored origin from the notice's own objective.
// That objective's origin wins. notify.fallback is used only when it has none.
func (st *step) noticeOrigin(stored, objectiveID string) string {
	if strings.TrimSpace(stored) != "" {
		return stored
	}
	id := strings.TrimSpace(objectiveID)
	if id == "" {
		id = st.snap.obj.ID
	}
	origin := ""
	if id == st.snap.obj.ID {
		origin = st.snap.obj.Origin
	} else if obj, err := st.db.GetObjective(st.ctx, id); err == nil {
		origin = obj.Origin
	}
	if strings.TrimSpace(origin) == "" {
		return st.cfg.NotifyFallback
	}
	return origin
}

func eventEpisode(event model.Event) string {
	var data struct {
		Episode string `json:"episode"`
	}
	if json.Unmarshal(event.Data, &data) != nil {
		return ""
	}
	return data.Episode
}

func (s *snap) episodeDelivered(id, episode string) bool {
	for _, event := range s.events {
		if event.Entity == "notice:"+id && event.Type == "notice_delivered" && eventEpisode(event) == episode {
			return true
		}
	}
	return false
}

func (s *snap) dueEpisode(id string) (string, bool) {
	found := false
	episode := ""
	for _, event := range s.events {
		if event.Entity != "notice:"+id || event.Type != "notice_due" {
			continue
		}
		episode = eventEpisode(event)
		found = true
	}
	return episode, found
}

func (st *step) absorb(card model.Card, attempt model.Attempt) error {
	switch card.Kind {
	case model.KindPlan:
		return st.absorbListed(card, attempt, st.absorbPlan)
	case model.KindReview:
		return st.absorbReview(card, attempt)
	case model.KindNormalize:
		return st.absorbNormalize(card, attempt)
	case model.KindReplan:
		return st.absorbListed(card, attempt, st.absorbReplan)
	default:
		return nil
	}
}

// absorbListed applies a plan or replan card list when the result has one,
// then absorbs design sections. A bad list leaves the card needing input and
// does not mark it done. Design prose without a list only absorbs design output.
// An unclear freeze on this card's own result dispatches one normalize card.
func (st *step) absorbListed(card model.Card, attempt model.Attempt, apply func(model.Card, model.Attempt) error) error {
	text := readResult(attempt)
	if attempt.Card == card.ID && judgeResultFreeze(text) == freezeUnclear {
		d := Deps{Store: st.db, WS: workspace.Git{}, Notify: notify.Stub{}}
		if _, err := d.recoverUnparsed(st.ctx, st.snap.obj, card); err != nil {
			return err
		}
		if cards, err := st.db.ListCards(st.ctx, st.snap.obj.ID); err == nil {
			st.snap.cards = cards
		}
		return nil
	}
	_, hasList := sectionBody(text, "新卡")
	list := ParseCardList(text, card.Node, nil)
	prose := strings.Contains(text, "##") && !hasList && list.Bad
	if !prose {
		if err := apply(card, attempt); err != nil {
			return err
		}
	}
	if current, ok := st.snap.card(card.ID); ok && current.Status == model.CardNeedsInput {
		return nil
	}
	return st.finishDesignFamily(card, attempt)
}

func (st *step) finishDesignFamily(card model.Card, attempt model.Attempt) error {
	current := card
	if saved, ok := st.snap.card(card.ID); ok {
		current = saved
		if len(card.Claims) > 0 {
			current.Claims = append([]model.Claim(nil), card.Claims...)
		}
	}
	d := Deps{Store: st.db, WS: workspace.Git{}, Notify: notify.Stub{}}
	if err := d.applyDesign(st.ctx, st.snap.obj, current, readResult(attempt)); err != nil {
		return err
	}
	if err := d.markPassedDone(st.ctx, current, model.OutcomePassed); err != nil {
		return err
	}
	fresh, err := st.db.GetCard(st.ctx, card.ID)
	if err != nil {
		return err
	}
	merge, err := d.designFileChanged(st.ctx, fresh, current.Claims)
	if err != nil {
		return err
	}
	if merge && fresh.Status != model.CardMerged && fresh.Status != model.CardMergeQueued {
		fresh.Status = model.CardMergeQueued
		fresh.WaitReason = ""
	}
	if len(current.Claims) > 0 {
		fresh.Claims = append([]model.Claim(nil), current.Claims...)
	}
	if err := st.saveCard(fresh); err != nil {
		return err
	}
	if err := d.markAbsorbed(st.ctx, card.ID, nil); err != nil {
		return err
	}
	return d.markAttempt(st.ctx, attempt.ID)
}

func (st *step) absorbPlan(card model.Card, attempt model.Attempt) error {
	known := map[string]bool{}
	for _, existing := range st.snap.cards {
		known[existing.ID] = true
	}
	list := ParseCardList(readResult(attempt), card.Node, known)
	if list.Bad {
		card.Status = model.CardNeedsInput
		if err := st.saveCard(card); err != nil {
			return err
		}
		return st.event("card:"+card.ID, "plan_unparsable", map[string]string{"card": card.ID})
	}
	for _, event := range list.Events {
		switch event.Type {
		case "invalid_dep":
			if err := st.event("card:"+card.ID, "invalid_dep", map[string]string{"card": event.Card, "dep": event.Dep}); err != nil {
				return err
			}
		case "cycle_break":
			if err := st.event("card:"+card.ID, "cycle_break", map[string]string{"from": event.From, "to": event.To}); err != nil {
				return err
			}
		}
	}
	if err := st.hangQuestions(list.Questions, card.Node); err != nil {
		return err
	}
	taken := map[string]bool{}
	for _, existing := range st.snap.cards {
		taken[existing.ID] = true
	}
	creates := make([]ListedCard, 0, len(list.Cards))
	for _, draft := range list.Cards {
		if draft.Op == "delete" {
			continue
		}
		creates = append(creates, draft)
	}
	creates, _, err := bindCreatedCards(creates, nil, taken)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(creates))
	for _, draft := range creates {
		if draft.ID == "" {
			return fmt.Errorf("new card %q has no id", draft.Key)
		}
		created, err := st.createCard(cardFromDraft(st.snap.obj.ID, card.Node, draft))
		if err != nil {
			return err
		}
		ids = append(ids, created.ID)
	}
	return st.event("card:"+card.ID, "plan_split", map[string]any{"node": card.Node, "cards": ids})
}

func (st *step) absorbReview(card model.Card, attempt model.Attempt) error {
	raw := readResult(attempt)
	verdict, opinion, ok := ReviewOf(raw)
	stored := reviewOpinion(raw)
	if !ok || len(card.Deps) == 0 {
		card.Status = model.CardNeedsInput
		if err := st.saveCard(card); err != nil {
			return err
		}
		return st.event("card:"+card.ID, "review_unparsable", map[string]string{"card": card.ID})
	}
	parent, ok := st.snap.card(card.Deps[0])
	if !ok {
		return fmt.Errorf("review %s missing card %s", card.ID, card.Deps[0])
	}
	if verdict == "pass" {
		parent.Status = model.CardMergeQueued
		if err := st.saveCard(parent); err != nil {
			return err
		}
		if err := st.noteReviewVerdict(parent, card, attempt, verdict, stored, false); err != nil {
			return err
		}
		card.Status = model.CardDone
		return st.saveCard(card)
	}
	parent.ReworkNote = opinion
	dec := reworkOrQuestion(parent, st.cfg.ReworkLimit, "review rework")
	questionOpened := !dec.SetRework
	if dec.SetRework {
		parent.ReworkCount = dec.ReworkCount
		parent.Status = model.CardReady
		parent.WaitReason = ""
		if branch := st.latestBranch(parent.ID); branch != "" {
			parent.Base = branch
		}
		if err := st.saveCard(parent); err != nil {
			return err
		}
	} else {
		parent.Status = model.CardNeedsInput
		if err := st.saveCard(parent); err != nil {
			return err
		}
		text := dec.QuestionText
		if opinion != "" {
			text += ": " + opinion
		}
		if err := st.openQuestion(dec.QuestionKind, parent.ID, "card", text, true); err != nil {
			return err
		}
	}
	if err := st.noteReviewVerdict(parent, card, attempt, verdict, stored, questionOpened); err != nil {
		return err
	}
	if err := st.noteRework(parent, attempt, reworkSourceReview, questionOpened); err != nil {
		return err
	}
	card.Status = model.CardDone
	return st.saveCard(card)
}

const (
	reworkSourceReview = "review"
	reworkSourceTests  = "tests_failed"
)

func reworkLimitQuestion(text string) bool {
	return strings.HasPrefix(text, "rework_count would exceed ")
}

func (st *step) noteReviewVerdict(parent, review model.Card, attempt model.Attempt, verdict, opinion string, questionOpened bool) error {
	text, truncated := truncateOpinion(opinion)
	return st.event("card:"+parent.ID, "review_verdict", map[string]any{
		"review_card_id":    review.ID,
		"review_attempt_id": attempt.ID,
		"verdict":           verdict,
		"opinion":           text,
		"truncated":         truncated,
		"rework_count":      parent.ReworkCount,
		"question_opened":   questionOpened,
	})
}

func (st *step) noteRework(card model.Card, attempt model.Attempt, source string, questionOpened bool) error {
	data := map[string]any{
		"source":          source,
		"attempt_id":      attempt.ID,
		"rework_count":    card.ReworkCount,
		"question_opened": questionOpened,
	}
	if source == reworkSourceTests {
		data["acceptance_log"] = "attempts/" + attempt.ID + "/acceptance.log"
		data["excerpt"] = st.existingExcerpt(attempt)
	}
	return st.event("card:"+card.ID, "rework", data)
}

// existingExcerpt prefers the excerpt already stored on the attempt. Deliver
// still leaves that value in place when it is non-empty.
func (st *step) existingExcerpt(attempt model.Attempt) string {
	stored, err := st.db.GetAttempt(st.ctx, attempt.ID)
	if err == nil && stored.ErrorExcerpt != "" {
		return stored.ErrorExcerpt
	}
	return attempt.ErrorExcerpt
}

func (st *step) absorbNormalize(card model.Card, attempt model.Attempt) error {
	if len(card.Deps) == 0 {
		return fmt.Errorf("normalize %s has no original card", card.ID)
	}
	parent, ok := st.snap.card(card.Deps[0])
	if !ok {
		return fmt.Errorf("normalize %s missing card %s", card.ID, card.Deps[0])
	}
	if designOriginal(parent.Kind) {
		return st.absorbNormalizedOriginal(card, parent, attempt)
	}
	text := readResult(attempt)
	for _, prior := range st.snap.attempts[parent.ID] {
		if prior.Worktree == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(prior.Worktree, ".cardex-result.md"), []byte(text), 0o644); err != nil {
			return err
		}
	}
	if text != "" && !strings.Contains(parent.Body, text) {
		parent.Body += resultMarker + text
	}
	if parent.Status == model.CardWaiting {
		parent.Status = model.CardReady
		parent.WaitReason = ""
	}
	if err := st.saveCard(parent); err != nil {
		return err
	}
	if err := st.event("card:"+parent.ID, "normalize_backfill", map[string]string{"card": parent.ID, "result": text}); err != nil {
		return err
	}
	card.Status = model.CardDone
	return st.saveCard(card)
}

func designOriginal(kind model.CardKind) bool {
	return kind == model.KindDesign || kind == model.KindPlan || kind == model.KindReplan
}

// absorbNormalizedOriginal applies a successful normalize onto a design,
// plan, or replan card through the design absorber and finishes that card.
// The normalize attempt gets the same loop.absorbed mark design advance
// already treats as consumed, so that step does not apply the result again.
// A dev original keeps the text backfill instead.
func (st *step) absorbNormalizedOriginal(norm model.Card, parent model.Card, attempt model.Attempt) error {
	text := readResult(attempt)
	d := Deps{Store: st.db, WS: workspace.Git{}, Notify: notify.Stub{}}
	if err := d.finishNormalizedSource(st.ctx, st.snap.obj, parent, text); err != nil {
		return err
	}
	if fresh, err := st.db.GetCard(st.ctx, parent.ID); err == nil {
		st.snap.putCard(fresh)
	}
	if nodes, err := st.db.ListNodes(st.ctx, st.snap.obj.ID); err == nil {
		st.snap.nodes = nodes
	}
	if err := d.markAttempt(st.ctx, attempt.ID); err != nil {
		return err
	}
	norm.Status = model.CardDone
	return st.saveCard(norm)
}

func (st *step) absorbReplan(card model.Card, attempt model.Attempt) error {
	known := map[string]bool{}
	for _, existing := range st.snap.cards {
		known[existing.ID] = true
	}
	list := ParseCardList(readResult(attempt), card.Node, known)
	if list.Bad {
		card.Status = model.CardNeedsInput
		if err := st.saveCard(card); err != nil {
			return err
		}
		return st.event("card:"+card.ID, "plan_unparsable", map[string]string{"card": card.ID})
	}
	started := map[string]bool{}
	for id, attempts := range st.snap.attempts {
		if len(attempts) > 0 {
			started[id] = true
		}
	}
	change := PlanReplan(st.snap.cards, started, card.Node, list)
	if err := st.hangQuestions(change.Questions, card.Node); err != nil {
		return err
	}
	taken := map[string]bool{}
	for _, existing := range st.snap.cards {
		taken[existing.ID] = true
	}
	creates, edits, err := bindCreatedCards(change.Create, change.Edit, taken)
	if err != nil {
		return err
	}
	for _, draft := range creates {
		if draft.ID == "" {
			return fmt.Errorf("new card %q has no id", draft.Key)
		}
		if _, err := st.createCard(cardFromDraft(st.snap.obj.ID, card.Node, draft)); err != nil {
			return err
		}
	}
	for _, draft := range edits {
		current, ok := st.snap.card(draft.Key)
		if !ok {
			continue
		}
		current.Kind = draft.Kind
		current.Title = draft.Title
		current.Body = draft.Body
		current.Tier = draft.Tier
		current.Deps = draft.Deps
		current.Acceptance = draft.Acceptance
		current.Claims = draft.Claims
		if err := st.saveCard(current); err != nil {
			return err
		}
	}
	for _, id := range change.Delete {
		if err := st.db.DeleteCard(st.ctx, id); err != nil {
			return err
		}
		st.snap.dropCard(id)
	}
	return nil
}

func cardFromDraft(objective, node string, draft ListedCard) model.Card {
	target := node
	if draft.Node != "" && !draft.Outside {
		target = draft.Node
	}
	return model.Card{
		ID:         draft.ID,
		Objective:  objective,
		Node:       target,
		Kind:       draft.Kind,
		Title:      draft.Title,
		Body:       draft.Body,
		Tier:       draft.Tier,
		Claims:     draft.Claims,
		Deps:       draft.Deps,
		Acceptance: draft.Acceptance,
		Status:     model.CardReady,
	}
}

func (st *step) hangQuestions(questions []ListQuestion, current string) error {
	for _, question := range questions {
		text := question.Text
		nodeID := question.Node
		node, ok := st.snap.node(nodeID)
		if !ok {
			if nodeID != "" && !strings.Contains(text, nodeID) {
				text += " (" + nodeID + ")"
			}
			node, ok = st.snap.node(current)
			if !ok {
				continue
			}
		}
		if hasOpenText(node, text) {
			continue
		}
		node.OpenQuestions = append(node.OpenQuestions, model.OpenQuestion{
			ID:     fmt.Sprintf("oq-%s-%d", node.ID, len(node.OpenQuestions)+1),
			Text:   text,
			Type:   model.OQDecision,
			Status: model.OQOpen,
		})
		if err := st.db.UpdateNode(st.ctx, &node); err != nil {
			return err
		}
		st.snap.putNode(node)
	}
	return nil
}

func hasOpenText(node model.DesignNode, text string) bool {
	for _, question := range node.OpenQuestions {
		if question.Text == text {
			return true
		}
	}
	return false
}

func (st *step) latestBranch(cardID string) string {
	attempts := append([]model.Attempt{}, st.snap.attempts[cardID]...)
	sort.Slice(attempts, func(i, j int) bool {
		if attempts[i].Index != attempts[j].Index {
			return attempts[i].Index > attempts[j].Index
		}
		return attempts[i].ID > attempts[j].ID
	})
	for _, attempt := range attempts {
		if attempt.Branch != "" {
			return attempt.Branch
		}
	}
	return ""
}

func hasDep(card model.Card, id string) bool {
	for _, dep := range card.Deps {
		if dep == id {
			return true
		}
	}
	return false
}

func readResult(attempt model.Attempt) string {
	if attempt.Worktree == "" {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(attempt.Worktree, ".cardex-result.md"))
	if err != nil {
		return ""
	}
	return string(body)
}

type replanTrigger struct {
	Kind string
	Key  string
}

func (st *step) advance() error {
	if st.snap.obj.Mode != model.ModeAuto {
		return nil
	}
	fired := map[string]string{}
	nodes := append([]model.DesignNode{}, st.snap.nodes...)
	for _, node := range nodes {
		if err := st.noteReopenGap(node); err != nil {
			return err
		}
		reason, ok := EvaluateBreaker(st.breakerInput(node.ID))
		if !ok {
			continue
		}
		fired[node.ID] = reason
		if err := st.pauseNode(node.ID); err != nil {
			return err
		}
		if err := st.openQuestion(model.QuestionBreaker, node.ID, "node", "breaker: "+reason, true); err != nil {
			return err
		}
	}
	for _, node := range nodes {
		if st.outstandingReplan(node.ID) {
			continue
		}
		split := st.completedSplit(node.ID)
		var triggers []replanTrigger
		if split != "" {
			triggers = append(triggers, replanTrigger{Kind: "split", Key: split})
		}
		if node.Status == model.NodeReopened {
			key := st.reopenKey(node.ID)
			if !st.triggerConsumed(node.ID, "reopen", key) {
				triggers = append(triggers, replanTrigger{Kind: "reopen", Key: key})
			}
		}
		if reason := fired[node.ID]; reason != "" {
			key := st.breakerKey(node.ID, reason)
			if !st.triggerConsumed(node.ID, "breaker", key) {
				triggers = append(triggers, replanTrigger{Kind: "breaker", Key: key})
			}
		}
		if len(triggers) == 0 {
			continue
		}
		if _, err := st.createCard(model.Card{
			Objective: st.snap.obj.ID,
			Node:      node.ID,
			Kind:      model.KindReplan,
			Title:     "replan " + node.ID,
			Tier:      model.TierT3,
			Status:    model.CardReady,
			Deps:      replanDeps(split),
		}); err != nil {
			return err
		}
		for _, trigger := range triggers {
			if trigger.Kind == "split" {
				continue
			}
			if err := st.event("node:"+node.ID, "replan_consumed", map[string]string{
				"node": node.ID,
				"kind": trigger.Kind,
				"key":  trigger.Key,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (st *step) noteReopenGap(node model.DesignNode) error {
	if node.Status == model.NodeReopened || !st.reopenConsumedSinceGap(node.ID) {
		return nil
	}
	return st.event("node:"+node.ID, "reopen_gap", map[string]string{"node": node.ID})
}

func (st *step) reopenKey(nodeID string) string {
	n := 0
	for _, event := range st.snap.events {
		if event.Type == "reopen_gap" && event.Entity == "node:"+nodeID {
			n++
		}
	}
	return strconvReopen(n)
}

func strconvReopen(n int) string {
	return fmt.Sprintf("%d", n)
}

func (st *step) reopenConsumedSinceGap(nodeID string) bool {
	consumed := false
	for _, event := range st.snap.events {
		if event.Entity != "node:"+nodeID {
			continue
		}
		switch event.Type {
		case "reopen_gap":
			consumed = false
		case "replan_consumed":
			var data struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.Kind == "reopen" {
				consumed = true
			}
		}
	}
	return consumed
}

// breakerKey names the evidence a breaker replan already handled. A failure
// batch is the attempt ids only, so a later reason for that same batch is
// not a new trigger. A stall keeps its own anchor because its evidence is
// the window, not those attempts.
func (st *step) breakerKey(nodeID, reason string) string {
	in := st.breakerInput(nodeID)
	facts := withoutReplanFacts(in.Attempts)
	if reason == "no merge within stall window" {
		return reason + "\n" + stallAnchorKey(in, facts)
	}
	ids := breakerEvidenceIDs(facts, reason)
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func withoutReplanFacts(facts []AttemptFact) []AttemptFact {
	out := make([]AttemptFact, 0, len(facts))
	for _, fact := range facts {
		if fact.Kind == model.KindReplan {
			continue
		}
		out = append(out, fact)
	}
	return out
}

// breakerEvidenceIDs is the attempts that make this breaker true.
// Attempts on the replan that handled it are already removed, so absorbing
// that replan does not change the key.
func breakerEvidenceIDs(facts []AttemptFact, reason string) []string {
	switch {
	case strings.HasPrefix(reason, "repeated error "):
		return signatureIDs(facts, strings.TrimPrefix(reason, "repeated error "))
	case strings.HasPrefix(reason, "consecutive failures"):
		return trailingFailureIDs(facts)
	case strings.HasPrefix(reason, "repair streak "):
		return repairEvidenceIDs(facts, reason)
	default:
		ids := make([]string, 0, len(facts))
		for _, fact := range facts {
			if fact.ID != "" {
				ids = append(ids, fact.ID)
			}
		}
		return ids
	}
}

func signatureIDs(facts []AttemptFact, sig string) []string {
	var ids []string
	for _, fact := range facts {
		if fact.ID == "" || fact.Outcome == "" || fact.Outcome == model.OutcomePassed {
			continue
		}
		if signature(fact) != sig {
			continue
		}
		ids = append(ids, fact.ID)
	}
	return ids
}

func trailingFailureIDs(facts []AttemptFact) []string {
	ordered := append([]AttemptFact(nil), facts...)
	sortFacts(ordered)
	var ids []string
	for i := len(ordered) - 1; i >= 0; i-- {
		if !countsAsFailure(ordered[i].Outcome) {
			if ordered[i].Outcome == model.OutcomePassed {
				break
			}
			continue
		}
		if ordered[i].ID != "" {
			ids = append(ids, ordered[i].ID)
		}
	}
	return ids
}

func repairEvidenceIDs(facts []AttemptFact, reason string) []string {
	parent := reason
	if i := strings.LastIndex(reason, " on "); i >= 0 {
		parent = reason[i+len(" on "):]
	}
	var list []AttemptFact
	for _, fact := range facts {
		if fact.Kind != model.KindRepair {
			continue
		}
		owner := fact.RepairOf
		if owner == "" {
			owner = fact.Card
		}
		if owner == parent {
			list = append(list, fact)
		}
	}
	sortFacts(list)
	var ids []string
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Outcome == model.OutcomePassed {
			break
		}
		if list[i].Outcome != model.OutcomeTestsFailed || list[i].ID == "" {
			continue
		}
		ids = append(ids, list[i].ID)
	}
	return ids
}

func stallAnchorKey(in BreakerInput, facts []AttemptFact) string {
	var first time.Time
	for _, fact := range facts {
		if fact.At.IsZero() {
			continue
		}
		if first.IsZero() || fact.At.Before(first) {
			first = fact.At
		}
	}
	anchor := first
	if !in.MergeAt.IsZero() {
		anchor = in.MergeAt
	}
	if anchor.IsZero() {
		return "none"
	}
	return anchor.UTC().Format(time.RFC3339Nano)
}

func (st *step) triggerConsumed(nodeID, kind, key string) bool {
	var batches []string
	for _, event := range st.snap.events {
		if event.Type != "replan_consumed" || event.Entity != "node:"+nodeID {
			continue
		}
		var data struct {
			Kind string `json:"kind"`
			Key  string `json:"key"`
		}
		if json.Unmarshal(event.Data, &data) != nil {
			continue
		}
		if data.Kind != kind {
			continue
		}
		if data.Key == key {
			return true
		}
		if kind == "breaker" && failureBatchKey(data.Key) {
			batches = append(batches, data.Key)
		}
	}
	return kind == "breaker" && failureBatchKey(key) && batchCovered(key, batches)
}

// failureBatchKey is a breaker consumption key made of attempt ids. Stall
// keys carry the reason and an anchor, so they stay on exact match.
func failureBatchKey(key string) bool {
	return key != "" && !strings.Contains(key, "\n")
}

// batchCovered reports whether every id in key already belongs to the union
// of consumed failure batches. One record does not have to hold every id.
// An id outside that union is a new trigger.
func batchCovered(key string, batches []string) bool {
	have := map[string]bool{}
	for _, batch := range batches {
		for _, id := range strings.Split(batch, ",") {
			if id != "" {
				have[id] = true
			}
		}
	}
	want := strings.Split(key, ",")
	if len(want) == 0 || len(have) == 0 {
		return false
	}
	for _, id := range want {
		if id == "" || !have[id] {
			return false
		}
	}
	return true
}

func replanDeps(split string) []string {
	if split == "" {
		return nil
	}
	return []string{split}
}

func (st *step) breakerInput(nodeID string) BreakerInput {
	in := BreakerInput{Now: st.now, Config: st.cfg}
	var nodeMerge time.Time
	mergedMissing := false
	for _, card := range st.snap.cards {
		if card.Node != nodeID {
			continue
		}
		var latest time.Time
		for _, attempt := range st.snap.attempts[card.ID] {
			at := attempt.EndedAt
			if at.IsZero() {
				at = attempt.StartedAt
			}
			if at.After(latest) {
				latest = at
			}
			repairOf := ""
			if card.Kind == model.KindRepair && len(card.Deps) > 0 {
				repairOf = card.Deps[0]
			}
			in.Attempts = append(in.Attempts, AttemptFact{
				ID:       attempt.ID,
				Card:     card.ID,
				Kind:     card.Kind,
				Outcome:  attempt.Outcome,
				Class:    attempt.ErrorClass,
				Excerpt:  attempt.ErrorExcerpt,
				Tokens:   attempt.Usage.Tokens,
				At:       at,
				RepairOf: repairOf,
			})
			if attempt.Usage.Tokens > 0 {
				in.Spend += attempt.Usage.Tokens
			}
		}
		cardMerge := time.Time{}
		for _, event := range st.snap.events {
			if event.Entity != "card:"+card.ID || event.Type != "merge_success" || !event.TS.After(cardMerge) {
				continue
			}
			cardMerge = event.TS
		}
		if card.Status == model.CardMerged && cardMerge.IsZero() {
			mergedMissing = true
		}
		if cardMerge.After(nodeMerge) {
			nodeMerge = cardMerge
		}
		in.Cards = append(in.Cards, NodeCard{ID: card.ID, Status: card.Status, Latest: latest})
	}
	// Unknown while any current merged card lacks its own merge_success.
	// Otherwise the clock is the latest own merge_success on the node,
	// whatever status that card has now. No event leaves MergeAt zero
	// so the stall uses the first attempt.
	if mergedMissing {
		in.MergeUnknown = true
		return in
	}
	in.MergeAt = nodeMerge
	return in
}

func (st *step) pauseNode(nodeID string) error {
	for _, card := range append([]model.Card{}, st.snap.cards...) {
		if card.Node != nodeID || card.Kind == model.KindReplan || card.Status == model.CardRunning || TerminalCard(card.Status) {
			continue
		}
		card.Status = model.CardPaused
		if err := st.saveCard(card); err != nil {
			return err
		}
	}
	return nil
}

func (st *step) outstandingReplan(nodeID string) bool {
	for _, card := range st.snap.cards {
		if card.Node == nodeID && card.Kind == model.KindReplan && !TerminalCard(card.Status) {
			return true
		}
	}
	return false
}

func (st *step) completedSplit(nodeID string) string {
	for _, event := range st.snap.events {
		if event.Type != "plan_split" {
			continue
		}
		var data struct {
			Node  string   `json:"node"`
			Cards []string `json:"cards"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			continue
		}
		if data.Node != nodeID || len(data.Cards) == 0 {
			continue
		}
		planID := strings.TrimPrefix(event.Entity, "card:")
		if st.splitReplanned(planID) {
			continue
		}
		ready := true
		for _, id := range data.Cards {
			card, ok := st.snap.card(id)
			if !ok || !TerminalCard(card.Status) {
				ready = false
				break
			}
		}
		if ready {
			return planID
		}
	}
	return ""
}

func (st *step) splitReplanned(planID string) bool {
	for _, card := range st.snap.cards {
		if card.Kind == model.KindReplan && hasDep(card, planID) {
			return true
		}
	}
	return false
}

func (st *step) budgetLines() error {
	spend := st.snap.spend()
	obj := st.snap.obj
	if SoftHit(spend, obj.Budget.SoftTokens) && !HardHit(spend, obj.Budget.HardTokens) {
		if !st.snap.hasEvent("objective:"+obj.ID, "budget_reminder") {
			if err := st.notice("budget-reminder-"+obj.ID, "budget_reminder", "objective spend reached 80% of soft_tokens"); err != nil {
				return err
			}
			if err := st.event("objective:"+obj.ID, "budget_reminder", map[string]int64{"spend": spend, "soft": obj.Budget.SoftTokens}); err != nil {
				return err
			}
		}
	}
	if !HardHit(spend, obj.Budget.HardTokens) {
		return nil
	}
	if obj.Status == model.ObjectiveActive {
		st.snap.obj.Status = model.ObjectivePaused
		if err := st.saveObjective(); err != nil {
			return err
		}
	}
	return st.openQuestion(model.QuestionBudget, obj.ID, "objective", "objective spend reached hard_tokens", true)
}

func (st *step) complete() error {
	obj := st.snap.obj
	if obj.Status == model.ObjectiveDone || st.snap.hasEvent("objective:"+obj.ID, "merged_local") {
		return nil
	}
	if obj.Status == model.ObjectiveDoneCandidate || st.pending(model.QuestionDone, obj.ID) {
		return st.mergeIfApproved()
	}
	if obj.Status != model.ObjectiveActive {
		return nil
	}
	if !st.allFrozen() || st.anyUnfinished() {
		return nil
	}
	ok, err := acceptancePasses(st.ctx, st.snap.project.RepoPath, obj.IntegrationBranch, obj.Acceptance.Commands)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	st.snap.obj.Status = model.ObjectiveDoneCandidate
	if err := st.saveObjective(); err != nil {
		return err
	}
	return st.openQuestion(model.QuestionDone, obj.ID, "objective", "objective is a done candidate; approve to merge the integration branch locally", true)
}

func (st *step) allFrozen() bool {
	if len(st.snap.nodes) == 0 {
		return false
	}
	for _, node := range st.snap.nodes {
		if node.Status != model.NodeFrozen {
			return false
		}
	}
	return true
}

func (st *step) anyUnfinished() bool {
	for _, card := range st.snap.cards {
		// The weekly ranking card stays on the active objective until it can run.
		// It is not that objective's unfinished work.
		if card.Kind == model.KindRanking && strings.HasPrefix(card.ID, "sch-ranking-") {
			continue
		}
		switch card.Status {
		case model.CardMerged, model.CardDone, model.CardFailed, model.CardCanceled:
		default:
			return true
		}
	}
	return false
}

func (st *step) pending(kind model.QuestionKind, scope string) bool {
	for _, question := range st.snap.questions {
		if question.Objective == st.snap.obj.ID && question.Kind == kind && question.Scope == scope && question.Answer == "" {
			return true
		}
	}
	return false
}

func (st *step) mergeIfApproved() error {
	var answer string
	found := false
	for _, question := range st.snap.questions {
		if question.Kind == model.QuestionDone && question.Objective == st.snap.obj.ID {
			answer = question.Answer
			found = true
		}
	}
	if !found || !approved(answer) {
		return nil
	}
	if st.snap.project.RepoPath == "" || st.snap.obj.IntegrationBranch == "" {
		return fmt.Errorf("done merge requires a project repo and an integration branch")
	}
	if err := mergeLocal(st.snap.project.RepoPath, st.snap.obj.IntegrationBranch); err != nil {
		return err
	}
	st.snap.obj.Status = model.ObjectiveDone
	if err := st.saveObjective(); err != nil {
		return err
	}
	return st.event("objective:"+st.snap.obj.ID, "merged_local", map[string]string{
		"branch": st.snap.obj.IntegrationBranch,
		"repo":   st.snap.project.RepoPath,
	})
}

func approved(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "approve", "approved", "yes":
		return true
	default:
		return false
	}
}

func acceptancePasses(ctx context.Context, repo, branch string, commands []string) (bool, error) {
	if len(commands) == 0 {
		return true, nil
	}
	if repo == "" || branch == "" {
		return false, nil
	}
	if _, err := git(repo, "rev-parse", "--verify", "--quiet", branch); err != nil {
		return false, nil
	}
	parent, err := os.MkdirTemp("", "cardex-accept-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(parent)
	dir := filepath.Join(parent, "wt")
	if _, err := git(repo, "worktree", "add", "--detach", dir, branch); err != nil {
		return false, err
	}
	defer git(repo, "worktree", "remove", "--force", dir)
	for _, command := range commands {
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if err := cmd.Run(); err != nil {
			return false, nil
		}
	}
	return true, nil
}

func mergeLocal(repo, integration string) error {
	main, err := mainBranch(repo)
	if err != nil {
		return err
	}
	if _, err := git(repo, "checkout", main); err != nil {
		return err
	}
	if _, err := git(repo, "merge", "--no-edit", integration); err != nil {
		return err
	}
	return nil
}

func mainBranch(repo string) (string, error) {
	for _, name := range []string{"main", "master"} {
		if _, err := git(repo, "rev-parse", "--verify", "--quiet", name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("project %s has no main branch", repo)
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{
		"-C", repo,
		"-c", "user.name=cardex",
		"-c", "user.email=cardex@cardex.invalid",
		"-c", "commit.gpgsign=false",
	}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// gitEnv drops inherited repository pointers so a merge or acceptance
// worktree always uses the project repo passed to git -C.
func gitEnv() []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+3)
	for _, env := range base {
		if strings.HasPrefix(env, "GIT_DIR=") || strings.HasPrefix(env, "GIT_WORK_TREE=") || strings.HasPrefix(env, "GIT_INDEX_FILE=") {
			continue
		}
		out = append(out, env)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_PAGER=cat")
}

// noOutputDetail names each no-output attempt on the card, with its channel,
// model, error class and excerpt, so the question shows the cause instead of
// one generic line.
func noOutputDetail(attempts []model.Attempt, cur model.Attempt) string {
	var b strings.Builder
	b.WriteString(noOutputQuestion)
	seen := map[string]bool{}
	list := append([]model.Attempt{}, attempts...)
	list = append(list, cur)
	for _, a := range list {
		if seen[a.ID] || (a.ID != cur.ID && a.Outcome != model.OutcomeNoOutput) {
			continue
		}
		if a.Index > cur.Index {
			continue
		}
		seen[a.ID] = true
		class := string(a.ErrorClass)
		if class == "" {
			class = "unclassified"
		}
		fmt.Fprintf(&b, "\n- attempt %s on %s/%s: %s", a.ID, a.Route.Channel, a.Route.Model, class)
		if ex := oneLine(a.ErrorExcerpt, 200); ex != "" {
			b.WriteString(": " + ex)
		}
	}
	return b.String()
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}
