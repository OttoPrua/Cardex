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

	goalStopSoftBudget = "provider budget is soft; existing step timeout is the hard deadline"
)

var (
	errGoalSyncRejected      = errors.New("workflow goal-sync rejected")
	errGoalRedispatchBlocked = errors.New("workflow goal redispatch blocked")
	errGoalManualRequired    = errors.New("workflow goal-run requires -manual or -hosted")
	errGoalCapability        = errors.New("workflow native-goal capability")
	errGoalLaunchUnstarted   = errors.New("workflow goal launch did not start")
	errGoalDesignProof       = errors.New("workflow goal design proof")
	errGoalDesignResult      = errors.New("workflow goal design-result")
	errGoalAttemptRequired   = errors.New("workflow goal requires the exact existing attempt")
	errGoalUnsupportedTuple  = errors.New("workflow goal unsupported execution tuple")
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
	Role                string   `json:"role"`
	Model               string   `json:"model,omitempty"`
	Runner              string   `json:"runner,omitempty"`
	ActualModel         string   `json:"actual_model,omitempty"`
	ActualRunner        string   `json:"actual_runner,omitempty"`
	Identity            string   `json:"identity,omitempty"`
	ReadOnly            bool     `json:"read_only"`
	At                  string   `json:"at,omitempty"`
	ConsumedCommit      string   `json:"consumed_commit,omitempty"`
	ConsumedTree        string   `json:"consumed_tree,omitempty"`
	ConsumedReviewTask  string   `json:"consumed_review_task,omitempty"`
	ConsumedTaskID      string   `json:"consumed_task_id,omitempty"`
	ConsumedSessionID   string   `json:"consumed_session_id,omitempty"`
	ConsumedAttemptID   string   `json:"consumed_attempt_id,omitempty"`
	ConsumedObservation string   `json:"consumed_observation,omitempty"`
	ConsumedRevision    int64    `json:"consumed_revision,omitempty"`
	DesignTaskID        string   `json:"design_task_id,omitempty"`
	DesignSessionID     string   `json:"design_session_id,omitempty"`
	ReceiptPath         string   `json:"receipt_path,omitempty"`
	ResultPath          string   `json:"result_path,omitempty"`
	Digest              string   `json:"digest,omitempty"`
	InputIdentity       string   `json:"input_identity,omitempty"`
	Provenance          string   `json:"provenance,omitempty"`
	AddWritePaths       []string `json:"add_write_paths,omitempty"`
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
	Hosted            bool   `json:"hosted,omitempty"`
	External          bool   `json:"external,omitempty"`
	ControlOwner      string `json:"control_owner,omitempty"`
	LastControl       string `json:"last_control,omitempty"`
	LastControlAt     string `json:"last_control_at,omitempty"`
	ControlConfirmed  bool   `json:"control_confirmed,omitempty"`
	FailureClass      string `json:"failure_class,omitempty"`
	PauseMessage      string `json:"pause_message,omitempty"`
	GrokHome          string `json:"grok_home,omitempty"`
	// LaunchSandboxSelector is invocation-scoped and never persisted.
	LaunchSandboxSelector string `json:"-"`
	// RecoveryOriginalAttemptID and LaunchRemainingTimeout are invocation-scoped
	// bootstrap-before-native recovery fields and are never persisted.
	RecoveryOriginalAttemptID string        `json:"-"`
	LaunchRemainingTimeout    time.Duration `json:"-"`
	SandboxProfile            string        `json:"sandbox_profile,omitempty"`
	SandboxProfileDigest      string        `json:"sandbox_profile_digest,omitempty"`
	GitCommonDir              string        `json:"git_common_dir,omitempty"`
	CardexRoot                string        `json:"cardex_root,omitempty"`
	SandboxWorktree           string        `json:"sandbox_worktree,omitempty"`
	LauncherSandbox           string        `json:"launcher_sandbox,omitempty"`
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
// fields mean "use the live task". An empty TaskID keeps the serial default:
// the workflow writer. A native direction is selected only by TaskID, never by
// rewriting WriterTaskID.
type GoalSyncRequest struct {
	TaskID           string
	ExpectedRevision int64
	ExpectedSession  string
	ExpectedGoalID   string
	ExpectedAttempt  string
	GrokHome         string
	GrokCWD          string
}

type workflowGoalView struct {
	TaskID      string        `json:"task_id"`
	Status      string        `json:"status"`
	SessionID   string        `json:"session_id"`
	GoalID      string        `json:"goal_id"`
	AttemptID   string        `json:"attempt_id"`
	Observation string        `json:"observation"`
	WriterMode  string        `json:"writer_mode"`
	Revision    int64         `json:"revision"`
	RawUsage    *taskRawUsage `json:"raw_usage,omitempty"`
}

type grokNativeGoalStateFile struct {
	GoalID                string          `json:"goal_id"`
	Status                string          `json:"status"`
	Phase                 string          `json:"phase"`
	TokenBudget           int64           `json:"token_budget"`
	LastClassifierVerdict string          `json:"last_classifier_verdict"`
	TotalVerifyRounds     int             `json:"total_verify_rounds"`
	TotalWorkerRounds     int             `json:"total_worker_rounds"`
	PauseMessage          string          `json:"pause_message,omitempty"`
	History               []grokGoalEvent `json:"history"`
	TokensUsedHighWater   json.RawMessage `json:"tokens_used_high_water,omitempty"`
	ElapsedMS             json.RawMessage `json:"elapsed_ms,omitempty"`
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
	AddWritePaths       []string             `json:"add_write_paths,omitempty"`
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
			Reason:       "Grok TUI native /goal is proven for complete and same-session restart. Cardex-hosted PTY injects a literal /goal on the master. Active pause/resume is offered only for hosted Goals after native post-state confirms; write-to-slave, exit 0, stale active, and shell kill are not pause. grok -p/--single is a single turn and is not goal proof.",
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
	role := strings.ToLower(strings.TrimSpace(node.Role))
	if role == "" {
		role = goalDesignRole
	}
	if node.Role != role {
		fmt.Fprintf(os.Stderr, "warning: design role %q normalized to %q\n", node.Role, role)
		node.Role = role
	}
	if node.Role != goalDesignRole {
		return fmt.Errorf("%w: design node role must be %q", errWorkflowMalformed, goalDesignRole)
	}
	if !node.ReadOnly {
		return fmt.Errorf("%w: design node must already be read-only; a false claim is not rewritten into acceptance", errGoalDesignProof)
	}
	return nil
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
	if strings.TrimSpace(node.ActualModel) == "" || strings.TrimSpace(node.ActualRunner) == "" {
		fmt.Fprintln(os.Stderr, "warning: design model/runner metadata is incomplete; preserving reported identity without guessing")
	}
	if strings.TrimSpace(node.Identity) == "" && strings.TrimSpace(node.DesignSessionID) == "" && strings.TrimSpace(node.DesignTaskID) == "" {
		fmt.Fprintln(os.Stderr, "warning: design actor/context metadata is missing; independence remains caller-attested, not inferred from a model name")
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

// Independence is about producers, not model diversity. Compare the producer
// coordinates we actually have; never infer a producer from a model label.
func designProducerDistinct(node *WorkflowDesignNode, writer *Task) error {
	if node == nil || writer == nil {
		return nil
	}
	actor, session := strings.TrimSpace(node.Identity), strings.TrimSpace(node.DesignSessionID)
	writerSession := strings.TrimSpace(writer.SessionID)
	if strings.TrimSpace(node.DesignTaskID) == writer.ID || actor == writer.ID ||
		(writerSession != "" && (session == writerSession || actor == writerSession)) {
		return fmt.Errorf("%w: design and writer must have distinct actor/context identities", errGoalDesignProof)
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
	if wf.WriterTaskID != "" {
		writer, err := workflowWriterTask(root, wf)
		if err != nil {
			return err
		}
		if err := designProducerDistinct(node, writer); err != nil {
			return err
		}
	}
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
		if fresh.ActualModel != node.ActualModel || fresh.ActualRunner != node.ActualRunner ||
			fresh.Identity != node.Identity || fresh.DesignSessionID != node.DesignSessionID {
			return fmt.Errorf("%w: design producer identity drifted since bind", errGoalDesignProof)
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
		if again.ActualModel != node.ActualModel || again.ActualRunner != node.ActualRunner ||
			(node.DesignSessionID != "" && again.DesignSessionID != node.DesignSessionID) {
			return fmt.Errorf("%w: design task producer identity drifted since bind", errGoalDesignProof)
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
	if req.Model != "" || req.Runner != "" || req.ActualModel != "" || req.ActualRunner != "" || req.Identity != "" {
		fmt.Fprintln(os.Stderr, "warning: design receipt/task identity is authoritative; CLI identity labels are ignored")
	}
	wf.DesignLineage = &WorkflowDesignLineage{Initial: node, LatestValid: node, RepairCount: 0}
	return nil
}

func designProofIdentityEqual(a, b *WorkflowDesignNode) bool {
	if a == nil || b == nil {
		return a == b
	}
	return strings.EqualFold(a.Digest, b.Digest) &&
		a.InputIdentity == b.InputIdentity &&
		a.ActualModel == b.ActualModel &&
		a.ActualRunner == b.ActualRunner &&
		a.Identity == b.Identity &&
		a.DesignSessionID == b.DesignSessionID &&
		a.DesignTaskID == b.DesignTaskID &&
		a.Provenance == b.Provenance
}

func workflowBoundInitialDesign(wf *WorkflowRecord) *WorkflowDesignNode {
	if wf == nil || wf.DesignLineage == nil {
		return nil
	}
	if wf.DesignLineage.LatestValid != nil {
		return wf.DesignLineage.LatestValid
	}
	return wf.DesignLineage.Initial
}

func workflowLateInitialDesignBindBlocked(wf *WorkflowRecord) error {
	if wf == nil {
		return errWorkflowMalformed
	}
	if wf.Status != workflowStatusDesign {
		return fmt.Errorf("%w: design-bind requires unstarted design-stage workflow, status is %s", errWorkflowMalformed, wf.Status)
	}
	if strings.TrimSpace(wf.WriterTaskID) != "" {
		return fmt.Errorf("%w: design-bind refuses replacement of existing writer", errWorkflowMalformed)
	}
	if strings.TrimSpace(wf.ReviewerTaskID) != "" {
		return fmt.Errorf("%w: design-bind refuses replacement of existing reviewer", errWorkflowMalformed)
	}
	if len(wf.DirectionTaskIDs) > 0 || len(wf.NativeGoalTaskIDs) > 0 {
		return fmt.Errorf("%w: design-bind refuses replacement of existing directions", errWorkflowMalformed)
	}
	if strings.TrimSpace(wf.AcceptanceTaskID) != "" || wf.GoalCompleted {
		return fmt.Errorf("%w: design-bind refuses replacement of existing acceptance", errWorkflowMalformed)
	}
	if wf.Candidate != nil || wf.Review != nil {
		return fmt.Errorf("%w: design-bind refuses replacement of existing contract", errWorkflowMalformed)
	}
	if wf.CurrentRound != 0 {
		return fmt.Errorf("%w: design-bind refuses started execution (round %d)", errWorkflowMalformed, wf.CurrentRound)
	}
	return nil
}

func bindWorkflowInitialDesign(root string, cfg *Config, wf *WorkflowRecord, receiptPath, designTaskID string) error {
	if wf == nil {
		return errWorkflowMalformed
	}
	receiptPath = strings.TrimSpace(receiptPath)
	designTaskID = strings.TrimSpace(designTaskID)
	if (receiptPath == "") == (designTaskID == "") {
		return fmt.Errorf("%w: exactly one of -design-receipt or -design-task is required", errGoalDesignProof)
	}
	return withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		return bindWorkflowInitialDesignLocked(root, cfg, wf, receiptPath, designTaskID)
	})
}

func bindWorkflowInitialDesignLocked(root string, cfg *Config, wf *WorkflowRecord, receiptPath, designTaskID string) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	if err := workflowLateInitialDesignBindBlocked(wf); err != nil {
		return err
	}
	node, err := loadLateInitialDesignProof(root, receiptPath, designTaskID)
	if err != nil {
		return err
	}
	if err := designProofComplete(node); err != nil {
		return err
	}
	if existing := workflowBoundInitialDesign(wf); existing != nil {
		if designProofIdentityEqual(existing, node) {
			fmt.Fprintln(os.Stderr, "warning: design-bind proof already bound; no state change")
			return nil
		}
		return fmt.Errorf("%w: existing design proof conflicts; identical rebind is the only no-op", errGoalDesignProof)
	}
	wf.DesignLineage = &WorkflowDesignLineage{Initial: node, LatestValid: node, RepairCount: 0}
	return persistWorkflow(root, cfg, wf)
}

func loadLateInitialDesignProof(root, receiptPath, designTaskID string) (*WorkflowDesignNode, error) {
	if strings.TrimSpace(receiptPath) != "" {
		return loadExternalDesignReceipt(receiptPath)
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: design task bind needs root", errGoalDesignProof)
	}
	return loadCompletedDesignTask(root, designTaskID)
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
	resultPath := strings.TrimSpace(rec.ResultPath)
	if resultPath == "" {
		return nil, fmt.Errorf("%w: receipt has no result artifact; identity metadata is not a design result", errGoalDesignProof)
	}
	resultRaw, err := os.ReadFile(resultPath)
	if err != nil {
		return nil, fmt.Errorf("%w: result artifact: %v", errGoalDesignProof, err)
	}
	if len(bytes.TrimSpace(resultRaw)) == 0 {
		return nil, fmt.Errorf("%w: design result artifact is empty", errGoalDesignProof)
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
		var parts []string
		for _, in := range rec.Inputs {
			parts = append(parts, strings.ToLower(strings.TrimSpace(in.SHA256)))
		}
		inputID = strings.Join(parts, "|")
		fmt.Fprintln(os.Stderr, "warning: design input_identity derived from verified input digests")
	}
	actualModel := firstNonBlank(rec.ActualModel, rec.Model)
	actualRunner := firstNonBlank(rec.ActualRunner, rec.Runner)
	if rec.ActualModel == "" || rec.ActualRunner == "" {
		fmt.Fprintln(os.Stderr, "warning: design receipt uses legacy model/runner fields for reported execution identity")
	}
	if (rec.Model != "" && rec.Model != actualModel) || (rec.Runner != "" && rec.Runner != actualRunner) {
		fmt.Fprintln(os.Stderr, "warning: design display model/runner differs from actual identity; preserving actual identity")
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
	addPaths, err := bindDesignReceiptAddWritePaths(rec.AddWritePaths, resultRaw)
	if err != nil {
		return nil, err
	}
	node.AddWritePaths = addPaths
	return node, nil
}

func bindDesignReceiptAddWritePaths(raw []string, resultRaw []byte) ([]string, error) {
	receiptPaths, err := uniqueTrimmedAddWritePaths(raw)
	if err != nil {
		return nil, err
	}
	if len(receiptPaths) == 0 {
		return nil, nil
	}
	declared, ok := declaredAddWritePathsJSONArray(resultRaw)
	if !ok || !equalStringSlice(declared, receiptPaths) {
		return nil, fmt.Errorf("%w: add_write_paths is not bound to the result digest", errGoalDesignProof)
	}
	return receiptPaths, nil
}

func uniqueTrimmedAddWritePaths(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, item := range raw {
		p := strings.TrimSpace(item)
		if p == "" {
			return nil, fmt.Errorf("%w: add_write_paths contains an empty path", errGoalDesignProof)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// declaredAddWritePathsJSONArray reads an explicit add_write_paths JSON array
// from the digest-verified result. A path string merely mentioned in the
// artifact is not a declaration.
func declaredAddWritePathsJSONArray(resultRaw []byte) ([]string, bool) {
	var paths []string
	if !extractAddWritePathsJSONArray(resultRaw, &paths) {
		return nil, false
	}
	out, err := uniqueTrimmedAddWritePaths(paths)
	if err != nil {
		return nil, false
	}
	return out, true
}

func extractAddWritePathsJSONArray(raw []byte, dest *[]string) bool {
	if dest == nil {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimSpace(raw), &obj) == nil {
		return decodeAddWritePathsJSONArray(obj, dest)
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '{' {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw[i:]))
		obj = nil
		if err := dec.Decode(&obj); err != nil {
			continue
		}
		if decodeAddWritePathsJSONArray(obj, dest) {
			return true
		}
	}
	return false
}

func decodeAddWritePathsJSONArray(obj map[string]json.RawMessage, dest *[]string) bool {
	field, ok := obj["add_write_paths"]
	if !ok {
		return false
	}
	trim := bytes.TrimSpace(field)
	if len(trim) == 0 || trim[0] != '[' {
		return false
	}
	var paths []string
	if err := json.Unmarshal(field, &paths); err != nil {
		return false
	}
	*dest = paths
	return true
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
		case "model":
			rec.Model = val
		case "runner":
			rec.Runner = val
		case "actual_runner":
			rec.ActualRunner = val
		case "actual_agent_identity":
			rec.Identity = val
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
		case "add_write_paths":
			rec.AddWritePaths = splitComma(val)
		}
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
		Role:            goalDesignRole,
		Model:           model,
		Runner:          runner,
		ActualModel:     model,
		ActualRunner:    runner,
		ReadOnly:        true,
		At:              firstNonBlank(t.UpdatedAt, time.Now().Format(time.RFC3339)),
		DesignTaskID:    t.ID,
		DesignSessionID: t.SessionID,
		Digest:          hex.EncodeToString(sum[:]),
		InputIdentity:   inputID,
		Provenance:      goalDesignProvenanceTask,
	}
	return node, nil
}

func bindWorkflowDesignRepair(root string, cfg *Config, wf *WorkflowRecord, receiptPath string) error {
	return withTaskControlLock(root, "workflow-admit:"+wf.ID, func() error {
		return bindWorkflowDesignRepairLocked(root, cfg, wf, receiptPath)
	})
}

func bindWorkflowDesignRepairLocked(root string, cfg *Config, wf *WorkflowRecord, receiptPath string) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	if wf.Candidate == nil || strings.TrimSpace(wf.Candidate.Commit) == "" || strings.TrimSpace(wf.Candidate.Tree) == "" {
		return fmt.Errorf("%w: design repair must consume the current frozen candidate", errWorkflowMalformed)
	}
	if wf.Review == nil || strings.TrimSpace(wf.Review.TaskID) == "" {
		return fmt.Errorf("%w: design repair must consume the current review results", errWorkflowMalformed)
	}
	if wf.CurrentRound >= wf.MaxRounds {
		return fmt.Errorf("%w: %d/%d", errWorkflowRoundsExceeded, wf.CurrentRound, wf.MaxRounds)
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
	writer, err := workflowWriterTask(root, wf)
	if err != nil {
		return err
	}
	if err := designProducerDistinct(node, writer); err != nil {
		return err
	}
	if wf.DesignLineage != nil && wf.DesignLineage.LatestValid != nil {
		old := wf.DesignLineage.LatestValid
		if old.Digest == node.Digest && old.InputIdentity == node.InputIdentity &&
			old.ActualModel == node.ActualModel && old.ActualRunner == node.ActualRunner &&
			old.Identity == node.Identity && old.DesignSessionID == node.DesignSessionID &&
			old.ConsumedCommit == node.ConsumedCommit && old.ConsumedTree == node.ConsumedTree &&
			old.ConsumedReviewTask == node.ConsumedReviewTask {
			fmt.Fprintln(os.Stderr, "warning: design-repair receipt already bound; no state change")
			return nil
		}
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
		engine := wf.WriterEngine
		if engine == "auto" {
			route, err := resolveWorkflowDefaultRoute(cfg, wf)
			if err != nil {
				return nil, err
			}
			engine = route.Legs[0].Runner
		}
		cap := nativeGoalCapability(engine)
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
		RawUsage:  t.RawUsage,
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

func launchManualWorkflowGoal(root string, cfg *Config, wf *WorkflowRecord, budget int64, sandboxProfile string) error {
	return launchWorkflowGoal(root, cfg, wf, budget, false, sandboxProfile)
}

func launchHostedWorkflowGoal(root string, cfg *Config, wf *WorkflowRecord, budget int64, sandboxProfile string) error {
	return launchWorkflowGoal(root, cfg, wf, budget, true, sandboxProfile)
}

func launchWorkflowGoal(root string, cfg *Config, wf *WorkflowRecord, budget int64, hosted bool, sandboxProfile string) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	t, err := workflowWriterTask(root, wf)
	if err != nil {
		return err
	}
	return launchWorkflowGoalTask(root, cfg, wf, t, budget, hosted, sandboxProfile)
}

func launchWorkflowGoalTask(root string, cfg *Config, wf *WorkflowRecord, t *Task, budget int64, hosted bool, sandboxProfile string) error {
	if t == nil {
		return fmt.Errorf("%w: missing Goal task", errWorkflowMalformed)
	}
	if t.Goal == nil {
		return fmt.Errorf("%w: admit the writer with -mode manual before goal-run", errWorkflowMalformed)
	}
	t.Goal.LaunchSandboxSelector = strings.TrimSpace(sandboxProfile)
	if t.Goal.LaunchSandboxSelector != "" {
		if _, _, _, err := resolveExistingSandboxSelector(t, t.Goal.LaunchSandboxSelector); err != nil {
			return err
		}
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
	// Freeze before admission. bindGoalSessionBeforeEffect assigns SessionID
	// only inside admission, and that id must not be present during the probe.
	if err := freezeManualGoalModel(context.Background(), root, cfg, t); err != nil {
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

	if err := admitManualGoalLaunchLocked(root, cfg, wf, t, budget, hosted); err != nil {
		return err
	}
	releaseAdmission()
	return executeAdmittedGoalLaunch(root, cfg, wf, t, hosted)
}

func abandonGoalLaunchUnstarted(root string, cfg *Config, t *Task) error {
	return withWorkflowSchedulerLock(root, cfg, func() error {
		if t != nil && t.Goal != nil && strings.TrimSpace(t.Goal.RecoveryOriginalAttemptID) != "" {
			return abandonUnstartedRecoveryAttempt(root, t)
		}
		return abandonUnstartedGoalAttempt(root, t)
	})
}

func executeAdmittedGoalLaunch(root string, cfg *Config, wf *WorkflowRecord, t *Task, hosted bool) error {
	// SessionID is bound now. Build argv from the frozen id; do not probe again.
	args, grokHome, err := manualGoalCommandArgs(cfg, t)
	if err != nil {
		_ = abandonGoalLaunchUnstarted(root, cfg, t)
		return err
	}
	if err := verifySandboxLauncherEvidence(root, t, args); err != nil {
		_ = abandonGoalLaunchUnstarted(root, cfg, t)
		return err
	}

	if hook := goalLaunchBeforeStartHook; hook != nil {
		if herr := hook(); herr != nil {
			_ = abandonGoalLaunchUnstarted(root, cfg, t)
			return fmt.Errorf("%w: %v", errGoalLaunchUnstarted, herr)
		}
	}

	timeout := time.Duration(t.Goal.HardTimeoutSec) * time.Second
	if t.Goal != nil && t.Goal.LaunchRemainingTimeout > 0 {
		timeout = t.Goal.LaunchRemainingTimeout
	}
	if timeout <= 0 && cfg != nil && cfg.StepTimeoutMin > 0 {
		timeout = time.Duration(cfg.StepTimeoutMin) * time.Minute
	}
	if timeout <= 0 {
		timeout = 60 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	contractPath := filepath.Join(workflowsDir(root), wf.ID+".stage-contract.txt")
	if hosted {
		// Hosted PTY bytes are drained in runHostedGrokGoal
		// (cmd/cardex/workflow_goal_host_unix.go). Feeding that stream into
		// bootstrap-before-provider proof is an adjacent callsite outside this
		// owned write domain; missing hosted capture refuses with no invented proof.
		return runHostedGrokGoal(root, cfg, wf, t, ctx, args, grokHome, contractPath, t.Goal.InputDigest)
	}

	stdin := os.Stdin
	if goalLaunchStdin != nil {
		stdin = goalLaunchStdin
	}
	if goalLaunchStdin == nil && !fileIsTerminal(stdin) {
		t.Goal.FailureClass = goalFailPTYMissing
		_ = saveTask(root, t)
		_ = abandonGoalLaunchUnstarted(root, cfg, t)
		return fmt.Errorf("%s: interactive -manual requires a controlling TTY; use -hosted for Cardex-owned PTY", goalFailPTYMissing)
	}
	cmd := exec.CommandContext(ctx, cfg.GrokBuildBin, args...)
	cmd.Dir = t.Dir
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
	cmd.Env = providerChildEnv(home, map[string]string{"GROK_HOME": grokHome, "GROK_WORKFLOWS": "1"})
	// Interactive -manual keeps a controlling TTY stderr fd. Capture of the
	// exact short refusal is supported only when that fd is not a TTY.
	captured := attachManualGoalLaunchStderrCapture(cmd, goalLaunchStdin == nil)

	taskExecRoot.Store(t.ID, root)
	defer taskExecRoot.Delete(t.ID)

	runErr := runCmdRegisteredForTaskWorkspace(cmd, t.ID, t.Dir)
	if runErr != nil {
		class := classifyGoalLaunchError(runErr)
		t.Goal.FailureClass = class
		_ = saveTask(root, t)
		if class == goalFailPTYIoctl || class == goalFailPTYMissing {
			_ = abandonGoalLaunchUnstarted(root, cfg, t)
			return fmt.Errorf("%s: %w", class, runErr)
		}
	}
	if rec, rerr := loadRequiredGoalAttempt(root, t); rerr == nil && rec != nil {
		recordRetainedBootstrapBeforeProviderEvidence(root, t, rec, runErr, captured.capturedOutput())
	}
	restoreErr := restore()
	finalErr := withWorkflowSchedulerLock(root, cfg, func() error {
		return finalizeManualGoalLaunch(root, cfg, wf, t, ctx, runErr)
	})
	if finalErr != nil {
		return finalErr
	}
	if restoreErr != nil {
		return restoreErr
	}
	return nil
}

// bootstrapRefusalCapture copies child stderr to the original sink and keeps a
// bounded adjacent-line window: the current incomplete line plus at most one
// prior complete line, each at most grokBuildProcessStderrMaxBytes. The real
// pre-provider refusal is an adjacent warning/error pair; a complete line
// matches only as that pair or an accepted closed-set single line after trim.
// Chunk splits and CRLF are joined into the same window. Prefixed, quoted, or
// concatenated junk+marker without a line boundary is not a match. After a
// match only that closed-set pair or line is retained. It is not a session log.
type bootstrapRefusalCapture struct {
	sink           io.Writer
	window         []byte
	prevComplete   string
	pendingCut     bool
	matched        string
	ttyLeftInPlace bool
}

func (c *bootstrapRefusalCapture) Write(p []byte) (int, error) {
	if c == nil {
		return len(p), nil
	}
	n := len(p)
	var err error
	if c.sink != nil {
		n, err = c.sink.Write(p)
	}
	if c.matched != "" {
		return n, err
	}
	chunk := p
	if n >= 0 && n < len(p) {
		chunk = p[:n]
	}
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			c.appendPendingLine(chunk)
			break
		}
		c.appendPendingLine(chunk[:i])
		c.finishPendingLine()
		chunk = chunk[i+1:]
		if c.matched != "" {
			break
		}
	}
	return n, err
}

func (c *bootstrapRefusalCapture) appendPendingLine(b []byte) {
	if c == nil || c.matched != "" || c.pendingCut {
		return
	}
	max := grokBuildProcessStderrMaxBytes
	if len(c.window)+len(b) > max {
		c.pendingCut = true
		c.window = c.window[:0]
		return
	}
	c.window = append(c.window, b...)
}

func (c *bootstrapRefusalCapture) finishPendingLine() {
	if c == nil || c.matched != "" {
		return
	}
	if c.pendingCut {
		c.pendingCut = false
		c.window = c.window[:0]
		c.prevComplete = ""
		return
	}
	line := strings.TrimSuffix(string(c.window), "\r")
	c.window = c.window[:0]
	c.considerCompleteLine(line)
}

func (c *bootstrapRefusalCapture) considerCompleteLine(line string) {
	if c == nil || c.matched != "" {
		return
	}
	if c.prevComplete != "" {
		if matched, ok := matchAcceptedBootstrapBeforeProviderRefusal(c.prevComplete + "\n" + line); ok {
			c.matched = matched
			c.prevComplete = ""
			c.window = nil
			return
		}
	}
	if matched, ok := matchAcceptedBootstrapBeforeProviderRefusal(line); ok {
		c.matched = matched
		c.prevComplete = ""
		c.window = nil
		return
	}
	c.prevComplete = line
}

func (c *bootstrapRefusalCapture) capturedOutput() string {
	if c == nil {
		return ""
	}
	if c.matched != "" {
		return c.matched
	}
	if c.pendingCut {
		return ""
	}
	line := strings.TrimSuffix(string(c.window), "\r")
	if c.prevComplete != "" && line != "" {
		if matched, ok := matchAcceptedBootstrapBeforeProviderRefusal(c.prevComplete + "\n" + line); ok {
			c.matched = matched
			c.prevComplete = ""
			c.window = nil
			return matched
		}
	}
	if line != "" {
		if matched, ok := matchAcceptedBootstrapBeforeProviderRefusal(line); ok {
			c.matched = matched
			c.prevComplete = ""
			c.window = nil
			return matched
		}
	}
	if c.prevComplete != "" && line == "" {
		if matched, ok := matchAcceptedBootstrapBeforeProviderRefusal(c.prevComplete); ok {
			c.matched = matched
			c.prevComplete = ""
			c.window = nil
			return matched
		}
	}
	if line != "" {
		return line
	}
	return c.prevComplete
}

func (c *bootstrapRefusalCapture) preservedTTYStderr() bool {
	return c != nil && c.ttyLeftInPlace
}

// attachManualGoalLaunchStderrCapture leaves a controlling TTY *os.File on
// stderr in place so exec inherits that fd. Wrapping it would replace the
// TTY with an os/exec pipe (isatty(2) false). Interactive -manual TUI
// capture is unsupported: that launch does not produce retained proof.
// Capture of the exact short refusal wraps stderr only when it is not a
// TTY, or when this is the headless/test path (goalLaunchStdin set).
// Stdin and stdout stay as the caller set them. Child stderr is the
// original TTY fd when left in place, and an exec pipe when wrapped.
func attachManualGoalLaunchStderrCapture(cmd *exec.Cmd, preserveTTYStderr bool) *bootstrapRefusalCapture {
	captured := &bootstrapRefusalCapture{}
	if cmd == nil {
		captured.sink = io.Discard
		return captured
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	sink, ok := cmd.Stderr.(io.Writer)
	if !ok || sink == nil {
		sink = os.Stderr
		cmd.Stderr = sink
	}
	captured.sink = sink
	if preserveTTYStderr {
		if f, isFile := cmd.Stderr.(*os.File); isFile && fileIsTerminal(f) {
			captured.ttyLeftInPlace = true
			return captured
		}
	}
	cmd.Stderr = captured
	return captured
}

func restoreLaunchSandboxSelector(t *Task, selector string) {
	if t != nil && t.Goal != nil {
		t.Goal.LaunchSandboxSelector = strings.TrimSpace(selector)
	}
}

func admitManualGoalLaunchLocked(root string, cfg *Config, wf *WorkflowRecord, t *Task, budget int64, hosted bool) error {
	selector := goalLaunchSandboxSelector(t)
	fresh, err := loadTask(root, t.ID)
	if err != nil {
		return err
	}
	*t = *fresh
	if t.Goal == nil {
		return fmt.Errorf("%w: admit the writer with -mode manual before goal-run", errWorkflowMalformed)
	}
	restoreLaunchSandboxSelector(t, selector)
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
	restoreLaunchSandboxSelector(t, selector)
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
		restoreLaunchSandboxSelector(t, selector)
	} else if t.ActiveAttemptID != "" {
		if err := abandonUnstartedGoalAttempt(root, t); err != nil {
			return err
		}
		reloaded, err := loadTask(root, t.ID)
		if err != nil {
			return err
		}
		*t = *reloaded
		restoreLaunchSandboxSelector(t, selector)
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

	if err := bindGoalSandboxEvidence(root, cfg, t); err != nil {
		return err
	}
	if strings.TrimSpace(t.Goal.GrokHome) == "" {
		t.Goal.GrokHome = defaultGrokHome()
	}

	contract := frozenGoalStageContract(wf, t)
	digest := sha256Hex(contract)
	t.Goal.InputDigest = digest
	if err := writeGoalStageContract(root, wf.ID, contract); err != nil {
		return err
	}
	t.Status = statusRunning
	t.Goal.Observation = goalObsRunning
	if hosted {
		t.Goal.Hosted = true
		t.Goal.ControlOwner = goalControlOwnerHosted
		t.Goal.ObservationNote = "cardex-hosted PTY will inject literal /goal on the master"
	} else {
		t.Goal.ControlOwner = goalControlOwnerInteractive
		t.Goal.ObservationNote = "manual Grok TUI; /goal is typed by the operator; pause is not claimed from stdin write"
	}
	printManualGoalInstructions(root, wf, t, contract, digest)
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

func finalizeManualGoalLaunch(root string, cfg *Config, wf *WorkflowRecord, t *Task, ctx context.Context, runErr error) error {
	// Reload drops an unsaved in-memory home. Keep the home and cwd the child
	// actually used so sync does not read a different session tree.
	childHome, childDir := goalChildHomeAndDir(t)
	origAttempt := ""
	remaining := time.Duration(0)
	if t != nil && t.Goal != nil {
		origAttempt = t.Goal.RecoveryOriginalAttemptID
		remaining = t.Goal.LaunchRemainingTimeout
	}
	reloaded, loadErr := loadTask(root, t.ID)
	if loadErr == nil && reloaded != nil {
		*t = *reloaded
	}
	if t.Goal == nil {
		t.Goal = &TaskGoalBinding{WriterMode: goalWriterManual}
	}
	t.Goal.RecoveryOriginalAttemptID = origAttempt
	t.Goal.LaunchRemainingTimeout = remaining

	started := goalAttemptStarted(root, t)
	if origAttempt != "" {
		if !started {
			if aerr := abandonUnstartedRecoveryAttempt(root, t); aerr != nil {
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
		t.Goal.Started = true
	} else {
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
	}

	timedOut := ctx != nil && ctx.Err() == context.DeadlineExceeded
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
	// BoundAttemptID and the frozen InputDigest stay. Sync accepts the exited
	// attempt and rejects a digest that no longer matches this launch.
	t.touch()
	if err := saveTask(root, t); err != nil {
		return err
	}
	// Exit 0 is not success. Only a normal return is offered to goal-sync.
	// Timeout and nonzero exit stay held/unknown even if native files say achieved.
	if runErr == nil && !timedOut {
		if err := autoSyncAfterGoalReturn(root, cfg, wf, t, childHome, childDir); err != nil {
			return err
		}
	}
	if runErr != nil && !timedOut && !errors.Is(runErr, context.DeadlineExceeded) {
		return runErr
	}
	return nil
}

func goalChildHomeAndDir(t *Task) (string, string) {
	if t == nil {
		return "", ""
	}
	home := ""
	if t.Goal != nil {
		home = strings.TrimSpace(t.Goal.GrokHome)
	}
	return home, strings.TrimSpace(t.Dir)
}

// autoSyncAfterGoalReturn invokes the existing goal-sync acceptor after the
// runner has returned. syncWorkflowGoal commits evDone or evFailed only when
// its current gates pass. A nil error is not done unless that commit happened.
// Native active is not offered to sync: mapNativeGoalToTask would save it as
// running, and this process has already returned.
func autoSyncAfterGoalReturn(root string, cfg *Config, wf *WorkflowRecord, t *Task, grokHome, cwd string) error {
	if t == nil || t.Goal == nil || wf == nil || cfg == nil {
		return nil
	}
	if strings.TrimSpace(grokHome) == "" {
		grokHome = defaultGrokHome()
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = t.Dir
	}
	if returnedNativeStillActive(t, grokHome, cwd) {
		return holdReturnedActiveGoal(root, t)
	}
	req := GoalSyncRequest{
		TaskID:          t.ID,
		ExpectedSession: t.SessionID,
		GrokHome:        grokHome,
		GrokCWD:         cwd,
	}
	if t.Goal != nil {
		req.ExpectedGoalID = t.Goal.NativeGoalID
		req.ExpectedAttempt = firstNonBlank(t.Goal.BoundAttemptID, t.ActiveAttemptID)
	}
	_, syncErr := syncWorkflowGoal(root, cfg, wf, req)
	// Rejection and a refused running write stay non-accepted. The pre-sync
	// save is already held/unknown. Disk done/failed is whatever sync committed.
	if syncErr != nil && !errors.Is(syncErr, errGoalSyncRejected) && !errors.Is(syncErr, errStaleTaskWrite) {
		return syncErr
	}
	fresh, err := loadTask(root, t.ID)
	if err != nil {
		return err
	}
	*t = *fresh
	if t.Goal != nil && (t.Status == statusRunning || t.Goal.Observation == goalObsRunning || normalizeNativeStatus(t.Goal.LastNativeStatus) == "active") {
		return holdReturnedActiveGoal(root, t)
	}
	return nil
}

func returnedNativeStillActive(t *Task, grokHome, cwd string) bool {
	if t == nil || t.SessionID == "" {
		return false
	}
	goalID := ""
	if t.Goal != nil {
		goalID = t.Goal.NativeGoalID
	}
	obs, err := observeNativeGrokGoal(grokHome, cwd, t.SessionID, goalID)
	if err != nil {
		return false
	}
	return normalizeNativeStatus(obs.NativeStatus) == "active"
}

// holdReturnedActiveGoal keeps a returned runner from becoming a live writer
// just because the native file still says active. It does not commit evDone.
func holdReturnedActiveGoal(root string, t *Task) error {
	if t == nil || t.Goal == nil {
		return nil
	}
	switch t.Status {
	case statusDone, statusFailed, statusCanceled:
		return nil
	}
	t.Status = statusHeld
	t.Goal.Observation = goalObsUnknown
	t.Goal.Continuation = ""
	t.Goal.EvidenceComplete = false
	t.Goal.LastNativeStatus = "active"
	t.Goal.ObservationNote = "runner returned while native state is still active; not accepted done and not a live writer"
	if t.Goal.CustodyReleased && t.ActiveAttemptID != "" {
		t.ActiveAttemptID = ""
	}
	if t.effectiveControlState() != controlRevoking && t.effectiveControlState() != controlTerminal {
		revokeScheduling(t)
	}
	t.touch()
	return saveTask(root, t)
}

// residualActiveHeldGoal is true when a held/unknown Goal has no live writer.
// Native "active" must not revive it; auto-return holdReturnedActiveGoal is
// not used here because it would overwrite a hard-timeout note.
func residualActiveHeldGoal(root string, t *Task) bool {
	if t == nil || t.Goal == nil {
		return false
	}
	if t.Status != statusHeld || t.Goal.Observation != goalObsUnknown {
		return false
	}
	if taskHasLiveWriterProof(root, t) {
		return false
	}
	return true
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
	if sel := goalLaunchSandboxSelector(t); sel != "" {
		if !writeCapable {
			return "", "", fmt.Errorf("%w: sandbox selector on read-only Goal", errGoalUnsupportedTuple)
		}
		name, _, _, rerr := resolveExistingSandboxSelector(t, sel)
		if rerr != nil {
			return "", "", rerr
		}
		sandbox, permission = name, "auto"
	} else if writeCapable {
		sandbox, permission = resolvedGrokBuildWriteSandbox(cfg), "auto"
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

func manualGrokGoalArgs(cfg *Config, t *Task, model, effort, sandbox, permission, grokHome string) []string {
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
	if t != nil && strings.TrimSpace(t.Dir) != "" {
		args = append(args, "--cwd", t.Dir)
	}
	if grokHome == "" {
		grokHome = defaultGrokHome()
	}
	sessionDir := grokGoalSessionDir(grokHome, t.Dir, t.SessionID)
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
	if t != nil && t.Goal != nil {
		if strings.TrimSpace(t.Goal.SandboxProfile) != "" {
			fmt.Fprintf(&b, "sandbox_profile: %s\n", strings.TrimSpace(t.Goal.SandboxProfile))
		}
		if strings.TrimSpace(t.Goal.SandboxProfileDigest) != "" {
			fmt.Fprintf(&b, "sandbox_profile_digest: %s\n", strings.TrimSpace(t.Goal.SandboxProfileDigest))
		}
		if strings.TrimSpace(t.Goal.CardexRoot) != "" {
			fmt.Fprintf(&b, "cardex_root: %s\n", strings.TrimSpace(t.Goal.CardexRoot))
		}
		if strings.TrimSpace(t.Goal.SandboxWorktree) != "" {
			fmt.Fprintf(&b, "worktree: %s\n", strings.TrimSpace(t.Goal.SandboxWorktree))
		}
		if strings.TrimSpace(t.Goal.GitCommonDir) != "" {
			fmt.Fprintf(&b, "git_common_dir: %s\n", strings.TrimSpace(t.Goal.GitCommonDir))
		}
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
	if t != nil && t.Goal != nil && t.Goal.Hosted {
		fmt.Printf("Cardex hosts this Goal: /goal is injected on the PTY master. pause/resume/stop require native post-state confirmation.\n")
	} else {
		fmt.Printf("Interactive TUI owner types /goal. Slave writes and exit 0 are not pause. Hosted control is a separate -hosted launch.\n")
	}
	fmt.Printf("grok -p is a single turn and is not goal proof.\n")
	if t != nil && wf != nil && t.ID != "" && t.ID != wf.WriterTaskID {
		fmt.Printf("After the TUI returns: cardex workflow goal-sync %s -task %s\n", wf.ID, t.ID)
	} else {
		fmt.Printf("After the TUI returns: cardex workflow goal-sync %s\n", wf.ID)
	}
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
		want = canonicalWorkspaceID(taskExecutionLeaseDir(root, t))
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

func workflowListsGoalSyncTarget(wf *WorkflowRecord, id string) bool {
	if wf == nil || id == "" {
		return false
	}
	if wf.WriterTaskID == id {
		return true
	}
	for _, nativeID := range wf.NativeGoalTaskIDs {
		if nativeID == id {
			return true
		}
	}
	return false
}

// goalSyncTarget resolves the card sync may update. No TaskID keeps the serial
// writer. An explicit id must already belong to this workflow as that writer
// or as a native direction. It is not written into WriterTaskID.
func goalSyncTarget(root string, wf *WorkflowRecord, taskID string) (*Task, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return workflowWriterTask(root, wf)
	}
	if wf == nil {
		return nil, fmt.Errorf("%w: missing workflow", errGoalSyncRejected)
	}
	t, err := findTaskAnywhere(root, taskID)
	if err != nil || t == nil {
		return nil, fmt.Errorf("%w: unknown sync target %s", errGoalSyncRejected, taskID)
	}
	if t.WorkflowID != wf.ID || !workflowListsGoalSyncTarget(wf, t.ID) {
		return t, fmt.Errorf("%w: unrelated sync target %s", errGoalSyncRejected, taskID)
	}
	return t, nil
}

func rejectSharedNativeIdentity(root string, wf *WorkflowRecord, t *Task) error {
	if wf == nil || t == nil {
		return nil
	}
	session := strings.TrimSpace(t.SessionID)
	goalID := ""
	if t.Goal != nil {
		goalID = strings.TrimSpace(t.Goal.NativeGoalID)
	}
	if session == "" && goalID == "" {
		return nil
	}
	seen := map[string]bool{}
	ids := make([]string, 0, 1+len(wf.NativeGoalTaskIDs))
	if wf.WriterTaskID != "" {
		ids = append(ids, wf.WriterTaskID)
	}
	ids = append(ids, wf.NativeGoalTaskIDs...)
	for _, id := range ids {
		if id == "" || id == t.ID || seen[id] {
			continue
		}
		seen[id] = true
		other, err := loadTask(root, id)
		if err != nil || other == nil {
			continue
		}
		if session != "" && strings.TrimSpace(other.SessionID) == session {
			return fmt.Errorf("%w: shared session %s", errGoalSyncRejected, session)
		}
		if goalID != "" && other.Goal != nil && strings.TrimSpace(other.Goal.NativeGoalID) == goalID {
			return fmt.Errorf("%w: shared native goal_id %s", errGoalSyncRejected, goalID)
		}
	}
	return nil
}

func syncWorkflowGoal(root string, cfg *Config, wf *WorkflowRecord, req GoalSyncRequest) (*Task, error) {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return nil, err
	}
	t, err := goalSyncTarget(root, wf, req.TaskID)
	if err != nil {
		return t, err
	}
	if t.Goal == nil {
		return t, fmt.Errorf("%w: task is not a Goal writer", errWorkflowMalformed)
	}
	if err := rejectSharedNativeIdentity(root, wf, t); err != nil {
		return t, err
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
	if grokHome == "" && t.Goal != nil {
		grokHome = strings.TrimSpace(t.Goal.GrokHome)
	}
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
	// A blank native goal id is not "the same goal". Terminal sync cannot
	// adopt or keep an identity the state file did not record.
	if strings.TrimSpace(obs.GoalID) == "" {
		return t, fmt.Errorf("%w: missing native goal_id", errGoalSyncRejected)
	}
	if expectedGoal != "" && obs.GoalID != expectedGoal {
		return t, fmt.Errorf("%w: other goal_id", errGoalSyncRejected)
	}
	if strings.TrimSpace(t.Goal.NativeGoalID) != "" && obs.GoalID != t.Goal.NativeGoalID {
		t.Goal.FailureClass = goalFailStaleIdentity
		_ = saveTask(root, t)
		return t, fmt.Errorf("%w: other goal_id", errGoalSyncRejected)
	}
	applyPlanningFailedMapping(t, obs)
	if processExited, custody := goalAttemptStarted(root, t) && !taskHasLiveWriterProof(root, t), goalCustodyReleased(root, t); true {
		if class := classifyNativeObservationGap(obs, processExited, custody); class != "" && t.Goal.FailureClass == "" {
			t.Goal.FailureClass = class
		}
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
	if (applied.AcceptDone || applied.AcceptFailed) && strings.TrimSpace(obs.GoalID) == "" {
		applied.AcceptDone = false
		applied.AcceptFailed = false
		applied.Status = statusHeld
		applied.Observation = goalObsUnknown
		applied.Note = "native goal_id missing; not accepted done/failed"
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

	residualActive := applied.Status == statusRunning && residualActiveHeldGoal(root, t)
	// A stale active file without new measurements is still a strict no-op.
	// Keep missing usage unknown; real measurements (including zero) may sync.
	if residualActive && obs.HighWaterTokens == nil && obs.ElapsedMS == nil {
		return t, nil
	}
	usageChanged := applyNativeGoalUsage(t, obs)
	// Residual native-active after a custody-released timeout (held/unknown,
	// no live writer) must not revive to running. Keep the existing timeout
	// or previous-terminal note. Live producers still map to running.
	if residualActive {
		if usageChanged {
			t.touch()
			if err := saveTask(root, t); err != nil {
				return t, err
			}
		}
		return t, nil
	}

	if t.Goal.SyncedRevision == expectedRev &&
		t.Goal.Observation == applied.Observation && t.Status == applied.Status {
		if usageChanged {
			t.touch()
			if err := saveTask(root, t); err != nil {
				return t, err
			}
		}
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
	alreadyDone := t.Status == statusDone && applied.AcceptDone
	alreadyFailed := t.Status == statusFailed && applied.AcceptFailed
	if alreadyDone || alreadyFailed {
		t.touch()
		if usageChanged {
			evType := evDone
			if alreadyFailed {
				evType = evFailed
			}
			if err := commitTaskTransition(root, t, transitionRequest{
				EventType: evType, Actor: "workflow:goal-sync", Status: t.Status,
				RequireSchedulerLock: false, UpdateTerminalAnnotations: true,
				Detail: withCostTelemetry(map[string]any{
					"workflow": wf.ID, "session_id": t.SessionID, "goal_id": t.Goal.NativeGoalID,
					"reason": "native_post_complete_usage",
				}, t),
			}); err != nil {
				return t, err
			}
		} else if err := saveTask(root, t); err != nil {
			return t, err
		}
		return t, persistWorkflow(root, cfg, wf)
	}
	// BoundAttemptID is historical identity. ActiveAttemptID is the live writer.
	// Copying the bound id back after custody reclaim resurrects an exited attempt.
	if custody {
		t.ActiveAttemptID = ""
	} else if t.ActiveAttemptID == "" && t.Goal.BoundAttemptID != "" && applied.Status == statusRunning {
		t.ActiveAttemptID = t.Goal.BoundAttemptID
	}

	switch {
	case applied.AcceptDone:
		// One native session done is this card only. Whole-goal acceptance
		// stays on cardex workflow accept -complete.
		t.Status = statusDone
		if err := commitTaskTransition(root, t, transitionRequest{
			EventType:            evDone,
			Actor:                "workflow:goal-sync",
			Status:               statusDone,
			RequireSchedulerLock: false,
			Detail: withCostTelemetry(map[string]any{
				"workflow": wf.ID, "session_id": t.SessionID, "goal_id": t.Goal.NativeGoalID,
				"attempt_id": t.Goal.BoundAttemptID, "last_classifier_verdict": obs.Classifier,
			}, t),
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
			Detail: withCostTelemetry(map[string]any{
				"workflow": wf.ID, "session_id": t.SessionID, "goal_id": t.Goal.NativeGoalID,
			}, t),
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
	SessionID        string
	GoalID           string
	NativeStatus     string
	Phase            string
	Classifier       string
	FinalStatus      string
	FinalClassifier  string
	UpdatesOK        bool
	SummaryOK        bool
	ObservedCWD      string
	Missing          bool
	Contradictory    bool
	BudgetLimited    bool
	NotAchieved      bool
	PauseMessage     string
	HighWaterTokens  *int64
	ElapsedMS        *int64
	HighWaterInvalid bool
	ElapsedInvalid   bool
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
	out.Phase = strings.ToLower(strings.TrimSpace(st.Phase))
	out.Classifier = strings.TrimSpace(st.LastClassifierVerdict)
	out.PauseMessage = strings.TrimSpace(st.PauseMessage)
	if n, ok := parseOptionalJSONInt64(st.TokensUsedHighWater); ok {
		out.HighWaterTokens = n
	} else if len(st.TokensUsedHighWater) > 0 && strings.TrimSpace(string(st.TokensUsedHighWater)) != "null" {
		out.HighWaterInvalid = true
	}
	if n, ok := parseOptionalJSONInt64(st.ElapsedMS); ok {
		out.ElapsedMS = n
	} else if len(st.ElapsedMS) > 0 && strings.TrimSpace(string(st.ElapsedMS)) != "null" {
		out.ElapsedInvalid = true
	}
	if out.NativeStatus == "budget_limited" {
		out.BudgetLimited = true
	}
	if out.NativeStatus == "complete" && strings.EqualFold(out.Classifier, "not_achieved") {
		out.NotAchieved = true
	}
	if out.GoalID == "" {
		out.Contradictory = true
		return out, fmt.Errorf("%w: missing native goal_id", errGoalSyncRejected)
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

	final, ok, updErr := verifyGrokGoalUpdates(updatesPath, sessionID, out.GoalID)
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
	if expectedGoalID != "" && out.GoalID != expectedGoalID {
		return out, fmt.Errorf("%w: other goal_id", errGoalSyncRejected)
	}
	if final.GoalID == "" || final.GoalID != out.GoalID {
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
		if updGoal == "" {
			return final, false, fmt.Errorf("updates.jsonl missing params.update.goal_id")
		}
		if goalID != "" && updGoal != goalID {
			continue
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

func executionObservationFromNativeGoal(obs nativeGoalObservation, custodyReleased, cancelRequested, stopEvidence bool) executionObservation {
	out := executionObservation{}
	if cancelRequested && stopEvidence {
		out.Canceled = true
		out.Reason = "canceled"
		return out
	}
	if obs.Missing || obs.Contradictory || !obs.UpdatesOK {
		out.Incomplete = true
		out.Reason = "unknown_terminal"
		return out
	}
	if obs.BudgetLimited {
		out.Complete = true
		out.HasTerminal = true
		out.Incomplete = true
		out.Reason = "budget_limited"
		return out
	}
	st := normalizeNativeStatus(obs.NativeStatus)
	switch st {
	case "active":
		out.WaitingApproval = true
		out.Reason = "native_active"
	case "paused", "user_paused", "needs-input":
		out.NeedsInteraction = true
		out.Complete = true
		out.HasTerminal = true
		out.Reason = "needs_input"
	case "failed":
		out.Complete = true
		out.HasTerminal = true
		if !custodyReleased {
			out.Incomplete = true
			out.RemoteUnknown = true
			out.Reason = "failed_without_custody"
		} else {
			out.FailureClass = failureUnknown
			out.Reason = "native_failed"
		}
	case "complete":
		out.Complete = true
		out.HasTerminal = true
		achieved := strings.EqualFold(obs.Classifier, "achieved") && !obs.NotAchieved &&
			strings.EqualFold(obs.FinalStatus, "complete") && strings.EqualFold(obs.FinalClassifier, "achieved")
		if achieved {
			out.SuccessStructure = true
			out.SemanticEvents = 1
			out.ModelEvents = 1
			if !custodyReleased || (cancelRequested && !stopEvidence) {
				out.VerifyFailed = true
				out.Reason = "complete_unverified"
			}
		} else {
			out.VerifyFailed = true
			out.Reason = "complete_not_achieved"
		}
	default:
		out.Incomplete = true
		out.HasTerminal = false
		out.Reason = "unknown_terminal"
	}
	return out
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
	case "paused", "user_paused", "needs-input":
		note := "verified paused/needs-input; same-session explicit continuation"
		if planningFailedUnknown(obs.PauseMessage) {
			note = "native paused with Planning failed; cause unspecified; not accepted complete; not auto-resumed"
		}
		return mappedGoal{
			Status:       statusHeld,
			Observation:  goalObsHeld,
			Continuation: "same-session",
			Note:         note,
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
		dec := decideExecutionOutcome(executionObservationFromNativeGoal(obs, custodyReleased, cancelRequested, stopEvidence), executionAttemptFacts{})
		if dec.Kind != executionDecisionSuccess {
			return mappedGoal{Status: statusHeld, Observation: goalObsUnknown, Note: "shared execution disposition refused done"}
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
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(sa, sb)
	}
	ra, errA2 := filepath.EvalSymlinks(a)
	rb, errB2 := filepath.EvalSymlinks(b)
	if errA2 != nil || ra == "" {
		ra = filepath.Clean(a)
	}
	if errB2 != nil || rb == "" {
		rb = filepath.Clean(b)
	}
	return ra == rb
}

func expandWorkflowWriteDomainForDesignResult(root string, cfg *Config, wf *WorkflowRecord, currentWriter *Task, paths []string) (WriteDomain, error) {
	if wf == nil {
		return WriteDomain{}, fmt.Errorf("%w: missing workflow for add_write_paths", errGoalDesignResult)
	}
	if len(paths) == 0 {
		return wf.WriteDomain, nil
	}
	proposed := wf.WriteDomain
	proposed.Paths = append([]string(nil), wf.WriteDomain.Paths...)
	proposed.Resources = append([]ResourceClaim(nil), wf.WriteDomain.Resources...)
	seen := map[string]bool{}
	for _, p := range proposed.Paths {
		seen[p] = true
	}
	repo := workflowRepoRoot(wf)
	for _, raw := range paths {
		norm, err := NormalizePathClaim(repo, raw)
		if err != nil {
			return WriteDomain{}, fmt.Errorf("%w: add_write_paths: %v", errGoalDesignResult, err)
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		proposed.Paths = append(proposed.Paths, norm)
	}
	out, err := NormalizeWriteDomain(repo, proposed)
	if err != nil {
		return WriteDomain{}, fmt.Errorf("%w: add_write_paths: %v", errGoalDesignResult, err)
	}
	probeWF := *wf
	probeWF.WriteDomain = out
	if err := auditWorkflowWriteDomains(root, cfg, &probeWF); err != nil {
		return WriteDomain{}, fmt.Errorf("%w: add_write_paths: %v", errGoalDesignResult, err)
	}
	probeTask := &Task{
		ID:          "_design-result-add-write-paths",
		Type:        typeSequence,
		Dir:         wf.Worktree,
		WriteDomain: &out,
	}
	live := mergeLiveWriterTasks(nil, reconstructLiveWriterClaims(root))
	filtered := make([]*Task, 0, len(live))
	for _, other := range live {
		if currentWriter != nil && other != nil && other.ID == currentWriter.ID {
			continue
		}
		filtered = append(filtered, other)
	}
	if writerConflictsWithActive(probeTask, filtered) {
		return WriteDomain{}, fmt.Errorf("%w: add_write_paths: write-domain/resource conflict with an active writer", errGoalDesignResult)
	}
	return out, nil
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
	fresh, err := loadExternalDesignReceipt(receiptPath)
	if err != nil {
		return nil, err
	}
	if err := designProofComplete(fresh); err != nil {
		return nil, err
	}
	var out *Task
	run := func() error {
		t, err := applyWorkflowDesignResultLocked(root, cfg, wf, decision, observation, fresh)
		out = t
		return err
	}
	if designResultRequestsWriteDomainExpansion(decision, fresh) {
		err = withWorkflowSchedulerLock(root, cfg, func() error {
			return withTaskControlLock(root, "global-admission", func() error {
				return withTaskControlLock(root, "workflow-admit:"+wf.ID, run)
			})
		})
	} else {
		err = withTaskControlLock(root, "workflow-admit:"+wf.ID, run)
	}
	return out, err
}

func designResultRequestsWriteDomainExpansion(decision string, fresh *WorkflowDesignNode) bool {
	if fresh == nil || len(fresh.AddWritePaths) == 0 {
		return false
	}
	switch decision {
	case goalDecisionSuccessor, goalDecisionRevise:
		return true
	default:
		return false
	}
}

func applyWorkflowDesignResultLocked(root string, cfg *Config, wf *WorkflowRecord, decision, observation string, fresh *WorkflowDesignNode) (*Task, error) {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, fmt.Errorf("%w: a fresh independent design receipt is required", errGoalDesignResult)
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
	if err := designProducerDistinct(fresh, t); err != nil {
		return nil, err
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
	doneStage := t.Status == statusDone || currentObs == goalObsDone
	if t.Status == statusCanceled && (decision == goalDecisionSuccessor || decision == goalDecisionRevise) {
		terminal, err := loadTransition(root, t.ID, t.LastCommittedTransitionID)
		if err != nil || terminal == nil || terminal.State != transitionCommitted ||
			terminal.TaskID != t.ID || terminal.Status != statusCanceled || terminal.EventType != evCanceled ||
			terminal.NewRevision != t.Revision || currentObs != goalObsCanceled || !t.Goal.StopEvidence {
			return nil, fmt.Errorf("%w: canceled stage requires durable cancellation and stop evidence", errGoalDesignResult)
		}
	}
	if decision == goalDecisionAccept || decision == goalDecisionSuccessor || decision == goalDecisionRevise {
		if doneStage && (!taskDurablyDone(root, t) || currentObs != goalObsDone || !goalCustodyReleased(root, t)) {
			return nil, fmt.Errorf("%w: completed stage requires durable terminal and released producer custody", errGoalDesignResult)
		}
		if !doneStage && currentObs == goalObsUnknown && native != "budget_limited" {
			// A fresh explicit successor decision may retire an unknown attempt
			// after custody recovery. It never makes the old result acceptable.
			if decision != goalDecisionSuccessor || !goalCustodyReleased(root, t) {
				return nil, fmt.Errorf("%w: unknown execution outcome cannot be accepted or revised; explicit successor requires released custody and fresh design evidence", errGoalDesignResult)
			}
		}
	}
	if doneStage && decision == goalDecisionSuccessor {
		fmt.Fprintln(os.Stderr, "warning: completed stage successor normalized to revise; existing round and custody limits still apply")
		decision = goalDecisionRevise
	}
	if len(fresh.AddWritePaths) > 0 && decision != goalDecisionSuccessor && decision != goalDecisionRevise {
		return nil, fmt.Errorf("%w: add_write_paths is only for successor|revise", errGoalDesignResult)
	}
	var expandedDomain *WriteDomain
	if len(fresh.AddWritePaths) > 0 {
		got, err := expandWorkflowWriteDomainForDesignResult(root, cfg, wf, t, fresh.AddWritePaths)
		if err != nil {
			return nil, err
		}
		expandedDomain = &got
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
		if observation == "paused" || observation == "user_paused" || observation == "needs-input" {
			return nil, fmt.Errorf("%w: paused/needs-input uses same-session goal-run, not a successor writer", errGoalDesignResult)
		}
		if wf.CurrentRound >= wf.MaxRounds {
			wf.Status = workflowStatusExhausted
			if err := persistWorkflow(root, cfg, wf); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %d/%d", errWorkflowRoundsExceeded, wf.CurrentRound, wf.MaxRounds)
		}
		if !doneStage && !goalCustodyReleased(root, t) {
			return nil, fmt.Errorf("%w: cannot open a successor while custody is held", errGoalDesignResult)
		}
		// A canceled producer is already terminal, possibly archived. Keep its
		// cancellation evidence intact instead of rewriting it through tasks/.
		if !doneStage && t.Status != statusFailed && t.Status != statusCanceled {
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
			if currentObs == goalObsUnknown {
				t.Goal.Observation = goalObsUnknown
			}
			t.Goal.CustodyReleased = true
			t.Goal.EvidenceComplete = false
			t.Goal.ObservationNote = "nonaccepted stage retired for one explicit Goal-bound successor; prior native outcome preserved; not manufactured done"
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
		if expandedDomain != nil {
			wf.WriteDomain = *expandedDomain
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
	case "paused", "user_paused", "needs-input":
		return currentObs == goalObsHeld && t.Goal.Continuation == "same-session"
	case "budget_limited":
		return native == "budget_limited"
	default:
		return observation == currentObs || observation == native
	}
}
