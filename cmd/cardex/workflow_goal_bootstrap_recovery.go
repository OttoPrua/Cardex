package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	bootstrapRecoveryKind           = "bootstrap-before-native-recovery"
	bootstrapBeforeProviderKind     = "bootstrap-before-provider-refusal"
	evBootstrapBeforeNative         = "bootstrap_before_native_recovery"
	bootstrapRecoveryActor          = "workflow:goal-bootstrap-before-native-recovery"
	bootstrapRecoveryUsage          = "cardex workflow goal-bootstrap-before-native-recovery <id> -authorization FILE -manual|-hosted [-executor-capture FILE -executor-digest SHA256 -executor-call-id CALL]"
	bootstrapAuthRequiredNote       = "explicit single-use authorization bound to workflow/writer/revision/original attempt/session/contract digest/profile digest"
	bootstrapLauncherEvidenceSuffix = ".bootstrap-refusal"
	// Exact pre-provider launcher rejection captured from the real grok
	// launch path. Broad FailureClass values and inferred event zeros are
	// not this proof.
	acceptedBootstrapBeforeProviderRefusal = "Grok hook write-deny ensure failed: cannot create managed_config.toml: Operation not permitted; named profile refused with protections missing"
)

var acceptedBootstrapBeforeProviderRefusalLines = []string{
	acceptedBootstrapBeforeProviderRefusal,
	"error: invalid option --goal",
}

var (
	errGoalBootstrapRecovery     = errors.New("workflow goal bootstrap-before-native recovery refused")
	errGoalBootstrapAuthRequired = errors.New("workflow goal bootstrap-before-native recovery requires an explicit single-use authorization")
)

type bootstrapRecoveryAuthorization struct {
	Kind              string `json:"kind"`
	WorkflowID        string `json:"workflow_id"`
	WriterTaskID      string `json:"writer_task_id"`
	Revision          int64  `json:"revision"`
	OriginalAttemptID string `json:"original_attempt_id"`
	SessionID         string `json:"session_id"`
	ContractDigest    string `json:"contract_digest"`
	ProfileDigest     string `json:"profile_digest"`
	IssuedAt          string `json:"issued_at"`
	ExpiresAt         string `json:"expires_at"`
	Nonce             string `json:"nonce"`
	// Executor* bind sidecar-missing original-executor proof into the same
	// single-use consume identity. Empty on the sidecar-backed path.
	ExecutorRawDigest  string `json:"executor_raw_digest,omitempty"`
	ExecutorCallID     string `json:"executor_call_id,omitempty"`
	ExecutorItemID     string `json:"executor_item_id,omitempty"`
	ExecutorReturnID   string `json:"executor_return_id,omitempty"`
	ExecutorProvenance string `json:"executor_provenance,omitempty"`
	// Original-async/terminal association over two unmodified raws. Empty on
	// the synchronous three-record path. Bound into consume identity.
	ExecutorAssociationKind string `json:"executor_association_kind,omitempty"`
	ExecutorHandle          string `json:"executor_handle,omitempty"`
	TerminalRawPath         string `json:"terminal_raw_path,omitempty"`
	TerminalRawDigest       string `json:"terminal_raw_digest,omitempty"`
	TerminalCallID          string `json:"terminal_call_id,omitempty"`
	TerminalReturnID        string `json:"terminal_return_id,omitempty"`
	TerminalProvenance      string `json:"terminal_provenance,omitempty"`
}

// bootstrapLauncherEvidence is trusted retained launcher/process proof bound to
// one original attempt. Owner authorization is not this document. The proof is
// the exact pre-provider refusal output captured on the real launch path plus
// the actual nonzero Wait exit; Class/typed zeros are not substitutes.
type bootstrapLauncherEvidence struct {
	Kind           string `json:"kind"`
	AttemptID      string `json:"attempt_id"`
	TaskID         string `json:"task_id"`
	ExitStatus     string `json:"exit_status"`
	Class          string `json:"class,omitempty"`
	RefusalOutput  string `json:"refusal_output,omitempty"`
	SemanticEvents int    `json:"semantic_events"`
	ModelEvents    int    `json:"model_events"`
	ToolEvents     int    `json:"tool_events"`
	NativeSession  bool   `json:"native_session"`
	NativeGoal     bool   `json:"native_goal"`
}

func bootstrapRecoveryRefused(reason string) error {
	return fmt.Errorf("%w: %s", errGoalBootstrapRecovery, reason)
}

func (a bootstrapRecoveryAuthorization) consumeDigest() string {
	parts := []string{
		a.Kind,
		a.WorkflowID,
		a.WriterTaskID,
		fmt.Sprintf("%d", a.Revision),
		a.OriginalAttemptID,
		a.SessionID,
		a.ContractDigest,
		a.ProfileDigest,
		a.Nonce,
	}
	digest := strings.ToLower(strings.TrimSpace(a.ExecutorRawDigest))
	call := strings.TrimSpace(a.ExecutorCallID)
	item := strings.TrimSpace(a.ExecutorItemID)
	ret := strings.TrimSpace(a.ExecutorReturnID)
	prov := strings.TrimSpace(a.ExecutorProvenance)
	if digest != "" || call != "" || item != "" || ret != "" || prov != "" {
		parts = append(parts, digest, call, item, ret, prov)
	}
	assoc := strings.TrimSpace(a.ExecutorAssociationKind)
	handle := strings.TrimSpace(a.ExecutorHandle)
	tdigest := strings.ToLower(strings.TrimSpace(a.TerminalRawDigest))
	tcall := strings.TrimSpace(a.TerminalCallID)
	tret := strings.TrimSpace(a.TerminalReturnID)
	tprov := strings.TrimSpace(a.TerminalProvenance)
	if assoc != "" || handle != "" || tdigest != "" || tcall != "" || tret != "" || tprov != "" {
		parts = append(parts, assoc, handle, tdigest, tcall, tret, tprov)
	}
	return sha256Hex(strings.Join(parts, "\n"))
}

func loadBootstrapRecoveryAuthorization(path string) (*bootstrapRecoveryAuthorization, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%w: %s", errGoalBootstrapAuthRequired, bootstrapAuthRequiredNote)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read authorization: %v", errGoalBootstrapAuthRequired, err)
	}
	var auth bootstrapRecoveryAuthorization
	if err := json.Unmarshal(raw, &auth); err != nil {
		return nil, fmt.Errorf("%w: malformed authorization", errGoalBootstrapAuthRequired)
	}
	auth.Kind = strings.TrimSpace(auth.Kind)
	auth.WorkflowID = strings.TrimSpace(auth.WorkflowID)
	auth.WriterTaskID = strings.TrimSpace(auth.WriterTaskID)
	auth.OriginalAttemptID = strings.TrimSpace(auth.OriginalAttemptID)
	auth.SessionID = strings.TrimSpace(auth.SessionID)
	auth.ContractDigest = strings.TrimSpace(auth.ContractDigest)
	auth.ProfileDigest = strings.TrimSpace(auth.ProfileDigest)
	auth.IssuedAt = strings.TrimSpace(auth.IssuedAt)
	auth.ExpiresAt = strings.TrimSpace(auth.ExpiresAt)
	auth.Nonce = strings.TrimSpace(auth.Nonce)
	auth.ExecutorRawDigest = strings.ToLower(strings.TrimSpace(auth.ExecutorRawDigest))
	auth.ExecutorCallID = strings.TrimSpace(auth.ExecutorCallID)
	auth.ExecutorItemID = strings.TrimSpace(auth.ExecutorItemID)
	auth.ExecutorReturnID = strings.TrimSpace(auth.ExecutorReturnID)
	auth.ExecutorProvenance = strings.TrimSpace(auth.ExecutorProvenance)
	auth.ExecutorAssociationKind = strings.TrimSpace(auth.ExecutorAssociationKind)
	auth.ExecutorHandle = strings.TrimSpace(auth.ExecutorHandle)
	auth.TerminalRawPath = strings.TrimSpace(auth.TerminalRawPath)
	auth.TerminalRawDigest = strings.ToLower(strings.TrimSpace(auth.TerminalRawDigest))
	auth.TerminalCallID = strings.TrimSpace(auth.TerminalCallID)
	auth.TerminalReturnID = strings.TrimSpace(auth.TerminalReturnID)
	auth.TerminalProvenance = strings.TrimSpace(auth.TerminalProvenance)
	if auth.Kind != bootstrapRecoveryKind || auth.WorkflowID == "" || auth.WriterTaskID == "" ||
		auth.OriginalAttemptID == "" || auth.SessionID == "" || auth.Nonce == "" || auth.ExpiresAt == "" {
		return nil, fmt.Errorf("%w: authorization missing required binding fields", errGoalBootstrapAuthRequired)
	}
	exp, err := time.Parse(time.RFC3339Nano, auth.ExpiresAt)
	if err != nil {
		exp, err = time.Parse(time.RFC3339, auth.ExpiresAt)
	}
	if err != nil {
		return nil, bootstrapRecoveryRefused("expired authorization")
	}
	if !time.Now().Before(exp) {
		return nil, bootstrapRecoveryRefused("expired authorization")
	}
	return &auth, nil
}

