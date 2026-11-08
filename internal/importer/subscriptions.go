package importer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"cardex/internal/model"
	"cardex/internal/notify"
)

type v1Subscription struct {
	ID          string   `json:"id"`
	ThreadID    string   `json:"thread_id"`
	Provider    string   `json:"provider"`
	Profile     string   `json:"profile"`
	Projects    []string `json:"projects"`
	TaskIDs     []string `json:"task_ids"`
	DirPrefixes []string `json:"dir_prefixes"`
	EventTypes  []string `json:"event_types"`
	Enabled     bool     `json:"enabled"`
}

type importedTask struct {
	ID      string
	Project string
}

// mapSubscriptions reads v1 manager_wake.subscriptions and converts them.
// It does not write either home. existing lists receiver ids already stored in v2.
func mapSubscriptions(v1Home string, tasks []importedTask, existing []string) (notes []string, receivers []notify.Receiver) {
	subs, readNotes := readV1Subscriptions(v1Home)
	notes = append(notes, readNotes...)
	byTask := map[string]importedTask{}
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) == "" {
			continue
		}
		byTask[task.ID] = task
	}
	have := map[string]bool{}
	for _, id := range existing {
		if id = strings.TrimSpace(id); id != "" {
			have[id] = true
		}
	}
	seen := map[string]bool{}
	for i, sub := range subs {
		rec, lines, ok := convertSubscription(i, sub, byTask)
		notes = append(notes, lines...)
		if !ok {
			continue
		}
		if seen[rec.ID] || have[rec.ID] {
			notes = append(notes, "receiver "+rec.ID+": already exists")
			continue
		}
		seen[rec.ID] = true
		receivers = append(receivers, rec)
	}
	return notes, receivers
}

func readV1Subscriptions(v1Home string) ([]v1Subscription, []string) {
	data, err := os.ReadFile(filepath.Join(v1Home, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{"config.json: unreadable"}
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return nil, []string{"config.json: unparsable"}
	}
	raw, ok := top["manager_wake"]
	if !ok || len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var body struct {
		Subscriptions []v1Subscription `json:"subscriptions"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return nil, []string{"manager_wake: unparsable"}
	}
	return body.Subscriptions, nil
}

func convertSubscription(index int, sub v1Subscription, tasks map[string]importedTask) (notify.Receiver, []string, bool) {
	label := strings.TrimSpace(sub.ID)
	if label == "" {
		label = "subscriptions[" + itoa(index) + "]"
	}
	var notes []string
	adapter := strings.ToLower(strings.TrimSpace(sub.Provider))
	if adapter == "" {
		adapter = "codex"
	}
	target := subscriptionTarget(adapter, sub)
	if adapter == "" || target == "" || strings.TrimSpace(sub.ID) == "" {
		notes = append(notes, "subscription "+label+": missing id, adapter, or target")
		return notify.Receiver{}, notes, false
	}
	if !notify.AcceptSubscriptionID(strings.TrimSpace(sub.ID)) {
		notes = append(notes, "subscription "+label+": invalid id")
		return notify.Receiver{}, notes, false
	}
	if adapter != "codex" && adapter != "hermes" && adapter != "lark" && adapter != "file" {
		notes = append(notes, "subscription "+label+": adapter "+adapter)
		return notify.Receiver{}, notes, false
	}
	objectives, missing := mapTaskIDs(sub.TaskIDs, tasks)
	for _, id := range missing {
		notes = append(notes, "subscription "+label+": task "+id+": not found")
	}
	events, eventNotes := mapEventTypes(sub.EventTypes)
	for _, line := range eventNotes {
		notes = append(notes, "subscription "+label+": "+line)
	}
	if len(cleanList(sub.EventTypes)) > 0 && len(events) == 0 {
		return notify.Receiver{}, notes, false
	}
	rec := notify.Receiver{
		ID:      strings.TrimSpace(sub.ID),
		Adapter: adapter,
		Target:  target,
		Scope: notify.Scope{
			Projects:    cleanList(sub.Projects),
			Objectives:  objectives,
			DirPrefixes: cleanList(sub.DirPrefixes),
		},
		Events:  events,
		Enabled: sub.Enabled,
	}
	return rec, notes, true
}

func subscriptionTarget(adapter string, sub v1Subscription) string {
	thread := strings.TrimSpace(sub.ThreadID)
	profile := strings.TrimSpace(sub.Profile)
	switch adapter {
	case "hermes":
		switch {
		case profile != "" && thread != "":
			return profile + "/" + thread
		case profile != "":
			return profile
		default:
			return thread
		}
	default:
		if thread != "" {
			return thread
		}
		return profile
	}
}

func mapTaskIDs(ids []string, tasks map[string]importedTask) (objectives, missing []string) {
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		task, ok := tasks[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		objective := objectiveID(task.Project)
		if objective == "" || seen[objective] {
			continue
		}
		seen[objective] = true
		objectives = append(objectives, objective)
	}
	return objectives, missing
}

func mapEventTypes(types []string) (kinds []string, notes []string) {
	if len(types) == 0 {
		return nil, nil
	}
	done := false
	other := false
	for _, raw := range types {
		switch strings.TrimSpace(raw) {
		case "":
			continue
		case "done":
			done = true
		case "needs_owner", "held", "failed":
			other = true
		default:
			notes = append(notes, "event "+strings.TrimSpace(raw)+": no v2 notice")
		}
	}
	want := map[string]bool{}
	if done {
		want[string(model.QuestionDone)] = true
	}
	if other {
		for _, kind := range model.AllQuestionKinds() {
			if kind != model.QuestionDone {
				want[string(kind)] = true
			}
		}
	}
	for _, kind := range model.AllQuestionKinds() {
		if want[string(kind)] {
			kinds = append(kinds, string(kind))
		}
	}
	return kinds, notes
}

func cleanList(in []string) []string {
	var out []string
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func existingReceiverIDs(home string) []string {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	receivers, _, err := notify.LoadReceivers(home)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(receivers))
	for _, rec := range receivers {
		ids = append(ids, rec.ID)
	}
	return ids
}
