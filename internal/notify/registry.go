package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

// Scope is who a receiver proactively subscribes to.
// An empty scope does not subscribe; the entry is used only when resolving an origin.
type Scope struct {
	Projects    []string `json:"projects,omitempty"`
	Objectives  []string `json:"objectives,omitempty"`
	DirPrefixes []string `json:"dir_prefixes,omitempty"`
	All         bool     `json:"all,omitempty"`
}

// Receiver is one notify.receivers entry. Design §9.
type Receiver struct {
	ID      string   `json:"id"`
	Adapter string   `json:"adapter"`
	Target  string   `json:"target"`
	Root    string   `json:"root,omitempty"`
	Scope   Scope    `json:"scope,omitempty"`
	Events  []string `json:"events,omitempty"`
	Enabled bool     `json:"enabled"`
}

// EntryWarning names one receivers entry that was skipped and why.
type EntryWarning struct {
	Entry  string
	Reason string
}

func (w EntryWarning) String() string {
	entry := w.Entry
	if entry == "" {
		entry = "receivers"
	}
	return entry + ": " + w.Reason
}

// LoadReceivers reads notify.receivers from the resolved home.
// Invalid entries are skipped. A missing file is an empty list.
// The config file is not rewritten and is not failed because receivers is present.
func LoadReceivers(home string) ([]Receiver, []EntryWarning, error) {
	if strings.TrimSpace(home) == "" {
		return nil, nil, nil
	}
	doc, missing, err := readConfigDoc(home)
	if err != nil {
		return nil, []EntryWarning{{Entry: "config.json", Reason: "unreadable"}}, nil
	}
	if missing {
		return nil, nil, nil
	}
	raw, warn, err := receiversRaw(doc)
	if err != nil {
		return nil, []EntryWarning{{Entry: "receivers", Reason: "unparsable"}}, nil
	}
	if warn != "" {
		return nil, []EntryWarning{{Entry: "notify", Reason: warn}}, nil
	}
	return decodeReceivers(raw)
}

// ListReceivers returns every stored entry that has an id, including disabled ones.
// Invalid entries are still returned when they have an id, with a warning.
func ListReceivers(home string) ([]Receiver, []EntryWarning, error) {
	return LoadReceivers(home)
}

// AddReceiver appends one receiver. A duplicate id is an error and does not write.
func AddReceiver(home string, rec Receiver) error {
	rec = normalizeReceiver(rec)
	if err := validateReceiver(rec); err != nil {
		return err
	}
	return mutateReceivers(home, func(raw []json.RawMessage) ([]json.RawMessage, error) {
		for _, item := range raw {
			if rawReceiverID(item) == rec.ID {
				return nil, fmt.Errorf("notify: receiver %s already exists", rec.ID)
			}
		}
		body, err := json.Marshal(rec)
		if err != nil {
			return nil, err
		}
		return append(raw, json.RawMessage(body)), nil
	})
}

// RemoveReceiver deletes the entry with id. Other entries and keys stay.
func RemoveReceiver(home, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("notify: receiver id is empty")
	}
	found := false
	err := mutateReceivers(home, func(raw []json.RawMessage) ([]json.RawMessage, error) {
		next := make([]json.RawMessage, 0, len(raw))
		for _, item := range raw {
			if rawReceiverID(item) == id {
				found = true
				continue
			}
			next = append(next, item)
		}
		return next, nil
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("notify: receiver %s not found", id)
	}
	return nil
}

// ImportReceivers appends receivers whose ids are not already stored.
// Existing entries are left unchanged. Skipped ids are returned.
func ImportReceivers(home string, incoming []Receiver) (skipped []string, err error) {
	if len(incoming) == 0 {
		return nil, nil
	}
	err = mutateReceivers(home, func(raw []json.RawMessage) ([]json.RawMessage, error) {
		have := map[string]bool{}
		for _, item := range raw {
			if id := rawReceiverID(item); id != "" {
				have[id] = true
			}
		}
		next := append([]json.RawMessage{}, raw...)
		for _, rec := range incoming {
			rec = normalizeReceiver(rec)
			if err := validateReceiver(rec); err != nil {
				return nil, err
			}
			if have[rec.ID] {
				skipped = append(skipped, rec.ID)
				continue
			}
			body, err := json.Marshal(rec)
			if err != nil {
				return nil, err
			}
			have[rec.ID] = true
			next = append(next, json.RawMessage(body))
		}
		return next, nil
	})
	return skipped, err
}

