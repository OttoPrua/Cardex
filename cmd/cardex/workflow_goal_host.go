package main

import (
	"bufio"
	"context"
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
	TaskID           string `json:"task_id"`
	AttemptID        string `json:"attempt_id"`
	SessionID        string `json:"session_id"`
	LastInject       string `json:"last_inject,omitempty"`
	LastInjectAt     string `json:"last_inject_at,omitempty"`
	LastControl      string `json:"last_control,omitempty"`
	ControlConfirmed bool   `json:"control_confirmed"`
	NativeStatus     string `json:"native_status,omitempty"`
	FailureClass     string `json:"failure_class,omitempty"`
	SupervisorAlive  bool   `json:"supervisor_alive"`
	UpdatedAt        string `json:"updated_at"`
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

func grokNativeEventsPath(grokHome, cwd, sessionID string) string {
	return filepath.Join(grokGoalSessionDir(grokHome, cwd, sessionID), "events.jsonl")
}

type grokNativeSessionEvent struct {
	Type      string `json:"type"`
	Phase     string `json:"phase"`
	ToolName  string `json:"tool_name"`
	Decision  string `json:"decision"`
	SessionID string `json:"session_id"`
}

// nativePermissionPromptPending reports an unmatched permission_requested or a
// last phase of permission_prompt. A missing events file is not pending: fake
// CLIs and healthy hosted tests do not write one. Enter/CR on that modal
// chooses the focused option (typically allow); Esc does not dismiss it.
func nativePermissionPromptPending(eventsPath string) bool {
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		return false
	}
	unmatched := 0
	lastPhase := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev grokNativeSessionEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "phase_changed":
			lastPhase = strings.ToLower(strings.TrimSpace(ev.Phase))
		case "permission_requested":
			unmatched++
		case "permission_resolved":
			if unmatched > 0 {
				unmatched--
			}
		}
	}
	return unmatched > 0 || lastPhase == "permission_prompt"
}

func hostedControlRefuseReason(grokHome, cwd, sessionID string) error {
	if strings.TrimSpace(grokHome) == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	if !nativePermissionPromptPending(grokNativeEventsPath(grokHome, cwd, sessionID)) {
		return nil
	}
	return fmt.Errorf("%w: %s: native permission prompt is pending; hosted pause/resume/stop cannot inject a payload that could approve the tool; use formal cancel",
		errGoalCapability, goalFailControlUnsupported)
}

func persistHostedControlUnsupported(root string, t *Task, action string) {
	if t == nil || t.Goal == nil {
		return
	}
	t.Goal.LastControl = action
	t.Goal.ControlConfirmed = false
	t.Goal.FailureClass = goalFailControlUnsupported
	t.Goal.ObservationNote = "native permission prompt pending; hosted control cannot inject Esc/slash/CR; use formal cancel, not native pause/complete"
	t.touch()
	_ = saveTask(root, t)
	attemptID := firstNonBlank(t.ActiveAttemptID, t.Goal.BoundAttemptID)
	if attemptID == "" {
		return
	}
	_ = writeGoalHostStatus(root, t.ID, attemptID, goalHostStatus{
		SessionID:        t.SessionID,
		LastControl:      action,
		ControlConfirmed: false,
		FailureClass:     goalFailControlUnsupported,
		SupervisorAlive:  true,
	})
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
		// Status clause only. budget_limited is not complete/failed/term; Idle+this-Goal
		// evidence is required by nativeStopConfirmed before treating it as confirmed.
		return st == "complete" || st == "failed" || st == "term" || st == "budget_limited"
	case goalControlStatus:
		return st != ""
	default:
		return false
	}
}

func nativeIdlePhase(phase string) bool {
	return strings.ToLower(strings.TrimSpace(phase)) == "idle"
}

func nativeStopConfirmed(obs nativeGoalObservation, expectedGoalID string) bool {
	if obs.Missing || obs.Contradictory {
		return false
	}
	if strings.TrimSpace(obs.GoalID) == "" {
		return false
	}
	if expectedGoalID != "" && obs.GoalID != expectedGoalID {
		return false
	}
	st := normalizeNativeStatus(obs.NativeStatus)
	switch st {
	case "complete", "failed", "term":
		return nativeStatusMatchesControl(goalControlStop, st)
	case "budget_limited":
		return obs.BudgetLimited && nativeIdlePhase(obs.Phase) && obs.UpdatesOK
	default:
		return false
	}
}

