package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/harvest"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

type disposeKey int

const (
	disposeHomeKey disposeKey = iota
	disposeAttemptKey
	disposeStoreKey
)

// WithHome tells Dispose which CARDEX_HOME holds cardex.db.
func WithHome(ctx context.Context, home string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, disposeHomeKey, home)
}

// WithAttempt tells Dispose which harvested attempt to apply.
func WithAttempt(ctx context.Context, attemptID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, disposeAttemptKey, attemptID)
}

// WithStore tells Dispose to use an already open store. The caller keeps it open.
func WithStore(ctx context.Context, db *store.Store) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, disposeStoreKey, db)
}

// Dispose applies one harvested result. Dev, review, and normalize cards use
// decideHarvest, the same decision as disposeAll. Design, research, plan,
// replan, and spike failures use that same decision, then any design-side
// cleanup. A passing plan or replan applies its card list and absorbs design
// output through that decision. Other design-family passes use absorbAttempt.
// The attempt outcome is left for the caller to store after nil.
func Dispose(ctx context.Context, card *model.Card, res harvest.Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if card == nil || strings.TrimSpace(card.ID) == "" {
		return fmt.Errorf("loop: Dispose: card required")
	}
	home, _ := ctx.Value(disposeHomeKey).(string)
	if strings.TrimSpace(home) == "" {
		home = os.Getenv("CARDEX_HOME")
	}
	if strings.TrimSpace(home) == "" {
		return fmt.Errorf("CARDEX_HOME is not set")
	}
	db, owned, err := disposeStore(ctx, home)
	if err != nil {
		return err
	}
	if owned {
		defer db.Close()
	}
	var notices []Notice
	err = db.WithTx(ctx, func(ctx context.Context) error {
		var n []Notice
		var callErr error
		n, callErr = disposeTx(ctx, db, home, card, res)
		notices = n
		return callErr
	})
	if err != nil {
		return err
	}
	n := notifierFrom(ctx, home)
	for _, notice := range notices {
		if err := n.Deliver(ctx, notice); err != nil {
			return err
		}
	}
	return nil
}

func disposeStore(ctx context.Context, home string) (*store.Store, bool, error) {
	if db, ok := ctx.Value(disposeStoreKey).(*store.Store); ok && db != nil {
		return db, false, nil
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		return nil, false, err
	}
	return db, true, nil
}

func disposeTx(ctx context.Context, db *store.Store, home string, card *model.Card, res harvest.Result) ([]Notice, error) {
	attemptID, _ := ctx.Value(disposeAttemptKey).(string)
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		found, err := openHarvestAttempt(ctx, db, card.ID)
		if err != nil {
			return nil, err
		}
		attemptID = found
	}
	if attemptID == "" {
		return nil, fmt.Errorf("loop: Dispose: no open attempt for %s", card.ID)
	}
	attempt, err := db.GetAttempt(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	if attempt.Card == "" {
		attempt.Card = card.ID
	}
	if card.Kind == model.KindRanking {
		return disposeRanking(ctx, db, home, card, attempt, res)
	}
	if designKind(card.Kind) {
		return disposeDesign(ctx, db, home, card, attempt, res)
	}
	return disposeDev(ctx, db, home, card, attempt, res)
}

func disposeRanking(ctx context.Context, db *store.Store, home string, card *model.Card, attempt model.Attempt, res harvest.Result) ([]Notice, error) {
	harvested := RankingHarvest{
		AttemptID:  attempt.ID,
		Outcome:    res.Outcome,
		ErrorClass: res.ErrorClass,
		Acceptance: acceptanceFromEvidence(res.Evidence),
		ResultText: res.ResultText,
	}
	fresh, err := db.GetCard(ctx, card.ID)
	if err != nil {
		return nil, err
	}
	stopped := fresh.Status == model.CardFailed || fresh.Status == model.CardCanceled
	if rankingAbsorbable(fresh, harvested) {
		if err := AbsorbHarvestedRanking(ctx, Deps{Store: db}, fresh, harvested); err != nil {
			return nil, err
		}
		if err := ensureDevDisposed(ctx, db, attempt, res.Outcome); err != nil {
			return nil, err
		}
		return refreshDisposedCard(ctx, db, card)
	}
	if stopped {
		if err := ensureDevDisposed(ctx, db, attempt, res.Outcome); err != nil {
			return nil, err
		}
		return refreshDisposedCard(ctx, db, card)
	}
	done, err := attemptDisposed(ctx, db, attempt.ID)
	if err != nil {
		return nil, err
	}
	if done {
		return refreshDisposedCard(ctx, db, card)
	}
	return disposeDev(ctx, db, home, card, attempt, rankingFailureResult(res))
}

func refreshDisposedCard(ctx context.Context, db *store.Store, card *model.Card) ([]Notice, error) {
	fresh, err := db.GetCard(ctx, card.ID)
	if err != nil {
		return nil, err
	}
	if len(fresh.Claims) == 0 && len(card.Claims) > 0 {
		fresh.Claims = append([]model.Claim(nil), card.Claims...)
	}
	if strings.TrimSpace(fresh.AuthorFamily) == "" && strings.TrimSpace(card.AuthorFamily) != "" {
		fresh.AuthorFamily = card.AuthorFamily
	}
	*card = fresh
	return nil, nil
}

