package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	grokPermissionPendingState   = "pending"
	grokPermissionAnsweredState  = "answered"
	grokPermissionCancelledState = "cancelled"
	grokPermissionDecisionAllow  = "allow-once"
	grokPermissionDecisionReject = "reject"
	grokPermissionDecisionCancel = "cancel"
)

var grokPermissionAnswerPoll = 50 * time.Millisecond

type grokPermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type grokPermissionIdentity struct {
	TaskID           string `json:"task_id"`
	SessionID        string `json:"session_id"`
	AttemptID        string `json:"attempt_id"`
	ToolCallID       string `json:"tool_call_id"`
	OperationDigest  string `json:"operation_digest"`
	JSONRPCRequestID string `json:"jsonrpc_request_id"`
	RequestID        string `json:"request_id"`
}

type grokPermissionPending struct {
	grokPermissionIdentity
	ToolName  string                 `json:"tool_name"`
	RawInput  json.RawMessage        `json:"raw_input,omitempty"`
	Options   []grokPermissionOption `json:"options"`
	State     string                 `json:"state"`
	CreatedAt string                 `json:"created_at"`
}

type grokPermissionAnswer struct {
	grokPermissionIdentity
	Decision   string `json:"decision"`
	AnsweredAt string `json:"answered_at"`
}

func grokPermissionOperationDigest(toolName string, rawInput json.RawMessage) string {
	sum := sha256.Sum256([]byte(toolName + "\x00" + string(bytesTrimJSON(rawInput))))
	return fmt.Sprintf("%x", sum[:])
}

func bytesTrimJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("null")
	}
	return raw
}

func grokPermissionDir(root, taskID, attemptID string) string {
	return filepath.Join(attemptsDir(root, taskID), attemptID+"-grok-permission")
}

func grokPermissionPendingPath(root, taskID, attemptID, requestID string) string {
	return filepath.Join(grokPermissionDir(root, taskID, attemptID), requestID+".json")
}

func grokPermissionAnswerPath(root, taskID, attemptID, requestID string) string {
	return filepath.Join(grokPermissionDir(root, taskID, attemptID), requestID+".answer.json")
}

func newGrokPermissionRequestID(ident grokPermissionIdentity) string {
	base := ident.OperationDigest
	if len(base) > 16 {
		base = base[:16]
	}
	rpc := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '.' {
			return '-'
		}
		return r
	}, ident.JSONRPCRequestID)
	if rpc == "" {
		rpc = "rpc"
	}
	return "req-" + base + "-" + rpc
}

func identitiesMatch(a, b grokPermissionIdentity) bool {
	return a.TaskID == b.TaskID &&
		a.SessionID == b.SessionID &&
		a.AttemptID == b.AttemptID &&
		a.ToolCallID == b.ToolCallID &&
		a.OperationDigest == b.OperationDigest &&
		a.JSONRPCRequestID == b.JSONRPCRequestID &&
		a.RequestID == b.RequestID
}

func persistGrokPermissionPending(root string, t *Task, pending grokPermissionPending) error {
	if t == nil || strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.ActiveAttemptID) == "" {
		return fmt.Errorf("Grok permission pending requires task and attempt identity")
	}
	if pending.RequestID == "" || pending.OperationDigest == "" || pending.ToolCallID == "" {
		return fmt.Errorf("Grok permission pending missing request identity")
	}
	pending.State = grokPermissionPendingState
	if pending.CreatedAt == "" {
		pending.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	dir := grokPermissionDir(root, t.ID, t.ActiveAttemptID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := grokPermissionPendingPath(root, t.ID, t.ActiveAttemptID, pending.RequestID)
	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	emitTaskEvent(root, t.ID, evGrokPermissionPending, "runner:grok-permission", t.Status, t.Step, map[string]any{
		"request_id":         pending.RequestID,
		"session_id":         pending.SessionID,
		"attempt_id":         pending.AttemptID,
		"tool_call_id":       pending.ToolCallID,
		"operation_digest":   pending.OperationDigest,
		"jsonrpc_request_id": pending.JSONRPCRequestID,
		"tool_name":          pending.ToolName,
	})
	if lg, logErr := openTaskLog(root, t.ID); logErr == nil {
		_, _ = fmt.Fprintf(lg, "--- GROK_PERMISSION_PENDING ---\n%s\n", data)
		_ = lg.Sync()
		_ = lg.Close()
	}
	return nil
}

func loadGrokPermissionPending(root, taskID, attemptID, requestID string) (*grokPermissionPending, error) {
	data, err := os.ReadFile(grokPermissionPendingPath(root, taskID, attemptID, requestID))
	if err != nil {
		return nil, err
	}
	var pending grokPermissionPending
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, err
	}
	return &pending, nil
}

