package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	goalControlOwnerInteractive = "interactive-tui"
	goalControlOwnerHosted      = "cardex-hosted"
	goalControlOwnerExternal    = "external"

	goalControlPause  = "pause"
	goalControlResume = "resume"
	goalControlStop   = "stop"
	goalControlStatus = "status"
)

type goalHostStatus struct {
	TaskID            string `json:"task_id"`
	AttemptID         string `json:"attempt_id"`
	SessionID         string `json:"session_id"`
	LastInject        string `json:"last_inject,omitempty"`
	LastInjectAt      string `json:"last_inject_at,omitempty"`
	LastControl       string `json:"last_control,omitempty"`
	ControlConfirmed  bool   `json:"control_confirmed"`
	NativeStatus      string `json:"native_status,omitempty"`
	FailureClass      string `json:"failure_class,omitempty"`
	SupervisorAlive   bool   `json:"supervisor_alive"`
	UpdatedAt         string `json:"updated_at"`
}

func goalHostDir(root, taskID, attemptID string) string {
	return filepath.Join(root, "control", "attempts", taskID, attemptID)
}

func goalHostControlPath(root, taskID, attemptID string) string {
	return filepath.Join(goalHostDir(root, taskID, attemptID), "host-control")
}

func goalHostStatusPath(root, taskID, attemptID string) string {
	return filepath.Join(goalHostDir(root, taskID, attemptID), "host-status.json")
}

func writeGoalHostStatus(root, taskID, attemptID string, st goalHostStatus) error {
	if err := os.MkdirAll(goalHostDir(root, taskID, attemptID), 0o755); err != nil {
		return err
	}
	st.TaskID = taskID
	st.AttemptID = attemptID
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteSync(goalHostStatusPath(root, taskID, attemptID), append(raw, '\n'))
}

func loadGoalHostStatus(root, taskID, attemptID string) (goalHostStatus, error) {
	var st goalHostStatus
	raw, err := os.ReadFile(goalHostStatusPath(root, taskID, attemptID))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, err
	}
	return st, nil
}

func copyableControlCommand(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case goalControlPause:
		return "/goal pause"
	case goalControlResume:
		return "/goal resume"
	case goalControlStop:
		return "/goal clear"
	case goalControlStatus:
		return "/goal status"
	default:
		return ""
	}
}

func nativeStatusMatchesControl(action, native string) bool {
	st := normalizeNativeStatus(native)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case goalControlPause:
		// Real Grok TUI writes status=user_paused + history goal_paused/user.
		return st == "paused" || st == "user_paused" || st == "needs-input"
	case goalControlResume:
		return st == "active"
	case goalControlStop:
		return st == "complete" || st == "failed" || st == "term"
	case goalControlStatus:
		return st != ""
	default:
		return false
	}
}

func observeExternalGrokGoal(grokHome, cwd, sessionID, expectedGoalID string) (nativeGoalObservation, error) {
	obs, err := observeNativeGrokGoal(grokHome, cwd, sessionID, expectedGoalID)
	if err != nil {
		return obs, err
	}
	return obs, nil
}

func attachExternalGoalObservation(root string, t *Task, grokHome, cwd, sessionID, expectedGoalID string) error {
	if t == nil {
		return fmt.Errorf("%w: missing task", errWorkflowMalformed)
	}
	if t.Goal != nil && t.Goal.BoundAttemptID != "" && !t.Goal.External {
		return fmt.Errorf("%w: refusing to invent or overwrite a Cardex attempt from an external session", errGoalAttemptRequired)
	}
	obs, err := observeExternalGrokGoal(grokHome, cwd, sessionID, expectedGoalID)
	if err != nil && obs.GoalID == "" && obs.NativeStatus == "" {
		return err
	}
	if t.Goal == nil {
		t.Goal = &TaskGoalBinding{WriterMode: goalWriterManual}
	}
	t.SessionID = sessionID
	t.Goal.External = true
	t.Goal.Hosted = false
	t.Goal.ControlOwner = goalControlOwnerExternal
	t.Goal.NativeGoalID = obs.GoalID
	t.Goal.LastNativeStatus = obs.NativeStatus
	t.Goal.ClassifierVerdict = obs.Classifier
	t.Goal.Observation = goalObsUnknown
	if obs.NativeStatus != "" {
		mapped := mapNativeGoalToTask(obs, false, false, false)
		t.Goal.Observation = mapped.Observation
		t.Goal.ObservationNote = "external observe-only; " + mapped.Note + "; no takeover"
	} else {
		t.Goal.ObservationNote = "external session observed; no Cardex attempt created; no takeover"
	}
	t.Goal.FailureClass = goalFailExternalNoTakeover
	t.touch()
	return saveTask(root, t)
}