func ensureDevDisposed(ctx context.Context, db *store.Store, attempt model.Attempt, outcome model.Outcome) error {
	done, err := attemptDisposed(ctx, db, attempt.ID)
	if err != nil || done {
		return err
	}
	return markDevDisposed(ctx, db, attempt, outcome)
}

func attemptDisposed(ctx context.Context, db *store.Store, attemptID string) (bool, error) {
	events, err := db.ListEvents(ctx, "attempt:"+attemptID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.Type == "dev_disposed" {
			return true, nil
		}
	}
	return false, nil
}

// rankingFailureResult keeps an outcome Decide already disposes. Acceptance
// failed or timeout, and a blocking error class on a passing outcome, use that same row.
func rankingFailureResult(res harvest.Result) harvest.Result {
	if designFailureOutcome(res.Outcome) {
		return res
	}
	switch strings.TrimSpace(acceptanceFromEvidence(res.Evidence)) {
	case "", "ok", "passed":
	default:
		res.Outcome = model.OutcomeTestsFailed
		if res.ErrorClass == "" || res.ErrorClass == model.ErrorPassed || res.ErrorClass == model.ErrorResultUnparsable {
			res.ErrorClass = model.ErrorTestsFailed
		}
		return res
	}
	switch res.ErrorClass {
	case model.ErrorTestsFailed:
		res.Outcome = model.OutcomeTestsFailed
	case model.ErrorRefusal:
		res.Outcome = model.OutcomeRefusal
	case model.ErrorBudgetLimited:
		res.Outcome = model.OutcomeBudgetLimited
	case model.ErrorViolation:
		res.Outcome = model.OutcomeViolation
	case model.ErrorNoOutput:
		res.Outcome = model.OutcomeNoOutput
	case model.ErrorQuotaHit:
		res.Outcome = model.OutcomeQuotaHit
	case model.ErrorAuthFailed:
		res.Outcome = model.OutcomeAuthFailed
	case model.ErrorTransport:
		res.Outcome = model.OutcomeTransport
	case model.ErrorTimeout:
		res.Outcome = model.OutcomeTimeout
	case model.ErrorPermissionDenied:
		res.Outcome = model.OutcomePermissionDenied
	}
	return res
}

func acceptanceFromEvidence(evidence string) string {
	const key = "acceptance="
	i := strings.Index(evidence, key)
	if i < 0 {
		return ""
	}
	rest := evidence[i+len(key):]
	if sp := strings.IndexAny(rest, " \t\n"); sp >= 0 {
		rest = rest[:sp]
	}
	return rest
}

func designKind(kind model.CardKind) bool {
	switch kind {
	case model.KindDesign, model.KindResearch, model.KindPlan, model.KindReplan, model.KindSpike:
		return true
	default:
		return false
	}
}

func disposeDesign(ctx context.Context, db *store.Store, home string, card *model.Card, attempt model.Attempt, res harvest.Result) ([]Notice, error) {
	if designFailureOutcome(res.Outcome) {
		return disposeDev(ctx, db, home, card, attempt, res)
	}
	if listedPlan(card.Kind, res) {
		return disposeDev(ctx, db, home, card, attempt, res)
	}
	obj, err := db.GetObjective(ctx, card.Objective)
	if err != nil {
		return nil, err
	}
	d := Deps{Store: db, WS: workspace.Git{}, Notify: notify.Stub{}}
	handled, err := d.attemptHandled(ctx, attempt.ID)
	if err != nil {
		return nil, err
	}
	if !handled {
		text := res.ResultText
		if err := d.absorbAttempt(ctx, obj, *card, attempt.ID, attempt.BaseCommit, text, res.Outcome); err != nil {
			return nil, err
		}
		if err := markDevDisposed(ctx, db, attempt, res.Outcome); err != nil {
			return nil, err
		}
	}
	fresh, err := db.GetCard(ctx, card.ID)
	if err != nil {
		return nil, err
	}
	if len(fresh.Claims) == 0 && len(card.Claims) > 0 {
		fresh.Claims = card.Claims
	}
	if strings.TrimSpace(fresh.AuthorFamily) == "" && strings.TrimSpace(card.AuthorFamily) != "" {
		fresh.AuthorFamily = card.AuthorFamily
	}
	*card = fresh
	return nil, nil
}

// designFailureOutcome is a design-family harvest §5 must dispose before consume.
func designFailureOutcome(outcome model.Outcome) bool {
	switch outcome {
	case model.OutcomeTestsFailed, model.OutcomeNoOutput, model.OutcomeQuotaHit,
		model.OutcomeAuthFailed, model.OutcomeTransport, model.OutcomeTimeout,
		model.OutcomePermissionDenied, model.OutcomeRefusal, model.OutcomeBudgetLimited,
		model.OutcomeViolation:
		return true
	default:
		return false
	}
}

