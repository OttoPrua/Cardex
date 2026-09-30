package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	sessionRoleDesign  = "design"
	sessionRoleManager = "manager"
	sessionProvGrok    = "grok"
	sessionProvCodex   = "codex"
	sessionProvHermes  = "hermes"
	sessionTypeGrok    = "grok_session"
	sessionTypeCodex   = "codex_thread"
	sessionTypeHermes  = "hermes_session"
	designRequestSchema = "cardex.workflow.design_request.v1"
	designReplySchema   = "cardex.workflow.design_reply.v1"
)

// PersistentSessionRef binds a real provider session. Prompt text is not the
// database. Unsupported types must not be stored as Codex UUIDs.
type PersistentSessionRef struct {
	Role          string `json:"role"`
	Provider      string `json:"provider"`
	SessionType   string `json:"session_type"`
	SessionID     string `json:"session_id"`
	Profile       string `json:"profile,omitempty"`
	ContextVer    int64  `json:"context_version,omitempty"`
	LastRequestID string `json:"last_request_id,omitempty"`
	LastReplyID   string `json:"last_reply_id,omitempty"`
	OwnerEntry    bool   `json:"owner_entry,omitempty"`
	RecoverPath   string `json:"recover_path,omitempty"`
	BoundAt       string `json:"bound_at,omitempty"`
}

type designRequestRecord struct {
	Schema         string `json:"schema"`
	RequestID      string `json:"request_id"`
	WorkflowID     string `json:"workflow_id"`
	Provider       string `json:"provider"`
	SessionType    string `json:"session_type"`
	SessionID      string `json:"session_id"`
	ContextVer     int64  `json:"context_version"`
	InputDigest    string `json:"input_digest,omitempty"`
	Candidate      string `json:"candidate,omitempty"`
	PriorRequestID string `json:"prior_request_id,omitempty"`
	PromptDigest   string `json:"prompt_digest"`
	ReplyID        string `json:"reply_id,omitempty"`
	ReplyPath      string `json:"reply_path,omitempty"`
	StdoutPath     string `json:"stdout_path,omitempty"`
	At             string `json:"at"`
}

type designReplyRecord struct {
	Schema      string `json:"schema"`
	RequestID   string `json:"request_id"`
	ReplyID     string `json:"reply_id"`
	SessionID   string `json:"session_id"`
	Provider    string `json:"provider"`
	Judgment    string `json:"judgment,omitempty"`
	StdoutDigest string `json:"stdout_digest"`
	At          string `json:"at"`
}

func normalizePersistentSessionRef(ref *PersistentSessionRef) error {
	if ref == nil {
		return nil
	}
	ref.Role = strings.ToLower(strings.TrimSpace(ref.Role))
	ref.Provider = strings.ToLower(strings.TrimSpace(ref.Provider))
	ref.SessionType = strings.ToLower(strings.TrimSpace(ref.SessionType))
	ref.SessionID = strings.TrimSpace(ref.SessionID)
	ref.Profile = strings.TrimSpace(ref.Profile)
	switch ref.Role {
	case sessionRoleDesign, sessionRoleManager:
	default:
		return fmt.Errorf("%w: session role %q", errWorkflowMalformed, ref.Role)
	}
	switch ref.Provider {
	case sessionProvGrok, sessionProvCodex, sessionProvHermes:
	default:
		return fmt.Errorf("%w: session provider %q", errWorkflowMalformed, ref.Provider)
	}
	wantType := ""
	switch ref.Provider {
	case sessionProvGrok:
		wantType = sessionTypeGrok
		if !managerWakeThreadRE.MatchString(ref.SessionID) {
			return fmt.Errorf("%w: grok session id must be a UUID, not a Codex stand-in for another provider", errWorkflowMalformed)
		}
	case sessionProvCodex:
		wantType = sessionTypeCodex
		if !managerWakeThreadRE.MatchString(ref.SessionID) {
			return fmt.Errorf("%w: codex thread id must be a UUID", errWorkflowMalformed)
		}
	case sessionProvHermes:
		wantType = sessionTypeHermes
		if !managerWakeHermesSessionRE.MatchString(ref.SessionID) {
			return fmt.Errorf("%w: hermes session id %q is not a Codex UUID and must not be stored as one", errWorkflowMalformed, ref.SessionID)
		}
		if managerWakeThreadRE.MatchString(ref.SessionID) {
			return fmt.Errorf("%w: hermes session must not be disguised as a Codex UUID", errWorkflowMalformed)
		}
	}
	if ref.SessionType == "" {
		ref.SessionType = wantType
	}
	if ref.SessionType != wantType {
		return fmt.Errorf("%w: provider %s cannot use session_type %s", errWorkflowMalformed, ref.Provider, ref.SessionType)
	}
	if ref.SessionID == "" {
		return fmt.Errorf("%w: session_id required", errWorkflowMalformed)
	}
	if ref.BoundAt == "" {
		ref.BoundAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return nil
}

func bindWorkflowSession(root string, cfg *Config, wf *WorkflowRecord, ref PersistentSessionRef) error {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return err
	}
	if err := normalizePersistentSessionRef(&ref); err != nil {
		return err
	}
	switch ref.Role {
	case sessionRoleDesign:
		if wf.DesignSession != nil && wf.DesignSession.SessionID != "" && wf.DesignSession.SessionID != ref.SessionID && !ref.OwnerEntry {
			return fmt.Errorf("%w: design session already bound to %s", errWorkflowMalformed, wf.DesignSession.SessionID)
		}
		if ref.OwnerEntry {
			wf.OwnerDesignEntry = &ref
		} else {
			if wf.DesignSession != nil {
				ref.ContextVer = wf.DesignSession.ContextVer
				ref.LastRequestID = wf.DesignSession.LastRequestID
				ref.LastReplyID = wf.DesignSession.LastReplyID
			}
			wf.DesignSession = &ref
		}
	case sessionRoleManager:
		if wf.ManagerSession != nil && wf.ManagerSession.SessionID != "" && wf.ManagerSession.SessionID != ref.SessionID {
			return fmt.Errorf("%w: manager session already bound to %s", errWorkflowMalformed, wf.ManagerSession.SessionID)
		}
		wf.ManagerSession = &ref
	}
	return persistWorkflow(root, cfg, wf)
}

