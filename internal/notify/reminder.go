package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	if err != nil {
		return deliveryState{}, false, err
	}
	rec, ok := all.Questions[id]
	if !ok {
		return deliveryState{}, false, nil
	}
	return stateFromRecord(rec), true, nil
}

func saveReminder(home, id string, st deliveryState) error {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	all, err := readReminders(home)
	if err != nil {
		return err
	}
	if all.Questions == nil {
		all.Questions = map[string]reminderRecord{}
	}
	all.Questions[id] = recordFromState(st)
	return writeReminders(home, all)
}

func readReminders(home string) (reminderFile, error) {
	raw, err := os.ReadFile(reminderPath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return reminderFile{}, nil
		}
		return reminderFile{}, err
	}
	var all reminderFile
	if err := json.Unmarshal(raw, &all); err != nil {
		return reminderFile{}, err
	}
	return all, nil
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
		completed: rec.Completed,
	}
}