// ReceiverFromOrigin maps codex:<thread> or hermes:<profile>/<session> to a receiver.
func ReceiverFromOrigin(raw string) (Receiver, error) {
	kind, id, ok := ParseOrigin(raw)
	if !ok {
		return Receiver{}, fmt.Errorf("notify: origin %q", raw)
	}
	if !knownAdapter(kind) {
		return Receiver{}, fmt.Errorf("notify: origin adapter %q", kind)
	}
	return Receiver{Adapter: kind, Target: id, Enabled: true}, nil
}

func knownAdapter(adapter string) bool {
	switch adapter {
	case "codex", "hermes", "lark", "file":
		return true
	default:
		return false
	}
}

func normalizeReceiver(rec Receiver) Receiver {
	rec.ID = strings.TrimSpace(rec.ID)
	rec.Adapter = strings.TrimSpace(rec.Adapter)
	rec.Target = strings.TrimSpace(rec.Target)
	rec.Root = strings.TrimSpace(rec.Root)
	rec.Scope.Projects = trimList(rec.Scope.Projects)
	rec.Scope.Objectives = trimList(rec.Scope.Objectives)
	rec.Scope.DirPrefixes = trimList(rec.Scope.DirPrefixes)
	rec.Events = trimList(rec.Events)
	return rec
}

func trimList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validateReceiver(rec Receiver) error {
	if rec.ID == "" {
		return errors.New("notify: receiver id is empty")
	}
	if !validSubscriptionID(rec.ID) {
		return fmt.Errorf("notify: receiver %s invalid id", rec.ID)
	}
	if !knownAdapter(rec.Adapter) {
		return fmt.Errorf("notify: receiver %s adapter %q", rec.ID, rec.Adapter)
	}
	if rec.Target == "" {
		return fmt.Errorf("notify: receiver %s missing target", rec.ID)
	}
	for _, ev := range rec.Events {
		if !model.QuestionKind(ev).Valid() {
			return fmt.Errorf("notify: receiver %s event %q", rec.ID, ev)
		}
	}
	return nil
}

func decodeReceivers(raw []json.RawMessage) ([]Receiver, []EntryWarning, error) {
	var (
		out  []Receiver
		warn []EntryWarning
		seen = map[string]bool{}
	)
	for i, item := range raw {
		rec, w, ok := parseReceiver(item, i)
		if !ok {
			warn = append(warn, w)
			continue
		}
		if seen[rec.ID] {
			warn = append(warn, EntryWarning{Entry: rec.ID, Reason: "duplicate id"})
			continue
		}
		seen[rec.ID] = true
		out = append(out, rec)
	}
	return out, warn, nil
}

func parseReceiver(raw json.RawMessage, index int) (Receiver, EntryWarning, bool) {
	entry := fmt.Sprintf("receivers[%d]", index)
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil {
		return Receiver{}, EntryWarning{Entry: entry, Reason: "not an object"}, false
	}
	if idRaw, ok := probe["id"]; ok {
		var id string
		if json.Unmarshal(idRaw, &id) == nil && strings.TrimSpace(id) != "" {
			entry = strings.TrimSpace(id)
		}
	}
	if reason, bad := rawFieldReason(probe); bad {
		return Receiver{}, EntryWarning{Entry: entry, Reason: reason}, false
	}
	var rec Receiver
	if json.Unmarshal(raw, &rec) != nil {
		return Receiver{}, EntryWarning{Entry: entry, Reason: "unparsable"}, false
	}
	rec = normalizeReceiver(rec)
	if rec.ID == "" {
		return Receiver{}, EntryWarning{Entry: entry, Reason: "missing id"}, false
	}
	if !validSubscriptionID(rec.ID) {
		return Receiver{}, EntryWarning{Entry: rec.ID, Reason: "invalid id"}, false
	}
	if !knownAdapter(rec.Adapter) {
		why := "missing adapter"
		if rec.Adapter != "" {
			why = "adapter " + rec.Adapter
		}
		return Receiver{}, EntryWarning{Entry: rec.ID, Reason: why}, false
	}
	if rec.Target == "" {
		return Receiver{}, EntryWarning{Entry: rec.ID, Reason: "missing target"}, false
	}
	return rec, EntryWarning{}, true
}

