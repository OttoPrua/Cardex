package sandbox

import (
	"encoding/json"
	"os/exec"
	"sync"
)

var (
	eventMu sync.Mutex
	events  = map[*exec.Cmd][]Event{}
)

func note(cmd *exec.Cmd, ev Event) {
	eventMu.Lock()
	events[cmd] = append(events[cmd], ev)
	eventMu.Unlock()
}

// Consume returns the events recorded for cmd and clears them.
func Consume(cmd *exec.Cmd) []Event {
	if cmd == nil {
		return nil
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	ev := events[cmd]
	delete(events, cmd)
	return ev
}

// Skip returns cmd unchanged and records one sandbox_skipped event.
// Non-macOS Wrap calls this.
func Skip(cmd *exec.Cmd) (*exec.Cmd, error) {
	if cmd == nil {
		return nil, osErrNil
	}
	payload, _ := json.Marshal(map[string]string{
		"message": "sandbox skipped: not macOS",
	})
	note(cmd, Event{Type: "sandbox_skipped", Data: string(payload)})
	return cmd, nil
}