func launchBootstrapBeforeNativeRecovery(root string, cfg *Config, wf *WorkflowRecord, authPath string, hosted bool, executor bootstrapExecutorCaptureAdmitted) error {
	if strings.TrimSpace(authPath) == "" {
		return fmt.Errorf("%w: %s", errGoalBootstrapAuthRequired, bootstrapAuthRequiredNote)
	}
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	t, err := workflowWriterTask(root, wf)
	if err != nil {
		return err
	}
	if t == nil || t.Goal == nil {
		return fmt.Errorf("%w: missing Goal task", errWorkflowMalformed)
	}
	if t.PreferRunner != grokBuildRunnerName {
		cap := nativeGoalCapability(t.PreferRunner)
		return fmt.Errorf("%w: manual Grok /goal is the proven interactive entry; %s is %s (%s)",
			errGoalCapability, cap.Runner, cap.Level, cap.Reason)
	}
	if !grokBuildEnabled(cfg) {
		return fmt.Errorf("%w: grok_build_bin/grok_build not enabled", errGoalCapability)
	}

	// Scheduler ownership is process-wide, so concurrent calls in one process
	// still need the existing workflow admission lock around proof/consume/CAS.
	// Release both admission locks before running the provider or launch hooks.
	if err := withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		acquired := false
		if !holdsSchedulerLock(root) {
			deadline := time.Now().Add(5 * time.Second)
			for !acquired {
				if acquireLock(root, lockTTL(cfg)) {
					acquired = true
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("another cardex instance holds the scheduler lock; stop it before bootstrap-before-native recovery")
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		releaseAdmission := func() {
			if acquired {
				releaseLock(root)
				acquired = false
			}
		}
		defer releaseAdmission()

		if err := admitBootstrapBeforeNativeRecoveryLocked(root, cfg, wf, t, authPath, hosted, executor); err != nil {
			return err
		}
		releaseAdmission()
		return nil
	}); err != nil {
		return err
	}
	return executeAdmittedGoalLaunch(root, cfg, wf, t, hosted)
}

func admitBootstrapBeforeNativeRecoveryLocked(root string, cfg *Config, wf *WorkflowRecord, t *Task, authPath string, hosted bool, executor bootstrapExecutorCaptureAdmitted) error {
	auth, err := loadBootstrapRecoveryAuthorization(authPath)
	if err != nil {
		return err
	}
	fresh, err := loadTask(root, t.ID)
	if err != nil {
		return err
	}
	*t = *fresh
	if t.Goal == nil {
		return fmt.Errorf("%w: admit the writer with -mode manual before recovery", errWorkflowMalformed)
	}

	consumed, err := findBootstrapRecoveryConsume(root, t.ID, auth.consumeDigest(), auth.OriginalAttemptID)
	if err != nil {
		return err
	}
	if consumed != nil {
		return bootstrapRecoveryRefused("replayed authorization: consumed authorization")
	}
	if err := reclaimUnconsumedUnstartedRecoveryReservation(root, t, auth); err != nil {
		return err
	}
	if err := verifyBootstrapRecoveryAuthorization(root, wf, t, auth); err != nil {
		return err
	}
	if err := executor.complete(); err != nil {
		return err
	}
	if err := bindExecutorCaptureAuthorization(&executor, auth); err != nil {
		return err
	}
	executor.bindWriter(t, auth)
	if err := proveBootstrapBeforeNative(root, t, executor); err != nil {
		return err
	}
	if err := refuseBootstrapRecoveryLimits(t); err != nil {
		return err
	}

	originalAttempt := strings.TrimSpace(auth.OriginalAttemptID)
	remaining, originalStart, originalDeadline, err := bootstrapRemainingTimeout(root, t, originalAttempt)
	if err != nil {
		return err
	}

	originalBudget := t.Goal.BudgetTokens
	originalTimeout := t.Goal.HardTimeoutSec
	originalDigest := t.Goal.InputDigest
	originalProfile := t.Goal.SandboxProfileDigest
	originalProfileName := t.Goal.SandboxProfile
	originalSession := t.SessionID
	originalGrokHome := t.Goal.GrokHome
	originalRunner := t.PreferRunner
	originalRound := 0
	originalMax := 0
	if wf != nil {
		originalRound = wf.CurrentRound
		originalMax = wf.MaxRounds
	}

	restoreScheduling(t)
	t.Status = statusQueued
	t.Goal.ObservationNote = "bootstrap-before-native recovery queued; original attempt preserved"
	t.touch()
	if err := saveTask(root, t); err != nil {
		return err
	}
	if err := reserveDispatchAttempt(root, t); err != nil {
		return err
	}
	newID := strings.TrimSpace(t.ActiveAttemptID)
	if newID == "" || newID == originalAttempt {
		return bootstrapRecoveryRefused("admission did not create a distinct new attempt")
	}

	t.Goal.BoundAttemptID = newID
	t.Goal.RecoveryOriginalAttemptID = originalAttempt
	t.Goal.LaunchRemainingTimeout = remaining
	t.Goal.BudgetTokens = originalBudget
	t.Goal.HardTimeoutSec = originalTimeout
	t.Goal.InputDigest = originalDigest
	t.Goal.SandboxProfileDigest = originalProfile
	t.Goal.SandboxProfile = originalProfileName
	t.Goal.LauncherSandbox = originalProfileName
	t.SessionID = originalSession
	t.Goal.GrokHome = originalGrokHome
	t.PreferRunner = originalRunner
	t.Goal.Started = true
	t.Status = statusRunning
	t.Goal.Observation = goalObsRunning
	t.Goal.Hosted = hosted
	if hosted {
		t.Goal.ControlOwner = goalControlOwnerHosted
		t.Goal.ObservationNote = "bootstrap-before-native recovery hosted launch; original attempt preserved"
	} else {
		t.Goal.ControlOwner = goalControlOwnerInteractive
		t.Goal.ObservationNote = "bootstrap-before-native recovery manual launch; original attempt preserved"
	}
	t.Runner = grokBuildRunnerName
	t.touch()
	if err := recordBootstrapRecoveryConsume(root, t, auth, originalAttempt, newID, originalRound, originalMax, originalStart, originalDeadline); err != nil {
		return err
	}
	if err := saveTask(root, t); err != nil {
		return err
	}
	fmt.Printf("workflow %s bootstrap-before-native-recovery writer=%s original_attempt=%s new_attempt=%s\n",
		wf.ID, t.ID, originalAttempt, newID)
	return nil
}

func reclaimUnconsumedUnstartedRecoveryReservation(root string, t *Task, auth *bootstrapRecoveryAuthorization) error {
	if t == nil || t.Goal == nil || auth == nil {
		return nil
	}
	newID := strings.TrimSpace(t.ActiveAttemptID)
	if newID == "" || newID == auth.OriginalAttemptID {
		return nil
	}
	rec, err := loadAttempt(root, t.ID, newID)
	if err != nil || rec == nil {
		return nil
	}
	if goalAttemptHasStartIdentity(rec) {
		return nil
	}
	if rec.PID > 0 && attemptProducerAlive(rec) {
		return bootstrapRecoveryRefused("live custody")
	}
	if rec.State != attemptReserved && rec.State != attemptBound {
		return nil
	}
	if rec.State != attemptExited && rec.State != attemptRevoked {
		if err := closeAttemptRecord(root, t.ID, rec.AttemptID, attemptExited); err != nil {
			return err
		}
	}
	t.ActiveAttemptID = ""
	if strings.TrimSpace(t.Goal.BoundAttemptID) == rec.AttemptID {
		t.Goal.BoundAttemptID = auth.OriginalAttemptID
	}
	t.touch()
	return saveTask(root, t)
}

func verifyBootstrapRecoveryAuthorization(root string, wf *WorkflowRecord, t *Task, auth *bootstrapRecoveryAuthorization) error {
	if auth == nil {
		return fmt.Errorf("%w: %s", errGoalBootstrapAuthRequired, bootstrapAuthRequiredNote)
	}
	ev, err := findBootstrapRecoveryConsume(root, t.ID, auth.consumeDigest(), auth.OriginalAttemptID)
	if err != nil {
		return err
	}
	if ev != nil {
		return bootstrapRecoveryRefused("replayed authorization: consumed authorization")
	}
	if err := verifyBootstrapRecoveryIdentity(wf, t, auth); err != nil {
		return err
	}
	if auth.Revision != t.Revision {
		return bootstrapRecoveryRefused("stale revision")
	}
	return nil
}

func verifyBootstrapRecoveryIdentity(wf *WorkflowRecord, t *Task, auth *bootstrapRecoveryAuthorization) error {
	if wf == nil || t == nil || t.Goal == nil || auth == nil {
		return bootstrapRecoveryRefused("changed binding")
	}
	currentAttempt := strings.TrimSpace(t.Goal.BoundAttemptID)
	if currentAttempt == "" {
		currentAttempt = strings.TrimSpace(t.ActiveAttemptID)
	}
	switch {
	case auth.WorkflowID != wf.ID,
		auth.WriterTaskID != t.ID,
		auth.OriginalAttemptID != currentAttempt,
		auth.SessionID != strings.TrimSpace(t.SessionID),
		auth.ContractDigest != strings.TrimSpace(t.Goal.InputDigest),
		auth.ProfileDigest != strings.TrimSpace(t.Goal.SandboxProfileDigest):
		return bootstrapRecoveryRefused("changed binding")
	}
	return nil
}

func proveBootstrapBeforeNative(root string, t *Task, executor bootstrapExecutorCaptureAdmitted) error {
	if t == nil || t.Goal == nil {
		return bootstrapRecoveryRefused("ambiguous proof")
	}
	if taskHasLiveWriterProof(root, t) {
		return bootstrapRecoveryRefused("live custody")
	}
	rec, err := loadRequiredGoalAttempt(root, t)
	if err != nil || rec == nil {
		return bootstrapRecoveryRefused("ambiguous proof")
	}
	if attemptProducerAlive(rec) {
		return bootstrapRecoveryRefused("live custody")
	}
	grokHome := strings.TrimSpace(t.Goal.GrokHome)
	sessionDirMissing := true
	if grokHome != "" && t.SessionID != "" {
		st, statErr := os.Stat(grokGoalSessionDir(grokHome, t.Dir, t.SessionID))
		switch {
		case statErr == nil && st.IsDir():
			sessionDirMissing = false
		case statErr == nil:
			return bootstrapRecoveryRefused("malformed native Session/Goal artifacts")
		case statErr != nil && !os.IsNotExist(statErr):
			return bootstrapRecoveryRefused("unreadable native Session/Goal artifacts")
		}
	}
	if !goalAttemptHasStartIdentity(rec) {
		if sessionDirMissing {
			return bootstrapRecoveryRefused("directory absence alone is not bootstrap-before-native proof")
		}
		return bootstrapRecoveryRefused("ambiguous proof")
	}
	if strings.TrimSpace(t.Goal.SandboxProfile) == "" || strings.TrimSpace(t.Goal.SandboxProfileDigest) == "" {
		return bootstrapRecoveryRefused("ambiguous proof")
	}
	if grokHome == "" {
		return bootstrapRecoveryRefused("ambiguous proof")
	}
	if rec.State != attemptExited && rec.State != attemptBound {
		return bootstrapRecoveryRefused("ambiguous proof")
	}
	if !producerGone(t, rec) || !goalCustodyReleased(root, t) {
		return bootstrapRecoveryRefused("live custody")
	}

	hasSession, hasGoal, nerr := inspectNativeGrokSessionOrGoal(grokHome, t.Dir, t.SessionID)
	if nerr != nil {
		return nerr
	}
	if strings.TrimSpace(t.Goal.NativeGoalID) != "" || hasGoal || hasSession {
		return bootstrapRecoveryRefused("genuinely executed unknown")
	}
	switch strings.ToLower(strings.TrimSpace(t.Goal.LastNativeStatus)) {
	case "complete", "failed", "active", "budget_limited", "paused":
		return bootstrapRecoveryRefused("genuinely executed unknown")
	}
	if !t.Goal.Started {
		return bootstrapRecoveryRefused("ambiguous proof")
	}
	if executor.specified() {
		if err := executor.complete(); err != nil {
			return err
		}
		raw, err := os.ReadFile(executor.Path)
		if err != nil {
			if os.IsNotExist(err) {
				return bootstrapRecoveryRefused("missing original executor capture")
			}
			return bootstrapRecoveryRefused("unreadable original executor capture")
		}
		if _, err := validateBootstrapExecutorCapture(raw, executor, rec, t); err != nil {
			return err
		}
		return nil
	}
	evidence, err := loadBootstrapLauncherEvidence(root, t.ID, rec.AttemptID)
	if err != nil {
		return err
	}
	return validateBootstrapLauncherEvidence(evidence, t, rec, hasSession, hasGoal)
}

func bootstrapLauncherEvidencePath(root, taskID, attemptID string) string {
	return filepath.Join(attemptsDir(root, taskID), attemptID+bootstrapLauncherEvidenceSuffix)
}

func loadBootstrapLauncherEvidence(root, taskID, attemptID string) (*bootstrapLauncherEvidence, error) {
	path := bootstrapLauncherEvidencePath(root, taskID, attemptID)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, bootstrapRecoveryRefused("missing launcher/process evidence: exact bootstrap-before-provider refusal is required; recovery unavailable")
		}
		return nil, bootstrapRecoveryRefused("unreadable launcher/process evidence")
	}
	var ev bootstrapLauncherEvidence
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, bootstrapRecoveryRefused("malformed launcher/process evidence")
	}
	ev.Kind = strings.TrimSpace(ev.Kind)
	ev.AttemptID = strings.TrimSpace(ev.AttemptID)
	ev.TaskID = strings.TrimSpace(ev.TaskID)
	ev.ExitStatus = strings.TrimSpace(ev.ExitStatus)
	if ev.Kind == "" || ev.AttemptID == "" || ev.TaskID == "" || ev.ExitStatus == "" {
		return nil, bootstrapRecoveryRefused("malformed launcher/process evidence")
	}
	return &ev, nil
}

