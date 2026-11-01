package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cardex/internal/harvest"
	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/store"
	"cardex/internal/workspace"
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
// replan, and spike cards use the design/research absorb path. The attempt
// outcome is left for the caller to store after nil.
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
	if designKind(card.Kind) {
		return nil, disposeDesign(ctx, db, card, attempt, res)
	}
	return disposeDev(ctx, db, home, card, attempt, res)
}

func designKind(kind model.CardKind) bool {
	switch kind {
	case model.KindDesign, model.KindResearch, model.KindPlan, model.KindReplan, model.KindSpike:
		return true
	default:
		return false
	}
}

func disposeDesign(ctx context.Context, db *store.Store, card *model.Card, attempt model.Attempt, res harvest.Result) error {
	obj, err := db.GetObjective(ctx, card.Objective)
	if err != nil {
		return err
	}
	d := Deps{Store: db, WS: workspace.Git{}, Notify: notify.Stub{}}
	handled, err := d.attemptHandled(ctx, attempt.ID)
	if err != nil {
		return err
	}
	if !handled {
		text := res.ResultText
		if err := d.absorbAttempt(ctx, obj, *card, attempt.ID, attempt.BaseCommit, text, res.Outcome); err != nil {
			return err
		}
		if err := markDevDisposed(ctx, db, attempt, res.Outcome); err != nil {
			return err
		}
	}
	fresh, err := db.GetCard(ctx, card.ID)
	if err != nil {
		return err
	}
	if len(fresh.Claims) == 0 && len(card.Claims) > 0 {
		fresh.Claims = card.Claims
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