func requestGoalControl(root string, cfg *Config, wf *WorkflowRecord, action string, grokHome string) error {
	action = strings.ToLower(strings.TrimSpace(action))
	cmd := copyableControlCommand(action)
	if cmd == "" {
		return fmt.Errorf("%w: unsupported control %q", errGoalCapability, action)
	}
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	t, err := workflowWriterTask(root, wf)
	if err != nil {
		return err
	}
	if t.Goal == nil {
		return fmt.Errorf("%w: not a Goal writer", errWorkflowMalformed)
	}
	if t.Goal.External || t.Goal.ControlOwner == goalControlOwnerExternal {
		return fmt.Errorf("%w: external Goal is observe-only; original owner keeps control", errGoalCapability)
	}
	if !t.Goal.Hosted || t.Goal.ControlOwner != goalControlOwnerHosted {
		return fmt.Errorf("%w: pause/resume/stop are only offered for Cardex-hosted Goals; interactive TUI owner stays on the original PTY", errGoalCapability)
	}
	attemptID := firstNonBlank(t.ActiveAttemptID, t.Goal.BoundAttemptID)
	if attemptID == "" {
		return fmt.Errorf("%w: no hosted attempt", errGoalAttemptRequired)
	}
	fifo := goalHostControlPath(root, t.ID, attemptID)
	f, err := os.OpenFile(fifo, os.O_WRONLY|syscallOpenNonblock, 0)
	if err != nil {
		t.Goal.FailureClass = goalFailControlUnsupported
		t.Goal.ObservationNote = "hosted supervisor not live; write success on a missing fifo is not pause"
		_ = saveTask(root, t)
		return fmt.Errorf("%w: control owner not live (%v)", errGoalCapability, err)
	}
	defer f.Close()
	if _, err := f.Write([]byte(action + "\n")); err != nil {
		return err
	}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		fresh, lerr := loadTask(root, t.ID)
		if lerr == nil && fresh != nil && fresh.Goal != nil {
			*t = *fresh
			if t.Goal.ControlConfirmed && t.Goal.LastControl == action {
				return nil
			}
		}
		st, serr := loadGoalHostStatus(root, t.ID, attemptID)
		if serr == nil && st.LastControl == action && st.ControlConfirmed {
			return nil
		}
		if grokHome == "" {
			grokHome = defaultGrokHome()
		}
		obs, oerr := observeNativeGrokGoal(grokHome, t.Dir, t.SessionID, t.Goal.NativeGoalID)
		if oerr == nil && nativeStatusMatchesControl(action, obs.NativeStatus) {
			t.Goal.LastControl = action
			t.Goal.ControlConfirmed = true
			t.Goal.LastNativeStatus = obs.NativeStatus
			t.Goal.NativeGoalID = firstNonBlank(obs.GoalID, t.Goal.NativeGoalID)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Goal.LastControl = action
	t.Goal.ControlConfirmed = false
	t.Goal.FailureClass = goalFailPauseNotConfirmed
	t.Goal.ObservationNote = "control bytes were sent to the PTY master; native post-state did not confirm; not accepted as pause/resume/stop"
	t.touch()
	_ = saveTask(root, t)
	return fmt.Errorf("%w: %s", errGoalSyncRejected, goalFailPauseNotConfirmed)
}

func persistControlConfirmation(root, taskID, action string, obs nativeGoalObservation) error {
	return withTaskControlLock(root, taskID, func() error {
		t, err := loadTask(root, taskID)
		if err != nil || t == nil || t.Goal == nil {
			return err
		}
		t.Goal.LastControl = action
		t.Goal.LastControlAt = time.Now().UTC().Format(time.RFC3339Nano)
		t.Goal.ControlConfirmed = true
		t.Goal.FailureClass = ""
		t.Goal.LastNativeStatus = obs.NativeStatus
		t.Goal.NativeGoalID = firstNonBlank(obs.GoalID, t.Goal.NativeGoalID)
		t.Goal.PauseMessage = obs.PauseMessage
		if action == goalControlPause {
			t.Goal.Observation = goalObsHeld
			t.Goal.Continuation = "same-session"
		}
		if planningFailedUnknown(obs.PauseMessage) {
			t.Goal.FailureClass = goalFailPlanningUnknown
		}
		t.touch()
		return saveTask(root, t)
	})
}

func confirmHostedControl(root string, t *Task, grokHome, action, injected string) {
	if t == nil {
		return
	}
	if fresh, err := loadTask(root, t.ID); err == nil && fresh != nil {
		*t = *fresh
	}
	if t.Goal == nil {
		return
	}
	st := goalHostStatus{
		SessionID:       t.SessionID,
		LastInject:      injected,
		LastInjectAt:    time.Now().UTC().Format(time.RFC3339Nano),
		LastControl:     action,
		SupervisorAlive: true,
	}
	obs, err := observeNativeGrokGoal(grokHome, t.Dir, t.SessionID, t.Goal.NativeGoalID)
	if err == nil {
		st.NativeStatus = obs.NativeStatus
		if t.Goal.NativeGoalID == "" {
			t.Goal.NativeGoalID = obs.GoalID
		}
		t.Goal.LastNativeStatus = obs.NativeStatus
		t.Goal.PauseMessage = obs.PauseMessage
		st.ControlConfirmed = nativeStatusMatchesControl(action, obs.NativeStatus)
	}
	if !st.ControlConfirmed && (action == goalControlPause || action == goalControlResume || action == goalControlStop) {
		st.FailureClass = goalFailPauseNotConfirmed
	}
	_ = writeGoalHostStatus(root, t.ID, firstNonBlank(t.ActiveAttemptID, t.Goal.BoundAttemptID), st)
}

func readHostControlLine(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return "", sc.Err()
	}
	return strings.ToLower(strings.TrimSpace(sc.Text())), nil
}

func applyPlanningFailedMapping(t *Task, obs nativeGoalObservation) {
	if t == nil || t.Goal == nil {
		return
	}
	t.Goal.PauseMessage = obs.PauseMessage
	if planningFailedUnknown(obs.PauseMessage) {
		t.Goal.FailureClass = goalFailPlanningUnknown
	}
}
