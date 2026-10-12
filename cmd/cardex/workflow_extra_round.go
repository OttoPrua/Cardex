package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

const (
	extraRoundKind = "workflow-extra-round"
	// extraRoundBudgetTokens / extraRoundHardTimeoutSeconds document the current
	// backend-consumer example. Admission binds the grant's own positive finite
	// values rather than forcing these constants.
	extraRoundBudgetTokens       = int64(400000)
	extraRoundHardTimeoutSeconds = int64(5400)
	extraRoundMaxAttempts        = 1
	extraRoundAuthRequiredNote   = "explicit one-use extra-round authorization bound to workflow/current round/base max/previous task/session/attempt/revision/fresh design digest/Human source-message-evidence identity/positive finite budget_tokens/representable hard_timeout_seconds/1/no_auto_retry"
)

var (
	errGoalExtraRoundAuthRequired = errors.New("workflow extra-round requires an explicit one-use authorization")
	errGoalExtraRoundRefused      = errors.New("workflow extra-round refused")
)

// workflowExtraRoundGrant is manager-attested local evidence. Bytes and
// bindings are checked; this is not cryptographic authentication of Human.
type workflowExtraRoundGrant struct {
	Kind               string `json:"kind"`
	WorkflowID         string `json:"workflow_id"`
	CurrentRound       int    `json:"current_round"`
	BaseMaxRounds      int    `json:"base_max_rounds"`
	PreviousTaskID     string `json:"previous_task_id"`
	PreviousSessionID  string `json:"previous_session_id"`
	PreviousAttemptID  string `json:"previous_attempt_id"`
	PreviousRevision   int64  `json:"previous_revision"`
	DesignDigest       string `json:"design_digest"`
	HumanSource        string `json:"human_source"`
	HumanMessage       string `json:"human_message"`
	HumanEvidence      string `json:"human_evidence"`
	BudgetTokens       int64  `json:"budget_tokens"`
	HardTimeoutSeconds int64  `json:"hard_timeout_seconds"`
	MaxAttempts        int    `json:"max_attempts"`
	NoAutoRetry        bool   `json:"no_auto_retry"`
	IssuedAt           string `json:"issued_at,omitempty"`
	Nonce              string `json:"nonce"`
}

type workflowExtraRoundConsumption struct {
	Kind          string                   `json:"kind"`
	GrantDigest   string                   `json:"grant_digest"`
	DesignDigest  string                   `json:"design_digest"`
	ConsumedAt    string                   `json:"consumed_at"`
	AdmittedRound int                      `json:"admitted_round,omitempty"`
	WriterTaskID  string                   `json:"writer_task_id,omitempty"`
	Grant         *workflowExtraRoundGrant `json:"grant,omitempty"`
}

func extraRoundRefused(reason string) error {
	return fmt.Errorf("%w: %s", errGoalExtraRoundRefused, reason)
}

func extraRoundAuthRequired(reason string) error {
	return fmt.Errorf("%w: %s", errGoalExtraRoundAuthRequired, reason)
}

func extraRoundHasConsumption(wf *WorkflowRecord) bool {
	return wf != nil && len(wf.ExtraRoundConsumptions) > 0
}

func extraRoundCurrentRoundAllowed(wf *WorkflowRecord) bool {
	if wf == nil || wf.CurrentRound < 0 {
		return false
	}
	if wf.CurrentRound <= wf.MaxRounds {
		return true
	}
	return extraRoundHasConsumption(wf) && wf.CurrentRound == wf.MaxRounds+1
}

func extraRoundGrantConsumed(wf *WorkflowRecord, digest string) bool {
	if wf == nil || digest == "" {
		return false
	}
	for _, c := range wf.ExtraRoundConsumptions {
		if strings.EqualFold(strings.TrimSpace(c.GrantDigest), digest) {
			return true
		}
	}
	return false
}

func extraRoundExistingWriter(root string, wf *WorkflowRecord, exceptWriterID string) (*Task, error) {
	if wf == nil {
		return nil, extraRoundRefused("missing workflow")
	}
	tasks, err := loadTasks(root)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t == nil || t.WorkflowID != wf.ID || t.Goal == nil || !t.Goal.ExtraRoundAuthorized {
			continue
		}
		if exceptWriterID != "" && t.ID == exceptWriterID {
			continue
		}
		return t, nil
	}
	return nil, nil
}