func writeBootstrapLauncherEvidence(root string, ev bootstrapLauncherEvidence) error {
	if strings.TrimSpace(ev.TaskID) == "" || strings.TrimSpace(ev.AttemptID) == "" {
		return bootstrapRecoveryRefused("malformed launcher/process evidence")
	}
	raw, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteSync(bootstrapLauncherEvidencePath(root, ev.TaskID, ev.AttemptID), append(raw, '\n'))
}

// Real Grok bootstrap rejection is an adjacent warning/error pair. Keep its
// recognition narrow; a generic filesystem failure is not pre-provider proof.
const realBootstrapWarningPrefix = "warning: sandbox could not be applied: hook write-deny ensure failed: cannot create Grok hooks-paths registry "
const realBootstrapWarningSuffix = ": Operation not permitted (os error 1)"
const realBootstrapErrorPrefix = "error: could not apply the '"
const realBootstrapErrorSuffix = "' sandbox profile; see the warning above for the cause. Refusing to start with its protections missing."

func realBootstrapRefusalPair(captured string) (registry, profile string, ok bool) {
	lines := strings.Split(executorNormalizeOutput(captured), "\n")
	found := false
	for i, line := range lines {
		if !strings.HasPrefix(line, realBootstrapWarningPrefix) {
			continue
		}
		if found || !strings.HasSuffix(line, realBootstrapWarningSuffix) || i+1 >= len(lines) {
			return "", "", false
		}
		registry = strings.TrimSuffix(strings.TrimPrefix(line, realBootstrapWarningPrefix), realBootstrapWarningSuffix)
		if !filepath.IsAbs(registry) || filepath.Clean(registry) != registry || filepath.Base(registry) != "managed_config.toml" {
			return "", "", false
		}
		next := lines[i+1]
		if !strings.HasPrefix(next, realBootstrapErrorPrefix) || !strings.HasSuffix(next, realBootstrapErrorSuffix) {
			return "", "", false
		}
		profile = strings.TrimSuffix(strings.TrimPrefix(next, realBootstrapErrorPrefix), realBootstrapErrorSuffix)
		if profile == "" {
			return "", "", false
		}
		for _, c := range profile {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", "", false
			}
		}
		found = true
	}
	return registry, profile, found
}

func bindRealBootstrapPairToGoal(captured string, writer *Task) error {
	if !strings.Contains(captured, realBootstrapWarningPrefix) {
		return nil
	}
	registry, profile, ok := realBootstrapRefusalPair(captured)
	if !ok || writer == nil || writer.Goal == nil || strings.TrimSpace(writer.Goal.GrokHome) == "" ||
		strings.TrimSpace(writer.Goal.SandboxProfile) == "" ||
		registry != filepath.Join(writer.Goal.GrokHome, "managed_config.toml") ||
		profile != writer.Goal.SandboxProfile {
		return bootstrapRecoveryRefused("wrong real bootstrap registry/profile binding")
	}
	return nil
}

func validateRealBootstrapRefusalBinding(captured, command string, writer *Task) error {
	if !strings.Contains(captured, realBootstrapWarningPrefix) {
		return nil
	}
	registry, profile, ok := realBootstrapRefusalPair(captured)
	if !ok || writer == nil || writer.Goal == nil || writer.Goal.GrokHome == "" ||
		registry != filepath.Join(writer.Goal.GrokHome, "managed_config.toml") || profile != writer.Goal.SandboxProfile ||
		extractExecutorBindingLine(captured, "sandbox_profile:") != profile {
		return bootstrapRecoveryRefused("wrong real bootstrap registry/profile binding")
	}
	fields := strings.Fields(command)
	for i, field := range fields {
		if field == "-sandbox" {
			if i+1 >= len(fields) || fields[i+1] != profile {
				return bootstrapRecoveryRefused("wrong real bootstrap registry/profile binding")
			}
		} else if strings.HasPrefix(field, "-sandbox=") && strings.TrimPrefix(field, "-sandbox=") != profile {
			return bootstrapRecoveryRefused("wrong real bootstrap registry/profile binding")
		}
	}
	return nil
}

func matchAcceptedBootstrapBeforeProviderRefusal(captured string) (string, bool) {
	normalized := executorNormalizeOutput(captured)
	if registry, profile, ok := realBootstrapRefusalPair(normalized); ok {
		return realBootstrapWarningPrefix + registry + realBootstrapWarningSuffix + "\n" +
			realBootstrapErrorPrefix + profile + realBootstrapErrorSuffix, true
	}
	if strings.Contains(captured, realBootstrapWarningPrefix) {
		return "", false
	}
	for _, line := range strings.Split(captured, "\n") {
		got := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if got == "" {
			continue
		}
		for _, want := range acceptedBootstrapBeforeProviderRefusalLines {
			if got == want {
				return want, true
			}
		}
	}
	return "", false
}

func outputContainsAcceptedBootstrapBeforeProviderRefusal(captured string) bool {
	_, ok := matchAcceptedBootstrapBeforeProviderRefusal(captured)
	return ok
}

func validateBootstrapLauncherEvidence(ev *bootstrapLauncherEvidence, t *Task, rec *AttemptRecord, hasSession, hasGoal bool) error {
	if ev == nil || t == nil || rec == nil {
		return bootstrapRecoveryRefused("missing launcher/process evidence: exact bootstrap-before-provider refusal is required; recovery unavailable")
	}
	if ev.Kind != bootstrapBeforeProviderKind {
		return bootstrapRecoveryRefused("contradictory launcher/process evidence")
	}
	if ev.AttemptID != rec.AttemptID || ev.TaskID != t.ID {
		return bootstrapRecoveryRefused("contradictory launcher/process evidence")
	}
	if ev.NativeSession || ev.NativeGoal || hasSession || hasGoal {
		return bootstrapRecoveryRefused("contradictory launcher/process evidence")
	}
	if !outputContainsAcceptedBootstrapBeforeProviderRefusal(ev.RefusalOutput) {
		if strings.TrimSpace(ev.RefusalOutput) == "" {
			return bootstrapRecoveryRefused("missing launcher/process evidence: exact bootstrap-before-provider refusal is required; recovery unavailable")
		}
		return bootstrapRecoveryRefused("generic failure is not bootstrap-before-provider refusal")
	}
	if err := bindRealBootstrapPairToGoal(ev.RefusalOutput, t); err != nil {
		return err
	}
	code, err := strconv.Atoi(ev.ExitStatus)
	if err != nil {
		return bootstrapRecoveryRefused("malformed launcher/process evidence")
	}
	if code == 0 {
		return bootstrapRecoveryRefused("generic success/no-output is not bootstrap-before-provider refusal")
	}
	if code < 0 {
		return bootstrapRecoveryRefused("generic failure is not bootstrap-before-provider refusal")
	}
	if ev.SemanticEvents != 0 || ev.ModelEvents != 0 || ev.ToolEvents != 0 {
		return bootstrapRecoveryRefused("contradictory launcher/process evidence")
	}
	return nil
}

func inspectNativeGrokSessionOrGoal(grokHome, cwd, sessionID string) (hasSession, hasGoal bool, err error) {
	grokHome = strings.TrimSpace(grokHome)
	sessionID = strings.TrimSpace(sessionID)
	if grokHome == "" || sessionID == "" {
		return false, false, nil
	}
	dir := grokGoalSessionDir(grokHome, cwd, sessionID)
	st, statErr := os.Stat(dir)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return false, false, nil
		}
		return false, false, bootstrapRecoveryRefused("unreadable native Session/Goal artifacts")
	}
	if !st.IsDir() {
		return false, false, bootstrapRecoveryRefused("malformed native Session/Goal artifacts")
	}
	hasSession, err = inspectNativeJSONFile(filepath.Join(dir, "summary.json"), func(raw []byte) (bool, error) {
		var sum grokSessionSummaryFile
		if jerr := json.Unmarshal(raw, &sum); jerr != nil {
			return false, jerr
		}
		if strings.TrimSpace(sum.Info.ID) == "" {
			return false, errors.New("empty session id")
		}
		return true, nil
	})
	if err != nil {
		return false, false, err
	}
	hasGoal, err = inspectNativeJSONFile(filepath.Join(dir, "goal", "state.json"), func(raw []byte) (bool, error) {
		var st grokNativeGoalStateFile
		if jerr := json.Unmarshal(raw, &st); jerr != nil {
			return false, jerr
		}
		if strings.TrimSpace(st.GoalID) == "" {
			return false, errors.New("empty goal id")
		}
		return true, nil
	})
	return hasSession, hasGoal, err
}

func inspectNativeJSONFile(path string, present func([]byte) (bool, error)) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, bootstrapRecoveryRefused("unreadable native Session/Goal artifacts")
	}
	ok, perr := present(raw)
	if perr != nil {
		return false, bootstrapRecoveryRefused("malformed native Session/Goal artifacts")
	}
	return ok, nil
}

