package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Goal workflow reuses WorkflowRecord (owner goal/criteria/rounds + optional
// design lineage) and Task (stage execution / native-goal fact). SessionID,
// Revision, ControlEpoch, ActiveAttemptID and the existing admission/lease/
// terminal machinery stay the identity and custody plane. There is no second
// queue and no universal state machine.

const (
	goalWriterManual = "manual"
	goalWriterNative = "native"

	goalObsUnstarted = "unstarted"
	goalObsRunning   = "running"
	goalObsHeld      = "held"
	goalObsUnknown   = "unknown"
	goalObsDone      = "done"
	goalObsFailed    = "failed"
	goalObsCanceled  = "canceled"

	goalCapSupported   = "supported"
	goalCapManualOnly  = "manual-only"
	goalCapUnsupported = "unsupported"

	goalDesignRole             = "design"
	goalDesignProvenanceTask   = "completed-task"
	goalDesignProvenanceExt    = "external-receipt"
	goalDesignProvenanceRepair = "design-repair"

	goalDecisionStop      = "stop"
	goalDecisionInput     = "input"
	goalDecisionSuccessor = "successor"
	goalDecisionAccept    = "accept"
	goalDecisionRevise    = "revise"

	workflowDesignRepairLimit = 1

	goalStopSoftBudget = "provider budget is soft; existing step timeout is the hard deadline"
)

var (
	errGoalSyncRejected              = errors.New("workflow goal-sync rejected")
	errGoalRedispatchBlocked         = errors.New("workflow goal redispatch blocked")
	errGoalManualRequired            = errors.New("workflow goal-run requires -manual")
	errGoalCapability                = errors.New("workflow native-goal capability")
	errWorkflowDesignRepairExhausted = errors.New("workflow design repair exhausted")
	errGoalLaunchUnstarted           = errors.New("workflow goal launch did not start")
	errGoalDesignProof               = errors.New("workflow goal design proof")
	errGoalDesignResult              = errors.New("workflow goal design-result")
	errGoalAttemptRequired           = errors.New("workflow goal requires the exact existing attempt")
	errGoalUnsupportedTuple          = errors.New("workflow goal unsupported execution tuple")
)

// Test-only seam: production leaves this nil. A non-nil error is a crash
// before cmd.Start, after session bind and attempt reserve.
var goalLaunchBeforeStartHook func() error

// Test-only stdin override so fake-executable launches do not take the
// process controlling TTY (Foreground+Ctty is the interactive path).
var goalLaunchStdin *os.File

// Test-only: called with the complete Goal-bound task immediately before the
// first save, so concurrent consumers can observe the publish seam.
var goalWriterPublishHook func(*Task)

// WorkflowDesignNode is one independent read-only design role instance.
type WorkflowDesignNode struct {
	Role                string `json:"role"`
	Model               string `json:"model,omitempty"`
	Runner              string `json:"runner,omitempty"`
	ActualModel         string `json:"actual_model,omitempty"`
	ActualRunner        string `json:"actual_runner,omitempty"`
	Identity            string `json:"identity,omitempty"`
	ReadOnly            bool   `json:"read_only"`
	At                  string `json:"at,omitempty"`
	ConsumedCommit      string `json:"consumed_commit,omitempty"`
	ConsumedTree        string `json:"consumed_tree,omitempty"`
	ConsumedReviewTask  string `json:"consumed_review_task,omitempty"`
	ConsumedTaskID      string `json:"consumed_task_id,omitempty"`
	ConsumedSessionID   string `json:"consumed_session_id,omitempty"`
	ConsumedAttemptID   string `json:"consumed_attempt_id,omitempty"`
	ConsumedObservation string `json:"consumed_observation,omitempty"`
	ConsumedRevision    int64  `json:"consumed_revision,omitempty"`
	DesignTaskID        string `json:"design_task_id,omitempty"`
	DesignSessionID     string `json:"design_session_id,omitempty"`
	ReceiptPath         string `json:"receipt_path,omitempty"`
	ResultPath          string `json:"result_path,omitempty"`
	Digest              string `json:"digest,omitempty"`
	InputIdentity       string `json:"input_identity,omitempty"`
	Provenance          string `json:"provenance,omitempty"`
}

// WorkflowDesignLineage is optional and omitempty on ordinary records.
type WorkflowDesignLineage struct {
	Initial                  *WorkflowDesignNode `json:"initial,omitempty"`
	LatestValid              *WorkflowDesignNode `json:"latest_valid,omitempty"`
	RepairCount              int                 `json:"repair_count,omitempty"`
	LastResultDecision       string              `json:"last_result_decision,omitempty"`
	LastConsumedTaskID       string              `json:"last_consumed_task_id,omitempty"`
	LastConsumedObservation  string              `json:"last_consumed_observation,omitempty"`
	LastConsumedNativeStatus string              `json:"last_consumed_native_status,omitempty"`
	LastConsumedDigest       string              `json:"last_consumed_digest,omitempty"`
	LastConsumedRevision     int64               `json:"last_consumed_revision,omitempty"`
	LastConsumedAt           string              `json:"last_consumed_at,omitempty"`
}

// TaskGoalBinding is the stage execution / native-goal fact on Task.
// Ordinary cards omit this field and keep current eligible()/tick semantics.
type TaskGoalBinding struct {
	Outcome           string `json:"outcome,omitempty"`
	Scope             string `json:"scope,omitempty"`
	Acceptance        string `json:"acceptance,omitempty"`
	Provider          string `json:"provider,omitempty"`
	NativeGoalID      string `json:"native_goal_id,omitempty"`
	BudgetTokens      int64  `json:"budget_tokens,omitempty"`
	HardTimeoutSec    int64  `json:"hard_timeout_seconds,omitempty"`
	Stop              string `json:"stop,omitempty"`
	ReturnToDesign    bool   `json:"return_to_design,omitempty"`
	WriterMode        string `json:"writer_mode,omitempty"`
	Observation       string `json:"observation,omitempty"`
	ObservationNote   string `json:"observation_note,omitempty"`
	BoundAttemptID    string `json:"bound_attempt_id,omitempty"`
	SyncedRevision    int64  `json:"synced_revision,omitempty"`
	LastNativeStatus  string `json:"last_native_status,omitempty"`
	ClassifierVerdict string `json:"classifier_verdict,omitempty"`
	EvidenceComplete  bool   `json:"evidence_complete,omitempty"`
	CustodyReleased   bool   `json:"custody_released,omitempty"`
	CancelRequested   bool   `json:"cancel_requested,omitempty"`
	StopEvidence      bool   `json:"stop_evidence,omitempty"`
	Continuation      string `json:"continuation,omitempty"`
	Started           bool   `json:"started,omitempty"`
	InputDigest       string `json:"input_digest,omitempty"`
}

// GoalCapability is owned by this adapter file. supported requires actual
// native launch/identity/lifecycle/stop/recovery/terminal proof. CLI help
// absence is not enough to mark unsupported. Generic prompt loops are not a
// substitute for a native ongoing-goal protocol. -p is a single turn.
type GoalCapability struct {
	Runner       string `json:"runner"`
	Level        string `json:"level"`
	Verified     bool   `json:"verified"`
	Reason       string `json:"reason"`
	CandidateAPI string `json:"candidate_api,omitempty"`
	Rejected     bool   `json:"rejected,omitempty"`
}

// GoalSyncRequest is the identity a sync call must match. Empty expected
// fields mean "use the live task". Tests inject stale/other identities.
type GoalSyncRequest struct {
	ExpectedRevision int64
	ExpectedSession  string
	ExpectedGoalID   string
	ExpectedAttempt  string
	GrokHome         string
	GrokCWD          string
}

type workflowGoalView struct {
	TaskID      string `json:"task_id"`
	Status      string `json:"status"`
	SessionID   string `json:"session_id"`
	GoalID      string `json:"goal_id"`
	AttemptID   string `json:"attempt_id"`
	Observation string `json:"observation"`
	WriterMode  string `json:"writer_mode"`
	Revision    int64  `json:"revision"`
}

type grokNativeGoalStateFile struct {
	GoalID                string          `json:"goal_id"`
	Status                string          `json:"status"`
	Phase                 string          `json:"phase"`
	TokenBudget           int64           `json:"token_budget"`
	LastClassifierVerdict string          `json:"last_classifier_verdict"`
	TotalVerifyRounds     int             `json:"total_verify_rounds"`
	TotalWorkerRounds     int             `json:"total_worker_rounds"`
	History               []grokGoalEvent `json:"history"`
}

type grokGoalEvent struct {
	Event string `json:"event"`
}

type grokGoalUpdateLine struct {
	Params grokGoalUpdateParams `json:"params"`
}

type grokGoalUpdateParams struct {
	SessionID string                `json:"sessionId"`
	Update    grokGoalSessionUpdate `json:"update"`
}

type grokGoalSessionUpdate struct {
	SessionUpdate         string `json:"sessionUpdate"`
	GoalID                string `json:"goal_id"`
	Status                string `json:"status"`
	Phase                 string `json:"phase"`
	LastClassifierVerdict string `json:"last_classifier_verdict"`
	LastEvent             string `json:"last_event"`
}

type grokSessionSummaryFile struct {
	Info grokSessionSummaryInfo `json:"info"`
}

type grokSessionSummaryInfo struct {
	ID  string `json:"id"`
	CWD string `json:"cwd"`
}

type designBindRequest struct {
	Model        string
	Runner       string
	ActualModel  string
	ActualRunner string
	Identity     string
	ReceiptPath  string
	DesignTaskID string
	Root         string
}

type designReceiptInput struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type designReceiptFile struct {
	Role                string               `json:"role"`
	Model               string               `json:"model,omitempty"`
	Runner              string               `json:"runner,omitempty"`
	ActualModel         string               `json:"actual_model,omitempty"`
	ActualRunner        string               `json:"actual_runner,omitempty"`
	Identity            string               `json:"identity,omitempty"`
	SessionID           string               `json:"session_id,omitempty"`
	Digest              string               `json:"digest,omitempty"`
	InputIdentity       string               `json:"input_identity,omitempty"`
	ReadOnly            *bool                `json:"read_only"`
	Status              string               `json:"status,omitempty"`
	ResultPath          string               `json:"result_path,omitempty"`
	ConsumedTaskID      string               `json:"consumed_task_id,omitempty"`
	ConsumedSessionID   string               `json:"consumed_session_id,omitempty"`
	ConsumedAttemptID   string               `json:"consumed_attempt_id,omitempty"`
	ConsumedObservation string               `json:"consumed_observation,omitempty"`
	ConsumedRevision    int64                `json:"consumed_revision,omitempty"`
	ConsumedCommit      string               `json:"consumed_commit,omitempty"`
	ConsumedTree        string               `json:"consumed_tree,omitempty"`
	Inputs              []designReceiptInput `json:"inputs,omitempty"`
	At                  string               `json:"at,omitempty"`
}

func newGoalSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func defaultGrokHome() string {
	if v := strings.TrimSpace(os.Getenv("GROK_HOME")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(filepath.Clean(home), ".grok")
}

func grokEncodeCwd(cwd string) string {
	enc := url.PathEscape(cwd)
	if len(enc) <= 255 {
		return enc
	}
	sum := sha256ShortHex(cwd)
	base := filepath.Base(strings.ReplaceAll(cwd, string(filepath.Separator), "-"))
	if base == "" || base == "." || base == "/" {
		base = "cwd"
	}
	if len(base) > 40 {
		base = base[:40]
	}
	return base + "-" + sum
}

func sha256ShortHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:8])
}

