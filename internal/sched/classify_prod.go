package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/store"
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
	noteCtx, noteCancel := classifyPersistContext(ctx)
	defer noteCancel()
	if callErr != nil {
		_ = r.noteClassify(noteCtx, db, cardID, callErr.Error())
		return "", callErr
	}
	if !classifyOutputOK(out) {
		if err := r.noteClassify(noteCtx, db, cardID, "unparseable classifier output"); err != nil {
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
	// dispatch writes this same file. Without it host.Run returns before the executor starts.
	if err := os.WriteFile(filepath.Join(dir, "hard-duration"), []byte(hardDur), 0o644); err != nil {
		return "", err
	}
	// The classifier attempt is a side card. Its script is scripts/cls-<id>.json,
	// the same lookup dispatch uses, so the work card's script is left alone.
	if script := r.scriptFor(model.Card{ID: sideID}); len(script) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "script.json"), script, 0o644); err != nil {
			return "", err
		}
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
	rawMark, err := json.Marshal(map[string]string{"card": sideID})
	if err != nil {
		return "", err
	}
	if err := db.AppendEvent(ctx, &model.Event{
		Entity: "attempt:" + attemptID,
		Type:   classifyAttemptEvent,
		Data:   rawMark,
	}); err != nil {
		return "", err
	}
	// The caller's context carries classify.Classify's 30s timeout.
	runErr := host.Run(ctx, r.Home, attemptID)
	out, _ := os.ReadFile(filepath.Join(dir, "output.log"))
	answer := classifyAnswer(string(ch.Runner), string(out))
	pctx, pcancel := classifyPersistContext(ctx)
	defer pcancel()
	got, gotErr := db.GetAttempt(pctx, attemptID)
	if gotErr == nil && got.Outcome == "" {
		got.Outcome = model.OutcomeNoOutput
		_ = db.UpdateAttempt(pctx, &got)
	}
	if reason := classifyRejected(runErr, got, gotErr); reason != "" {
		return "", fmt.Errorf("%s", reason)
	}
	return answer, nil
}

// classifyPersistContext outlives the 30s classify call context the way host
// persistContext outlives the run context. Cleanup and the error event use it.
func classifyPersistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

func classifyRejected(runErr error, got model.Attempt, gotErr error) string {
	if runErr != nil {
		return runErr.Error()
	}
	if gotErr != nil {
		return gotErr.Error()
	}
	if got.Exit != nil && *got.Exit != 0 {
		return fmt.Sprintf("classifier exit %d", *got.Exit)
	}
	if got.ErrorClass != "" && got.ErrorClass != model.ErrorPassed {
		return string(got.ErrorClass)
	}
	return ""
}

// classifyAttemptEvent marks an attempt created by the classification call.
// Harvest uses it so a user card whose id merely starts with cls- stays on
// the ordinary path.
const classifyAttemptEvent = "classify_attempt"

func classificationAttempt(ctx context.Context, db *store.Store, attemptID string) (bool, error) {
	events, err := db.ListEvents(ctx, "attempt:"+attemptID)
	if err != nil {
		return false, err
	}
	for _, ev := range events {
		if ev.Type == classifyAttemptEvent {
			return true, nil
		}
	}
	return false, nil
}

// classifyAnswer is the executor's final answer. The tier parser only sees
// line-start TIER and REASON, so structured logs are reduced to that text.
func classifyAnswer(runnerName, raw string) string {
	switch model.RunnerName(runnerName) {
	case model.RunnerClaude:
		if text, ok := claudeAnswer(raw); ok {
			return text
		}
	case model.RunnerCodex:
		if text, ok := codexAnswer(raw); ok {
			return text
		}
	case model.RunnerGrok:
		if text, ok := grokAnswer(raw); ok {
			return text
		}
	case model.RunnerCursor:
		if text, ok := cursorAnswer(raw); ok {
			return text
		}
	case model.RunnerKimi:
		if text, ok := kimiAnswer(raw); ok {
			return text
		}
	case model.RunnerAGY:
		if text, ok := agyAnswer(raw); ok {
			return text
		}
	case model.RunnerOpenCode:
		if text, ok := openCodeAnswer(raw); ok {
			return text
		}
	}
	return raw
}