// listedPlan is a passing plan or replan whose result can carry a card list
// or design sections. Empty and header-less unusable text stays on absorbAttempt.
func listedPlan(kind model.CardKind, res harvest.Result) bool {
	if kind != model.KindPlan && kind != model.KindReplan {
		return false
	}
	if res.Outcome != model.OutcomePassed {
		return false
	}
	text := res.ResultText
	if _, ok := sectionBody(text, "新卡"); ok {
		return true
	}
	list := ParseCardList(text, "", nil)
	if !list.Bad && (len(list.Cards) > 0 || len(list.Events) > 0 || len(list.Questions) > 0) {
		return true
	}
	if designUnusable(text, res.Outcome) {
		return false
	}
	return true
}

// finishDesignFailure records the design-side skip after §5 has succeeded.
// A card that was sent back to work is not treated as a terminal research skip.
func finishDesignFailure(ctx context.Context, db *store.Store, card *model.Card, attempt model.Attempt) error {
	d := Deps{Store: db, WS: workspace.Git{}, Notify: notify.Stub{}}
	fresh, err := db.GetCard(ctx, card.ID)
	if err != nil {
		return err
	}
	obj, err := db.GetObjective(ctx, fresh.Objective)
	if err != nil {
		return err
	}
	if fresh.Kind == model.KindResearch && terminalCard(fresh.Status) {
		if err := d.disposeSkippedResearch(ctx, obj, fresh, attempt.ID, attempt.BaseCommit); err != nil {
			return err
		}
	}
	if err := d.markAttempt(ctx, attempt.ID); err != nil {
		return err
	}
	if err := d.settleFrozenResearch(ctx, obj); err != nil {
		return err
	}
	if len(fresh.Claims) == 0 && len(card.Claims) > 0 {
		fresh.Claims = append([]model.Claim(nil), card.Claims...)
	}
	if strings.TrimSpace(fresh.AuthorFamily) == "" && strings.TrimSpace(card.AuthorFamily) != "" {
		fresh.AuthorFamily = card.AuthorFamily
	}
	*card = fresh
	return nil
}

func disposeDev(ctx context.Context, db *store.Store, home string, card *model.Card, attempt model.Attempt, res harvest.Result) ([]Notice, error) {
	cfg := LoadConfig(home)
	snap, err := loadSnap(ctx, db, card.Objective)
	if err != nil {
		return nil, err
	}
	st := &step{ctx: ctx, db: db, home: home, cfg: cfg, snap: snap, now: time.Now().UTC()}
	current, ok := st.snap.card(card.ID)
	if !ok {
		current = *card
	}
	if len(card.Claims) > 0 {
		current.Claims = append([]model.Claim(nil), card.Claims...)
	}
	if strings.TrimSpace(card.AuthorFamily) != "" {
		current.AuthorFamily = card.AuthorFamily
	}
	st.snap.putCard(current)
	attempt.Outcome = res.Outcome
	if res.ErrorClass != "" {
		attempt.ErrorClass = res.ErrorClass
	}
	if strings.TrimSpace(res.Excerpt) != "" {
		attempt.ErrorExcerpt = res.Excerpt
	}
	putAttempt(st.snap, attempt)
	if st.snap.disposed(attempt.ID) {
		if saved, ok := st.snap.card(card.ID); ok {
			*card = saved
		}
		return nil, nil
	}
	if err := st.decideHarvest(current, attempt); err != nil {
		return nil, err
	}
	if saved, ok := st.snap.card(card.ID); ok {
		*card = saved
	}
	notices := append([]Notice(nil), st.notices...)
	return notices, nil
}

func putAttempt(s *snap, attempt model.Attempt) {
	list := s.attempts[attempt.Card]
	for i := range list {
		if list[i].ID == attempt.ID {
			list[i] = attempt
			s.attempts[attempt.Card] = list
			return
		}
	}
	s.attempts[attempt.Card] = append(list, attempt)
}

func openHarvestAttempt(ctx context.Context, db *store.Store, cardID string) (string, error) {
	attempts, err := db.ListAttempts(ctx, cardID)
	if err != nil {
		return "", err
	}
	var id string
	var index int
	for _, attempt := range attempts {
		if attempt.Outcome != "" {
			continue
		}
		if attempt.EndedAt.IsZero() && attempt.Exit == nil {
			continue
		}
		if id == "" || attempt.Index >= index {
			id = attempt.ID
			index = attempt.Index
		}
	}
	return id, nil
}

func markDevDisposed(ctx context.Context, db *store.Store, attempt model.Attempt, outcome model.Outcome) error {
	raw, err := json.Marshal(map[string]string{
		"outcome": string(outcome),
		"card":    attempt.Card,
	})
	if err != nil {
		return err
	}
	event := model.Event{Entity: "attempt:" + attempt.ID, Type: "dev_disposed", Data: raw}
	return db.AppendEvent(ctx, &event)
}