func extraRoundRefuseIfAlreadyAdmitted(root string, wf *WorkflowRecord, exceptWriterID string) error {
	if extraRoundHasConsumption(wf) {
		return extraRoundRefused("extra-round grant already consumed")
	}
	existing, err := extraRoundExistingWriter(root, wf, exceptWriterID)
	if err != nil {
		return extraRoundRefused("cannot inspect extra-round writers: " + err.Error())
	}
	if existing != nil {
		return extraRoundRefused("extra-round writer already admitted")
	}
	return nil
}

func extraRoundMaxRepresentableTimeoutSeconds() int64 {
	return int64(math.MaxInt64 / int64(time.Second))
}

func extraRoundParsePositiveInt64(n json.Number, field string) (int64, error) {
	// Grants use integer literals. Never round an authorization through float64:
	// a fractional or large value can otherwise silently become another budget.
	i, err := n.Int64()
	if err != nil || i <= 0 {
		return 0, extraRoundRefused(field + " must be a positive integer literal within int64")
	}
	return i, nil
}

func extraRoundValidateGrantBounds(grant *workflowExtraRoundGrant) error {
	if grant == nil {
		return extraRoundAuthRequired(extraRoundAuthRequiredNote)
	}
	if grant.BudgetTokens <= 0 {
		return extraRoundRefused("authorization budget_tokens must be a positive finite integer")
	}
	if grant.HardTimeoutSeconds <= 0 {
		return extraRoundRefused("authorization hard_timeout_seconds must be a positive finite duration")
	}
	if grant.HardTimeoutSeconds > extraRoundMaxRepresentableTimeoutSeconds() {
		return extraRoundRefused("authorization hard_timeout_seconds is not representable as a duration")
	}
	d := time.Duration(grant.HardTimeoutSeconds) * time.Second
	if d <= 0 || int64(d/time.Second) != grant.HardTimeoutSeconds {
		return extraRoundRefused("authorization hard_timeout_seconds is not representable as a duration")
	}
	if grant.MaxAttempts != extraRoundMaxAttempts {
		return extraRoundRefused("authorization max_attempts must be 1")
	}
	if !grant.NoAutoRetry {
		return extraRoundRefused("authorization no_auto_retry must be true")
	}
	return nil
}

func (g workflowExtraRoundGrant) consumeDigest() string {
	parts := []string{
		g.Kind,
		g.WorkflowID,
		fmt.Sprintf("%d", g.CurrentRound),
		fmt.Sprintf("%d", g.BaseMaxRounds),
		g.PreviousTaskID,
		g.PreviousSessionID,
		g.PreviousAttemptID,
		fmt.Sprintf("%d", g.PreviousRevision),
		strings.ToLower(strings.TrimSpace(g.DesignDigest)),
		g.HumanSource,
		g.HumanMessage,
		g.HumanEvidence,
		fmt.Sprintf("%d", g.BudgetTokens),
		fmt.Sprintf("%d", g.HardTimeoutSeconds),
		fmt.Sprintf("%d", g.MaxAttempts),
		fmt.Sprintf("%v", g.NoAutoRetry),
		g.Nonce,
	}
	return sha256Hex(strings.Join(parts, "\n"))
}

