package notify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

const (
	// EventMerged is delivered when a card enters merged.
	EventMerged = "merged"
	// EventNeedsInput is delivered when a card enters needs_input.
	EventNeedsInput = "needs_input"
	// EventFailed is delivered when a card enters failed.
	EventFailed = "failed"
	// EventReworkLimit is delivered when a card hits the rework ceiling.
	EventReworkLimit = "rework_limit"
	// EventChannelDegraded is delivered when the card's channel is taken out of routing.
	EventChannelDegraded = "channel_degraded"

	lifecycleMaxAttempts = 3
)

// Pulse is the one-line wait result and the lifecycle event body.
type Pulse struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Model      string `json:"model"`
	SHA        string `json:"sha"`
	QuestionID string `json:"question_id"`
}

// EventsEnabled reports whether lifecycle delivery is on.
// An objective value of on or off wins. Empty inherits the project.
// An empty project value is off.
func EventsEnabled(objective, project string) bool {
	switch strings.ToLower(strings.TrimSpace(objective)) {
	case "on", "true":
		return true
	case "off", "false":
		return false
	}
	switch strings.ToLower(strings.TrimSpace(project)) {
	case "on", "true":
		return true
	default:
		return false
	}
}

// lifecycleExec runs adapter commands for lifecycle events. Tests replace it.
var lifecycleExec = CommandExec

func init() {
	store.PublishCardNote = deliverCardNote
}

type lifeFile struct {
	Events map[string]lifeRec `json:"events"`
}

type lifeRec struct {
	Attempts  int    `json:"attempts"`
	Done      bool   `json:"done"`
	Origin    string `json:"origin"`
	Objective string `json:"objective"`
	Pulse     Pulse  `json:"pulse"`
}

var lifecycleMu sync.Mutex

func lifecyclePath(home string) string {
	return filepath.Join(home, "notify", "lifecycle.json")
}

func deliverCardNote(db *store.Store, note store.CardNote) {
	defer func() { _ = recover() }()
	if db == nil || note.CardID == "" || note.Kind == "" {
		return
	}
	ctx := context.Background()
	card, err := db.GetCard(ctx, note.CardID)
	if err != nil {
		return
	}
	obj, err := db.GetObjective(ctx, card.Objective)
	if err != nil {
		return
	}
	projectEvents := ""
	if obj.Project != "" {
		if project, err := db.GetProject(ctx, obj.Project); err == nil {
			projectEvents = project.Events
		}
	}
	if !EventsEnabled(obj.Events, projectEvents) {
		return
	}
	kinds := []struct{ kind, reason, suffix string }{{note.Kind, note.Reason, note.Suffix}}
	if note.Kind == EventNeedsInput {
		if text, id := reworkLimitQuestion(ctx, db, obj.ID, card.ID); text != "" {
			kinds = append(kinds, struct{ kind, reason, suffix string }{EventReworkLimit, text, ""})
			if note.Reason == "" {
				kinds[0].reason = text
			}
			_ = id
		}
	}
	for _, item := range kinds {
		pulse := cardPulse(ctx, db, card, item.kind, item.reason)
		key := note.CardID + "|" + item.kind
		if item.suffix != "" {
			key += "|" + item.suffix
		}
		tryLifecycle(ctx, db.Home(), obj.Origin, obj.ID, key, pulse)
	}
}

func reworkLimitQuestion(ctx context.Context, db *store.Store, objective, cardID string) (string, string) {
	questions, err := db.ListQuestions(ctx, objective)
	if err != nil {
		return "", ""
	}
	var text, id string
	for _, question := range questions {
		if question.Answer != "" || question.Scope != cardID {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(question.Text), "rework_count would exceed ") {
			text = question.Text
			id = question.ID
		}
	}
	return text, id
}

// CardSnapshot is the wait line for a card: its status, the model that ran,
// the latest commit, and an unanswered question.
func CardSnapshot(ctx context.Context, db *store.Store, card model.Card) Pulse {
	return cardPulse(ctx, db, card, string(card.Status), "")
}

// ObjectiveSnapshot is the wait line for an objective whose own status matched.
func ObjectiveSnapshot(ctx context.Context, db *store.Store, obj model.Objective) Pulse {
	questionID, _ := pendingQuestion(ctx, db, obj.ID, "")
	return Pulse{
		ID:         obj.ID,
		Status:     string(obj.Status),
		Model:      obj.Model,
		QuestionID: questionID,
	}
}

func cardPulse(ctx context.Context, db *store.Store, card model.Card, kind, reason string) Pulse {
	modelName, sha, excerpt := latestAttempt(ctx, db, card)
	questionID, questionText := pendingQuestion(ctx, db, card.Objective, card.ID)
	if reason == "" {
		reason = card.WaitReason
	}
	if reason == "" && (kind == EventFailed || kind == EventNeedsInput) {
		reason = excerpt
	}
	if reason == "" && kind == EventNeedsInput {
		reason = questionText
	}
	if kind == EventReworkLimit && questionText != "" && strings.HasPrefix(strings.TrimSpace(questionText), "rework_count would exceed ") {
		reason = questionText
	}
	return Pulse{
		ID:         card.ID,
		Status:     kind,
		Reason:     reason,
		Model:      modelName,
		SHA:        sha,
		QuestionID: questionID,
	}
}