func recordRetainedBootstrapBeforeProviderEvidence(root string, t *Task, rec *AttemptRecord, runErr error, capturedOutput string) {
	if t == nil || t.Goal == nil || rec == nil || runErr == nil {
		return
	}
	if !goalAttemptHasStartIdentity(rec) {
		return
	}
	exitStatus := grokBuildNormalizedProcessExitStatus(runErr)
	code, convErr := strconv.Atoi(exitStatus)
	if convErr != nil || code <= 0 {
		return
	}
	matched, ok := matchAcceptedBootstrapBeforeProviderRefusal(capturedOutput)
	if !ok {
		return
	}
	if err := bindRealBootstrapPairToGoal(matched, t); err != nil {
		return
	}
	hasSession, hasGoal, err := inspectNativeGrokSessionOrGoal(t.Goal.GrokHome, t.Dir, t.SessionID)
	if err != nil || hasSession || hasGoal || strings.TrimSpace(t.Goal.NativeGoalID) != "" {
		return
	}
	if _, existing := loadBootstrapLauncherEvidence(root, t.ID, rec.AttemptID); existing == nil {
		return
	}
	_ = writeBootstrapLauncherEvidence(root, bootstrapLauncherEvidence{
		Kind:           bootstrapBeforeProviderKind,
		AttemptID:      rec.AttemptID,
		TaskID:         t.ID,
		ExitStatus:     exitStatus,
		RefusalOutput:  matched,
		SemanticEvents: 0,
		ModelEvents:    0,
		ToolEvents:     0,
		NativeSession:  false,
		NativeGoal:     false,
	})
}

func refuseBootstrapRecoveryLimits(t *Task) error {
	if t == nil || t.Goal == nil {
		return bootstrapRecoveryRefused("ambiguous proof")
	}
	switch t.Goal.Observation {
	case goalObsDone, goalObsFailed, goalObsCanceled:
		return bootstrapRecoveryRefused("no-retry/sealed limit")
	}
	switch t.Status {
	case statusDone, statusFailed, statusCanceled:
		return bootstrapRecoveryRefused("no-retry/sealed limit")
	}
	if t.MaxAttempts > 0 && t.Attempts >= t.MaxAttempts {
		return bootstrapRecoveryRefused("no-retry/sealed limit")
	}
	if streamIncompleteGoal(t) {
		return bootstrapRecoveryRefused("stream incomplete")
	}
	return nil
}

func streamIncompleteGoal(t *Task) bool {
	if t == nil {
		return false
	}
	if t.LastRouteAttempt != nil {
		kind := strings.ToLower(strings.TrimSpace(t.LastRouteAttempt.FailureKind))
		if strings.Contains(kind, "stream_incomplete") {
			return true
		}
		sub := strings.ToLower(strings.TrimSpace(t.LastRouteAttempt.FinalReason))
		if strings.Contains(sub, "stream_incomplete") {
			return true
		}
	}
	if t.Goal != nil {
		if strings.Contains(strings.ToLower(t.Goal.FailureClass), "stream_incomplete") {
			return true
		}
		if strings.Contains(strings.ToLower(t.Goal.ObservationNote), "stream_incomplete") {
			return true
		}
	}
	return false
}

func bootstrapRemainingTimeout(root string, t *Task, originalID string) (time.Duration, time.Time, time.Time, error) {
	if t == nil || t.Goal == nil {
		return 0, time.Time{}, time.Time{}, bootstrapRecoveryRefused("unknown remaining allowance")
	}
	if t.Goal.BudgetTokens <= 0 || t.Goal.HardTimeoutSec <= 0 {
		return 0, time.Time{}, time.Time{}, bootstrapRecoveryRefused("unknown remaining allowance")
	}
	start := originalAttemptStartTime(root, t, originalID)
	if start.IsZero() {
		return 0, time.Time{}, time.Time{}, bootstrapRecoveryRefused("unknown remaining allowance")
	}
	now := time.Now()
	if start.After(now) {
		return 0, time.Time{}, time.Time{}, bootstrapRecoveryRefused("future remaining allowance")
	}
	deadline := start.Add(time.Duration(t.Goal.HardTimeoutSec) * time.Second)
	remain := deadline.Sub(now)
	if remain <= 0 {
		return 0, start, deadline, bootstrapRecoveryRefused("exhausted remaining allowance")
	}
	return remain, start, deadline, nil
}

func originalAttemptStartTime(root string, t *Task, originalID string) time.Time {
	if t == nil {
		return time.Time{}
	}
	id := strings.TrimSpace(originalID)
	if id == "" && t.Goal != nil {
		id = strings.TrimSpace(t.Goal.RecoveryOriginalAttemptID)
	}
	if id == "" {
		if recorded := originalAttemptIDFromConsume(root, t); recorded != "" {
			id = recorded
		}
	}
	var rec *AttemptRecord
	if id != "" {
		loaded, err := loadAttempt(root, t.ID, id)
		if err != nil || loaded == nil {
			return time.Time{}
		}
		rec = loaded
	} else {
		loaded, err := loadRequiredGoalAttempt(root, t)
		if err != nil || loaded == nil {
			return time.Time{}
		}
		rec = loaded
	}
	if ts, err := time.Parse(time.RFC3339Nano, rec.CreatedAt); err == nil {
		return ts
	}
	if ts, err := time.Parse(time.RFC3339, rec.CreatedAt); err == nil {
		return ts
	}
	return time.Time{}
}

func originalAttemptIDFromConsume(root string, t *Task) string {
	if t == nil {
		return ""
	}
	events, _, err := loadTaskEvents(root, t.ID)
	if err != nil {
		return ""
	}
	for i := range events {
		ev := &events[i]
		if ev.Type != evBootstrapBeforeNative {
			continue
		}
		if id := eventString(ev.Detail, "original_attempt_id"); id != "" {
			return id
		}
	}
	return ""
}

func findBootstrapRecoveryConsume(root, taskID, digest, originalAttempt string) (*TaskEvent, error) {
	events, _, err := loadTaskEvents(root, taskID)
	if err != nil {
		return nil, bootstrapRecoveryRefused("unreadable launcher/process evidence")
	}
	for i := range events {
		ev := &events[i]
		if ev.Type != evBootstrapBeforeNative {
			continue
		}
		if digest != "" && eventString(ev.Detail, "authorization_digest") == digest {
			return ev, nil
		}
		if originalAttempt != "" && eventString(ev.Detail, "original_attempt_id") == originalAttempt {
			return ev, nil
		}
	}
	return nil, nil
}

func recordBootstrapRecoveryConsume(root string, t *Task, auth *bootstrapRecoveryAuthorization, originalID, newID string, round, maxRounds int, originalStart, originalDeadline time.Time) error {
	if t == nil || auth == nil {
		return fmt.Errorf("%w: missing consume identity", errGoalBootstrapRecovery)
	}
	detail := map[string]any{
		"kind":                 bootstrapRecoveryKind,
		"authorization_digest": auth.consumeDigest(),
		"original_attempt_id":  originalID,
		"new_attempt_id":       newID,
		"workflow_id":          auth.WorkflowID,
		"session_id":           t.SessionID,
		"contract_digest":      t.Goal.InputDigest,
		"profile_digest":       t.Goal.SandboxProfileDigest,
		"round":                round,
		"max_rounds":           maxRounds,
		"budget_tokens":        t.Goal.BudgetTokens,
		"hard_timeout_seconds": t.Goal.HardTimeoutSec,
	}
	if !originalStart.IsZero() {
		detail["original_started_at"] = originalStart.UTC().Format(time.RFC3339Nano)
	}
	if !originalDeadline.IsZero() {
		detail["original_deadline"] = originalDeadline.UTC().Format(time.RFC3339Nano)
	}
	ev := TaskEvent{
		Type:      evBootstrapBeforeNative,
		Actor:     bootstrapRecoveryActor,
		Status:    t.Status,
		Step:      t.Step,
		AttemptID: newID,
		Revision:  t.Revision,
		Detail:    detail,
	}
	return recordEvent(root, t.ID, ev)
}

