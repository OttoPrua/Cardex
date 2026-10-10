package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

const hostedInputProtocol = 2
const hostedInputKind = "operator-key-v2"

type hostedInputRequest struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	SessionID string `json:"session_id"`
	Key       string `json:"key"`
	Prompt    string `json:"prompt"`
}

// Hash the native event prefix ending at the sole outstanding permission
// request. Appending ordinary pending events keeps the identity; a subsequent
// request changes it even when the tool and arguments are identical.
func hostedPendingPermissionFingerprint(grokHome, cwd, sessionID string) (string, error) {
	data, err := os.ReadFile(grokNativeEventsPath(grokHome, cwd, sessionID))
	if err != nil {
		return "", err
	}
	count, offset, end := 0, 0, 0
	phase := ""
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		offset += len(line)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev grokNativeSessionEvent
		if json.Unmarshal(line, &ev) != nil || (ev.SessionID != "" && ev.SessionID != sessionID) {
			return "", fmt.Errorf("native permission evidence incomplete or wrong session")
		}
		switch ev.Type {
		case "phase_changed":
			phase = ev.Phase
		case "permission_requested":
			count++
			end = offset
		case "permission_resolved":
			count--
		}
		if count < 0 {
			return "", fmt.Errorf("native permission evidence inconsistent")
		}
	}
	if count != 1 || phase != "permission_prompt" || end == 0 {
		return "", fmt.Errorf("one unambiguous current native permission prompt is required")
	}
	return sha256Hex(string(data[:end])), nil
}

// Only explicit keys, never an auto-approval policy or a shell/slash payload.
func hostedOperatorKey(key string) ([]byte, error) {
	keys := map[string]string{"enter": "\r", "escape": "\x1b", "tab": "\t", "up": "\x1b[A", "down": "\x1b[B", "ctrl-f": "\x06"}
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		return []byte(key), nil
	}
	if payload, ok := keys[key]; ok {
		return []byte(payload), nil
	}
	return nil, fmt.Errorf("unsupported operator key %q (1..9|enter|escape|tab|up|down|ctrl-f)", key)
}

func attemptIDForHostedTask(t *Task) string {
	if t == nil || t.Goal == nil {
		return ""
	}
	return firstNonBlank(t.ActiveAttemptID, t.Goal.BoundAttemptID)
}

func validateHostedOperatorInput(root string, t *Task, req hostedInputRequest) error {
	if _, err := hostedOperatorKey(req.Key); err != nil {
		return err
	}
	if req.Kind != hostedInputKind || req.ID == "" || t == nil || t.Goal == nil ||
		req.TaskID != t.ID || req.AttemptID == "" || req.AttemptID != t.ActiveAttemptID ||
		req.AttemptID != t.Goal.BoundAttemptID || req.SessionID == "" || req.SessionID != t.SessionID {
		return fmt.Errorf("operator input identity mismatch")
	}
	if t.Status != statusRunning || t.Goal.Observation != goalObsRunning || !t.schedulingAllowed() || t.Goal.External || !t.Goal.Hosted ||
		t.Goal.ControlOwner != goalControlOwnerHosted || t.Goal.CancelRequested {
		return fmt.Errorf("operator input requires the current running hosted owner")
	}
	rec, err := loadRequiredGoalAttempt(root, t)
	if err != nil || rec.State != attemptBound || !attemptProducerAlive(rec) {
		return fmt.Errorf("operator input requires a live bound original attempt")
	}
	started, err := time.Parse(time.RFC3339Nano, rec.CreatedAt)
	if err != nil || t.Goal.HardTimeoutSec <= 0 || !time.Now().Before(started.Add(time.Duration(t.Goal.HardTimeoutSec)*time.Second)) {
		return fmt.Errorf("original hosted deadline missing or expired")
	}
	st, err := loadGoalHostStatus(root, t.ID, req.AttemptID)
	if err != nil || st.TaskID != t.ID || st.AttemptID != req.AttemptID || st.SessionID != req.SessionID ||
		!st.SupervisorAlive || st.InputProtocol != hostedInputProtocol {
		return fmt.Errorf("running supervisor has no operator-input capability; installing a binary does not upgrade an existing host")
	}
	prompt, err := hostedPendingPermissionFingerprint(goalChildGrokHome(t), t.Dir, t.SessionID)
	if err != nil || req.Prompt == "" || prompt != req.Prompt {
		return fmt.Errorf("pending native permission prompt changed or missing; observe again, do not replay an old choice")
	}
	return nil
}