func grokGoalSessionDir(grokHome, cwd, sessionID string) string {
	return filepath.Join(grokHome, "sessions", grokEncodeCwd(cwd), sessionID)
}

func nativeGoalCapabilities() []GoalCapability {
	return []GoalCapability{
		{
			Runner:       grokBuildRunnerName,
			Level:        goalCapManualOnly,
			Verified:     true,
			Reason:       "Grok TUI native /goal is manually proven for complete and same-session restart. Automatic protocol and active pause/resume remain unverified. grok -p/--single is a single turn and is not goal proof.",
			CandidateAPI: "/goal <objective> [--budget <tokens>]; /goal status|pause|resume|clear",
		},
		{
			Runner:       kimiCLIRunnerName,
			Level:        goalCapManualOnly,
			Verified:     false,
			Reason:       "Kimi source supports native headless create and goal.summary; the configured-engine isolated probe is unverified (blocked on automatic approval). Manual-only until adapter proof exists. Not emulated by prompt loops.",
			CandidateAPI: "print / goal.summary (source-level native candidate; adapter unverified)",
		},
		{
			Runner:       "codex",
			Level:        goalCapManualOnly,
			Verified:     false,
			Reason:       "Named native candidate APIs exist; headless ongoing-goal protocol is unverified. Manual-only, not blanket unsupported. Not emulated by prompt loops.",
			CandidateAPI: "codex exec (ordinary single-run; ongoing-goal adapter unverified)",
		},
		{
			Runner:       "claude",
			Level:        goalCapManualOnly,
			Verified:     false,
			Reason:       "Named native candidate APIs exist; headless ongoing-goal protocol is unverified. CLI help absence is not the reason. Not emulated by prompt loops.",
			CandidateAPI: "claude CLI session (ordinary; ongoing-goal adapter unverified)",
		},
		{
			Runner:       cursorRunnerName,
			Level:        goalCapManualOnly,
			Verified:     false,
			Reason:       "Named native candidate APIs exist; headless ongoing-goal protocol is unverified. Existing model=fable Cursor routing is unchanged. Not emulated by prompt loops.",
			CandidateAPI: "cursor agent (ordinary; ongoing-goal adapter unverified)",
		},
		{
			Runner:       antigravityRunnerName,
			Level:        goalCapManualOnly,
			Verified:     false,
			Reason:       "Named native candidate APIs exist; headless ongoing-goal protocol is unverified. Manual-only, not blanket unsupported. Not emulated by prompt loops.",
			CandidateAPI: "agy (ordinary; ongoing-goal adapter unverified)",
		},
		{
			Runner:       "opencode",
			Level:        goalCapManualOnly,
			Verified:     false,
			Reason:       "OpenCode is a current ordinary runner. Native ongoing-goal protocol is unverified. Manual-only, not absent. Not emulated by prompt loops.",
			CandidateAPI: "opencode CLI (ordinary; ongoing-goal adapter unverified)",
		},
		{
			Runner:   "gemini",
			Level:    goalCapUnsupported,
			Verified: false,
			Rejected: true,
			Reason:   "Retired Gemini executor remains rejected for new workflow/goal work.",
		},
	}
}

func nativeGoalCapability(runner string) GoalCapability {
	name := strings.TrimSpace(runner)
	for _, cap := range nativeGoalCapabilities() {
		if cap.Runner == name {
			return cap
		}
	}
	if name == "" {
		return GoalCapability{Runner: "", Level: goalCapUnsupported, Reason: "empty runner"}
	}
	return GoalCapability{
		Runner:   name,
		Level:    goalCapManualOnly,
		Verified: false,
		Reason:   "engine profiles inherit the underlying executable limits; headless ongoing-goal protocol is unverified for this adapter/version",
	}
}

func goalBlocksOrdinaryDispatch(t *Task) bool {
	return t != nil && t.Goal != nil
}

func newTaskGoalBinding(wf *WorkflowRecord, mode string, budget int64, hardTimeoutSec int64) *TaskGoalBinding {
	return &TaskGoalBinding{
		Outcome:        wf.Goal,
		Scope:          wf.ModuleID,
		Acceptance:     wf.TerminalCriteria,
		Provider:       wf.WriterEngine,
		BudgetTokens:   budget,
		HardTimeoutSec: hardTimeoutSec,
		Stop:           goalStopSoftBudget,
		ReturnToDesign: true,
		WriterMode:     mode,
		Observation:    goalObsUnstarted,
	}
}

func copyGoalContract(src *TaskGoalBinding) *TaskGoalBinding {
	if src == nil {
		return nil
	}
	return &TaskGoalBinding{
		Outcome:        src.Outcome,
		Scope:          src.Scope,
		Acceptance:     src.Acceptance,
		Provider:       src.Provider,
		BudgetTokens:   src.BudgetTokens,
		HardTimeoutSec: src.HardTimeoutSec,
		Stop:           src.Stop,
		ReturnToDesign: src.ReturnToDesign,
		WriterMode:     src.WriterMode,
		Observation:    goalObsUnstarted,
	}
}

func normalizeDesignNode(node *WorkflowDesignNode) error {
	if node == nil {
		return nil
	}
	if strings.TrimSpace(node.Role) == "" {
		node.Role = goalDesignRole
	}
	if node.Role != goalDesignRole {
		return fmt.Errorf("%w: design node role must be %q", errWorkflowMalformed, goalDesignRole)
	}
	if !node.ReadOnly {
		return fmt.Errorf("%w: design node must already be read-only; a false claim is not rewritten into acceptance", errGoalDesignProof)
	}
	if strings.TrimSpace(node.Model) == "" && strings.TrimSpace(node.ActualModel) == "" {
		return fmt.Errorf("%w: design node needs model", errWorkflowMalformed)
	}
	if strings.TrimSpace(node.Runner) == "" && strings.TrimSpace(node.ActualRunner) == "" {
		return fmt.Errorf("%w: design node needs runner", errWorkflowMalformed)
	}
	return nil
}

func fictionalAstraGrokBuild(model, runner string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	r := strings.ToLower(strings.TrimSpace(runner))
	return strings.Contains(m, "astra") && r == grokBuildRunnerName
}

func independentDesignModelOK(actualModel string) bool {
	m := strings.ToLower(strings.TrimSpace(actualModel))
	if m == "" {
		return false
	}
	if m == "gpt-6-astra" || strings.HasPrefix(m, "gpt-6-astra") {
		return true
	}
	if m == "claude-fable-5" || strings.HasPrefix(m, "claude-fable-5") || m == "claude-fable-5-thinking-max" {
		return true
	}
	return false
}

func independentDesignQualified(node *WorkflowDesignNode) bool {
	if node == nil {
		return false
	}
	// Qualify on the proven actual model only. Identity/runner display text
	// containing "astra" is not independent-design proof.
	return independentDesignModelOK(node.ActualModel)
}

func designProofComplete(node *WorkflowDesignNode) error {
	if err := normalizeDesignNode(node); err != nil {
		return err
	}
	if node == nil {
		return fmt.Errorf("%w: missing design node", errGoalDesignProof)
	}
	if !node.ReadOnly {
		return fmt.Errorf("%w: design node must be read-only", errGoalDesignProof)
	}
	if strings.TrimSpace(node.Digest) == "" || strings.TrimSpace(node.InputIdentity) == "" {
		return fmt.Errorf("%w: design node needs digest and current input identity", errGoalDesignProof)
	}
	if strings.TrimSpace(node.ActualModel) == "" && strings.TrimSpace(node.Model) == "" {
		return fmt.Errorf("%w: design node needs actual model", errGoalDesignProof)
	}
	if fictionalAstraGrokBuild(firstNonBlank(node.ActualModel, node.Model), firstNonBlank(node.ActualRunner, node.Runner)) {
		return fmt.Errorf("%w: independent Astra design is not a grok-build tuple", errGoalDesignProof)
	}
	if !independentDesignQualified(node) {
		return fmt.Errorf("%w: independent design requires actual Fable/Astra identity", errGoalDesignProof)
	}
	switch node.Provenance {
	case goalDesignProvenanceTask, goalDesignProvenanceExt, goalDesignProvenanceRepair:
	default:
		return fmt.Errorf("%w: design provenance must be a completed task, external receipt, or governed repair", errGoalDesignProof)
	}
	if node.Provenance == goalDesignProvenanceTask && strings.TrimSpace(node.DesignTaskID) == "" {
		return fmt.Errorf("%w: completed design task id required", errGoalDesignProof)
	}
	if node.Provenance == goalDesignProvenanceExt && strings.TrimSpace(node.ReceiptPath) == "" && strings.TrimSpace(node.DesignSessionID) == "" {
		return fmt.Errorf("%w: external design receipt or session required", errGoalDesignProof)
	}
	return nil
}

func requireGoalDesignProof(wf *WorkflowRecord) error {
	if wf == nil || wf.DesignLineage == nil || wf.DesignLineage.LatestValid == nil {
		return fmt.Errorf("%w: Goal writer requires a completed independent design artifact or governed external receipt", errGoalDesignProof)
	}
	return designProofComplete(wf.DesignLineage.LatestValid)
}