func designRequestsDir(root, wfID string) string {
	return filepath.Join(workflowsDir(root), wfID+".design-requests")
}

func submitWorkflowDesignRequest(root string, cfg *Config, wf *WorkflowRecord, prompt, inputDigest, candidate string) (*designRequestRecord, error) {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return nil, err
	}
	if wf.DesignSession == nil {
		return nil, fmt.Errorf("%w: bind a design session first", errGoalDesignProof)
	}
	if wf.DesignSession.OwnerEntry {
		return nil, fmt.Errorf("%w: owner design entry is not the executing consumer", errGoalDesignProof)
	}
	if err := normalizePersistentSessionRef(wf.DesignSession); err != nil {
		return nil, err
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("%w: empty design prompt", errGoalDesignProof)
	}
	if wf.Candidate != nil && strings.TrimSpace(candidate) == "" {
		candidate = wf.Candidate.Commit
	}
	if wf.DesignLineage != nil && wf.DesignLineage.LatestValid != nil {
		prev := strings.TrimSpace(wf.DesignLineage.LatestValid.Digest)
		if prev != "" && inputDigest != "" && prev != inputDigest {
			// Old PASS cannot be reused against new evidence.
			wf.DesignSession.LastReplyID = ""
		}
	}
	wf.DesignSession.ContextVer++
	sum := sha256.Sum256([]byte(prompt))
	req := &designRequestRecord{
		Schema:         designRequestSchema,
		RequestID:      fmt.Sprintf("dr%08x", time.Now().UnixNano()&0xffffffff),
		WorkflowID:     wf.ID,
		Provider:       wf.DesignSession.Provider,
		SessionType:    wf.DesignSession.SessionType,
		SessionID:      wf.DesignSession.SessionID,
		ContextVer:     wf.DesignSession.ContextVer,
		InputDigest:    strings.TrimSpace(inputDigest),
		Candidate:      strings.TrimSpace(candidate),
		PriorRequestID: wf.DesignSession.LastRequestID,
		PromptDigest:   hex.EncodeToString(sum[:]),
		At:             time.Now().UTC().Format(time.RFC3339Nano),
	}
	dir := designRequestsDir(root, wf.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	promptPath := filepath.Join(dir, req.RequestID+".prompt.txt")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return nil, err
	}
	stdout, err := deliverDesignPrompt(cfg, wf.DesignSession, prompt, req)
	if err != nil {
		return nil, err
	}
	stdoutPath := filepath.Join(dir, req.RequestID+".stdout.txt")
	if err := os.WriteFile(stdoutPath, []byte(stdout), 0o644); err != nil {
		return nil, err
	}
	req.StdoutPath = stdoutPath
	outSum := sha256.Sum256([]byte(stdout))
	req.ReplyID = hex.EncodeToString(outSum[:])
	reply := designReplyRecord{
		Schema:       designReplySchema,
		RequestID:    req.RequestID,
		ReplyID:      req.ReplyID,
		SessionID:    req.SessionID,
		Provider:     req.Provider,
		StdoutDigest: req.ReplyID,
		At:           time.Now().UTC().Format(time.RFC3339Nano),
	}
	replyPath := filepath.Join(dir, req.RequestID+".reply.json")
	raw, err := json.MarshalIndent(reply, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(replyPath, append(raw, '\n'), 0o644); err != nil {
		return nil, err
	}
	req.ReplyPath = replyPath
	reqRaw, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, req.RequestID+".json"), append(reqRaw, '\n'), 0o644); err != nil {
		return nil, err
	}
	wf.DesignSession.LastRequestID = req.RequestID
	wf.DesignSession.LastReplyID = req.ReplyID
	wf.DesignSession.RecoverPath = filepath.Join(dir, req.RequestID+".json")
	if err := persistWorkflow(root, cfg, wf); err != nil {
		return nil, err
	}
	return req, nil
}