func eventString(detail map[string]any, key string) string {
	if detail == nil {
		return ""
	}
	v, ok := detail[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func listGoalAttemptRecords(root, taskID string) ([]*AttemptRecord, error) {
	entries, err := os.ReadDir(attemptsDir(root, taskID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*AttemptRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rec, err := loadAttempt(root, taskID, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil || rec == nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].AttemptID < out[j].AttemptID
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out, nil
}

func abandonUnstartedRecoveryAttempt(root string, t *Task) error {
	if t == nil {
		return nil
	}
	orig := ""
	if t.Goal != nil {
		orig = strings.TrimSpace(t.Goal.RecoveryOriginalAttemptID)
	}
	if current, err := loadTask(root, t.ID); err == nil && current != nil {
		*t = *current
	}
	if t.Goal == nil {
		t.Goal = &TaskGoalBinding{WriterMode: goalWriterManual}
	}
	t.Goal.RecoveryOriginalAttemptID = orig
	if t.ActiveAttemptID != "" && t.ActiveAttemptID != orig {
		rec, err := loadAttempt(root, t.ID, t.ActiveAttemptID)
		if err == nil && rec != nil && rec.PID > 0 && attemptProducerAlive(rec) {
			return fmt.Errorf("%w: attempt already started", errGoalRedispatchBlocked)
		}
		if rec != nil && rec.State != attemptExited && rec.State != attemptRevoked {
			if err := closeAttemptRecord(root, t.ID, t.ActiveAttemptID, attemptExited); err != nil {
				return err
			}
		}
		t.ActiveAttemptID = ""
	}
	if orig != "" {
		t.Goal.BoundAttemptID = orig
	}
	t.Goal.Started = true
	t.Goal.Observation = goalObsUnknown
	t.Goal.ObservationNote = "bootstrap-before-native recovery attempt did not start; original attempt preserved"
	t.Status = statusHeld
	revokeScheduling(t)
	t.touch()
	return saveTask(root, t)
}

// bootstrapExecutorCaptureAdmitted is the thinnest manager-admitted identity for
// a sidecar-missing original Codex executor JSONL snapshot. Path/digest/call-id
// come from the existing recovery entry and must match the authorization
// consume identity (digest, call/item/return, provenance).
type bootstrapExecutorCaptureAdmitted struct {
	Path           string
	Digest         string
	CallID         string
	ItemID         string
	ReturnID       string
	Provenance     string
	SessionID      string
	ContractDigest string
	ProfileDigest  string
	TaskID         string
	AttemptID      string
	// Original-async/terminal association over a later unmodified write_stdin raw.
	AssociationKind    string
	Handle             string
	TerminalPath       string
	TerminalDigest     string
	TerminalCallID     string
	TerminalReturnID   string
	TerminalProvenance string
}

const bootstrapExecutorAsyncAssociation = "original-async-terminal"

type bootstrapExecutorCaptureProof struct {
	CallID         string
	CommandID      string
	ReturnID       string
	Command        string
	ExitCode       int
	Output         string
	SessionID      string
	ContractDigest string
	ProfileDigest  string
}

func (a *bootstrapExecutorCaptureAdmitted) normalize() {
	if a == nil {
		return
	}
	a.Path = strings.TrimSpace(a.Path)
	a.Digest = strings.ToLower(strings.TrimSpace(a.Digest))
	a.CallID = strings.TrimSpace(a.CallID)
	a.ItemID = strings.TrimSpace(a.ItemID)
	a.ReturnID = strings.TrimSpace(a.ReturnID)
	a.Provenance = strings.TrimSpace(a.Provenance)
	a.SessionID = strings.TrimSpace(a.SessionID)
	a.ContractDigest = strings.TrimSpace(a.ContractDigest)
	a.ProfileDigest = strings.TrimSpace(a.ProfileDigest)
	a.TaskID = strings.TrimSpace(a.TaskID)
	a.AttemptID = strings.TrimSpace(a.AttemptID)
	a.AssociationKind = strings.TrimSpace(a.AssociationKind)
	a.Handle = strings.TrimSpace(a.Handle)
	a.TerminalPath = strings.TrimSpace(a.TerminalPath)
	a.TerminalDigest = strings.ToLower(strings.TrimSpace(a.TerminalDigest))
	a.TerminalCallID = strings.TrimSpace(a.TerminalCallID)
	a.TerminalReturnID = strings.TrimSpace(a.TerminalReturnID)
	a.TerminalProvenance = strings.TrimSpace(a.TerminalProvenance)
}

func (a bootstrapExecutorCaptureAdmitted) specified() bool {
	a.normalize()
	return a.Path != "" || a.Digest != "" || a.CallID != ""
}

func (a *bootstrapExecutorCaptureAdmitted) complete() error {
	if a == nil {
		return nil
	}
	a.normalize()
	if !a.specified() {
		return nil
	}
	if a.Path == "" {
		return bootstrapRecoveryRefused("missing original executor capture")
	}
	if a.Digest == "" {
		return bootstrapRecoveryRefused("missing executor capture digest")
	}
	if a.CallID == "" {
		return bootstrapRecoveryRefused("missing call identity")
	}
	return nil
}

func (a *bootstrapExecutorCaptureAdmitted) bindWriter(t *Task, auth *bootstrapRecoveryAuthorization) {
	if a == nil || !a.specified() || t == nil || t.Goal == nil {
		return
	}
	a.SessionID = strings.TrimSpace(t.SessionID)
	a.ContractDigest = strings.TrimSpace(t.Goal.InputDigest)
	a.ProfileDigest = strings.TrimSpace(t.Goal.SandboxProfileDigest)
}

func bindExecutorCaptureAuthorization(executor *bootstrapExecutorCaptureAdmitted, auth *bootstrapRecoveryAuthorization) error {
	if executor == nil || !executor.specified() {
		return nil
	}
	if auth == nil {
		return bootstrapRecoveryRefused("unbound executor digest/event in authorization")
	}
	executor.normalize()
	digest := strings.ToLower(strings.TrimSpace(auth.ExecutorRawDigest))
	call := strings.TrimSpace(auth.ExecutorCallID)
	item := strings.TrimSpace(auth.ExecutorItemID)
	ret := strings.TrimSpace(auth.ExecutorReturnID)
	prov := strings.TrimSpace(auth.ExecutorProvenance)
	if digest == "" || call == "" || item == "" || ret == "" || prov == "" {
		return bootstrapRecoveryRefused("unbound executor digest/event in authorization")
	}
	if executor.Digest != digest || executor.CallID != call {
		return bootstrapRecoveryRefused("unbound executor digest/event in authorization")
	}
	executor.ItemID = item
	executor.ReturnID = ret
	executor.Provenance = prov
	assoc := strings.TrimSpace(auth.ExecutorAssociationKind)
	handle := strings.TrimSpace(auth.ExecutorHandle)
	tpath := strings.TrimSpace(auth.TerminalRawPath)
	tdigest := strings.ToLower(strings.TrimSpace(auth.TerminalRawDigest))
	tcall := strings.TrimSpace(auth.TerminalCallID)
	tret := strings.TrimSpace(auth.TerminalReturnID)
	tprov := strings.TrimSpace(auth.TerminalProvenance)
	if assoc != "" || handle != "" || tpath != "" || tdigest != "" || tcall != "" || tret != "" || tprov != "" {
		if assoc != bootstrapExecutorAsyncAssociation || handle == "" || tpath == "" || tdigest == "" || tcall == "" || tret == "" || tprov == "" {
			return bootstrapRecoveryRefused("unbound original-async/terminal association")
		}
		if tcall == call || executorSamePath(tpath, executor.Path) {
			return bootstrapRecoveryRefused("unbound original-async/terminal association")
		}
		executor.AssociationKind = assoc
		executor.Handle = handle
		executor.TerminalPath = tpath
		executor.TerminalDigest = tdigest
		executor.TerminalCallID = tcall
		executor.TerminalReturnID = tret
		executor.TerminalProvenance = tprov
	}
	return nil
}

func validateBootstrapExecutorCapture(raw []byte, admitted bootstrapExecutorCaptureAdmitted, rec *AttemptRecord, writer *Task) (*bootstrapExecutorCaptureProof, error) {
	admitted.normalize()
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, bootstrapRecoveryRefused("truncated original executor capture")
	}
	if admitted.Digest == "" {
		return nil, bootstrapRecoveryRefused("missing executor capture digest")
	}
	if sha256Hex(string(raw)) != admitted.Digest {
		return nil, bootstrapRecoveryRefused("executor capture digest mismatch")
	}
	if admitted.CallID == "" {
		return nil, bootstrapRecoveryRefused("missing call identity")
	}
	if admitted.ItemID == "" || admitted.ReturnID == "" {
		return nil, bootstrapRecoveryRefused("unbound executor digest/event in authorization")
	}

	records, err := splitExecutorJSONL(raw)
	if err != nil {
		return nil, err
	}
	if len(records) < 3 {
		if len(records) == 1 && !executorLooksLikeKnownRecord(records[0]) {
			return nil, bootstrapRecoveryRefused("unknown executor capture format")
		}
		return nil, bootstrapRecoveryRefused("truncated original executor capture")
	}
	if len(records) > 3 {
		for _, extra := range records[3:] {
			if executorLooksLikeLaterApproval(extra) {
				return nil, bootstrapRecoveryRefused("later approved retry is not original executor capture")
			}
		}
		return nil, bootstrapRecoveryRefused("modified original executor capture")
	}

	callPayload, item, outPayload, err := parseKnownExecutorTriple(records)
	if err != nil {
		return nil, err
	}

	callID := strings.TrimSpace(executorString(callPayload["call_id"]))
	if callID != admitted.CallID {
		return nil, bootstrapRecoveryRefused("wrong call identity")
	}
	itemID := strings.TrimSpace(executorString(item["id"]))
	if itemID != admitted.ItemID {
		return nil, bootstrapRecoveryRefused("wrong item identity")
	}
	if executorString(callPayload["name"]) != "exec" {
		return nil, bootstrapRecoveryRefused("wrong command identity")
	}
	input := executorString(callPayload["input"])
	selected := executorExtractGoalRunCmd(input)
	command := executorSelectedCommand(item)
	if selected == "" || command == "" || selected != command {
		return nil, bootstrapRecoveryRefused("wrong command identity")
	}
	wfID := ""
	if writer != nil {
		wfID = strings.TrimSpace(writer.WorkflowID)
	}
	if wfID == "" || !executorCommandTargetsWorkflow(selected, wfID) {
		return nil, bootstrapRecoveryRefused("wrong command identity")
	}
	processID := executorProcessID(item)
	if processID == "" {
		return nil, bootstrapRecoveryRefused("truncated original executor capture")
	}

	status := strings.ToLower(strings.TrimSpace(executorString(item["status"])))
	switch status {
	case "completed", "approved", "success":
		return nil, bootstrapRecoveryRefused("later approved retry is not original executor capture")
	case "failed":
	default:
		return nil, bootstrapRecoveryRefused("missing terminal nonzero exit")
	}

	code, ok := executorJSONInt(item["exit_code"])
	if !ok {
		return nil, bootstrapRecoveryRefused("missing terminal nonzero exit")
	}
	if code == 0 {
		return nil, bootstrapRecoveryRefused("missing terminal nonzero exit")
	}
	if code < 0 {
		return nil, bootstrapRecoveryRefused("generic failure is not bootstrap-before-provider refusal")
	}

	outCall := strings.TrimSpace(executorString(outPayload["call_id"]))
	outID := strings.TrimSpace(executorString(outPayload["id"]))
	if outCall != callID || outID != admitted.ReturnID {
		return nil, bootstrapRecoveryRefused("wrong return identity")
	}

	stdout := executorNormalizeOutput(executorString(item["stdout"]))
	agg := executorNormalizeOutput(executorString(item["aggregated_output"]))
	formatted := executorNormalizeOutput(executorString(item["formatted_output"]))
	// Some retained executor events expose only the complete aggregate. The
	// selected content still passes every refusal, channel and identity guard.
	if strings.TrimSpace(stdout) == "" {
		stdout = agg
	}
	var retNorm string
	if admitted.AssociationKind != "" {
		if admitted.AssociationKind != bootstrapExecutorAsyncAssociation {
			return nil, bootstrapRecoveryRefused("unbound original-async/terminal association")
		}
		retOut, err := executorOriginalAsyncHandleOutput(outPayload, admitted.Handle, processID)
		if err != nil {
			return nil, err
		}
		terminalOut, err := validateExecutorWriteStdinTerminal(admitted, records, rec, processID, code)
		if err != nil {
			return nil, err
		}
		// Compare the exact complete consumer view after the independent wait
		// proof is validated. No nonempty terminal output may be ignored.
		retNorm = executorNormalizeOutput(retOut + terminalOut)
	} else {
		retOut, chunkCode, chunkOK := executorLastFailedChunkOutput(outPayload)
		if !chunkOK {
			return nil, bootstrapRecoveryRefused("missing terminal nonzero exit")
		}
		if chunkCode == 0 {
			return nil, bootstrapRecoveryRefused("later approved retry is not original executor capture")
		}
		if chunkCode != code {
			return nil, bootstrapRecoveryRefused("wrong return identity")
		}
		retNorm = executorNormalizeOutput(retOut)
	}
	if err := executorRequireRefusalChannel("stdout", stdout, true); err != nil {
		return nil, err
	}
	if strings.TrimSpace(agg) != "" {
		if err := executorRequireRefusalChannel("aggregated", agg, true); err != nil {
			return nil, err
		}
		if agg != stdout {
			return nil, bootstrapRecoveryRefused("contradictory executor output")
		}
	}
	if strings.TrimSpace(formatted) != "" && formatted != stdout {
		return nil, bootstrapRecoveryRefused("contradictory executor output")
	}
	if stderr := executorNormalizeOutput(executorString(item["stderr"])); strings.TrimSpace(stderr) != "" && stderr != stdout {
		return nil, bootstrapRecoveryRefused("contradictory executor output")
	}
	if strings.TrimSpace(retNorm) == "" {
		return nil, bootstrapRecoveryRefused("wrong return identity")
	}
	if err := executorRequireRefusalChannel("return", retNorm, true); err != nil {
		return nil, err
	}
	if retNorm != stdout {
		return nil, bootstrapRecoveryRefused("contradictory executor output")
	}
	if err := validateRealBootstrapRefusalBinding(stdout, selected, writer); err != nil {
		return nil, err
	}

	if rec == nil {
		return nil, bootstrapRecoveryRefused("wrong attempt time")
	}
	if admitted.TaskID != "" && admitted.TaskID != strings.TrimSpace(rec.TaskID) {
		return nil, bootstrapRecoveryRefused("unchecked writer/attempt identity")
	}
	if admitted.AttemptID != "" && admitted.AttemptID != strings.TrimSpace(rec.AttemptID) {
		return nil, bootstrapRecoveryRefused("unchecked writer/attempt identity")
	}
	if writer != nil && strings.TrimSpace(writer.ID) != strings.TrimSpace(rec.TaskID) {
		return nil, bootstrapRecoveryRefused("unchecked writer/attempt identity")
	}
	jsonlWriter := extractExecutorWriterID(outPayload)
	if jsonlWriter != "" && writer != nil && jsonlWriter != strings.TrimSpace(writer.ID) {
		return nil, bootstrapRecoveryRefused("wrong writer identity")
	}
	if err := validateExecutorAttemptTimestamps(records, rec); err != nil {
		return nil, err
	}
	if err := validateExecutorCaptureProvenance(raw, admitted); err != nil {
		return nil, err
	}

	session := extractExecutorSessionID(stdout)
	if session == "" {
		return nil, bootstrapRecoveryRefused("missing session identity")
	}
	if session != admitted.SessionID {
		return nil, bootstrapRecoveryRefused("wrong session identity")
	}
	contract := extractExecutorBindingLine(stdout, "stage_contract_digest=")
	if contract == "" {
		return nil, bootstrapRecoveryRefused("missing contract digest")
	}
	if contract != admitted.ContractDigest {
		return nil, bootstrapRecoveryRefused("wrong contract digest")
	}
	profile := extractExecutorBindingLine(stdout, "sandbox_profile_digest:")
	if profile == "" {
		return nil, bootstrapRecoveryRefused("missing profile digest")
	}
	if profile != admitted.ProfileDigest {
		return nil, bootstrapRecoveryRefused("wrong profile digest")
	}

	return &bootstrapExecutorCaptureProof{
		CallID:         callID,
		CommandID:      itemID,
		ReturnID:       outID,
		Command:        command,
		ExitCode:       code,
		Output:         stdout,
		SessionID:      session,
		ContractDigest: contract,
		ProfileDigest:  profile,
	}, nil
}

func splitExecutorJSONL(raw []byte) ([]map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var recs []map[string]any
	for {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			if err == io.EOF {
				break
			}
			if len(recs) == 0 {
				return nil, bootstrapRecoveryRefused("unknown executor capture format")
			}
			return nil, bootstrapRecoveryRefused("truncated original executor capture")
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

func parseKnownExecutorTriple(records []map[string]any) (callPayload, item, outPayload map[string]any, err error) {
	if executorRecordType(records[0]) != "response_item" || executorPayloadType(records[0]) != "custom_tool_call" {
		return nil, nil, nil, bootstrapRecoveryRefused("unknown executor capture format")
	}
	if executorRecordType(records[1]) != "event_msg" || executorPayloadType(records[1]) != "item_completed" {
		return nil, nil, nil, bootstrapRecoveryRefused("unknown executor capture format")
	}
	item = executorCommandItem(records[1])
	if item == nil || executorString(item["type"]) != "CommandExecution" {
		return nil, nil, nil, bootstrapRecoveryRefused("unknown executor capture format")
	}
	if executorRecordType(records[2]) != "response_item" || executorPayloadType(records[2]) != "custom_tool_call_output" {
		return nil, nil, nil, bootstrapRecoveryRefused("unknown executor capture format")
	}
	callPayload = executorPayload(records[0])
	outPayload = executorPayload(records[2])
	if callPayload == nil || outPayload == nil {
		return nil, nil, nil, bootstrapRecoveryRefused("truncated original executor capture")
	}
	return callPayload, item, outPayload, nil
}

func executorLooksLikeKnownRecord(rec map[string]any) bool {
	switch executorRecordType(rec) {
	case "response_item", "event_msg":
		return true
	default:
		return false
	}
}

func executorLooksLikeLaterApproval(rec map[string]any) bool {
	item := executorCommandItem(rec)
	if item != nil {
		st := strings.ToLower(strings.TrimSpace(executorString(item["status"])))
		if st == "completed" || st == "approved" || st == "success" {
			return true
		}
		if code, ok := executorJSONInt(item["exit_code"]); ok && code == 0 {
			return true
		}
	}
	if executorPayloadType(rec) == "custom_tool_call_output" {
		if code, ok := executorLastChunkExit(executorPayload(rec)); ok && code == 0 {
			return true
		}
	}
	return false
}

func executorRecordType(rec map[string]any) string {
	if rec == nil {
		return ""
	}
	return strings.TrimSpace(executorString(rec["type"]))
}

func executorPayload(rec map[string]any) map[string]any {
	if rec == nil {
		return nil
	}
	p, _ := rec["payload"].(map[string]any)
	return p
}

func executorPayloadType(rec map[string]any) string {
	p := executorPayload(rec)
	if p == nil {
		return ""
	}
	return strings.TrimSpace(executorString(p["type"]))
}

func executorCommandItem(rec map[string]any) map[string]any {
	p := executorPayload(rec)
	if p == nil {
		return nil
	}
	item, _ := p["item"].(map[string]any)
	return item
}

func executorString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func executorJSONInt(v any) (int, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case float64:
		return int(x), true
	case float32:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	case json.Number:
		n, err := x.Int64()
		return int(n), err == nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	default:
		return 0, false
	}
}

func executorProcessID(item map[string]any) string {
	if item == nil {
		return ""
	}
	if s := strings.TrimSpace(executorString(item["process_id"])); s != "" && s != "<nil>" {
		return s
	}
	if n, ok := executorJSONInt(item["process_id"]); ok {
		return strconv.Itoa(n)
	}
	return ""
}

func executorCommandLine(item map[string]any) string {
	if item == nil {
		return ""
	}
	switch x := item["command"].(type) {
	case []any:
		parts := make([]string, 0, len(x))
		for _, el := range x {
			parts = append(parts, executorString(el))
		}
		return strings.Join(parts, " ")
	case []string:
		return strings.Join(x, " ")
	case string:
		return x
	default:
		return executorString(x)
	}
}

func executorOutputChunks(payload map[string]any) []map[string]any {
	if payload == nil {
		return nil
	}
	arr, ok := payload["output"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, el := range arr {
		if m, ok := el.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func executorLastChunkExit(payload map[string]any) (int, bool) {
	var last int
	var found bool
	for _, ch := range executorOutputChunks(payload) {
		text := strings.TrimSpace(executorString(ch["text"]))
		if !strings.HasPrefix(text, "{") {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(text), &obj) != nil {
			continue
		}
		if code, ok := executorJSONInt(obj["exit_code"]); ok {
			last = code
			found = true
		}
	}
	return last, found
}

func executorCollectOutput(item, outPayload map[string]any) string {
	var b strings.Builder
	write := func(s string) {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
		if strings.TrimSpace(s) == "" {
			return
		}
		b.WriteString(s)
		if !strings.HasSuffix(s, "\n") {
			b.WriteByte('\n')
		}
	}
	if item != nil {
		write(executorString(item["stdout"]))
		write(executorString(item["stderr"]))
		write(executorString(item["aggregated_output"]))
		write(executorString(item["formatted_output"]))
	}
	for _, ch := range executorOutputChunks(outPayload) {
		text := executorString(ch["text"])
		write(text)
		trim := strings.TrimSpace(text)
		if !strings.HasPrefix(trim, "{") {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(trim), &obj) != nil {
			continue
		}
		if s, ok := obj["output"].(string); ok {
			write(s)
		}
	}
	return b.String()
}

func extractExecutorSessionID(output string) string {
	if id := extractExecutorBindingLine(output, "session_id="); id != "" {
		if i := strings.IndexByte(id, ' '); i >= 0 {
			return strings.TrimSpace(id[:i])
		}
		return id
	}
	return extractExecutorBindingLine(output, "session_id:")
}

func extractExecutorBindingLine(output, key string) string {
	for _, line := range strings.Split(output, "\n") {
		got := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !strings.HasPrefix(got, key) {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(got, key))
	}
	return ""
}

func executorNormalizeOutput(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

func executorRequireRefusalChannel(name, body string, required bool) error {
	if !required {
		return nil
	}
	if !outputContainsAcceptedBootstrapBeforeProviderRefusal(body) {
		if strings.TrimSpace(body) == "" {
			return bootstrapRecoveryRefused("missing bootstrap-before-provider refusal")
		}
		return bootstrapRecoveryRefused("generic failure is not bootstrap-before-provider refusal")
	}
	return nil
}

func executorExtractGoalRunCmd(input string) string {
	var selected string
	search := input
	for {
		idx := strings.Index(search, "cmd:")
		if idx < 0 {
			break
		}
		search = search[idx+4:]
		search = strings.TrimLeft(search, " \t")
		if search == "" {
			break
		}
		q := search[0]
		if q != '"' && q != '\'' {
			continue
		}
		search = search[1:]
		end := strings.IndexByte(search, q)
		if end < 0 {
			break
		}
		cmd := search[:end]
		search = search[end+1:]
		if !strings.Contains(cmd, "workflow goal-run") {
			continue
		}
		if selected != "" && selected != cmd {
			return ""
		}
		selected = cmd
	}
	return selected
}

func executorSelectedCommand(item map[string]any) string {
	if item == nil {
		return ""
	}
	parsed := executorParsedGoalRunCmd(item)
	fromCommand := executorCommandGoalRunCmd(item)
	if parsed != "" && fromCommand != "" && parsed != fromCommand {
		return ""
	}
	if parsed != "" {
		return parsed
	}
	return fromCommand
}

func executorParsedGoalRunCmd(item map[string]any) string {
	if item == nil {
		return ""
	}
	parsed, ok := item["parsed_cmd"].([]any)
	if !ok {
		return ""
	}
	var selected string
	for _, el := range parsed {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		cmd := strings.TrimSpace(executorString(m["cmd"]))
		if !strings.Contains(cmd, "workflow goal-run") {
			continue
		}
		if selected != "" && selected != cmd {
			return ""
		}
		selected = cmd
	}
	return selected
}

func executorCommandGoalRunCmd(item map[string]any) string {
	if item == nil {
		return ""
	}
	switch x := item["command"].(type) {
	case []any:
		parts := make([]string, 0, len(x))
		for _, el := range x {
			parts = append(parts, executorString(el))
		}
		return executorShellOrLast(parts)
	case []string:
		return executorShellOrLast(x)
	case string:
		return x
	}
	return strings.TrimSpace(executorCommandLine(item))
}

func executorShellOrLast(argv []string) string {
	if len(argv) >= 3 {
		shell := filepath.Base(argv[0])
		flag := argv[1]
		if (shell == "zsh" || shell == "bash" || shell == "sh") && (flag == "-c" || flag == "-lc") {
			return argv[2]
		}
	}
	if len(argv) > 0 {
		return argv[len(argv)-1]
	}
	return ""
}

func executorCommandTargetsWorkflow(command, workflowID string) bool {
	id, ok := executorGoalRunPositionalID(command)
	return ok && id == strings.TrimSpace(workflowID)
}

func executorGoalRunPositionalID(command string) (string, bool) {
	command = strings.TrimSpace(command)
	workflowNeedle := "workflow goal-run"
	if command == "" || !strings.Contains(command, workflowNeedle) {
		return "", false
	}
	if strings.ContainsAny(command, ";|`$") || strings.Contains(command, "&&") ||
		strings.Contains(command, "||") || strings.Contains(command, "$(") ||
		strings.Contains(command, "\n") {
		return "", false
	}
	fields := strings.Fields(command)
	i := 0
	for i < len(fields) && executorEnvAssign(fields[i]) {
		i++
	}
	if i >= len(fields) {
		return "", false
	}
	if filepath.Base(fields[i]) != "cardex" {
		return "", false
	}
	i++
	if i+1 >= len(fields) || fields[i] != "workflow" || fields[i+1] != "goal-run" {
		return "", false
	}
	i += 2
	boolFlags := map[string]bool{
		"-hosted": true, "--hosted": true,
		"-manual": true, "--manual": true,
	}
	valueFlags := map[string]bool{
		"-root": true, "--root": true,
		"-budget": true, "--budget": true,
		"-sandbox": true, "--sandbox": true,
	}
	var positional []string
	for i < len(fields) {
		f := fields[i]
		if strings.HasPrefix(f, "-") && f != "-" {
			name := f
			val := ""
			hasEq := false
			if eq := strings.IndexByte(f, '='); eq >= 0 {
				name = f[:eq]
				val = f[eq+1:]
				hasEq = true
			}
			if boolFlags[name] {
				if hasEq {
					return "", false
				}
				i++
				continue
			}
			if valueFlags[name] {
				if hasEq {
					if strings.TrimSpace(val) == "" {
						return "", false
					}
					i++
					continue
				}
				if i+1 >= len(fields) {
					return "", false
				}
				i += 2
				continue
			}
			return "", false
		}
		if strings.ContainsAny(f, `"'`) {
			return "", false
		}
		positional = append(positional, f)
		i++
	}
	if len(positional) != 1 || strings.TrimSpace(positional[0]) == "" {
		return "", false
	}
	return positional[0], true
}

func executorEnvAssign(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	key := tok[:eq]
	if key[0] != '_' && (key[0] < 'A' || key[0] > 'Z') && (key[0] < 'a' || key[0] > 'z') {
		return false
	}
	for _, c := range key[1:] {
		if c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}

func executorLastFailedChunkOutput(payload map[string]any) (output string, code int, ok bool) {
	for _, ch := range executorOutputChunks(payload) {
		text := strings.TrimSpace(executorString(ch["text"]))
		if !strings.HasPrefix(text, "{") {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(text), &obj) != nil {
			continue
		}
		c, cok := executorJSONInt(obj["exit_code"])
		if !cok {
			continue
		}
		code = c
		ok = true
		if s, sok := obj["output"].(string); sok {
			output = s
		} else {
			output = ""
		}
	}
	return output, code, ok
}

func extractExecutorWriterID(outPayload map[string]any) string {
	scan := func(s string) string {
		s = executorNormalizeOutput(s)
		for _, line := range strings.Split(s, "\n") {
			got := strings.TrimSpace(line)
			const key = "writer="
			if i := strings.Index(got, key); i >= 0 {
				rest := got[i+len(key):]
				if j := strings.IndexAny(rest, " \t\r\n"); j >= 0 {
					rest = rest[:j]
				}
				return strings.TrimSpace(rest)
			}
		}
		return ""
	}
	for _, ch := range executorOutputChunks(outPayload) {
		text := executorString(ch["text"])
		if id := scan(text); id != "" {
			return id
		}
		trim := strings.TrimSpace(text)
		if !strings.HasPrefix(trim, "{") {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(trim), &obj) != nil {
			continue
		}
		if s, ok := obj["output"].(string); ok {
			if id := scan(s); id != "" {
				return id
			}
		}
	}
	return ""
}

func parseExecutorTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("missing")
	}
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts, nil
	}
	return time.Parse(time.RFC3339, s)
}

func executorRecordTimestamp(rec map[string]any) (time.Time, error) {
	if rec == nil {
		return time.Time{}, fmt.Errorf("missing")
	}
	return parseExecutorTime(executorString(rec["timestamp"]))
}

func validateExecutorAttemptTimestamps(records []map[string]any, rec *AttemptRecord) error {
	if rec == nil || len(records) < 3 {
		return bootstrapRecoveryRefused("wrong attempt time")
	}
	callTs, err := executorRecordTimestamp(records[0])
	if err != nil {
		return bootstrapRecoveryRefused("missing executor timestamp")
	}
	termTs, err := executorRecordTimestamp(records[1])
	if err != nil {
		return bootstrapRecoveryRefused("missing executor timestamp")
	}
	retTs, err := executorRecordTimestamp(records[2])
	if err != nil {
		return bootstrapRecoveryRefused("missing executor timestamp")
	}
	created, err := parseExecutorTime(rec.CreatedAt)
	if err != nil {
		return bootstrapRecoveryRefused("wrong attempt time")
	}
	updated, err := parseExecutorTime(rec.UpdatedAt)
	if err != nil {
		return bootstrapRecoveryRefused("wrong attempt time")
	}
	if retTs.Sub(updated) > 5*time.Second || callTs.Sub(created) > 5*time.Second {
		return bootstrapRecoveryRefused("later executor event is not original capture")
	}
	if !callTs.Before(created) || updated.Before(created) || !updated.Before(termTs) || !termTs.Before(retTs) {
		return bootstrapRecoveryRefused("wrong attempt time")
	}
	return nil
}

func executorSamePath(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	aa, ea := filepath.Abs(a)
	bb, eb := filepath.Abs(b)
	if ea == nil && eb == nil && aa == bb {
		return true
	}
	sa, e1 := filepath.EvalSymlinks(a)
	sb, e2 := filepath.EvalSymlinks(b)
	if e1 == nil && e2 == nil && sa == sb {
		return true
	}
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	if err1 == nil && err2 == nil && os.SameFile(ai, bi) {
		return true
	}
	return false
}

func splitJSONLRawRecords(raw []byte) [][]byte {
	if len(raw) == 0 {
		return nil
	}
	var recs [][]byte
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\n' {
			recs = append(recs, raw[start:i+1])
			start = i + 1
		}
	}
	if start < len(raw) {
		recs = append(recs, raw[start:])
	}
	return recs
}

func executorOriginalAsyncHandleOutput(outPayload map[string]any, admittedHandle, processID string) (string, error) {
	handle := strings.TrimSpace(admittedHandle)
	if handle == "" || handle != strings.TrimSpace(processID) {
		return "", bootstrapRecoveryRefused("wrong handle identity")
	}
	var found string
	n := 0
	for _, ch := range executorOutputChunks(outPayload) {
		text := strings.TrimSpace(executorString(ch["text"]))
		if !strings.HasPrefix(text, "{") {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(text), &obj) != nil {
			continue
		}
		session := strings.TrimSpace(executorString(obj["session_id"]))
		_, hasExit := executorJSONInt(obj["exit_code"])
		if session != handle {
			continue
		}
		if hasExit {
			return "", bootstrapRecoveryRefused("missing terminal nonzero exit")
		}
		n++
		if s, ok := obj["output"].(string); ok {
			found = s
		} else {
			found = ""
		}
	}
	if n != 1 {
		return "", bootstrapRecoveryRefused("wrong handle identity")
	}
	if strings.TrimSpace(found) == "" {
		return "", bootstrapRecoveryRefused("wrong return identity")
	}
	return found, nil
}

func validateExecutorWriteStdinTerminal(admitted bootstrapExecutorCaptureAdmitted, originalRecords []map[string]any, rec *AttemptRecord, processID string, originalCode int) (string, error) {
	if strings.TrimSpace(admitted.Handle) == "" || admitted.Handle != strings.TrimSpace(processID) {
		return "", bootstrapRecoveryRefused("wrong handle identity")
	}
	path := strings.TrimSpace(admitted.TerminalPath)
	if path == "" {
		return "", bootstrapRecoveryRefused("unbound original-async/terminal association")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", bootstrapRecoveryRefused("missing original executor capture")
		}
		return "", bootstrapRecoveryRefused("unreadable original executor capture")
	}
	if sha256Hex(string(raw)) != admitted.TerminalDigest {
		return "", bootstrapRecoveryRefused("executor capture digest mismatch")
	}
	records, err := splitExecutorJSONL(raw)
	if err != nil {
		return "", err
	}
	if len(records) != 2 {
		if len(records) > 2 {
			return "", bootstrapRecoveryRefused("modified original executor capture")
		}
		return "", bootstrapRecoveryRefused("truncated original executor capture")
	}
	if executorRecordType(records[0]) != "response_item" || executorPayloadType(records[0]) != "custom_tool_call" {
		return "", bootstrapRecoveryRefused("unknown executor capture format")
	}
	if executorRecordType(records[1]) != "response_item" || executorPayloadType(records[1]) != "custom_tool_call_output" {
		return "", bootstrapRecoveryRefused("unknown executor capture format")
	}
	callPayload := executorPayload(records[0])
	outPayload := executorPayload(records[1])
	if callPayload == nil || outPayload == nil {
		return "", bootstrapRecoveryRefused("truncated original executor capture")
	}
	callID := strings.TrimSpace(executorString(callPayload["call_id"]))
	if callID != admitted.TerminalCallID || callID == admitted.CallID {
		return "", bootstrapRecoveryRefused("wrong call identity")
	}
	if executorString(callPayload["name"]) != "exec" {
		return "", bootstrapRecoveryRefused("wrong command identity")
	}
	if err := executorRequireUniqueWriteStdin(executorString(callPayload["input"]), admitted.Handle); err != nil {
		return "", err
	}
	outCall := strings.TrimSpace(executorString(outPayload["call_id"]))
	outID := strings.TrimSpace(executorString(outPayload["id"]))
	if outCall != callID || outID != admitted.TerminalReturnID {
		return "", bootstrapRecoveryRefused("wrong return identity")
	}
	terminalOutput, err := executorRequireWriteStdinFirstTerminal(outPayload, originalCode)
	if err != nil {
		return "", err
	}
	if err := validateExecutorWriteStdinTimestamps(originalRecords, records, rec); err != nil {
		return "", err
	}
	termAdmitted := bootstrapExecutorCaptureAdmitted{
		Path:       admitted.TerminalPath,
		CallID:     admitted.TerminalCallID,
		ReturnID:   admitted.TerminalReturnID,
		Provenance: admitted.TerminalProvenance,
	}
	if err := validateExecutorCallReturnProvenance(raw, termAdmitted); err != nil {
		return "", err
	}
	return terminalOutput, nil
}

func executorRequireWriteStdinFirstTerminal(outPayload map[string]any, originalCode int) (string, error) {
	var jsonChunks []map[string]any
	for _, ch := range executorOutputChunks(outPayload) {
		text := strings.TrimSpace(executorString(ch["text"]))
		if !strings.HasPrefix(text, "{") {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(text), &obj) != nil {
			continue
		}
		jsonChunks = append(jsonChunks, obj)
	}
	if len(jsonChunks) == 0 {
		return "", bootstrapRecoveryRefused("missing terminal nonzero exit")
	}
	first := jsonChunks[0]
	code, ok := executorJSONInt(first["exit_code"])
	if !ok {
		return "", bootstrapRecoveryRefused("missing terminal nonzero exit")
	}
	if sid := strings.TrimSpace(executorString(first["session_id"])); sid != "" {
		return "", bootstrapRecoveryRefused("wrong handle identity")
	}
	if code == 0 {
		if len(jsonChunks) > 1 {
			if later, lok := executorJSONInt(jsonChunks[len(jsonChunks)-1]["exit_code"]); lok && later != 0 {
				return "", bootstrapRecoveryRefused("unrelated second-command failure")
			}
		}
		return "", bootstrapRecoveryRefused("missing terminal nonzero exit")
	}
	if code < 0 {
		return "", bootstrapRecoveryRefused("generic failure is not bootstrap-before-provider refusal")
	}
	if code != originalCode {
		return "", bootstrapRecoveryRefused("wrong return identity")
	}
	output, ok := first["output"].(string)
	if !ok {
		return "", bootstrapRecoveryRefused("wrong return identity")
	}
	return output, nil
}

func executorRequireUniqueWriteStdin(input, handle string) error {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return bootstrapRecoveryRefused("wrong handle identity")
	}
	if n := strings.Count(input, "write_stdin("); n != 1 {
		if n > 1 {
			return bootstrapRecoveryRefused("wrong handle identity")
		}
		return bootstrapRecoveryRefused("wrong handle identity")
	}
	trimmed := strings.TrimSpace(input)
	const lead = "text(await tools.write_stdin("
	if !strings.HasPrefix(trimmed, lead) {
		return bootstrapRecoveryRefused("wrong handle identity")
	}
	rest := strings.TrimSpace(trimmed[len(lead):])
	obj, after, ok := parseExecutorBareObject(rest)
	if !ok {
		return bootstrapRecoveryRefused("wrong handle identity")
	}
	after = strings.TrimSpace(after)
	if !strings.HasPrefix(after, "))") {
		return bootstrapRecoveryRefused("wrong handle identity")
	}
	if strings.Contains(after[2:], "write_stdin(") {
		return bootstrapRecoveryRefused("wrong handle identity")
	}
	sid, hasSID := obj["session_id"]
	chars, hasChars := obj["chars"]
	if !hasSID || sid != handle || !hasChars || chars != "" {
		return bootstrapRecoveryRefused("wrong handle identity")
	}
	for k, v := range obj {
		switch k {
		case "session_id", "chars":
		case "yield_time_ms", "max_output_tokens":
			if v == "" || !executorAllDigits(v) {
				return bootstrapRecoveryRefused("wrong handle identity")
			}
		default:
			return bootstrapRecoveryRefused("wrong handle identity")
		}
	}
	return nil
}

func parseExecutorBareObject(s string) (map[string]string, string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return nil, s, false
	}
	s = s[1:]
	out := map[string]string{}
	for {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "}") {
			return out, s[1:], true
		}
		if len(out) > 0 {
			if !strings.HasPrefix(s, ",") {
				return nil, s, false
			}
			s = strings.TrimSpace(s[1:])
		}
		key, next, ok := parseExecutorBareIdent(s)
		if !ok {
			return nil, s, false
		}
		s = strings.TrimSpace(next)
		if !strings.HasPrefix(s, ":") {
			return nil, s, false
		}
		s = strings.TrimSpace(s[1:])
		val, next, ok := parseExecutorBareValue(s)
		if !ok {
			return nil, s, false
		}
		out[key] = val
		s = next
	}
}

