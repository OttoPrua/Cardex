//go:build !windows

package main

import (
	"fmt"
	"os"
)

func handleHostedOperatorInput(root, taskID, attemptID, sessionID string, master *os.File, req hostedInputRequest) {
	// Bind to this supervisor's original identity as well as the current task.
	if req.TaskID != taskID || req.AttemptID != attemptID || req.SessionID != sessionID || req.ID == "" {
		return
	}
	t, err := loadTask(root, taskID)
	if err == nil {
		err = validateHostedOperatorInput(root, t, req)
	}
	st, serr := loadGoalHostStatus(root, taskID, attemptID)
	if serr != nil || st.SessionID != sessionID || st.LastInputID == req.ID {
		return
	}
	if err == nil {
		payload, _ := hostedOperatorKey(req.Key)
		if master == nil {
			err = fmt.Errorf("missing original PTY master")
		} else {
			_, err = master.Write(payload)
		}
	}
	st.LastInputID, st.InputError = req.ID, ""
	if err != nil {
		st.InputError = err.Error()
	}
	_ = writeGoalHostStatus(root, taskID, attemptID, st)
}