func nativeControlConfirmed(action string, obs nativeGoalObservation, expectedGoalID string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case goalControlStop:
		return nativeStopConfirmed(obs, expectedGoalID)
	default:
		return nativeStatusMatchesControl(action, obs.NativeStatus)
	}
}

// nativeNaturalCompletionReady is the observation predicate for automatic
// hosted /quit. It is not process exit, custody release, or done. failed/term/
// budget_limited stay on the explicit stop path. Raw state.json complete or
// task status alone is never enough.
func nativeNaturalCompletionReady(obs nativeGoalObservation, expectedGoalID string) bool {
	if obs.Missing || obs.Contradictory || !obs.UpdatesOK || !obs.SummaryOK {
		return false
	}
	if strings.TrimSpace(obs.GoalID) == "" {
		return false
	}
	if expectedGoalID != "" && obs.GoalID != expectedGoalID {
		return false
	}
	if obs.BudgetLimited || obs.NotAchieved {
		return false
	}
	if normalizeNativeStatus(obs.NativeStatus) != "complete" {
		return false
	}
	if !nativeIdlePhase(obs.Phase) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(obs.Classifier), "achieved") {
		return false
	}
	if !strings.EqualFold(obs.FinalStatus, "complete") || !strings.EqualFold(obs.FinalClassifier, "achieved") {
		return false
	}
	return true
}

func nativeGoalEvidenceFingerprint(grokHome, cwd, sessionID string) string {
	if strings.TrimSpace(grokHome) == "" || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	dir := grokGoalSessionDir(grokHome, cwd, sessionID)
	var b strings.Builder
	for _, rel := range []string{filepath.Join("goal", "state.json"), "updates.jsonl", "summary.json", "events.jsonl"} {
		st, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			b.WriteString(rel)
			b.WriteString(":missing;")
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d;", rel, st.Size(), st.ModTime().UnixNano())
	}
	return b.String()
}

func nativeGoalEvidenceStable(prev, next string) bool {
	return prev != "" && next != "" && prev == next && !strings.Contains(next, ":missing;")
}

// hostedNaturalQuitReady composes current-session observation, pending-permission
// refusal, current-session final-turn evidence, and the final-output quiet gate.
// Native complete/Idle/achieved and a quiet window are additional checks: they
// are not session-turn completion. A false outputQuiet means the TUI is still
// producing summary/output.
func hostedNaturalQuitReady(grokHome, cwd, sessionID, expectedGoalID string, outputQuiet bool) bool {
	if !outputQuiet {
		return false
	}
	if hostedControlRefuseReason(grokHome, cwd, sessionID) != nil {
		return false
	}
	obs, err := observeNativeGrokGoal(grokHome, cwd, sessionID, expectedGoalID)
	if err != nil {
		return false
	}
	if !nativeNaturalCompletionReady(obs, expectedGoalID) {
		return false
	}
	return nativeCurrentSessionFinalTurnReady(grokHome, cwd, sessionID, expectedGoalID)
}

// nativeCurrentSessionFinalTurnReady scans the current session's events.jsonl
// and updates.jsonl. Ready requires turn_ended after phase_changed
// streaming_text with no later turn_started, and turn_completed after a
// matching goal_updated complete/Idle. Missing, inconsistent, wrong-session,
// out-of-order, or superseded-turn evidence is not ready.
func nativeCurrentSessionFinalTurnReady(grokHome, cwd, sessionID, expectedGoalID string) bool {
	if strings.TrimSpace(grokHome) == "" || strings.TrimSpace(sessionID) == "" {
		return false
	}
	if !nativeEventsFinalTurnReady(grokNativeEventsPath(grokHome, cwd, sessionID), sessionID) {
		return false
	}
	updatesPath := filepath.Join(grokGoalSessionDir(grokHome, cwd, sessionID), "updates.jsonl")
	return nativeUpdatesFinalTurnReady(updatesPath, sessionID, expectedGoalID)
}