func latestAttempt(ctx context.Context, db *store.Store, card model.Card) (modelName, sha, excerpt string) {
	attempts, err := db.ListAttempts(ctx, card.ID)
	modelName = card.Model
	sha = card.Base
	if err != nil || len(attempts) == 0 {
		return modelName, sha, ""
	}
	best := attempts[0]
	for _, attempt := range attempts[1:] {
		if attempt.Index > best.Index || (attempt.Index == best.Index && attempt.EndedAt.After(best.EndedAt)) {
			best = attempt
		}
	}
	if best.Route.Model != "" {
		modelName = best.Route.Model
	}
	if best.HeadCommit != "" {
		sha = best.HeadCommit
	} else if best.BaseCommit != "" {
		sha = best.BaseCommit
	}
	return modelName, sha, strings.TrimSpace(best.ErrorExcerpt)
}

func pendingQuestion(ctx context.Context, db *store.Store, objective, cardID string) (id, text string) {
	questions, err := db.ListQuestions(ctx, objective)
	if err != nil {
		return "", ""
	}
	var fallbackID, fallbackText string
	for _, question := range questions {
		if question.Answer != "" {
			continue
		}
		if question.Scope == cardID {
			return question.ID, question.Text
		}
		if fallbackID == "" && (question.Scope == "" || question.Scope == objective) {
			fallbackID = question.ID
			fallbackText = question.Text
		}
	}
	return fallbackID, fallbackText
}

func tryLifecycle(ctx context.Context, home, origin, objective, key string, pulse Pulse) {
	if strings.TrimSpace(home) == "" || key == "" {
		return
	}
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	all, err := readLifecycle(home)
	if err != nil {
		return
	}
	if all.Events == nil {
		all.Events = map[string]lifeRec{}
	}
	rec, ok := all.Events[key]
	if ok && (rec.Done || rec.Attempts >= lifecycleMaxAttempts) {
		return
	}
	rec.Origin = origin
	rec.Objective = objective
	rec.Pulse = pulse
	if strings.TrimSpace(origin) == "" {
		rec.Done = true
		rec.Attempts++
		all.Events[key] = rec
		_ = writeLifecycle(home, all)
		return
	}
	err = deliverOriginEvent(ctx, home, origin, objective, key, pulse)
	rec.Attempts++
	if err == nil {
		rec.Done = true
	}
	all.Events[key] = rec
	_ = writeLifecycle(home, all)
}

func deliverOriginEvent(ctx context.Context, home, origin, objective, key string, pulse Pulse) error {
	body, err := json.Marshal(pulse)
	if err != nil {
		return err
	}
	msg := Message{
		QuestionID: "lifecycle-" + key,
		Objective:  objective,
		Origin:     origin,
		Text:       string(body),
		Home:       home,
	}
	exec := lifecycleExec
	if exec == nil {
		exec = CommandExec
	}
	kind, id, ok := ParseOrigin(origin)
	if !ok {
		return errors.New("notify: origin missing")
	}
	svc := New(exec, nil)
	hop, skip, problem := originHop("origin", kind, id, msg)
	if problem != nil {
		return problem
	}
	if skip != "" {
		return &SkipError{Reason: skip}
	}
	return svc.deliverHop(ctx, msg, hop, time.Now().UTC(), 1)
}

// FlushLifecycle retries lifecycle deliveries that have not succeeded and have
// attempts left. A missing ledger is a no-op. Delivery failure is recorded and
// does not surface to the scheduler.
func FlushLifecycle(ctx context.Context, home string) error {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	if _, err := os.Stat(lifecyclePath(home)); err != nil {
		return nil
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		return nil
	}
	defer db.Close()
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	all, err := readLifecycle(home)
	if err != nil || len(all.Events) == 0 {
		return nil
	}
	changed := false
	for key, rec := range all.Events {
		if rec.Done || rec.Attempts >= lifecycleMaxAttempts {
			continue
		}
		if !lifecycleStillOn(ctx, db, rec.Objective) {
			continue
		}
		err := deliverOriginEvent(ctx, home, rec.Origin, rec.Objective, key, rec.Pulse)
		rec.Attempts++
		if err == nil {
			rec.Done = true
		}
		all.Events[key] = rec
		changed = true
	}
	if changed {
		_ = writeLifecycle(home, all)
	}
	return nil
}

func lifecycleStillOn(ctx context.Context, db *store.Store, objectiveID string) bool {
	obj, err := db.GetObjective(ctx, objectiveID)
	if err != nil {
		return false
	}
	projectEvents := ""
	if obj.Project != "" {
		if project, err := db.GetProject(ctx, obj.Project); err == nil {
			projectEvents = project.Events
		}
	}
	return EventsEnabled(obj.Events, projectEvents)
}

func readLifecycle(home string) (lifeFile, error) {
	raw, err := os.ReadFile(lifecyclePath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return lifeFile{}, nil
		}
		return lifeFile{}, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return lifeFile{}, nil
	}
	var all lifeFile
	if err := json.Unmarshal(raw, &all); err != nil {
		return lifeFile{}, err
	}
	return all, nil
}

func writeLifecycle(home string, all lifeFile) error {
	dir := filepath.Join(home, "notify")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(dir, ".lifecycle-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	dest := lifecyclePath(home)
	_ = os.Remove(dest)
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