func rawFieldReason(probe map[string]json.RawMessage) (string, bool) {
	if rawScope, ok := probe["scope"]; ok && jsonPresent(rawScope) {
		var scope Scope
		if json.Unmarshal(rawScope, &scope) != nil {
			return "scope unparsable", true
		}
	}
	if rawEvents, ok := probe["events"]; ok && jsonPresent(rawEvents) {
		var events []string
		if json.Unmarshal(rawEvents, &events) != nil {
			return "events unparsable", true
		}
		for _, ev := range events {
			if !model.QuestionKind(strings.TrimSpace(ev)).Valid() {
				return "event " + strings.TrimSpace(ev), true
			}
		}
	}
	if rawEnabled, ok := probe["enabled"]; ok && jsonPresent(rawEnabled) {
		var enabled bool
		if json.Unmarshal(rawEnabled, &enabled) != nil {
			return "enabled unparsable", true
		}
	}
	return "", false
}

func jsonPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && string(trimmed) != "null"
}

func rawReceiverID(raw json.RawMessage) string {
	var probe struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return ""
	}
	return strings.TrimSpace(probe.ID)
}

func configPath(home string) string {
	return filepath.Join(home, "config.json")
}

func readConfigDoc(home string) (doc map[string]json.RawMessage, missing bool, err error) {
	data, err := os.ReadFile(configPath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, true, nil
		}
		return nil, false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, false, nil
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("notify: config.json unparsable")
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	return doc, false, nil
}

func receiversRaw(doc map[string]json.RawMessage) ([]json.RawMessage, string, error) {
	raw, ok := doc["notify"]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, "", nil
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return nil, "unparsable", nil
	}
	rec, ok := body["receivers"]
	if !ok || len(bytes.TrimSpace(rec)) == 0 || string(bytes.TrimSpace(rec)) == "null" {
		return nil, "", nil
	}
	var list []json.RawMessage
	if json.Unmarshal(rec, &list) != nil {
		return nil, "", fmt.Errorf("notify: receivers unparsable")
	}
	return list, "", nil
}

func mutateReceivers(home string, fn func([]json.RawMessage) ([]json.RawMessage, error)) error {
	if strings.TrimSpace(home) == "" {
		return errors.New("notify: home is empty")
	}
	doc, _, err := readConfigDoc(home)
	if err != nil {
		return err
	}
	raw, warn, err := receiversRaw(doc)
	if err != nil || warn != "" {
		if warn != "" {
			return fmt.Errorf("notify: notify config %s", warn)
		}
		return err
	}
	next, err := fn(raw)
	if err != nil {
		return err
	}
	if next == nil {
		next = []json.RawMessage{}
	}
	return setReceivers(home, doc, next)
}

func setReceivers(home string, doc map[string]json.RawMessage, list []json.RawMessage) error {
	body := map[string]json.RawMessage{}
	if raw, ok := doc["notify"]; ok && len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if json.Unmarshal(raw, &body) != nil {
			return fmt.Errorf("notify: notify config unparsable")
		}
	}
	if body == nil {
		body = map[string]json.RawMessage{}
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		return err
	}
	body["receivers"] = encoded
	notifyRaw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	doc["notify"] = notifyRaw
	return writeConfigDoc(home, doc)
}

func writeConfigDoc(home string, doc map[string]json.RawMessage) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(home, ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, configPath(home)); err != nil {
		return err
	}
	ok = true
	return nil
}
