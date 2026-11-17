package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/OttoPrua/Cardex/internal/loop"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// EventScheduleSkipped records a default schedule card that was not created
// because no approved model can do it.
const EventScheduleSkipped = "schedule_skipped"

// scheduleNoticeScope is the question scope of the one notice per objective
// that tells the deploying session why the weekly ranking card is skipped.
const scheduleNoticeScope = "schedule:ranking"

const scheduleNoticeAck = "acknowledged"

// applyDefaultSchedule enqueues the built-in weekly ranking card only when the
// approved positioning can route it. Before any table is approved nothing is
// created (the starter proposal comes first). When the table has no model with
// a required capability such as web, the card is skipped and the objective's
// origin gets one notice instead of a card that waits in ready forever.
func (r *Runner) applyDefaultSchedule(ctx context.Context, db *store.Store) error {
	tasks, err := loop.DefaultSchedule(r.Home)
	if err != nil || len(tasks) == 0 {
		return err
	}
	pos, err := db.ListPositioning(ctx)
	if err != nil {
		return err
	}
	if !hasModelRows(pos) {
		return nil
	}
	channels, err := db.ListChannels(ctx)
	if err != nil {
		return err
	}
	var need []string
	for _, task := range tasks {
		need = append(need, task.Requires...)
	}
	if missing := missingCaps(need, channels, pos); len(missing) > 0 {
		return r.noteScheduleSkipped(ctx, db, missing)
	}
	return loop.ApplyDefaultSchedule(ctx, db, r.Home, r.now())
}

func scheduleNoticeText(missing []string) string {
	caps := strings.Join(missing, ", ")
	return fmt.Sprintf(`Cardex skipped the weekly model ranking card: it needs a model with the %[1]s capability, and no approved positioning row has it. Nothing else is blocked.
Please pick one and tell the user:
1. Run the ranking from your side: if you (the deploying agent) can browse the web, create your own timer that researches current model evaluations and submits a table with cardex positioning propose --file <json>; the user then approves it. Set "schedules": [] in config.json so Cardex stops creating its own card.
2. Let Cardex run it: mark a model that can browse with %[2]q in its capabilities (cardex positioning propose --file <json>, then cardex positioning approve <id>). The card is then created on its own.
3. Turn it off: set "schedules": [] in config.json.
Also confirm the check interval with the user. The default is once a week (168h); to change it, set config.json "schedules": [{"name":"ranking","every":"336h","card_template":{"kind":"ranking","requires":["web"]}}] with the interval they want.`, caps, missing[0])
}

// noteScheduleSkipped opens the notice once per objective, answered or not, and
// records an event. It never changes positioning or config.
func (r *Runner) noteScheduleSkipped(ctx context.Context, db *store.Store, missing []string) error {
	objs, err := db.ListObjectives(ctx, "")
	if err != nil {
		return err
	}
	objID := ""
	for _, obj := range objs {
		if obj.Status == model.ObjectiveActive {
			objID = obj.ID
			break
		}
	}
	if objID == "" {
		return nil
	}
	questions, err := db.ListQuestions(ctx, objID)
	if err != nil {
		return err
	}
	for _, q := range questions {
		if q.Scope == scheduleNoticeScope {
			return nil
		}
	}
	q := model.Question{
		Objective: objID,
		Scope:     scheduleNoticeScope,
		ScopeKind: "node",
		Kind:      model.QuestionCustom,
		Text:      scheduleNoticeText(missing),
		Options:   []string{scheduleNoticeAck},
	}
	if err := db.CreateQuestion(ctx, &q); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "schedule: weekly ranking skipped, no approved model has %s; notice %s sent to objective %s\n", strings.Join(missing, ", "), q.ID, objID)
	raw, _ := json.Marshal(map[string]any{"schedule": "ranking", "missing": missing, "question": q.ID})
	return db.AppendEvent(ctx, &model.Event{TS: r.now().UTC(), Entity: "objective:" + objID, Type: EventScheduleSkipped, Data: raw})
}
