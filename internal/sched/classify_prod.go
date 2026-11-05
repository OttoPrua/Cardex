package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cardex/internal/host"
	"cardex/internal/model"
	"cardex/internal/runner"
	"cardex/internal/store"
)

type classifyCardKey struct{}

func withClassifyCard(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, classifyCardKey{}, id)
}

// productionClassify is the §7.4 call. It runs one host attempt with the
// channel's registered executor. classify.Classify applies the 30s timeout.
func (r *Runner) productionClassify(ctx context.Context, channelID, prompt string) (string, error) {
	cardID, _ := ctx.Value(classifyCardKey{}).(string)
	if strings.TrimSpace(cardID) == "" {
		return "", fmt.Errorf("sched: classify card is missing")
	}
	db, err := store.Open(filepath.Join(r.Home, "cardex.db"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	ch, err := db.GetChannel(ctx, channelID)
	if err != nil {
		return "", err
	}
	if _, ok := runner.Lookup(string(ch.Runner)); !ok {
		err := fmt.Errorf("sched: classifier runner %s is not registered", ch.Runner)
		_ = r.noteClassify(ctx, db, cardID, err.Error())
		return "", err
	}
	out, callErr := r.classifyAttempt(ctx, db, cardID, ch, prompt)
	if callErr != nil {
		_ = r.noteClassify(ctx, db, cardID, callErr.Error())
		return out, callErr
	}
	if !classifyOutputOK(out) {
		if err := r.noteClassify(ctx, db, cardID, "unparseable classifier output"); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (r *Runner) classifyAttempt(ctx context.Context, db *store.Store, cardID string, ch model.Channel, prompt string) (string, error) {
	card, err := db.GetCard(ctx, cardID)
	if err != nil {
		return "", err
	}
	sideID := "cls-" + card.ID
	if _, err := db.GetCard(ctx, sideID); err != nil {
		side := model.Card{
			ID: sideID, Objective: card.Objective, Kind: model.KindDev, Title: "classify",
			Tier: model.TierT2, Status: model.CardDone,
		}
		if err := db.CreateCard(ctx, &side); err != nil {
			return "", err
		}
	}
	attemptID, err := newID("a")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(r.Home, "attempts", attemptID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	promptPath := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return "", err
	}
	wt := filepath.Join(dir, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		return "", err
	}
	modelName := ""
	if len(ch.Models) > 0 {
		modelName = ch.Models[0]
	}
	attempt := model.Attempt{
		ID: attemptID, Card: sideID, Index: 0,
		Route:      model.Route{Channel: ch.ID, Model: modelName},
		Worktree:   wt,
		PromptPath: promptPath,
	}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		return "", err
	}
	// The caller's context carries classify.Classify's 30s timeout.
	runErr := host.Run(ctx, r.Home, attemptID)
	out, _ := os.ReadFile(filepath.Join(dir, "output.log"))
	if got, err := db.GetAttempt(ctx, attemptID); err == nil && got.Outcome == "" {
		got.Outcome = model.OutcomeNoOutput
		_ = db.UpdateAttempt(ctx, &got)
	}
	return string(out), runErr
}

func (r *Runner) noteClassify(ctx context.Context, db *store.Store, cardID, reason string) error {
	raw, err := json.Marshal(map[string]string{
		"card":   cardID,
		"reason": reason,
	})
	if err != nil {
		return err
	}
	return db.AppendEvent(ctx, &model.Event{
		Entity: "card:" + cardID,
		Type:   "classify_error",
		Data:   raw,
	})
}

func classifyOutputOK(out string) bool {
	sawTier := false
	sawReason := false
	var tier model.Tier
	reason := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "TIER:") {
			tier = model.Tier(strings.ToUpper(strings.TrimSpace(line[len("TIER:"):])))
			sawTier = true
		}
		if strings.HasPrefix(upper, "REASON:") {
			reason = strings.TrimSpace(line[len("REASON:"):])
			sawReason = true
		}
	}
	return sawTier && sawReason && reason != "" && tier.Valid() && tier != model.TierNone
}