func parseExecutorBareIdent(s string) (string, string, bool) {
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return "", s, false
	}
	return s[:i], s[i:], true
}

func parseExecutorBareValue(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", s, false
	}
	if s[0] == '"' || s[0] == '\'' {
		q := s[0]
		i := 1
		for i < len(s) {
			if s[i] == '\\' && i+1 < len(s) {
				i += 2
				continue
			}
			if s[i] == q {
				return s[1:i], s[i+1:], true
			}
			i++
		}
		return "", s, false
	}
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '_' || c == '-' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return "", s, false
	}
	return s[:i], s[i:], true
}

func executorAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func validateExecutorWriteStdinTimestamps(original, later []map[string]any, rec *AttemptRecord) error {
	if rec == nil || len(original) < 3 || len(later) < 2 {
		return bootstrapRecoveryRefused("wrong attempt time")
	}
	origRet, err := executorRecordTimestamp(original[2])
	if err != nil {
		return bootstrapRecoveryRefused("missing executor timestamp")
	}
	callTs, err := executorRecordTimestamp(later[0])
	if err != nil {
		return bootstrapRecoveryRefused("missing executor timestamp")
	}
	retTs, err := executorRecordTimestamp(later[1])
	if err != nil {
		return bootstrapRecoveryRefused("missing executor timestamp")
	}
	// Stdout collection follows original event order: original return,
	// then write_stdin call, then write_stdin return.
	if !origRet.Before(callTs) || !callTs.Before(retTs) {
		return bootstrapRecoveryRefused("wrong attempt time")
	}
	return nil
}