func claudeAnswer(raw string) (string, bool) {
	var parts []string
	found := false
	for _, msg := range jsonObjects(raw) {
		var ev struct {
			Type   string `json:"type"`
			Result string `json:"result"`
		}
		if json.Unmarshal(msg, &ev) != nil || ev.Type != "result" {
			continue
		}
		found = true
		parts = append(parts, ev.Result)
	}
	if !found {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

func codexAnswer(raw string) (string, bool) {
	var parts []string
	found := false
	for _, msg := range jsonObjects(raw) {
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(msg, &ev) != nil || ev.Type != "item.completed" {
			continue
		}
		found = true
		if ev.Item.Type == "agent_message" {
			parts = append(parts, ev.Item.Text)
		}
	}
	if !found {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}

func grokAnswer(raw string) (string, bool) {
	var parts []string
	found := false
	for _, msg := range jsonObjects(raw) {
		var ev struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}
		if json.Unmarshal(msg, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "text":
			found = true
			parts = append(parts, ev.Data)
		case "thought", "reasoning", "plan":
			found = true
		}
	}
	if !found {
		return "", false
	}
	// Text fragments are one stream. A newline between them would split
	// "TIER: T" and "3" into a tier the line parser cannot accept.
	return strings.Join(parts, ""), true
}

func cursorAnswer(raw string) (string, bool) {
	var last string
	found := false
	for _, msg := range jsonObjects(raw) {
		var ev struct {
			Type   string `json:"type"`
			Result string `json:"result"`
		}
		if json.Unmarshal(msg, &ev) != nil || ev.Type != "result" {
			continue
		}
		found = true
		last = ev.Result
	}
	if !found {
		return "", false
	}
	return last, true
}

func kimiAnswer(raw string) (string, bool) {
	var last string
	found := false
	for _, msg := range jsonObjects(raw) {
		var ev struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(msg, &ev) != nil || strings.ToLower(strings.TrimSpace(ev.Role)) != "assistant" {
			continue
		}
		found = true
		if text := kimiContentText(ev.Content); text != "" {
			last = text
		}
	}
	if !found {
		return "", false
	}
	return last, true
}

func kimiContentText(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, block := range blocks {
		typ := strings.ToLower(strings.TrimSpace(block.Type))
		if typ != "" && typ != "text" {
			continue
		}
		b.WriteString(block.Text)
	}
	return b.String()
}

func agyAnswer(raw string) (string, bool) {
	var last string
	found := false
	for _, msg := range jsonObjects(raw) {
		var ev struct {
			Response string `json:"response"`
			Result   string `json:"result"`
			Text     string `json:"text"`
		}
		if json.Unmarshal(msg, &ev) != nil {
			continue
		}
		body := ev.Response
		if strings.TrimSpace(body) == "" {
			body = ev.Result
		}
		if strings.TrimSpace(body) == "" {
			body = ev.Text
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		found = true
		last = body
	}
	if !found {
		return "", false
	}
	return last, true
}

func openCodeAnswer(raw string) (string, bool) {
	var b strings.Builder
	found := false
	for _, msg := range jsonObjects(raw) {
		var ev struct {
			Type string `json:"type"`
			Part struct {
				Text string `json:"text"`
			} `json:"part"`
		}
		if json.Unmarshal(msg, &ev) != nil || ev.Type != "text" {
			continue
		}
		found = true
		b.WriteString(ev.Part.Text)
	}
	if !found {
		return "", false
	}
	return b.String(), true
}

func jsonObjects(raw string) []json.RawMessage {
	var out []json.RawMessage
	for i := 0; i < len(raw); i++ {
		if raw[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(raw[i:]))
		var msg json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			continue
		}
		out = append(out, msg)
		next := i + int(dec.InputOffset())
		if next <= i {
			continue
		}
		i = next - 1
	}
	return out
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
