package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// reminderReadError is an I/O failure reading an existing ledger.
// A missing file is an empty book. A format fault is not this error.
type reminderReadError struct{ err error }

func (e *reminderReadError) Error() string { return e.err.Error() }
func (e *reminderReadError) Unwrap() error { return e.err }

// reminderFile is the per-home bookkeeping a new process reloads.
type reminderFile struct {
	Questions map[string]reminderRecord `json:"questions"`
}

type reminderRecord struct {
	AtUnixNano int64           `json:"at_unix_nano"`
	GapNS      int64           `json:"gap_ns"`
	WakeSeq    int             `json:"wake_seq"`
	WakeID     string          `json:"wake_id"`
	Digest     string          `json:"digest"`
	WakeRoot   string          `json:"wake_root,omitempty"`
	WakeSub    string          `json:"wake_sub,omitempty"`
	WakeKey    string          `json:"wake_key,omitempty"`
	Completed  map[string]bool `json:"completed,omitempty"`
}

func reminderPath(home string) string {
	return filepath.Join(home, "notify", "reminders.json")
}

func loadReminder(home, id string) (deliveryState, bool, error) {
	if strings.TrimSpace(home) == "" {
		return deliveryState{}, false, nil
	}
	all, err := readReminders(home)
	rec, ok := all.Questions[id]
	if !ok {
		return deliveryState{}, false, err
	}
	return stateFromRecord(rec), true, err
}

func saveReminder(home, id string, st deliveryState) error {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	all, err := readReminders(home)
	// A failed read is not an empty book. Leave the existing file in place.
	if err != nil {
		var readErr *reminderReadError
		if errors.As(err, &readErr) {
			return err
		}
	}
	if all.Questions == nil {
		all.Questions = map[string]reminderRecord{}
	}
	all.Questions[id] = recordFromState(st)
	return writeReminders(home, all)
}

// readReminders loads the ledger. A missing file is an empty book.
// An I/O error reading an existing file is not an empty book: callers must
// not replace that file. A format fault returns the records that still
// parsed, plus a diagnostic. Callers deliver anyway and merge new records
// onto the ones that parsed.
func readReminders(home string) (reminderFile, error) {
	raw, err := os.ReadFile(reminderPath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return reminderFile{}, nil
		}
		return reminderFile{}, &reminderReadError{err: fmt.Errorf("notify: reminders.json: %w", err)}
	}
	return decodeReminders(raw)
}

func decodeReminders(raw []byte) (reminderFile, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return reminderFile{}, fmt.Errorf("notify: reminders.json is empty")
	}
	var doc struct {
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return reminderFile{}, fmt.Errorf("notify: reminders.json: %w", err)
	}
	qs := bytes.TrimSpace(doc.Questions)
	if len(qs) == 0 || bytes.Equal(qs, []byte("null")) {
		return reminderFile{}, nil
	}
	var rawQuestions map[string]json.RawMessage
	if err := json.Unmarshal(qs, &rawQuestions); err != nil {
		return reminderFile{}, fmt.Errorf("notify: reminders.json questions: %w", err)
	}
	all := reminderFile{Questions: make(map[string]reminderRecord, len(rawQuestions))}
	ids := make([]string, 0, len(rawQuestions))
	for id := range rawQuestions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var bad []string
	for _, id := range ids {
		var rec reminderRecord
		if err := json.Unmarshal(rawQuestions[id], &rec); err != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		all.Questions[id] = rec
	}
	if len(bad) == 0 {
		return all, nil
	}
	return all, fmt.Errorf("notify: reminders.json: %s", strings.Join(bad, "; "))
}

func writeReminders(home string, all reminderFile) error {
	dir := filepath.Join(home, "notify")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(dir, ".reminders-*.json")
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
	if err := os.Rename(tmpName, reminderPath(home)); err != nil {
		return err
	}
	ok = true
	return nil
}

func recordFromState(st deliveryState) reminderRecord {
	return reminderRecord{
		AtUnixNano: st.at.UnixNano(),
		GapNS:      int64(st.gap),
		WakeSeq:    st.wakeSeq,
		WakeID:     st.wakeID,
		Digest:     st.digest,
		WakeRoot:   st.wakeRoot,
		WakeSub:    st.wakeSub,
		WakeKey:    st.wakeKey,
		Completed:  st.completed,
	}
}

func stateFromRecord(rec reminderRecord) deliveryState {
	return deliveryState{
		at:        time.Unix(0, rec.AtUnixNano).UTC(),
		gap:       time.Duration(rec.GapNS),
		wakeSeq:   rec.WakeSeq,
		wakeID:    rec.WakeID,
		digest:    rec.Digest,
		wakeRoot:  rec.WakeRoot,
		wakeSub:   rec.WakeSub,
		wakeKey:   rec.WakeKey,
		completed: rec.Completed,
	}
}