func cmdWorkflowGoalInput(args []string) error {
	fs := flag.NewFlagSet("workflow goal-input", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "Cardex root")
	attempt := fs.String("attempt", "", "exact current hosted attempt ID (required)")
	session := fs.String("session", "", "exact current native session ID (required)")
	key := fs.String("key", "", "one explicit operator key: 1..9|enter|escape|tab|up|down|ctrl-f; observe the actual prompt first")
	prompt := fs.String("prompt", "", "exact pending prompt fingerprint from -observe")
	observe := fs.Bool("observe", false, "read current pending identity and a copyable command; sends no keys")
	if err := parseWorkflowFlags(fs, args); err != nil {
		return err
	}
	root, _, wf, err := workflowTarget(fs, rootFlag, "cardex workflow goal-input <id> -attempt ID -session ID -key KEY")
	if err != nil {
		return err
	}
	if *observe {
		if *key != "" || *prompt != "" {
			return fmt.Errorf("-observe sends no input; do not combine with -key/-prompt")
		}
		t, err := workflowWriterTask(root, wf)
		if err != nil {
			return err
		}
		fp, err := hostedPendingPermissionFingerprint(goalChildGrokHome(t), t.Dir, t.SessionID)
		if err != nil {
			return err
		}
		st, _ := loadGoalHostStatus(root, t.ID, attemptIDForHostedTask(t))
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"task": t.ID, "attempt": attemptIDForHostedTask(t), "session": t.SessionID, "prompt": fp, "input_protocol": st.InputProtocol, "command": fmt.Sprintf("cardex workflow goal-input %s -root %s -attempt %s -session %s -prompt %s -key KEY", wf.ID, shellQuote(root), attemptIDForHostedTask(t), t.SessionID, fp), "note": "Choose KEY only from the actual visible prompt and existing authority; numbers can select remembered/always-allow options too."})
	}
	return requestHostedOperatorInput(root, wf.WriterTaskID, *attempt, *session, *key, *prompt)
}

func goalChildGrokHome(t *Task) string {
	if t != nil && t.Goal != nil && t.Goal.GrokHome != "" {
		return t.Goal.GrokHome
	}
	return defaultGrokHome()
}

func requestHostedOperatorInput(root, taskID, attemptID, sessionID, key, prompt string) error {
	// Serialize operator requests. The original supervisor remains the sole
	// PTY owner and serializes this request with its existing Goal controls.
	return withTaskControlLock(root, taskID, func() error {
		t, err := loadTask(root, taskID)
		if err != nil {
			return err
		}
		req := hostedInputRequest{Kind: hostedInputKind, ID: newGoalSessionID(), TaskID: taskID, AttemptID: attemptID, SessionID: sessionID, Key: key, Prompt: prompt}
		if err := validateHostedOperatorInput(root, t, req); err != nil {
			return err
		}
		path := goalHostControlPath(root, taskID, attemptID)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return fmt.Errorf("operator input requires the original control FIFO")
		}
		f, err := os.OpenFile(path, os.O_WRONLY|syscallOpenNonblock, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		if actual, err := f.Stat(); err != nil || !os.SameFile(info, actual) || actual.Mode()&os.ModeNamedPipe == 0 {
			return fmt.Errorf("operator control FIFO changed")
		}
		payload, err := json.Marshal(req)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(payload, '\n')); err != nil {
			return err
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			st, err := loadGoalHostStatus(root, taskID, attemptID)
			if err == nil && st.SessionID == sessionID && st.LastInputID == req.ID {
				if st.InputError != "" {
					return fmt.Errorf("operator input refused: %s", st.InputError)
				}
				fmt.Printf("operator key delivered task=%s attempt=%s session=%s; not proof of native approval or tool completion\n", taskID, attemptID, sessionID)
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		return fmt.Errorf("operator input acknowledgement unknown; do not automatically resend the key")
	})
}