func listGrokPermissionPending(root, taskID string) ([]grokPermissionPending, error) {
	var out []grokPermissionPending
	attemptsRoot := filepath.Join(controlDir(root), "attempts")
	taskFilter := strings.TrimSpace(taskID)
	entries, err := os.ReadDir(attemptsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		id := ent.Name()
		if taskFilter != "" && id != taskFilter {
			continue
		}
		taskDir := filepath.Join(attemptsRoot, id)
		subs, err := os.ReadDir(taskDir)
		if err != nil {
			continue
		}
		for _, sub := range subs {
			if !sub.IsDir() || !strings.HasSuffix(sub.Name(), "-grok-permission") {
				continue
			}
			permDir := filepath.Join(taskDir, sub.Name())
			files, err := os.ReadDir(permDir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") || strings.HasSuffix(f.Name(), ".answer.json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(permDir, f.Name()))
				if err != nil {
					continue
				}
				var pending grokPermissionPending
				if json.Unmarshal(data, &pending) != nil {
					continue
				}
				if pending.State == grokPermissionPendingState {
					out = append(out, pending)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].RequestID < out[j].RequestID
	})
	return out, nil
}

func findGrokPermissionPending(root, taskID, requestID string) (*grokPermissionPending, error) {
	all, err := listGrokPermissionPending(root, taskID)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].RequestID == requestID {
			p := all[i]
			return &p, nil
		}
	}
	return nil, fmt.Errorf("Grok permission pending %s not found", requestID)
}

func submitGrokPermissionAnswer(root string, want grokPermissionIdentity, decision string) error {
	switch decision {
	case grokPermissionDecisionAllow, grokPermissionDecisionReject, grokPermissionDecisionCancel:
	default:
		return fmt.Errorf("unsupported Grok permission decision %q (allow-once, reject, cancel)", decision)
	}
	if want.TaskID == "" || want.RequestID == "" {
		return fmt.Errorf("Grok permission answer requires task and request identity")
	}
	pending, err := findGrokPermissionPending(root, want.TaskID, want.RequestID)
	if err != nil {
		if want.AttemptID == "" {
			return fmt.Errorf("Grok permission pending not found: %w", err)
		}
		pending, err = loadGrokPermissionPending(root, want.TaskID, want.AttemptID, want.RequestID)
		if err != nil {
			return fmt.Errorf("Grok permission pending not found: %w", err)
		}
	}
	if pending.State != grokPermissionPendingState {
		return fmt.Errorf("Grok permission request %s is %s; a stale or duplicate answer cannot authorize it", pending.RequestID, pending.State)
	}
	got := pending.grokPermissionIdentity
	if got.TaskID != want.TaskID {
		return fmt.Errorf("Grok permission answer task mismatch")
	}
	if want.SessionID != "" && want.SessionID != got.SessionID {
		return fmt.Errorf("Grok permission answer session mismatch")
	}
	if want.AttemptID != "" && want.AttemptID != got.AttemptID {
		return fmt.Errorf("Grok permission answer attempt mismatch")
	}
	if want.ToolCallID != "" && want.ToolCallID != got.ToolCallID {
		return fmt.Errorf("Grok permission answer tool-call mismatch")
	}
	if want.OperationDigest != "" && want.OperationDigest != got.OperationDigest {
		return fmt.Errorf("Grok permission answer operation digest mismatch")
	}
	if want.JSONRPCRequestID != "" && want.JSONRPCRequestID != got.JSONRPCRequestID {
		return fmt.Errorf("Grok permission answer jsonrpc id mismatch")
	}
	ans := grokPermissionAnswer{
		grokPermissionIdentity: got,
		Decision:               decision,
		AnsweredAt:             time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(ans, "", "  ")
	if err != nil {
		return err
	}
	path := grokPermissionAnswerPath(root, got.TaskID, got.AttemptID, got.RequestID)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("Grok permission request %s already has an answer; duplicate/stale responses cannot authorize it", got.RequestID)
		}
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	pending.State = grokPermissionAnsweredState
	if decision == grokPermissionDecisionCancel {
		pending.State = grokPermissionCancelledState
	}
	updated, _ := json.MarshalIndent(pending, "", "  ")
	_ = os.WriteFile(grokPermissionPendingPath(root, got.TaskID, got.AttemptID, got.RequestID), append(updated, '\n'), 0o600)
	emitTaskEvent(root, got.TaskID, evGrokPermissionAnswered, "cli:grok-permission", "", 0, map[string]any{
		"request_id":       got.RequestID,
		"session_id":       got.SessionID,
		"attempt_id":       got.AttemptID,
		"tool_call_id":     got.ToolCallID,
		"operation_digest": got.OperationDigest,
		"decision":         decision,
	})
	return nil
}