func nativeEventsFinalTurnReady(path, sessionID string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	sawStreaming := false
	turnEndedAfterStreaming := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev grokNativeSessionEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			return false
		}
		if sid := strings.TrimSpace(ev.SessionID); sid != "" && sid != sessionID {
			return false
		}
		switch ev.Type {
		case "turn_started":
			// A new current-session turn is not the previous final turn.
			// Clear both flags so a later turn_ended cannot reuse prior streaming_text.
			sawStreaming = false
			turnEndedAfterStreaming = false
		case "phase_changed":
			if strings.EqualFold(strings.TrimSpace(ev.Phase), "streaming_text") {
				sawStreaming = true
				turnEndedAfterStreaming = false
			}
		case "turn_ended":
			if sawStreaming {
				turnEndedAfterStreaming = true
			}
		}
	}
	return turnEndedAfterStreaming
}

func nativeUpdatesFinalTurnReady(path, sessionID, expectedGoalID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	matchedCompleteIdle := false
	turnCompletedAfter := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec grokGoalUpdateLine
		if json.Unmarshal([]byte(line), &rec) != nil {
			return false
		}
		sid := strings.TrimSpace(rec.Params.SessionID)
		su := rec.Params.Update.SessionUpdate
		switch su {
		case "goal_updated":
			if sid != "" && sid != sessionID {
				return false
			}
			gid := strings.TrimSpace(rec.Params.Update.GoalID)
			if expectedGoalID != "" && gid != expectedGoalID {
				continue
			}
			if gid == "" {
				continue
			}
			completeIdle := normalizeNativeStatus(rec.Params.Update.Status) == "complete" && nativeIdlePhase(rec.Params.Update.Phase)
			matchedCompleteIdle = completeIdle
			turnCompletedAfter = false
		case "turn_completed":
			if sid != sessionID {
				return false
			}
			if matchedCompleteIdle {
				turnCompletedAfter = true
			}
		}
	}
	if sc.Err() != nil {
		return false
	}
	return matchedCompleteIdle && turnCompletedAfter
}

func observeExternalGrokGoal(grokHome, cwd, sessionID, expectedGoalID string) (nativeGoalObservation, error) {
	obs, err := observeNativeGrokGoal(grokHome, cwd, sessionID, expectedGoalID)
	if err != nil {
		return obs, err
	}
	return obs, nil
}

// persistHostedGoalSupervisorStart is the post-admission hosted metadata
// path used by runHostedGrokGoal after releaseAdmission. Hosted/ControlOwner
// are already persisted under scheduler ownership in admitManualGoalLaunchLocked;
// an extra active-eligible saveTask here is a former-owner write and would
// emit stale_attempt_write_rejected and invalidate the producer.
func persistHostedGoalSupervisorStart(root string, t *Task) error {
	if t == nil {
		return fmt.Errorf("%w: missing task", errWorkflowMalformed)
	}
	attemptID := t.ActiveAttemptID
	if t.Goal != nil {
		attemptID = firstNonBlank(t.ActiveAttemptID, t.Goal.BoundAttemptID)
	}
	return writeGoalHostStatus(root, t.ID, attemptID, goalHostStatus{SessionID: t.SessionID, SupervisorAlive: true})
}

// persistHostedGoalOwned re-acquires the scheduler lock before writing
// FailureClass (or other post-start hosted fields) so the write is owned
// rather than discarded as a former-owner stale CAS.
func persistHostedGoalOwned(root string, cfg *Config, t *Task) error {
	if t == nil {
		return fmt.Errorf("%w: missing task", errWorkflowMalformed)
	}
	t.touch()
	return withWorkflowSchedulerLock(root, cfg, func() error {
		return saveTask(root, t)
	})
}