func loadExtraRoundAuthorization(path string) (*workflowExtraRoundGrant, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, extraRoundAuthRequired(extraRoundAuthRequiredNote)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, extraRoundAuthRequired("read authorization: " + err.Error())
	}
	var file struct {
		Kind               string      `json:"kind"`
		WorkflowID         string      `json:"workflow_id"`
		CurrentRound       int         `json:"current_round"`
		BaseMaxRounds      int         `json:"base_max_rounds"`
		PreviousTaskID     string      `json:"previous_task_id"`
		PreviousSessionID  string      `json:"previous_session_id"`
		PreviousAttemptID  string      `json:"previous_attempt_id"`
		PreviousRevision   int64       `json:"previous_revision"`
		DesignDigest       string      `json:"design_digest"`
		HumanSource        string      `json:"human_source"`
		HumanMessage       string      `json:"human_message"`
		HumanEvidence      string      `json:"human_evidence"`
		BudgetTokens       json.Number `json:"budget_tokens"`
		HardTimeoutSeconds json.Number `json:"hard_timeout_seconds"`
		MaxAttempts        int         `json:"max_attempts"`
		NoAutoRetry        bool        `json:"no_auto_retry"`
		IssuedAt           string      `json:"issued_at,omitempty"`
		Nonce              string      `json:"nonce"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, extraRoundAuthRequired("malformed authorization JSON")
	}
	budget, err := extraRoundParsePositiveInt64(file.BudgetTokens, "budget_tokens")
	if err != nil {
		return nil, err
	}
	timeout, err := extraRoundParsePositiveInt64(file.HardTimeoutSeconds, "hard_timeout_seconds")
	if err != nil {
		return nil, err
	}
	grant := workflowExtraRoundGrant{
		Kind:               strings.TrimSpace(file.Kind),
		WorkflowID:         strings.TrimSpace(file.WorkflowID),
		CurrentRound:       file.CurrentRound,
		BaseMaxRounds:      file.BaseMaxRounds,
		PreviousTaskID:     strings.TrimSpace(file.PreviousTaskID),
		PreviousSessionID:  strings.TrimSpace(file.PreviousSessionID),
		PreviousAttemptID:  strings.TrimSpace(file.PreviousAttemptID),
		PreviousRevision:   file.PreviousRevision,
		DesignDigest:       strings.ToLower(strings.TrimSpace(file.DesignDigest)),
		HumanSource:        strings.TrimSpace(file.HumanSource),
		HumanMessage:       strings.TrimSpace(file.HumanMessage),
		HumanEvidence:      strings.TrimSpace(file.HumanEvidence),
		BudgetTokens:       budget,
		HardTimeoutSeconds: timeout,
		MaxAttempts:        file.MaxAttempts,
		NoAutoRetry:        file.NoAutoRetry,
		IssuedAt:           strings.TrimSpace(file.IssuedAt),
		Nonce:              strings.TrimSpace(file.Nonce),
	}
	if grant.Kind != extraRoundKind || grant.WorkflowID == "" || grant.PreviousTaskID == "" ||
		grant.PreviousSessionID == "" || grant.PreviousAttemptID == "" || grant.DesignDigest == "" ||
		grant.Nonce == "" {
		return nil, extraRoundAuthRequired("authorization missing required binding fields")
	}
	if grant.HumanSource == "" || grant.HumanMessage == "" || grant.HumanEvidence == "" {
		return nil, extraRoundRefused("Human source-message-evidence identity is missing or empty")
	}
	if err := extraRoundValidateGrantBounds(&grant); err != nil {
		return nil, err
	}
	return &grant, nil
}

func bindExtraRoundAuthorization(grant *workflowExtraRoundGrant, wf *WorkflowRecord, t *Task, fresh *WorkflowDesignNode) error {
	if grant == nil {
		return extraRoundAuthRequired(extraRoundAuthRequiredNote)
	}
	if wf == nil || t == nil || t.Goal == nil || fresh == nil {
		return extraRoundRefused("missing workflow, writer, or fresh design")
	}
	if grant.WorkflowID != wf.ID {
		return extraRoundRefused("authorization workflow_id does not match")
	}
	if grant.CurrentRound != wf.CurrentRound {
		return extraRoundRefused("authorization current_round does not match")
	}
	if grant.BaseMaxRounds != wf.MaxRounds {
		return extraRoundRefused("authorization base_max_rounds does not match")
	}
	if grant.PreviousTaskID != t.ID {
		return extraRoundRefused("authorization previous_task_id does not match")
	}
	if grant.PreviousSessionID != t.SessionID {
		return extraRoundRefused("authorization previous_session_id does not match")
	}
	if grant.PreviousAttemptID != goalAttemptID(t) {
		return extraRoundRefused("authorization previous_attempt_id does not match")
	}
	if grant.PreviousRevision != t.Revision {
		return extraRoundRefused("authorization previous_revision does not match")
	}
	if !strings.EqualFold(grant.DesignDigest, strings.TrimSpace(fresh.Digest)) {
		return extraRoundRefused("authorization design_digest does not match the fresh receipt")
	}
	if err := extraRoundValidateGrantBounds(grant); err != nil {
		return err
	}
	digest := grant.consumeDigest()
	if extraRoundGrantConsumed(wf, digest) {
		return extraRoundRefused("authorization already consumed")
	}
	return nil
}

func extraRoundShowView(wf *WorkflowRecord) map[string]any {
	if wf == nil || !extraRoundHasConsumption(wf) {
		return nil
	}
	last := wf.ExtraRoundConsumptions[len(wf.ExtraRoundConsumptions)-1]
	return map[string]any{
		"original_max_rounds":    wf.MaxRounds,
		"authorized_extra_round": wf.CurrentRound,
		"consumed":               last,
	}
}

func recordDesignLineageForResult(wf *WorkflowRecord, t *Task, fresh *WorkflowDesignNode, decision, observation, native, now string) {
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
}

func admitAuthorizedExtraRoundLocked(root string, cfg *Config, wf *WorkflowRecord, t *Task, decision, observation string, fresh *WorkflowDesignNode, authPath string, expandedDomain *WriteDomain) (*Task, error) {
	if decision != goalDecisionSuccessor {
		return nil, extraRoundRefused("extra-round authorization is only valid with -decision successor")
	}
	if observation == "paused" || observation == "user_paused" || observation == "needs-input" {
		return nil, extraRoundRefused("paused/needs-input uses same-session goal-run, not an extra-round writer")
	}
	if wf.CurrentRound < wf.MaxRounds {
		return nil, extraRoundRefused("workflow is not at the ordinary round ceiling")
	}
	if err := extraRoundRefuseIfAlreadyAdmitted(root, wf, t.ID); err != nil {
		return nil, err
	}
	if taskIsDispatchable(t) {
		return nil, extraRoundRefused("active writer is still dispatchable")
	}
	if !goalCustodyReleased(root, t) {
		return nil, extraRoundRefused("cannot open an extra-round successor while custody is held")
	}
	grant, err := loadExtraRoundAuthorization(authPath)
	if err != nil {
		return nil, err
	}
	if err := bindExtraRoundAuthorization(grant, wf, t, fresh); err != nil {
		return nil, err
	}

	now := time.Now().Format(time.RFC3339)
	native := ""
	if t.Goal != nil {
		native = t.Goal.LastNativeStatus
	}
	recordDesignLineageForResult(wf, t, fresh, decision, observation, native, now)
	if expandedDomain != nil {
		wf.WriteDomain = *expandedDomain
	}

	// Keep grant/consumption in memory until publish persists them with the extra
	// writer. A hook or saveTask failure therefore leaves the same grant retryable.
	wf.ExtraRoundGrants = append(wf.ExtraRoundGrants, *grant)
	wf.ExtraRoundConsumptions = append(wf.ExtraRoundConsumptions, workflowExtraRoundConsumption{
		Kind:         extraRoundKind,
		GrantDigest:  grant.consumeDigest(),
		DesignDigest: fresh.Digest,
		ConsumedAt:   now,
		Grant:        grant,
	})

	wf.Candidate = nil
	wf.ReviewerTaskID = ""
	wf.Review = nil
	wf.CurrentRound++

	goal := copyGoalContract(t.Goal)
	if goal == nil {
		goal = newTaskGoalBinding(wf, goalWriterManual, grant.BudgetTokens, grant.HardTimeoutSeconds)
	}
	goal.BudgetTokens = grant.BudgetTokens
	goal.HardTimeoutSec = grant.HardTimeoutSeconds
	goal.WriterMode = goalWriterManual
	goal.ExtraRoundAuthorized = true
	goal.Observation = goalObsUnstarted

	oldWriterID := t.ID
	next, err := publishWorkflowWriterWithOpts(root, cfg, wf, "", goal, true, workflowWriterPublishOpts{
		exceptLiveWriterID:        oldWriterID,
		rollbackTaskOnPersistFail: false,
		prepare: func(next *Task) {
			next.MaxAttempts = extraRoundMaxAttempts
			next.AutomaticCodex = false
			next.SessionID = ""
			next.ActiveAttemptID = ""
			next.Attempts = 0
			if next.Goal != nil {
				next.Goal.BudgetTokens = grant.BudgetTokens
				next.Goal.HardTimeoutSec = grant.HardTimeoutSeconds
				next.Goal.ExtraRoundAuthorized = true
				next.Goal.WriterMode = goalWriterManual
				next.Goal.Observation = goalObsUnstarted
			}
			if n := len(wf.ExtraRoundConsumptions); n > 0 {
				wf.ExtraRoundConsumptions[n-1].WriterTaskID = next.ID
				wf.ExtraRoundConsumptions[n-1].AdmittedRound = wf.CurrentRound
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}