func validateExecutorCallReturnProvenance(raw []byte, admitted bootstrapExecutorCaptureAdmitted) error {
	if strings.TrimSpace(admitted.Provenance) == "" {
		return bootstrapRecoveryRefused("unbound executor digest/event in authorization")
	}
	if admitted.Path != "" && executorSamePath(admitted.Path, admitted.Provenance) {
		return bootstrapRecoveryRefused("self-hashed capture is not original executor provenance")
	}
	provRaw, err := os.ReadFile(admitted.Provenance)
	if err != nil {
		if os.IsNotExist(err) {
			return bootstrapRecoveryRefused("missing original executor provenance")
		}
		return bootstrapRecoveryRefused("unreadable original executor provenance")
	}
	var callLine, retLine []byte
	extra := 0
	for _, line := range splitJSONLRawRecords(provRaw) {
		trim := bytes.TrimSpace(line)
		if len(trim) == 0 {
			continue
		}
		var rec map[string]any
		if json.Unmarshal(trim, &rec) != nil {
			extra++
			continue
		}
		p := executorPayload(rec)
		switch {
		case executorPayloadType(rec) == "custom_tool_call" && p != nil && strings.TrimSpace(executorString(p["call_id"])) == admitted.CallID:
			callLine = line
		case executorPayloadType(rec) == "custom_tool_call_output" && p != nil && strings.TrimSpace(executorString(p["id"])) == admitted.ReturnID:
			retLine = line
		default:
			extra++
		}
	}
	if len(callLine) == 0 || len(retLine) == 0 {
		return bootstrapRecoveryRefused("missing original executor provenance")
	}
	if extra == 0 {
		return bootstrapRecoveryRefused("self-hashed capture is not original executor provenance")
	}
	selected := append([]byte{}, callLine...)
	selected = append(selected, retLine...)
	if !bytes.Equal(selected, raw) {
		return bootstrapRecoveryRefused("rewritten executor snapshot is not original capture")
	}
	return nil
}