func deliverDesignPrompt(cfg *Config, ref *PersistentSessionRef, prompt string, req *designRequestRecord) (string, error) {
	if ref == nil {
		return "", fmt.Errorf("%w: missing design session", errGoalDesignProof)
	}
	switch ref.Provider {
	case sessionProvGrok:
		return grokDesignAsk(cfg, ref, prompt)
	case sessionProvCodex:
		return "", fmt.Errorf("%w: codex design send is queue-only from this executor; bind a grok or hermes executing design session, keep the Codex thread as owner_entry", errGoalDesignProof)
	case sessionProvHermes:
		return "", fmt.Errorf("%w: hermes design collect is owned by the manager receive path; bind grok executing design or wait for B inbound", errGoalDesignProof)
	default:
		return "", fmt.Errorf("%w: provider %s", errGoalDesignProof, ref.Provider)
	}
}

func grokDesignAsk(cfg *Config, ref *PersistentSessionRef, prompt string) (string, error) {
	if cfg == nil || strings.TrimSpace(cfg.GrokBuildBin) == "" {
		return "", fmt.Errorf("%w: grok_build_bin required for design session", errGoalCapability)
	}
	cwd, _ := os.Getwd()
	if cfg != nil {
		// keep the caller's cwd; design sessions are not product writers
	}
	home := defaultGrokHome()
	args := []string{"--no-auto-update", "--disable-web-search", "--no-subagents", "--cwd", cwd, "-p", prompt}
	sessDir := grokGoalSessionDir(home, cwd, ref.SessionID)
	if _, err := os.Stat(sessDir); err == nil {
		args = append(args, "--resume", ref.SessionID)
	} else {
		args = append(args, "-s", ref.SessionID)
	}
	cmd := exec.Command(cfg.GrokBuildBin, args...)
	cmd.Dir = cwd
	userHome, _ := os.UserHomeDir()
	cmd.Env = providerChildEnv(userHome, map[string]string{"GROK_HOME": home, "GROK_WORKFLOWS": "1"})
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("grok design session: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func collectWorkflowDesignReply(root string, cfg *Config, wf *WorkflowRecord, requestID string) (*designReplyRecord, error) {
	if err := refreshWorkflow(root, cfg, wf); err != nil {
		return nil, err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" && wf.DesignSession != nil {
		requestID = wf.DesignSession.LastRequestID
	}
	if requestID == "" {
		return nil, fmt.Errorf("%w: request id required", errGoalDesignResult)
	}
	path := filepath.Join(designRequestsDir(root, wf.ID), requestID+".reply.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: design reply missing (saved summary is not a judgment): %v", errGoalDesignResult, err)
	}
	var reply designReplyRecord
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	if reply.Schema != designReplySchema || reply.RequestID != requestID {
		return nil, fmt.Errorf("%w: stale or foreign design reply", errGoalDesignResult)
	}
	if wf.DesignSession != nil && reply.SessionID != wf.DesignSession.SessionID {
		return nil, fmt.Errorf("%w: design reply session mismatch", errGoalDesignResult)
	}
	return &reply, nil
}