// completeHostedGrokGoalAfterCmd always reclaims custody after the child has
// returned. A FailureClass persist error is returned after finalize; it must
// not skip held/unknown projection or attempt close.
func completeHostedGrokGoalAfterCmd(root string, cfg *Config, wf *WorkflowRecord, t *Task, ctx context.Context, runErr, restoreErr, persistErr error, processStarted bool) error {
	if runErr != nil && t != nil && t.Goal != nil && t.Goal.FailureClass == "" && !processStarted {
		t.Goal.FailureClass = classifyGoalLaunchError(runErr)
		if serr := persistHostedGoalOwned(root, cfg, t); serr != nil && persistErr == nil {
			persistErr = serr
		}
		if t.Goal.FailureClass == goalFailPTYIoctl || t.Goal.FailureClass == goalFailPTYMissing {
			_ = withWorkflowSchedulerLock(root, cfg, func() error {
				return abandonUnstartedGoalAttempt(root, t)
			})
			return fmt.Errorf("%s: %w", t.Goal.FailureClass, runErr)
		}
	}
	finalErr := withWorkflowSchedulerLock(root, cfg, func() error {
		return finalizeManualGoalLaunch(root, cfg, wf, t, ctx, runErr)
	})
	if finalErr != nil {
		return finalErr
	}
	if persistErr != nil {
		return persistErr
	}
	return restoreErr
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
	if grokHome == "" {
		grokHome = defaultGrokHome()
	}
	if err := hostedControlRefuseReason(grokHome, t.Dir, t.SessionID); err != nil {
		persistHostedControlUnsupported(root, t, action)
		return err
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
			if t.Goal.FailureClass == goalFailControlUnsupported && t.Goal.LastControl == action {
				return fmt.Errorf("%w: %s", errGoalCapability, goalFailControlUnsupported)
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
		if oerr == nil && nativeControlConfirmed(action, obs, t.Goal.NativeGoalID) {
			t.Goal.LastControl = action
			t.Goal.ControlConfirmed = true
			t.Goal.LastNativeStatus = obs.NativeStatus
			t.Goal.NativeGoalID = firstNonBlank(obs.GoalID, t.Goal.NativeGoalID)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := persistHostedControlUnconfirmed(root, cfg, t, action); err != nil && t.Goal != nil {
		t.Goal.LastControl = action
		t.Goal.ControlConfirmed = false
		t.Goal.FailureClass = goalFailPauseNotConfirmed
	}
	return fmt.Errorf("%w: %s", errGoalSyncRejected, goalFailPauseNotConfirmed)
}

// persistHostedControlUnconfirmed writes pause_not_confirmed through the owned
// single-writer path. A former-owner saveTask after hosted admission is discarded.
func persistHostedControlUnconfirmed(root string, cfg *Config, t *Task, action string) error {
	if t == nil {
		return fmt.Errorf("%w: missing task", errWorkflowMalformed)
	}
	note := "control bytes were sent to the PTY master; native post-state did not confirm; not accepted as pause/resume/stop"
	err := withWorkflowSchedulerLock(root, cfg, func() error {
		fresh, err := loadTask(root, t.ID)
		if err != nil {
			return err
		}
		if fresh.Goal == nil {
			return fmt.Errorf("%w: not a Goal writer", errWorkflowMalformed)
		}
		fresh.Goal.LastControl = action
		fresh.Goal.ControlConfirmed = false
		fresh.Goal.FailureClass = goalFailPauseNotConfirmed
		fresh.Goal.ObservationNote = note
		fresh.touch()
		if err := saveTask(root, fresh); err != nil {
			return err
		}
		*t = *fresh
		return nil
	})
	attemptID := ""
	if t.Goal != nil {
		attemptID = firstNonBlank(t.ActiveAttemptID, t.Goal.BoundAttemptID)
	}
	if attemptID != "" {
		_ = writeGoalHostStatus(root, t.ID, attemptID, goalHostStatus{
			SessionID:        t.SessionID,
			LastControl:      action,
			ControlConfirmed: false,
			NativeStatus:     t.Goal.LastNativeStatus,
			FailureClass:     goalFailPauseNotConfirmed,
			SupervisorAlive:  true,
		})
	}
	return err
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

func waitHostedControlConfirmed(root string, t *Task, grokHome, action, injected string, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		confirmHostedControl(root, t, grokHome, action, injected)
		if t != nil && t.Goal != nil {
			attemptID := firstNonBlank(t.ActiveAttemptID, t.Goal.BoundAttemptID)
			st, err := loadGoalHostStatus(root, t.ID, attemptID)
			if err == nil && st.LastControl == action && st.ControlConfirmed {
				return true
			}
			obs, oerr := observeNativeGrokGoal(grokHome, t.Dir, t.SessionID, t.Goal.NativeGoalID)
			if oerr == nil && nativeControlConfirmed(action, obs, t.Goal.NativeGoalID) {
				confirmHostedControl(root, t, grokHome, action, injected)
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
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
		st.ControlConfirmed = nativeControlConfirmed(action, obs, t.Goal.NativeGoalID)
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