func waitGrokPermissionAnswer(ctx context.Context, root string, pending grokPermissionPending) (*grokPermissionAnswer, error) {
	path := grokPermissionAnswerPath(root, pending.TaskID, pending.AttemptID, pending.RequestID)
	ticker := time.NewTicker(grokPermissionAnswerPoll)
	defer ticker.Stop()
	for {
		if ans, err := readVerifiedGrokPermissionAnswer(path, pending.grokPermissionIdentity); err == nil {
			return ans, nil
		} else if err != errGrokPermissionAnswerMissing && err != errGrokPermissionAnswerMismatch {
			return nil, err
		} else if err == errGrokPermissionAnswerMismatch {
			return nil, fmt.Errorf("permission denied: grok request_permission identity mismatch; stale/duplicate/mismatched answer cannot authorize this call")
		}
		select {
		case <-ctx.Done():
			emitTaskEvent(root, pending.TaskID, evGrokPermissionUnanswered, "runner:grok-permission", "", 0, map[string]any{
				"request_id":       pending.RequestID,
				"session_id":       pending.SessionID,
				"attempt_id":       pending.AttemptID,
				"tool_call_id":     pending.ToolCallID,
				"operation_digest": pending.OperationDigest,
				"reason":           "no-response",
			})
			return nil, fmt.Errorf("permission denied: grok request_permission unanswered")
		case <-ticker.C:
		}
	}
}

var (
	errGrokPermissionAnswerMissing  = fmt.Errorf("grok permission answer missing")
	errGrokPermissionAnswerMismatch = fmt.Errorf("grok permission answer identity mismatch")
)

func readVerifiedGrokPermissionAnswer(path string, want grokPermissionIdentity) (*grokPermissionAnswer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errGrokPermissionAnswerMissing
		}
		return nil, err
	}
	var ans grokPermissionAnswer
	if err := json.Unmarshal(data, &ans); err != nil {
		return nil, errGrokPermissionAnswerMismatch
	}
	if !identitiesMatch(ans.grokPermissionIdentity, want) {
		return nil, errGrokPermissionAnswerMismatch
	}
	switch ans.Decision {
	case grokPermissionDecisionAllow, grokPermissionDecisionReject, grokPermissionDecisionCancel:
		return &ans, nil
	default:
		return nil, errGrokPermissionAnswerMismatch
	}
}

func grokPermissionOptionID(options []grokPermissionOption, kind string) (string, bool) {
	for _, opt := range options {
		if opt.Kind == kind && strings.TrimSpace(opt.OptionID) != "" {
			return opt.OptionID, true
		}
	}
	return "", false
}

func grokPermissionJSONRPCResult(jsonrpcID json.RawMessage, decision string, options []grokPermissionOption) (map[string]any, error) {
	var id any
	if len(jsonrpcID) > 0 && json.Unmarshal(jsonrpcID, &id) != nil {
		id = string(jsonrpcID)
	}
	switch decision {
	case grokPermissionDecisionAllow:
		optID, ok := grokPermissionOptionID(options, "allow_once")
		if !ok {
			return nil, fmt.Errorf("permission denied: grok request_permission has no allow_once option; refusing wildcard/always grants")
		}
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"outcome": map[string]any{
					"outcome":  "selected",
					"optionId": optID,
				},
			},
		}, nil
	case grokPermissionDecisionReject:
		optID, ok := grokPermissionOptionID(options, "reject_once")
		if !ok {
			return grokPermissionJSONRPCCancelled(id), nil
		}
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result": map[string]any{
				"outcome": map[string]any{
					"outcome":  "selected",
					"optionId": optID,
				},
			},
		}, nil
	default:
		return grokPermissionJSONRPCCancelled(id), nil
	}
}

func grokPermissionJSONRPCCancelled(id any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"outcome": map[string]any{
				"outcome": "cancelled",
			},
		},
	}
}

func emitGrokAllowPolicyEvent(root string, t *Task, policy grokAllowPolicy) {
	if t == nil {
		return
	}
	emitTaskEvent(root, t.ID, evGrokAllowPolicy, "runner:grok-allow", t.Status, t.Step, map[string]any{
		"requested":  policy.Requested,
		"consumed":   policy.Consumed,
		"attempt_id": t.ActiveAttemptID,
	})
}