func validateExecutorCaptureProvenance(raw []byte, admitted bootstrapExecutorCaptureAdmitted) error {
	if strings.TrimSpace(admitted.Provenance) == "" {
		return bootstrapRecoveryRefused("unbound executor digest/event in authorization")
	}
	if admitted.Path != "" && executorSamePath(admitted.Path, admitted.Provenance) {
		return bootstrapRecoveryRefused("self-hashed capture is not original executor provenance")
	}
	provRaw, err := os.ReadFile(admitted.Provenance)
	if err != nil {
		if os.IsNotExist(err) {
			return bootstrapRecoveryRefused("missing original executor provenance")
		}
		return bootstrapRecoveryRefused("unreadable original executor provenance")
	}
	var callLine, itemLine, retLine []byte
	extra := 0
	for _, line := range splitJSONLRawRecords(provRaw) {
		trim := bytes.TrimSpace(line)
		if len(trim) == 0 {
			continue
		}
		var rec map[string]any
		if json.Unmarshal(trim, &rec) != nil {
			extra++
			continue
		}
		p := executorPayload(rec)
		item := executorCommandItem(rec)
		switch {
		case executorPayloadType(rec) == "custom_tool_call" && p != nil && strings.TrimSpace(executorString(p["call_id"])) == admitted.CallID:
			callLine = line
		case item != nil && executorString(item["type"]) == "CommandExecution" && strings.TrimSpace(executorString(item["id"])) == admitted.ItemID:
			itemLine = line
		case executorPayloadType(rec) == "custom_tool_call_output" && p != nil && strings.TrimSpace(executorString(p["id"])) == admitted.ReturnID:
			retLine = line
		default:
			extra++
		}
	}
	if len(callLine) == 0 || len(itemLine) == 0 || len(retLine) == 0 {
		return bootstrapRecoveryRefused("missing original executor provenance")
	}
	if extra == 0 {
		return bootstrapRecoveryRefused("self-hashed capture is not original executor provenance")
	}
	selected := append([]byte{}, callLine...)
	selected = append(selected, itemLine...)
	selected = append(selected, retLine...)
	if !bytes.Equal(selected, raw) {
		return bootstrapRecoveryRefused("rewritten executor snapshot is not original capture")
	}
	return nil
}