func reverifyGoalDesignProof(root string, wf *WorkflowRecord) error {
	if err := requireGoalDesignProof(wf); err != nil {
		return err
	}
	node := wf.DesignLineage.LatestValid
	if strings.TrimSpace(node.ReceiptPath) != "" {
		fresh, err := loadExternalDesignReceipt(node.ReceiptPath)
		if err != nil {
			return err
		}
		if !strings.EqualFold(fresh.Digest, node.Digest) {
			return fmt.Errorf("%w: design artifact digest drifted since bind", errGoalDesignProof)
		}
		if strings.TrimSpace(fresh.InputIdentity) == "" || fresh.InputIdentity != node.InputIdentity {
			return fmt.Errorf("%w: design input identity drifted since bind", errGoalDesignProof)
		}
		return designProofComplete(fresh)
	}
	if strings.TrimSpace(node.ResultPath) != "" {
		raw, err := os.ReadFile(node.ResultPath)
		if err != nil {
			return fmt.Errorf("%w: design result artifact: %v", errGoalDesignProof, err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != strings.ToLower(strings.TrimSpace(node.Digest)) {
			return fmt.Errorf("%w: design artifact digest drifted since bind", errGoalDesignProof)
		}
	}
	if node.Provenance == goalDesignProvenanceTask && root != "" && strings.TrimSpace(node.DesignTaskID) != "" {
		again, err := loadCompletedDesignTask(root, node.DesignTaskID)
		if err != nil {
			return err
		}
		if !strings.EqualFold(again.Digest, node.Digest) {
			return fmt.Errorf("%w: design task result digest drifted since bind", errGoalDesignProof)
		}
		if again.InputIdentity != node.InputIdentity {
			return fmt.Errorf("%w: design task input identity drifted since bind", errGoalDesignProof)
		}
	}
	if strings.TrimSpace(node.Digest) == "" || strings.TrimSpace(node.InputIdentity) == "" {
		return fmt.Errorf("%w: design node needs digest and current input identity", errGoalDesignProof)
	}
	return nil
}

func bindInitialDesignNode(wf *WorkflowRecord, model, runner, actualModel, actualRunner string) error {
	return bindInitialDesignProof(wf, designBindRequest{
		Model: model, Runner: runner, ActualModel: actualModel, ActualRunner: actualRunner,
	})
}

func bindInitialDesignProof(wf *WorkflowRecord, req designBindRequest) error {
	if wf == nil {
		return errWorkflowMalformed
	}
	empty := strings.TrimSpace(req.ReceiptPath) == "" && strings.TrimSpace(req.DesignTaskID) == ""
	if empty {
		if strings.TrimSpace(req.Model) != "" || strings.TrimSpace(req.Runner) != "" ||
			strings.TrimSpace(req.ActualModel) != "" || strings.TrimSpace(req.ActualRunner) != "" ||
			strings.TrimSpace(req.Identity) != "" {
			return fmt.Errorf("%w: caller model/runner strings are not design proof; bind -design-receipt or -design-task", errGoalDesignProof)
		}
		return nil
	}
	var node *WorkflowDesignNode
	var err error
	switch {
	case strings.TrimSpace(req.ReceiptPath) != "":
		node, err = loadExternalDesignReceipt(req.ReceiptPath)
	default:
		if strings.TrimSpace(req.Root) == "" {
			return fmt.Errorf("%w: design task bind needs root", errGoalDesignProof)
		}
		node, err = loadCompletedDesignTask(req.Root, req.DesignTaskID)
	}
	if err != nil {
		return err
	}
	if err := designProofComplete(node); err != nil {
		return err
	}
	wf.DesignLineage = &WorkflowDesignLineage{Initial: node, LatestValid: node, RepairCount: 0}
	return nil
}

func loadExternalDesignReceipt(path string) (*WorkflowDesignNode, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read receipt: %v", errGoalDesignProof, err)
	}
	rec, err := parseDesignReceipt(path, raw)
	if err != nil {
		return nil, err
	}
	if rec.ReadOnly == nil || !*rec.ReadOnly {
		return nil, fmt.Errorf("%w: receipt is not read-only (false/missing is not rewritten)", errGoalDesignProof)
	}
	st := strings.ToLower(strings.TrimSpace(rec.Status))
	if st != "" && st != "completed" && st != statusDone {
		return nil, fmt.Errorf("%w: receipt status %q is not completed", errGoalDesignProof, rec.Status)
	}
	if st == "" {
		return nil, fmt.Errorf("%w: receipt status must be completed", errGoalDesignProof)
	}
	resultPath := firstNonBlank(rec.ResultPath, path)
	resultRaw, err := os.ReadFile(resultPath)
	if err != nil {
		return nil, fmt.Errorf("%w: result artifact: %v", errGoalDesignProof, err)
	}
	sum := sha256.Sum256(resultRaw)
	digest := hex.EncodeToString(sum[:])
	if strings.TrimSpace(rec.Digest) != "" && !strings.EqualFold(rec.Digest, digest) {
		return nil, fmt.Errorf("%w: receipt digest does not match result bytes", errGoalDesignProof)
	}
	if err := verifyDesignReceiptInputs(rec.Inputs); err != nil {
		return nil, err
	}
	inputID := strings.TrimSpace(rec.InputIdentity)
	if inputID == "" {
		return nil, fmt.Errorf("%w: receipt needs input_identity bound to current source, not a path default", errGoalDesignProof)
	}
	actualModel := firstNonBlank(rec.ActualModel, rec.Model)
	actualRunner := firstNonBlank(rec.ActualRunner, rec.Runner)
	if fictionalAstraGrokBuild(actualModel, actualRunner) {
		return nil, fmt.Errorf("%w: independent Astra design is not a grok-build tuple", errGoalDesignProof)
	}
	node := &WorkflowDesignNode{
		Role:                firstNonBlank(rec.Role, goalDesignRole),
		Model:               rec.Model,
		Runner:              rec.Runner,
		ActualModel:         actualModel,
		ActualRunner:        actualRunner,
		Identity:            rec.Identity,
		ReadOnly:            true,
		At:                  firstNonBlank(rec.At, time.Now().Format(time.RFC3339)),
		DesignSessionID:     rec.SessionID,
		ReceiptPath:         path,
		ResultPath:          resultPath,
		Digest:              digest,
		InputIdentity:       inputID,
		Provenance:          goalDesignProvenanceExt,
		ConsumedTaskID:      rec.ConsumedTaskID,
		ConsumedSessionID:   rec.ConsumedSessionID,
		ConsumedAttemptID:   rec.ConsumedAttemptID,
		ConsumedObservation: rec.ConsumedObservation,
		ConsumedRevision:    rec.ConsumedRevision,
		ConsumedCommit:      rec.ConsumedCommit,
		ConsumedTree:        rec.ConsumedTree,
	}
	return node, nil
}

func parseDesignReceipt(path string, raw []byte) (designReceiptFile, error) {
	var rec designReceiptFile
	trim := bytes.TrimSpace(raw)
	if len(trim) > 0 && trim[0] == '{' {
		if err := json.Unmarshal(raw, &rec); err != nil {
			return rec, fmt.Errorf("%w: parse receipt: %v", errGoalDesignProof, err)
		}
		return rec, nil
	}
	return parseMarkdownDesignReceipt(path, string(raw))
}

func parseMarkdownDesignReceipt(path, body string) (designReceiptFile, error) {
	var rec designReceiptFile
	ro := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			if strings.Contains(line, "SHA256") {
				hash := ""
				if i := strings.Index(line, "SHA256"); i >= 0 {
					hash = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[i+6:]), ":")))
					if fs := strings.Fields(hash); len(fs) > 0 {
						hash = fs[0]
					}
				}
				rest := strings.TrimSpace(strings.TrimPrefix(line, "-"))
				pathPart := rest
				if j := strings.Index(rest, "SHA256"); j >= 0 {
					pathPart = strings.TrimSpace(rest[:j])
					pathPart = strings.Trim(pathPart, "—- ")
				}
				if pathPart != "" && hash != "" {
					rec.Inputs = append(rec.Inputs, designReceiptInput{Path: pathPart, SHA256: hash})
				}
			}
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		val = strings.TrimSpace(val)
		switch key {
		case "role":
			rec.Role = val
		case "actual_model":
			rec.ActualModel = val
		case "actual_runner", "actual_agent_identity":
			if rec.ActualRunner == "" {
				rec.ActualRunner = val
			}
			if key == "actual_agent_identity" && rec.Identity == "" {
				rec.Identity = val
			}
		case "identity":
			rec.Identity = val
		case "read_only":
			ro = val == "true"
			rec.ReadOnly = &ro
		case "status":
			rec.Status = val
		case "session_id", "actual_agent_context_id":
			if rec.SessionID == "" {
				rec.SessionID = val
			}
		case "input_identity":
			rec.InputIdentity = val
		case "digest":
			rec.Digest = val
		case "consumed_task_id":
			rec.ConsumedTaskID = val
		case "consumed_session_id":
			rec.ConsumedSessionID = val
		case "consumed_attempt_id":
			rec.ConsumedAttemptID = val
		case "consumed_observation":
			rec.ConsumedObservation = val
		}
	}
	if rec.ActualRunner == "" {
		rec.ActualRunner = "astra"
	}
	if rec.InputIdentity == "" && len(rec.Inputs) > 0 {
		var parts []string
		for _, in := range rec.Inputs {
			parts = append(parts, in.SHA256)
		}
		rec.InputIdentity = strings.Join(parts, "|")
	}
	rec.ResultPath = path
	if rec.ReadOnly == nil && ro {
		rec.ReadOnly = &ro
	}
	return rec, nil
}

func verifyDesignReceiptInputs(inputs []designReceiptInput) error {
	if len(inputs) == 0 {
		return fmt.Errorf("%w: receipt must verify referenced input bytes", errGoalDesignProof)
	}
	for _, in := range inputs {
		p := strings.TrimSpace(in.Path)
		want := strings.ToLower(strings.TrimSpace(in.SHA256))
		if p == "" || want == "" {
			return fmt.Errorf("%w: receipt input missing path/sha256", errGoalDesignProof)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("%w: receipt input %s: %v", errGoalDesignProof, p, err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != want {
			return fmt.Errorf("%w: receipt input %s digest mismatch", errGoalDesignProof, p)
		}
	}
	return nil
}

func observedDesignModel(t *Task) string {
	if t == nil {
		return ""
	}
	if t.LastRouteAttempt != nil && strings.TrimSpace(t.LastRouteAttempt.ActualModel) != "" {
		return t.LastRouteAttempt.ActualModel
	}
	return firstNonBlank(t.CodexModel, t.CursorModel, t.GrokModel, t.Model)
}

func observedDesignRunner(t *Task) string {
	if t == nil {
		return ""
	}
	if t.LastRouteAttempt != nil && strings.TrimSpace(t.LastRouteAttempt.ActualRunner) != "" {
		return t.LastRouteAttempt.ActualRunner
	}
	return firstNonBlank(t.PreferRunner, t.Runner)
}

func loadCompletedDesignTask(root, id string) (*WorkflowDesignNode, error) {
	t, err := findTaskAnywhere(root, id)
	if err != nil || t == nil {
		return nil, fmt.Errorf("%w: design task %s: %v", errGoalDesignProof, id, err)
	}
	if t.Type != typeReview {
		return nil, fmt.Errorf("%w: design task %s is %s, not a read-only design-review role", errGoalDesignProof, id, t.Type)
	}
	if !taskDurablyDone(root, t) {
		return nil, fmt.Errorf("%w: design task %s is not durably done", errGoalDesignProof, id)
	}
	if t.WriteDomain != nil {
		return nil, fmt.Errorf("%w: design task %s is not read-only", errGoalDesignProof, id)
	}
	result := loadTaskResultForGate(root, t)
	if strings.TrimSpace(result) == "" {
		return nil, fmt.Errorf("%w: design task %s has no producer-bound ReviewOutput/result-gate (prompt hash is not design output)", errGoalDesignProof, id)
	}
	inputID := ""
	if t.ReviewCandidate != nil {
		inputID = strings.TrimSpace(t.ReviewCandidate.Commit) + ":" + strings.TrimSpace(t.ReviewCandidate.Tree)
	}
	if t.ReviewOutput != nil && inputID == "" {
		inputID = strings.TrimSpace(t.ReviewOutput.CandidateCommit) + ":" + strings.TrimSpace(t.ReviewOutput.CandidateTree)
	}
	if strings.Trim(inputID, ":") == "" {
		return nil, fmt.Errorf("%w: design task %s is missing current input identity (candidate commit/tree)", errGoalDesignProof, id)
	}
	model := observedDesignModel(t)
	runner := observedDesignRunner(t)
	sum := sha256.Sum256([]byte(result))
	node := &WorkflowDesignNode{
		Role:          goalDesignRole,
		Model:         model,
		Runner:        runner,
		ActualModel:   model,
		ActualRunner:  runner,
		ReadOnly:      true,
		At:            firstNonBlank(t.UpdatedAt, time.Now().Format(time.RFC3339)),
		DesignTaskID:  t.ID,
		Digest:        hex.EncodeToString(sum[:]),
		InputIdentity: inputID,
		Provenance:    goalDesignProvenanceTask,
	}
	return node, nil
}

func bindWorkflowDesignRepair(root string, cfg *Config, wf *WorkflowRecord, receiptPath string) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	if wf.Candidate == nil || strings.TrimSpace(wf.Candidate.Commit) == "" || strings.TrimSpace(wf.Candidate.Tree) == "" {
		return fmt.Errorf("%w: design repair must consume the current frozen candidate", errWorkflowMalformed)
	}
	if wf.Review == nil || strings.TrimSpace(wf.Review.TaskID) == "" {
		return fmt.Errorf("%w: design repair must consume the current review results", errWorkflowMalformed)
	}
	if wf.DesignLineage != nil && wf.DesignLineage.RepairCount >= workflowDesignRepairLimit {
		return fmt.Errorf("%w: at most %d design repair round", errWorkflowDesignRepairExhausted, workflowDesignRepairLimit)
	}
	node, err := loadExternalDesignReceipt(receiptPath)
	if err != nil {
		return err
	}
	if node.ConsumedCommit != wf.Candidate.Commit || node.ConsumedTree != wf.Candidate.Tree {
		return fmt.Errorf("%w: design repair receipt does not consume the current candidate", errGoalDesignProof)
	}
	node.Provenance = goalDesignProvenanceRepair
	node.ConsumedReviewTask = wf.Review.TaskID
	if err := designProofComplete(node); err != nil {
		return err
	}
	if wf.DesignLineage == nil {
		wf.DesignLineage = &WorkflowDesignLineage{}
	}
	if wf.DesignLineage.Initial == nil {
		wf.DesignLineage.Initial = node
	}
	wf.DesignLineage.LatestValid = node
	wf.DesignLineage.RepairCount++
	return persistWorkflow(root, cfg, wf)
}

func admitWorkflowWriterMode(root string, cfg *Config, wf *WorkflowRecord, prompt, mode string) (*Task, error) {
	mode = strings.TrimSpace(mode)
	if mode != "" && mode != goalWriterManual && mode != goalWriterNative {
		return nil, fmt.Errorf("%w: writer mode %q (native|manual)", errWorkflowMalformed, mode)
	}
	if mode == "" {
		return admitWorkflowWriter(root, cfg, wf, prompt)
	}
	if mode == goalWriterNative {
		cap := nativeGoalCapability(wf.WriterEngine)
		if cap.Rejected {
			return nil, fmt.Errorf("%w: %s rejected: %s", errGoalCapability, cap.Runner, cap.Reason)
		}
		if cap.Level != goalCapSupported {
			return nil, fmt.Errorf("%w: %s is %s (%s); automatic native goal is unproven — use -mode manual",
				errGoalCapability, cap.Runner, cap.Level, cap.Reason)
		}
	}
	hard := int64(0)
	if cfg != nil && cfg.StepTimeoutMin > 0 {
		hard = int64(cfg.StepTimeoutMin) * 60
	}
	return publishWorkflowWriter(root, cfg, wf, prompt, newTaskGoalBinding(wf, mode, 0, hard))
}

func workflowWriterTask(root string, wf *WorkflowRecord) (*Task, error) {
	if wf == nil || wf.WriterTaskID == "" {
		return nil, fmt.Errorf("%w: no writer task", errWorkflowMalformed)
	}
	return findTaskAnywhere(root, wf.WriterTaskID)
}

func buildWorkflowShowView(root string, wf *WorkflowRecord) (writer *workflowGoalView, caps []GoalCapability) {
	caps = nativeGoalCapabilities()
	if wf == nil || wf.WriterTaskID == "" {
		return nil, caps
	}
	t, err := findTaskAnywhere(root, wf.WriterTaskID)
	if err != nil || t == nil {
		return nil, caps
	}
	gv := &workflowGoalView{
		TaskID:    t.ID,
		Status:    t.Status,
		SessionID: t.SessionID,
		AttemptID: t.ActiveAttemptID,
		Revision:  t.Revision,
	}
	if t.Goal != nil {
		gv.GoalID = t.Goal.NativeGoalID
		gv.Observation = t.Goal.Observation
		gv.WriterMode = t.Goal.WriterMode
		if gv.AttemptID == "" {
			gv.AttemptID = t.Goal.BoundAttemptID
		}
	}
	return gv, caps
}

func encodeWorkflowShow(w io.Writer, root string, wf *WorkflowRecord) error {
	if wf == nil {
		return errWorkflowMalformed
	}
	raw, err := json.Marshal(wf)
	if err != nil {
		return err
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	writer, caps := buildWorkflowShowView(root, wf)
	if writer != nil {
		obj["writer"] = writer
	}
	if len(caps) > 0 {
		obj["native_goal_capabilities"] = caps
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(obj)
}

func goalRunAllowed(root string, t *Task) error {
	if t == nil || t.Goal == nil {
		return fmt.Errorf("%w: task is not a Goal writer; admit with -mode manual", errWorkflowMalformed)
	}
	if t.Goal.WriterMode != goalWriterManual {
		return fmt.Errorf("%w: writer mode %s is not manual", errGoalManualRequired, t.Goal.WriterMode)
	}
	if t.Goal.CancelRequested && !t.Goal.StopEvidence {
		return fmt.Errorf("%w: cancel in flight without stop evidence", errGoalRedispatchBlocked)
	}
	started := goalAttemptStarted(root, t)
	switch t.Goal.Observation {
	case goalObsUnknown:
		if started || t.Goal.Started {
			return fmt.Errorf("%w: crash-after-start unknown blocks redispatch", errGoalRedispatchBlocked)
		}
	case goalObsDone, goalObsFailed, goalObsCanceled:
		return fmt.Errorf("%w: goal already terminal (%s)", errGoalRedispatchBlocked, t.Goal.Observation)
	case goalObsRunning:
		if t.ActiveAttemptID != "" {
			return fmt.Errorf("%w: goal already running on attempt %s", errGoalRedispatchBlocked, t.ActiveAttemptID)
		}
	}
	return nil
}

func bindGoalSessionBeforeEffect(root string, t *Task) error {
	if t.SessionID != "" {
		return nil
	}
	t.SessionID = newGoalSessionID()
	t.touch()
	return saveTask(root, t)
}

func abandonUnstartedGoalAttempt(root string, t *Task) error {
	if t == nil {
		return nil
	}
	if current, err := loadTask(root, t.ID); err == nil && current != nil {
		*t = *current
	}
	if t.ActiveAttemptID != "" {
		rec, err := loadAttempt(root, t.ID, t.ActiveAttemptID)
		if err == nil && rec != nil && rec.State == attemptBound && rec.PID > 0 && attemptProducerAlive(rec) {
			return fmt.Errorf("%w: attempt already started", errGoalRedispatchBlocked)
		}
		if rec != nil && rec.State != attemptExited && rec.State != attemptRevoked {
			if err := closeAttemptRecord(root, t.ID, t.ActiveAttemptID, attemptExited); err != nil {
				return err
			}
		}
		t.ActiveAttemptID = ""
	}
	if t.Goal != nil {
		t.Goal.Started = false
		t.Goal.Observation = goalObsUnstarted
		t.Goal.ObservationNote = "crash before start remains unstarted"
		t.Goal.BoundAttemptID = ""
	}
	t.Status = statusQueued
	t.touch()
	return saveTask(root, t)
}

func withWorkflowSchedulerLock(root string, cfg *Config, fn func() error) error {
	if holdsSchedulerLock(root) {
		return fn()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if acquireLock(root, lockTTL(cfg)) {
			defer releaseLock(root)
			return fn()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("another cardex instance holds the scheduler lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func launchManualWorkflowGoal(root string, cfg *Config, wf *WorkflowRecord, budget int64) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	t, err := workflowWriterTask(root, wf)
	if err != nil {
		return err
	}
	if t.Goal == nil {
		return fmt.Errorf("%w: admit the writer with -mode manual before goal-run", errWorkflowMalformed)
	}
	if err := goalRunAllowed(root, t); err != nil {
		return err
	}
	if t.PreferRunner != grokBuildRunnerName {
		cap := nativeGoalCapability(t.PreferRunner)
		return fmt.Errorf("%w: manual Grok /goal is the proven interactive entry; %s is %s (%s)",
			errGoalCapability, cap.Runner, cap.Level, cap.Reason)
	}
	if !grokBuildEnabled(cfg) {
		return fmt.Errorf("%w: grok_build_bin/grok_build not enabled", errGoalCapability)
	}
	model := resolveGrokBuildModel(cfg, t)
	effort := resolveGrokBuildEffort(cfg, t)
	if model == "" || effort == "" {
		return fmt.Errorf("unresolved Grok model/effort")
	}
	sandbox, permission, err := resolveManualGrokTuple(cfg, t)
	if err != nil {
		return err
	}

	acquired := false
	if !holdsSchedulerLock(root) {
		deadline := time.Now().Add(5 * time.Second)
		for !acquired {
			if acquireLock(root, lockTTL(cfg)) {
				acquired = true
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("another cardex instance holds the scheduler lock; stop it before goal-run")
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

	if err := admitManualGoalLaunchLocked(root, cfg, wf, t, budget); err != nil {
		return err
	}
	releaseAdmission()

	if hook := goalLaunchBeforeStartHook; hook != nil {
		if herr := hook(); herr != nil {
			_ = withWorkflowSchedulerLock(root, cfg, func() error {
				return abandonUnstartedGoalAttempt(root, t)
			})
			return fmt.Errorf("%w: %v", errGoalLaunchUnstarted, herr)
		}
	}

	timeout := time.Duration(t.Goal.HardTimeoutSec) * time.Second
	if timeout <= 0 && cfg != nil && cfg.StepTimeoutMin > 0 {
		timeout = time.Duration(cfg.StepTimeoutMin) * time.Minute
	}
	if timeout <= 0 {
		timeout = 60 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := manualGrokGoalArgs(cfg, t, model, effort, sandbox, permission)
	cmd := exec.CommandContext(ctx, cfg.GrokBuildBin, args...)
	cmd.Dir = t.Dir
	stdin := os.Stdin
	if goalLaunchStdin != nil {
		stdin = goalLaunchStdin
	}
	cmd.Stdin = stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	restore, err := setupManualGoalProcGroup(cmd)
	if err != nil {
		return err
	}
	if cmd.Cancel == nil {
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			return killProcGroup(cmd.Process.Pid)
		}
	}
	home, _ := os.UserHomeDir()
	cmd.Env = providerChildEnv(home, nil)

	taskExecRoot.Store(t.ID, root)
	defer taskExecRoot.Delete(t.ID)

	runErr := runCmdRegisteredForTaskWorkspace(cmd, t.ID, t.Dir)
	restoreErr := restore()
	finalErr := withWorkflowSchedulerLock(root, cfg, func() error {
		return finalizeManualGoalLaunch(root, wf, t, ctx, runErr)
	})
	if finalErr != nil {
		return finalErr
	}
	if restoreErr != nil {
		return restoreErr
	}
	return nil
}

func admitManualGoalLaunchLocked(root string, cfg *Config, wf *WorkflowRecord, t *Task, budget int64) error {
	fresh, err := loadTask(root, t.ID)
	if err != nil {
		return err
	}
	*t = *fresh
	if t.Goal == nil {
		return fmt.Errorf("%w: admit the writer with -mode manual before goal-run", errWorkflowMalformed)
	}
	if err := goalRunAllowed(root, t); err != nil {
		return err
	}
	if err := reverifyGoalDesignProof(root, wf); err != nil {
		return err
	}
	if !admissionAllowsScheduling(root) {
		return errAdmissionDenied
	}
	if err := bindGoalSessionBeforeEffect(root, t); err != nil {
		return err
	}
	fresh, err = loadTask(root, t.ID)
	if err != nil {
		return err
	}
	*t = *fresh
	if budget > 0 {
		t.Goal.BudgetTokens = budget
	}
	if t.Goal.HardTimeoutSec <= 0 && cfg != nil && cfg.StepTimeoutMin > 0 {
		t.Goal.HardTimeoutSec = int64(cfg.StepTimeoutMin) * 60
	}

	if t.Goal.Observation == goalObsHeld && t.Goal.Continuation != "" {
		if taskHasLiveWriterProof(root, t) {
			return fmt.Errorf("%w: live producer/lease still held", errGoalRedispatchBlocked)
		}
		if err := persistSameSessionResume(root, t); err != nil {
			return err
		}
		reloaded, err := loadTask(root, t.ID)
		if err != nil {
			return err
		}
		*t = *reloaded
	} else if t.ActiveAttemptID != "" {
		if err := abandonUnstartedGoalAttempt(root, t); err != nil {
			return err
		}
		reloaded, err := loadTask(root, t.ID)
		if err != nil {
			return err
		}
		*t = *reloaded
	}

	tasks, err := loadTasks(root)
	if err != nil {
		return err
	}
	if !dagAllowsTask(root, tasks, t.ID) {
		return fmt.Errorf("%w: DAG predecessor missing or not durably done", errAdmissionDenied)
	}
	live := mergeLiveWriterTasks(nil, reconstructLiveWriterClaims(root))
	if writerConflictsWithActive(t, live) {
		return fmt.Errorf("%w: write-domain/resource conflict with an active writer", errAdmissionDenied)
	}
	maxPar := 1
	if cfg != nil && cfg.MaxParallel > 0 {
		maxPar = cfg.MaxParallel
	}
	slots, err := liveGoalLaunchSlots(root)
	if err != nil {
		return err
	}
	if slots >= maxPar {
		return fmt.Errorf("%w: max_parallel %d reached", errAdmissionDenied, maxPar)
	}
	if grokSlots := countLiveRunner(root, grokBuildRunnerName); grokSlots >= providerParallelLimit(cfg, grokBuildRunnerName) {
		return fmt.Errorf("%w: grok max_parallel reached", errAdmissionDenied)
	}

	contract := frozenGoalStageContract(wf, t)
	digest := sha256Hex(contract)
	t.Goal.InputDigest = digest
	if err := writeGoalStageContract(root, wf.ID, contract); err != nil {
		return err
	}
	printManualGoalInstructions(root, wf, t, contract, digest)

	t.Status = statusRunning
	t.Goal.Observation = goalObsRunning
	t.Goal.ObservationNote = "manual Grok TUI; /goal is typed by the operator"
	t.Runner = grokBuildRunnerName
	t.touch()
	if err := reserveDispatchAttempt(root, t); err != nil {
		t.Status = statusQueued
		if t.Goal != nil {
			t.Goal.Observation = goalObsUnstarted
		}
		_ = saveTask(root, t)
		return err
	}
	t.Goal.BoundAttemptID = t.ActiveAttemptID
	t.touch()
	return saveTask(root, t)
}

func persistSameSessionResume(root string, t *Task) error {
	session, goalID := t.SessionID, ""
	if t.Goal != nil {
		goalID = t.Goal.NativeGoalID
	}
	restoreScheduling(t)
	t.Status = statusQueued
	if t.Goal != nil {
		t.Goal.ObservationNote = "same-session resume persisted before reserve"
	}
	if err := commitTaskTransition(root, t, transitionRequest{
		EventType:            evQueued,
		Actor:                "workflow:goal-run",
		Status:               statusQueued,
		RequireSchedulerLock: true,
		Detail: map[string]any{
			"continuation": "same-session",
			"session_id":   session,
			"goal_id":      goalID,
		},
	}); err != nil {
		return err
	}
	if t.SessionID == "" {
		t.SessionID = session
	}
	if t.Goal != nil && t.Goal.NativeGoalID == "" {
		t.Goal.NativeGoalID = goalID
	}
	return nil
}

func finalizeManualGoalLaunch(root string, wf *WorkflowRecord, t *Task, ctx context.Context, runErr error) error {
	reloaded, loadErr := loadTask(root, t.ID)
	if loadErr == nil && reloaded != nil {
		*t = *reloaded
	}
	if t.Goal == nil {
		t.Goal = &TaskGoalBinding{WriterMode: goalWriterManual}
	}

	started := goalAttemptStarted(root, t)
	t.Goal.Started = started
	if !started {
		if aerr := abandonUnstartedGoalAttempt(root, t); aerr != nil {
			if runErr != nil {
				return fmt.Errorf("%w: %v (abandon: %v)", errGoalLaunchUnstarted, runErr, aerr)
			}
			return fmt.Errorf("%w: %v", errGoalLaunchUnstarted, aerr)
		}
		if runErr != nil {
			return fmt.Errorf("%w: %v", errGoalLaunchUnstarted, runErr)
		}
		return fmt.Errorf("%w", errGoalLaunchUnstarted)
	}

	timedOut := ctx.Err() == context.DeadlineExceeded
	custody := goalCustodyReleased(root, t)
	t.Goal.CustodyReleased = custody
	t.Goal.Observation = goalObsUnknown
	switch {
	case timedOut:
		t.Goal.ObservationNote = "hard timeout fired; native budget is soft; no guessed success"
	case !custody:
		t.Goal.ObservationNote = "process ended with live descendants or held lease; observation only"
	default:
		t.Goal.ObservationNote = "TUI returned; accept done only via goal-sync with native proof plus released custody"
	}
	t.Status = statusHeld
	revokeScheduling(t)
	if custody && t.ActiveAttemptID != "" {
		_ = closeAttemptRecord(root, t.ID, t.ActiveAttemptID, attemptExited)
		t.ActiveAttemptID = ""
	}
	t.touch()
	if err := saveTask(root, t); err != nil {
		return err
	}
	if runErr != nil && !timedOut && !errors.Is(runErr, context.DeadlineExceeded) {
		return runErr
	}
	return nil
}

func liveGoalLaunchSlots(root string) (int, error) {
	tasks, err := loadTasks(root)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, tk := range tasks {
		if tk == nil || taskIsReadOnlyType(tk) || tk.IntegrationGate != nil {
			continue
		}
		live, rec := liveAttempt(root, tk)
		if live || attemptProducerAlive(rec) || anyTaskProcAlive(tk.ID) || taskProcessResidue(tk.ID) {
			n++
		}
	}
	return n, nil
}

func countLiveRunner(root, runner string) int {
	tasks, err := loadTasks(root)
	if err != nil {
		return 1 << 20
	}
	n := 0
	for _, tk := range tasks {
		if tk == nil || !taskHasLiveWriterProof(root, tk) {
			continue
		}
		if firstNonBlank(tk.Runner, tk.PreferRunner) == runner {
			n++
		}
	}
	return n
}

func resolveManualGrokTuple(cfg *Config, t *Task) (sandbox, permission string, err error) {
	if t == nil {
		return "", "", fmt.Errorf("%w: empty task", errGoalUnsupportedTuple)
	}
	perm := strings.TrimSpace(t.PermissionMode)
	switch strings.ToLower(perm) {
	case "", "auto", "plan", "default", "acceptedits", "accept":
	default:
		return "", "", fmt.Errorf("%w: permission-mode %q", errGoalUnsupportedTuple, perm)
	}
	writeCapable := grokBuildWriteCapable(t)
	sandbox, permission = resolvedGrokBuildReadOnlySandbox(cfg), "plan"
	if writeCapable {
		sandbox, permission = "workspace", "auto"
	}
	if strings.EqualFold(perm, "plan") {
		permission = "plan"
	}
	if strings.EqualFold(perm, "auto") && !writeCapable {
		return "", "", fmt.Errorf("%w: auto permission on read-only tuple", errGoalUnsupportedTuple)
	}
	if sandbox == "" || permission == "" {
		return "", "", fmt.Errorf("%w: unresolved sandbox/permission", errGoalUnsupportedTuple)
	}
	return sandbox, permission, nil
}

func manualGrokGoalArgs(cfg *Config, t *Task, model, effort, sandbox, permission string) []string {
	args := []string{
		"--no-auto-update",
		"--model", model,
		"--reasoning-effort", effort,
		"--sandbox", sandbox,
		"--permission-mode", permission,
		"--no-memory", "--no-subagents", "--disable-web-search",
	}
	if grokBuildWriteCapable(t) && permission != "plan" {
		args = append(args, "--no-plan")
	}
	sessionDir := grokGoalSessionDir(defaultGrokHome(), t.Dir, t.SessionID)
	if _, err := os.Stat(sessionDir); err == nil && t.SessionID != "" {
		args = append(args, "--resume", t.SessionID)
	} else if t.SessionID != "" {
		args = append(args, "-s", t.SessionID)
	}
	return args
}

func frozenGoalStageContract(wf *WorkflowRecord, t *Task) string {
	var b strings.Builder
	outcome, accept, stop := "", "", goalStopSoftBudget
	ret := true
	if wf != nil {
		outcome = wf.Goal
		accept = wf.TerminalCriteria
	}
	if t != nil && t.Goal != nil {
		if strings.TrimSpace(t.Goal.Outcome) != "" {
			outcome = t.Goal.Outcome
		}
		if strings.TrimSpace(t.Goal.Acceptance) != "" {
			accept = t.Goal.Acceptance
		}
		if strings.TrimSpace(t.Goal.Stop) != "" {
			stop = t.Goal.Stop
		}
		ret = t.Goal.ReturnToDesign
	}
	fmt.Fprintf(&b, "outcome: %s\n", strings.TrimSpace(outcome))
	fmt.Fprintf(&b, "acceptance: %s\n", strings.TrimSpace(accept))
	fmt.Fprintf(&b, "stop: %s\n", stop)
	fmt.Fprintf(&b, "return_to_design: %v\n", ret)
	if t != nil && t.Goal != nil {
		fmt.Fprintf(&b, "scope: %s\n", strings.TrimSpace(t.Goal.Scope))
		fmt.Fprintf(&b, "budget_tokens: %d (soft)\n", t.Goal.BudgetTokens)
		fmt.Fprintf(&b, "hard_timeout_seconds: %d\n", t.Goal.HardTimeoutSec)
	}
	if t != nil {
		fmt.Fprintf(&b, "prompts:\n")
		for i, p := range t.Prompts {
			fmt.Fprintf(&b, "  [%d] %s\n", i+1, strings.TrimSpace(p))
		}
		if t.WriteDomain != nil {
			fmt.Fprintf(&b, "owned_paths: %s\n", strings.Join(t.WriteDomain.Paths, ", "))
			fmt.Fprintf(&b, "write_domain: %s\n", t.WriteDomain.ID)
		}
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(&b, "depends_on: %s\n", strings.Join(t.DependsOn, ", "))
		} else {
			fmt.Fprintf(&b, "depends_on: (none)\n")
		}
		fmt.Fprintf(&b, "session_id: %s\n", t.SessionID)
	}
	if wf != nil && wf.DesignLineage != nil && wf.DesignLineage.LatestValid != nil {
		n := wf.DesignLineage.LatestValid
		fmt.Fprintf(&b, "latest_design_path: %s\n", firstNonBlank(n.ResultPath, n.ReceiptPath))
		fmt.Fprintf(&b, "latest_design_digest: %s\n", strings.TrimSpace(n.Digest))
		fmt.Fprintf(&b, "latest_design_input_identity: %s\n", strings.TrimSpace(n.InputIdentity))
		fmt.Fprintf(&b, "latest_design_decision: %s\n", strings.TrimSpace(wf.DesignLineage.LastResultDecision))
	}
	return b.String()
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func writeGoalStageContract(root, workflowID, contract string) error {
	if err := os.MkdirAll(workflowsDir(root), 0o755); err != nil {
		return err
	}
	path := filepath.Join(workflowsDir(root), workflowID+".stage-contract.txt")
	return os.WriteFile(path, []byte(contract), 0o644)
}

func copyableNativeGoalCommand(contractPath, digest string, budget int64) string {
	cmd := fmt.Sprintf("/goal Read and implement the complete stage contract at %s; verify SHA256 %s before any write.", contractPath, digest)
	if budget > 0 {
		cmd += fmt.Sprintf(" --budget %d", budget)
	}
	return cmd
}

func printManualGoalInstructions(root string, wf *WorkflowRecord, t *Task, contract, digest string) {
	contractPath := filepath.Join(workflowsDir(root), wf.ID+".stage-contract.txt")
	fmt.Printf("session_id=%s bound before launch\n", t.SessionID)
	fmt.Printf("stage_contract_path=%s\n", contractPath)
	fmt.Printf("stage_contract_digest=%s\n", digest)
	fmt.Printf("Frozen stage contract is the native /goal input (not grok -p, not outcome-only):\n")
	fmt.Print(contract)
	budget := int64(0)
	if t.Goal != nil {
		budget = t.Goal.BudgetTokens
	}
	fmt.Printf("Copyable native command (literal path+digest; TUI is not a shell):\n")
	fmt.Printf("  %s\n", copyableNativeGoalCommand(contractPath, digest, budget))
	fmt.Printf("  /goal status\n")
	fmt.Printf("Active pause/resume is unverified and is not accepted behavior.\n")
	fmt.Printf("grok -p is a single turn and is not goal proof.\n")
	fmt.Printf("After the TUI returns: cardex workflow goal-sync %s\n", wf.ID)
	fmt.Printf("goal-sync does not launch a provider; it updates stage facts.\n")
}

func goalAttemptID(t *Task) string {
	if t == nil {
		return ""
	}
	if t.ActiveAttemptID != "" {
		return t.ActiveAttemptID
	}
	if t.Goal != nil {
		return t.Goal.BoundAttemptID
	}
	return ""
}

func loadRequiredGoalAttempt(root string, t *Task) (*AttemptRecord, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: empty task", errGoalAttemptRequired)
	}
	id := goalAttemptID(t)
	if id == "" {
		return nil, fmt.Errorf("%w: missing attempt identity", errGoalAttemptRequired)
	}
	rec, err := loadAttempt(root, t.ID, id)
	if err != nil || rec == nil {
		return nil, fmt.Errorf("%w: %s", errGoalAttemptRequired, id)
	}
	if rec.TaskID != t.ID || rec.AttemptID != id {
		return nil, fmt.Errorf("%w: attempt identity mismatch", errGoalAttemptRequired)
	}
	return rec, nil
}

func goalAttemptHasStartIdentity(rec *AttemptRecord) bool {
	return rec != nil && rec.PID > 0 && rec.PGID > 0 && strings.TrimSpace(rec.StartIdentity) != ""
}

func goalAttemptAcceptsTerminal(root string, t *Task, rec *AttemptRecord) error {
	if rec == nil {
		return fmt.Errorf("%w: missing attempt record", errGoalAttemptRequired)
	}
	if t != nil && rec.TaskID != t.ID {
		return fmt.Errorf("%w: attempt identity mismatch", errGoalAttemptRequired)
	}
	if rec.State == attemptReserved && rec.PID <= 0 {
		return fmt.Errorf("%w: reserved attempt never started", errGoalAttemptRequired)
	}
	if rec.State == attemptRevoked {
		return fmt.Errorf("%w: attempt revoked", errGoalAttemptRequired)
	}
	if rec.State != attemptBound && rec.State != attemptExited {
		return fmt.Errorf("%w: attempt state %s is not bound/exited", errGoalAttemptRequired, rec.State)
	}
	if !goalAttemptHasStartIdentity(rec) {
		return fmt.Errorf("%w: exited/bound without start/process identity is not started", errGoalAttemptRequired)
	}
	want := ""
	if t != nil {
		want = canonicalWorkspaceID(t.Dir)
	}
	if want != "" && rec.WorkspaceLeaseID != want {
		return fmt.Errorf("%w: attempt workspace mismatch", errGoalAttemptRequired)
	}
	return nil
}

func goalAttemptStarted(root string, t *Task) bool {
	rec, err := loadRequiredGoalAttempt(root, t)
	if err != nil {
		return false
	}
	if !goalAttemptHasStartIdentity(rec) {
		return false
	}
	return rec.State == attemptBound || rec.State == attemptExited
}

func matchFrozenGoalInputDigest(root string, wf *WorkflowRecord, t *Task) error {
	if t == nil || t.Goal == nil || strings.TrimSpace(t.Goal.InputDigest) == "" {
		return fmt.Errorf("%w: missing frozen InputDigest for this native session/goal/input", errGoalSyncRejected)
	}
	if wf == nil {
		return fmt.Errorf("%w: missing workflow for InputDigest", errGoalSyncRejected)
	}
	path := filepath.Join(workflowsDir(root), wf.ID+".stage-contract.txt")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: missing frozen stage contract", errGoalSyncRejected)
	}
	if sha256Hex(string(raw)) != t.Goal.InputDigest {
		return fmt.Errorf("%w: InputDigest mismatch for this native session/goal/input", errGoalSyncRejected)
	}
	return nil
}

func goalCustodyReleased(root string, t *Task) bool {
	rec, err := loadRequiredGoalAttempt(root, t)
	if err != nil || rec == nil {
		return false
	}
	return producerGone(t, rec) && !taskHasLiveWriterProof(root, t)
}

func syncWorkflowGoal(root string, cfg *Config, wf *WorkflowRecord, req GoalSyncRequest) (*Task, error) {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return nil, err
	}
	t, err := workflowWriterTask(root, wf)
	if err != nil {
		return nil, err
	}
	if t.Goal == nil {
		return t, fmt.Errorf("%w: task is not a Goal writer", errWorkflowMalformed)
	}

	expectedRev := req.ExpectedRevision
	if expectedRev == 0 {
		expectedRev = t.Revision
	}
	expectedSession := strings.TrimSpace(req.ExpectedSession)
	if expectedSession == "" {
		expectedSession = t.SessionID
	}
	expectedGoal := strings.TrimSpace(req.ExpectedGoalID)
	if expectedGoal == "" {
		expectedGoal = t.Goal.NativeGoalID
	}
	expectedAttempt := strings.TrimSpace(req.ExpectedAttempt)
	if expectedAttempt == "" {
		expectedAttempt = t.Goal.BoundAttemptID
		if expectedAttempt == "" {
			expectedAttempt = t.ActiveAttemptID
		}
	}
	if expectedSession == "" || t.SessionID == "" || t.SessionID != expectedSession {
		return t, fmt.Errorf("%w: other session", errGoalSyncRejected)
	}
	if expectedGoal != "" && t.Goal.NativeGoalID != "" && expectedGoal != t.Goal.NativeGoalID {
		return t, fmt.Errorf("%w: other goal_id", errGoalSyncRejected)
	}
	if expectedAttempt != "" {
		bound := t.Goal.BoundAttemptID
		if bound == "" {
			bound = t.ActiveAttemptID
		}
		if bound != "" && bound != expectedAttempt {
			return t, fmt.Errorf("%w: old attempt", errGoalSyncRejected)
		}
	}
	if t.Revision != expectedRev {
		if t.Goal.SyncedRevision == expectedRev {
			return t, nil
		}
		return t, fmt.Errorf("%w: stale revision (task=%d expected=%d)", errGoalSyncRejected, t.Revision, expectedRev)
	}

	grokHome := strings.TrimSpace(req.GrokHome)
	if grokHome == "" {
		grokHome = defaultGrokHome()
	}
	cwd := strings.TrimSpace(req.GrokCWD)
	if cwd == "" {
		cwd = t.Dir
	}
	obs, obsErr := observeNativeGrokGoal(grokHome, cwd, t.SessionID, expectedGoal)
	if errors.Is(obsErr, errGoalSyncRejected) {
		return t, obsErr
	}
	if obsErr != nil {
		return applyHeldUnknown(root, t, expectedRev, "native observation failed: "+obsErr.Error())
	}
	if obs.SessionID == "" || obs.SessionID != t.SessionID {
		return t, fmt.Errorf("%w: other session", errGoalSyncRejected)
	}
	if expectedGoal != "" && obs.GoalID != "" && obs.GoalID != expectedGoal {
		return t, fmt.Errorf("%w: other goal_id", errGoalSyncRejected)
	}
	if t.Goal.NativeGoalID != "" && obs.GoalID != "" && obs.GoalID != t.Goal.NativeGoalID {
		return t, fmt.Errorf("%w: other goal_id", errGoalSyncRejected)
	}
	if obs.ObservedCWD == "" || !samePath(obs.ObservedCWD, cwd) {
		if obs.NativeStatus == "complete" {
			return t, fmt.Errorf("%w: summary cwd mismatch or empty", errGoalSyncRejected)
		}
	}

	rec, recErr := loadRequiredGoalAttempt(root, t)
	attemptOK := recErr == nil && rec != nil && goalAttemptAcceptsTerminal(root, t, rec) == nil
	if expectedAttempt != "" && (!attemptOK || rec == nil || rec.AttemptID != expectedAttempt) {
		attemptOK = false
	}
	custody := attemptOK && goalCustodyReleased(root, t)
	applied := mapNativeGoalToTask(obs, custody, t.Goal.CancelRequested, t.Goal.StopEvidence)
	if !attemptOK && (applied.AcceptDone || applied.AcceptFailed || obs.NativeStatus == "complete" || obs.NativeStatus == "failed") {
		applied.AcceptDone = false
		applied.AcceptFailed = false
		applied.Status = statusHeld
		applied.Observation = goalObsUnknown
		applied.Note = "exact existing started attempt is required; reserved/missing/revoked attempts are not accepted done/failed"
	}
	if (applied.AcceptDone || applied.AcceptFailed) && matchFrozenGoalInputDigest(root, wf, t) != nil {
		applied.AcceptDone = false
		applied.AcceptFailed = false
		applied.Status = statusHeld
		applied.Observation = goalObsUnknown
		applied.Note = "frozen InputDigest must match this native session/goal/input"
	}
	if applied.AcceptDone && (!obs.SummaryOK || obs.ObservedCWD == "") {
		applied.AcceptDone = false
		applied.Status = statusHeld
		applied.Observation = goalObsUnknown
		applied.Note = "session summary cwd/ID is required before accepted complete"
	}

	if t.Status == statusCanceled || t.Goal.Observation == goalObsCanceled {
		applied.AcceptDone = false
		applied.AcceptFailed = false
		applied.AcceptCanceled = t.Goal.StopEvidence
		applied.Status = statusCanceled
		applied.Observation = goalObsCanceled
		applied.Note = "canceled; late native return does not resurrect done"
	} else if t.Goal.CancelRequested && !t.Goal.StopEvidence {
		applied.Observation = goalObsUnknown
		applied.Status = statusHeld
		applied.Note = "cancel requested without stop evidence; late native return is not accepted done/canceled"
		applied.AcceptDone = false
		applied.AcceptCanceled = false
	}

	if t.Goal.SyncedRevision == expectedRev &&
		t.Goal.Observation == applied.Observation && t.Status == applied.Status {
		return t, nil
	}

	t.Goal.LastNativeStatus = obs.NativeStatus
	t.Goal.ClassifierVerdict = obs.Classifier
	t.Goal.EvidenceComplete = applied.AcceptDone
	t.Goal.CustodyReleased = custody
	t.Goal.Observation = applied.Observation
	t.Goal.ObservationNote = applied.Note
	t.Goal.Continuation = applied.Continuation
	t.Goal.SyncedRevision = expectedRev
	if t.Goal.NativeGoalID == "" && obs.GoalID != "" {
		t.Goal.NativeGoalID = obs.GoalID
	}
	if t.ActiveAttemptID == "" && t.Goal.BoundAttemptID != "" {
		t.ActiveAttemptID = t.Goal.BoundAttemptID
	}

	switch {
	case applied.AcceptDone:
		t.Status = statusDone
		if err := commitTaskTransition(root, t, transitionRequest{
			EventType:            evDone,
			Actor:                "workflow:goal-sync",
			Status:               statusDone,
			RequireSchedulerLock: false,
			Detail: map[string]any{
				"workflow": wf.ID, "session_id": t.SessionID, "goal_id": t.Goal.NativeGoalID,
				"attempt_id": t.Goal.BoundAttemptID, "last_classifier_verdict": obs.Classifier,
			},
		}); err != nil {
			return applyHeldUnknown(root, t, expectedRev, "durable done refused: "+err.Error())
		}
	case applied.AcceptFailed:
		t.Status = statusFailed
		if err := commitTaskTransition(root, t, transitionRequest{
			EventType:            evFailed,
			Actor:                "workflow:goal-sync",
			Status:               statusFailed,
			RequireSchedulerLock: false,
			Detail: map[string]any{
				"workflow": wf.ID, "session_id": t.SessionID, "goal_id": t.Goal.NativeGoalID,
			},
		}); err != nil {
			return applyHeldUnknown(root, t, expectedRev, "durable failed refused: "+err.Error())
		}
	case applied.Status == statusRunning:
		t.Status = statusRunning
		t.Goal.Observation = goalObsRunning
		t.touch()
		if err := saveTask(root, t); err != nil {
			return t, err
		}
	default:
		t.Status = statusHeld
		if t.Goal.Observation == goalObsUnknown || applied.Observation == goalObsUnknown {
			revokeScheduling(t)
		}
		t.touch()
		if err := saveTask(root, t); err != nil {
			return t, err
		}
	}
	return t, persistWorkflow(root, cfg, wf)
}

type nativeGoalObservation struct {
	SessionID       string
	GoalID          string
	NativeStatus    string
	Classifier      string
	FinalStatus     string
	FinalClassifier string
	UpdatesOK       bool
	SummaryOK       bool
	ObservedCWD     string
	Missing         bool
	Contradictory   bool
	BudgetLimited   bool
	NotAchieved     bool
}

type mappedGoal struct {
	Status         string
	Observation    string
	Note           string
	Continuation   string
	AcceptDone     bool
	AcceptFailed   bool
	AcceptCanceled bool
}

func observeNativeGrokGoal(grokHome, cwd, sessionID, expectedGoalID string) (nativeGoalObservation, error) {
	var out nativeGoalObservation
	if grokHome == "" || sessionID == "" {
		out.Missing = true
		return out, fmt.Errorf("missing grok home or session")
	}
	dir := grokGoalSessionDir(grokHome, cwd, sessionID)
	statePath := filepath.Join(dir, "goal", "state.json")
	updatesPath := filepath.Join(dir, "updates.jsonl")
	summaryPath := filepath.Join(dir, "summary.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		out.Missing = true
		return out, err
	}
	var st grokNativeGoalStateFile
	if err := json.Unmarshal(raw, &st); err != nil {
		out.Contradictory = true
		return out, err
	}
	out.GoalID = strings.TrimSpace(st.GoalID)
	out.NativeStatus = strings.ToLower(strings.TrimSpace(st.Status))
	out.Classifier = strings.TrimSpace(st.LastClassifierVerdict)
	if out.NativeStatus == "budget_limited" {
		out.BudgetLimited = true
	}
	if out.NativeStatus == "complete" && strings.EqualFold(out.Classifier, "not_achieved") {
		out.NotAchieved = true
	}

	if sumRaw, sumErr := os.ReadFile(summaryPath); sumErr == nil {
		var sum grokSessionSummaryFile
		if err := json.Unmarshal(sumRaw, &sum); err != nil {
			out.Contradictory = true
			return out, fmt.Errorf("%w: malformed summary.json", errGoalSyncRejected)
		}
		observedID := strings.TrimSpace(sum.Info.ID)
		observedCWD := strings.TrimSpace(sum.Info.CWD)
		if observedID == "" || observedID != sessionID {
			return out, fmt.Errorf("%w: other session", errGoalSyncRejected)
		}
		if observedCWD == "" {
			out.SummaryOK = false
		} else {
			out.SessionID = observedID
			out.ObservedCWD = observedCWD
			out.SummaryOK = true
		}
	}

	final, ok, updErr := verifyGrokGoalUpdates(updatesPath, sessionID, firstNonBlank(out.GoalID, expectedGoalID))
	out.UpdatesOK = ok
	out.FinalStatus = strings.TrimSpace(final.Status)
	out.FinalClassifier = strings.TrimSpace(final.Classifier)
	if errors.Is(updErr, errGoalSyncRejected) {
		return out, updErr
	}
	if updErr != nil {
		out.Contradictory = true
		out.UpdatesOK = false
		return out, updErr
	}
	if out.SessionID == "" {
		out.SessionID = final.SessionID
	} else if final.SessionID != "" && final.SessionID != out.SessionID {
		return out, fmt.Errorf("%w: other session", errGoalSyncRejected)
	}
	if expectedGoalID != "" && out.GoalID != "" && out.GoalID != expectedGoalID {
		return out, fmt.Errorf("%w: other goal_id", errGoalSyncRejected)
	}
	if final.GoalID != "" && out.GoalID != "" && final.GoalID != out.GoalID {
		out.Contradictory = true
		return out, fmt.Errorf("%w: update goal_id contradicts state", errGoalSyncRejected)
	}
	if strings.TrimSpace(final.Status) == "" || strings.TrimSpace(final.Status) != out.NativeStatus {
		out.Contradictory = true
		out.UpdatesOK = false
		return out, fmt.Errorf("final goal_updated status %q contradicts state %q", final.Status, out.NativeStatus)
	}
	if strings.TrimSpace(final.Classifier) == "" && out.NativeStatus == "complete" {
		out.Contradictory = true
		out.UpdatesOK = false
		return out, fmt.Errorf("final goal_updated missing last_classifier_verdict")
	}
	if strings.TrimSpace(final.Classifier) != "" && !strings.EqualFold(final.Classifier, out.Classifier) {
		out.Contradictory = true
		return out, fmt.Errorf("final goal_updated verdict %q contradicts state %q", final.Classifier, out.Classifier)
	}
	return out, nil
}

func normalizeNativeStatus(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

type grokFinalGoalUpdate struct {
	SessionID  string
	GoalID     string
	Status     string
	Classifier string
}

func verifyGrokGoalUpdates(path, sessionID, goalID string) (grokFinalGoalUpdate, bool, error) {
	var final grokFinalGoalUpdate
	f, err := os.Open(path)
	if err != nil {
		return final, false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	matched := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec grokGoalUpdateLine
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return final, false, fmt.Errorf("malformed updates.jsonl: %w", err)
		}
		if rec.Params.Update.SessionUpdate != "goal_updated" {
			continue
		}
		if rec.Params.SessionID != sessionID {
			return final, false, fmt.Errorf("%w: other session", errGoalSyncRejected)
		}
		updGoal := strings.TrimSpace(rec.Params.Update.GoalID)
		if goalID != "" && updGoal != "" && updGoal != goalID {
			continue
		}
		if updGoal == "" && goalID != "" {
			return final, false, fmt.Errorf("updates.jsonl missing params.update.goal_id")
		}
		matched = true
		final = grokFinalGoalUpdate{
			SessionID:  rec.Params.SessionID,
			GoalID:     updGoal,
			Status:     rec.Params.Update.Status,
			Classifier: rec.Params.Update.LastClassifierVerdict,
		}
	}
	if err := sc.Err(); err != nil {
		return final, false, err
	}
	if !matched {
		return final, false, fmt.Errorf("no goal_updated update for this session+goal")
	}
	return final, true, nil
}

func mapNativeGoalToTask(obs nativeGoalObservation, custodyReleased, cancelRequested, stopEvidence bool) mappedGoal {
	if obs.Missing || obs.Contradictory || !obs.UpdatesOK {
		return mappedGoal{
			Status:      statusHeld,
			Observation: goalObsUnknown,
			Note:        "missing, wrong-session/goal, missing-terminal, or contradictory native state stays held unknown",
		}
	}
	if obs.BudgetLimited {
		return mappedGoal{
			Status:      statusHeld,
			Observation: goalObsUnknown,
			Note:        "budget_limited is a nonaccepted provider result; not inferred complete",
		}
	}
	st := normalizeNativeStatus(obs.NativeStatus)
	switch st {
	case "active":
		return mappedGoal{Status: statusRunning, Observation: goalObsRunning, Note: "matched native active"}
	case "paused", "needs-input":
		return mappedGoal{
			Status:       statusHeld,
			Observation:  goalObsHeld,
			Continuation: "same-session",
			Note:         "verified paused/needs-input; same-session explicit continuation; active pause/resume is unverified",
		}
	case "failed":
		if custodyReleased {
			return mappedGoal{Status: statusFailed, Observation: goalObsFailed, AcceptFailed: true, Note: "true failure with custody"}
		}
		return mappedGoal{Status: statusHeld, Observation: goalObsUnknown, Note: "native failed but custody not released"}
	case "complete":
		if obs.NotAchieved || !strings.EqualFold(obs.Classifier, "achieved") {
			return mappedGoal{
				Status:      statusHeld,
				Observation: goalObsUnknown,
				Note:        "complete with last_classifier_verdict!=achieved is not accepted done; not_achieved is not budget_limited",
			}
		}
		if !strings.EqualFold(obs.FinalStatus, "complete") || !strings.EqualFold(obs.FinalClassifier, "achieved") {
			return mappedGoal{
				Status:      statusHeld,
				Observation: goalObsUnknown,
				Note:        "final current-goal update must carry status=complete and last_classifier_verdict=achieved",
			}
		}
		if cancelRequested && !stopEvidence {
			return mappedGoal{Status: statusHeld, Observation: goalObsUnknown, Note: "cancel without stop evidence; not accepted done"}
		}
		if !custodyReleased {
			return mappedGoal{
				Status:      statusHeld,
				Observation: goalObsUnknown,
				Note:        "native completed observed but TUI/descendants/lease still live; observation only, not accepted done",
			}
		}
		return mappedGoal{Status: statusDone, Observation: goalObsDone, AcceptDone: true, Note: "verified completed with last_classifier_verdict=achieved, matching current goal update, and custody released"}
	case "term", "lost", "interrupted", "unknown", "":
		return mappedGoal{Status: statusHeld, Observation: goalObsUnknown, Note: "TERM/lost/missing terminal stays held unknown"}
	default:
		return mappedGoal{Status: statusHeld, Observation: goalObsUnknown, Note: "unknown native status stays held unknown"}
	}
}

func applyHeldUnknown(root string, t *Task, syncedRev int64, note string) (*Task, error) {
	t.Goal.Observation = goalObsUnknown
	t.Goal.ObservationNote = note
	t.Goal.SyncedRevision = syncedRev
	t.Status = statusHeld
	revokeScheduling(t)
	t.touch()
	if err := saveTask(root, t); err != nil {
		return t, err
	}
	return t, nil
}

func markGoalCancelRequested(t *Task) {
	if t == nil || t.Goal == nil {
		return
	}
	t.Goal.CancelRequested = true
	if t.Goal.Observation == goalObsRunning {
		t.Goal.Observation = goalObsUnknown
		t.Goal.ObservationNote = "cancel revoked eligibility before provider stop"
	}
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || ra == "" {
		ra = filepath.Clean(a)
	}
	if errB != nil || rb == "" {
		rb = filepath.Clean(b)
	}
	return ra == rb
}

func applyWorkflowDesignResult(root string, cfg *Config, wf *WorkflowRecord, decision, observation, receiptPath string) (*Task, error) {
	decision = strings.TrimSpace(decision)
	observation = strings.TrimSpace(observation)
	if strings.TrimSpace(receiptPath) == "" {
		return nil, fmt.Errorf("%w: a fresh independent design receipt is required", errGoalDesignResult)
	}
	switch decision {
	case goalDecisionStop, goalDecisionInput, goalDecisionSuccessor, goalDecisionAccept, goalDecisionRevise:
	default:
		return nil, fmt.Errorf("%w: decision must be stop|input|successor|accept|revise", errGoalDesignResult)
	}
	var out *Task
	err := withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		t, err := applyWorkflowDesignResultLocked(root, cfg, wf, decision, observation, receiptPath)
		out = t
		return err
	})
	return out, err
}

func applyWorkflowDesignResultLocked(root string, cfg *Config, wf *WorkflowRecord, decision, observation, receiptPath string) (*Task, error) {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return nil, err
	}
	fresh, err := loadExternalDesignReceipt(receiptPath)
	if err != nil {
		return nil, err
	}
	if err := designProofComplete(fresh); err != nil {
		return nil, err
	}
	t, err := workflowWriterTask(root, wf)
	if err != nil {
		return nil, err
	}
	if t.Goal == nil {
		return nil, fmt.Errorf("%w: stage is not a Goal writer", errGoalDesignResult)
	}
	currentObs := t.Goal.Observation
	native := t.Goal.LastNativeStatus
	if observation == "" {
		if t.Status == statusDone || currentObs == goalObsDone {
			observation = "complete"
		} else if native != "" {
			observation = native
		} else {
			observation = currentObs
		}
	}
	if !stageObservationMatches(t, observation) {
		return nil, fmt.Errorf("%w: stage observation %s/%s does not match %s", errGoalDesignResult, currentObs, native, observation)
	}
	if fresh.ConsumedTaskID != t.ID || fresh.ConsumedSessionID != t.SessionID ||
		fresh.ConsumedAttemptID != goalAttemptID(t) || fresh.ConsumedRevision != t.Revision {
		return nil, fmt.Errorf("%w: fresh design result must consume exact current task/session/attempt/revision", errGoalDesignResult)
	}
	if strings.TrimSpace(fresh.ConsumedObservation) != "" && fresh.ConsumedObservation != observation &&
		fresh.ConsumedObservation != currentObs && fresh.ConsumedObservation != native {
		return nil, fmt.Errorf("%w: fresh design result observation does not match the current stage", errGoalDesignResult)
	}
	if wf.DesignLineage != nil && wf.DesignLineage.LastConsumedDigest == fresh.Digest &&
		wf.DesignLineage.LastConsumedTaskID == t.ID && wf.DesignLineage.LastConsumedRevision == t.Revision {
		return nil, fmt.Errorf("%w: duplicate/stale design result", errGoalDesignResult)
	}

	now := time.Now().Format(time.RFC3339)
	if wf.DesignLineage == nil {
		wf.DesignLineage = &WorkflowDesignLineage{}
	}
	fresh.Provenance = goalDesignProvenanceExt
	wf.DesignLineage.LatestValid = fresh
	wf.DesignLineage.LastResultDecision = decision
	wf.DesignLineage.LastConsumedTaskID = t.ID
	wf.DesignLineage.LastConsumedObservation = observation
	wf.DesignLineage.LastConsumedNativeStatus = native
	wf.DesignLineage.LastConsumedDigest = fresh.Digest
	wf.DesignLineage.LastConsumedRevision = t.Revision
	wf.DesignLineage.LastConsumedAt = now

	doneStage := t.Status == statusDone || currentObs == goalObsDone
	switch decision {
	case goalDecisionAccept:
		if !doneStage {
			return nil, fmt.Errorf("%w: accept requires a completed native stage", errGoalDesignResult)
		}
		if err := persistWorkflow(root, cfg, wf); err != nil {
			return t, err
		}
		return t, nil
	case goalDecisionStop:
		wf.Status = workflowStatusOwnerChoice
		if err := persistWorkflow(root, cfg, wf); err != nil {
			return t, err
		}
		return t, nil
	case goalDecisionInput:
		if doneStage {
			return nil, fmt.Errorf("%w: completed stage does not take input; use accept|revise|stop", errGoalDesignResult)
		}
		t.Goal.ObservationNote = "design-result requests operator input; stage stays held; not done"
		t.touch()
		if err := saveTask(root, t); err != nil {
			return t, err
		}
		return t, persistWorkflow(root, cfg, wf)
	default: // successor or revise
		if observation == "paused" || observation == "needs-input" {
			return nil, fmt.Errorf("%w: paused/needs-input uses same-session goal-run, not a successor writer", errGoalDesignResult)
		}
		if doneStage && decision == goalDecisionSuccessor {
			return nil, fmt.Errorf("%w: completed stage uses accept|revise|stop, not successor", errGoalDesignResult)
		}
		if wf.CurrentRound >= wf.MaxRounds {
			wf.Status = workflowStatusExhausted
			if err := persistWorkflow(root, cfg, wf); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %d/%d", errWorkflowRoundsExceeded, wf.CurrentRound, wf.MaxRounds)
		}
		if !doneStage && t.Status != statusFailed {
			if !goalCustodyReleased(root, t) {
				return nil, fmt.Errorf("%w: cannot open a successor while custody is held", errGoalDesignResult)
			}
			if t.ActiveAttemptID == "" && t.Goal.BoundAttemptID != "" {
				t.ActiveAttemptID = t.Goal.BoundAttemptID
			}
			if t.Status == statusHeld {
				restoreScheduling(t)
				t.Status = statusQueued
				if err := commitTaskTransition(root, t, transitionRequest{
					EventType:            evQueued,
					Actor:                "workflow:design-result",
					Status:               statusQueued,
					RequireSchedulerLock: false,
					Detail:               map[string]any{"reason": "retire-nonaccepted-for-successor"},
				}); err != nil {
					return nil, err
				}
			}
			t.Status = statusFailed
			t.Goal.Observation = goalObsFailed
			t.Goal.ObservationNote = "nonaccepted stage retired for one Goal-bound successor; not manufactured done"
			if err := commitTaskTransition(root, t, transitionRequest{
				EventType:            evFailed,
				Actor:                "workflow:design-result",
				Status:               statusFailed,
				RequireSchedulerLock: false,
				Detail:               map[string]any{"observation": observation, "decision": decision},
			}); err != nil {
				return nil, err
			}
		}
		wf.CurrentRound++
		wf.Candidate = nil
		wf.ReviewerTaskID = ""
		wf.Review = nil
		next, err := publishWorkflowWriterLocked(root, cfg, wf, "", copyGoalContract(t.Goal), true)
		if err != nil {
			return nil, err
		}
		return next, nil
	}
}

func stageObservationMatches(t *Task, observation string) bool {
	if t == nil || t.Goal == nil {
		return false
	}
	currentObs := t.Goal.Observation
	native := t.Goal.LastNativeStatus
	switch observation {
	case "complete", goalObsDone:
		return t.Status == statusDone || currentObs == goalObsDone
	case goalObsFailed:
		return currentObs == goalObsFailed || t.Status == statusFailed
	case "paused", "needs-input":
		return currentObs == goalObsHeld && t.Goal.Continuation == "same-session"
	case "budget_limited":
		return native == "budget_limited"
	default:
		return observation == currentObs || observation == native
	}
}
