package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type acpJSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type acpRequestPermissionParams struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string          `json:"toolCallId"`
		Title      string          `json:"title"`
		Kind       string          `json:"kind"`
		Status     string          `json:"status"`
		ToolName   string          `json:"toolName"`
		RawInput   json.RawMessage `json:"rawInput"`
	} `json:"toolCall"`
	Options []grokPermissionOption `json:"options"`
}

type acpSessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
	} `json:"update"`
}

func invokeGrokBuildACP(ctx context.Context, root string, cfg *Config, t *Task, home, prompt string, args []string, policy grokAllowPolicy) (*claudeResult, string, error) {
	if grokBuildArgvHasPrivilegeWidening(args) {
		return nil, "", fmt.Errorf("Grok ACP invoke refused privilege-widening flags")
	}
	emitGrokAllowPolicyEvent(root, t, policy)

	runCtx, cancel := context.WithTimeout(ctx, grokBuildStepTimeout(cfg))
	defer cancel()
	cmd := providerCommandContext(runCtx, cfg.GrokBuildBin, args...)
	setupProcGroup(cmd)
	cmd.Dir = t.Dir
	cmd.Env = providerChildEnv(home, nil)

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, "", err
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, "", err
	}
	var stderr bytes.Buffer
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = &stderr

	var (
		acpRes      *claudeResult
		acpCombined string
		acpErr      error
	)
	processErr := runCmdRegisteredHarvestForTaskWorkspace(cmd, nil, t.ID, t.Dir, func(_ *exec.Cmd) {
		_ = stdoutW.Close()
		_ = stdinR.Close()
		acpRes, acpCombined, acpErr = runGrokACPClient(runCtx, root, t, prompt, stdinW, stdoutR)
		_ = stdinW.Close()
	})
	_ = stdoutR.Close()
	_ = stdinW.Close()
	_ = stdoutW.Close()
	_ = stdinR.Close()

	runErr := processErr
	processTimedOut := false
	if runCtx.Err() == context.DeadlineExceeded {
		processTimedOut = true
		if runErr == nil {
			runErr = fmt.Errorf("步骤超时（%d 分钟）", cfg.StepTimeoutMin)
		} else {
			runErr = fmt.Errorf("步骤超时（%d 分钟）: %w", cfg.StepTimeoutMin, runErr)
		}
	}
	if processErr != nil && stderr.Len() > 0 {
		if err := retainGrokBuildStderr(root, t, stderr.String()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: Grok stderr evidence capture incomplete for task %s: %v\n", t.ID, err)
		}
	}
	combined := acpCombined
	if stderr.Len() > 0 {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr.String()
	}
	res := acpRes
	if res == nil {
		res = &claudeResult{IsError: true, ObservationComplete: true, Subtype: "grok_build_acp_incomplete"}
	}
	res.GrokAllowPolicy = &policy
	if acpErr != nil && (runErr == nil || runCtx.Err() == context.DeadlineExceeded) {
		if runErr == nil {
			runErr = acpErr
		}
		res.IsError = true
		if res.Result == "" {
			res.Result = acpErr.Error()
		}
		if res.Subtype == "" || res.Subtype == "grok_build_acp_incomplete" {
			res.Subtype = grokPermissionResultSubtype(acpErr)
		}
	}
	if res.IsError && runErr == nil {
		runErr = fmt.Errorf("Grok Build 返回错误: %s", summarizeResult(res.Result))
	}
	if res.GrokDiagnostics == nil {
		res.GrokDiagnostics = &grokBuildDiagnostics{}
	}
	res.GrokDiagnostics.Subtype = grokBuildDiagnosticSubtype(res.Subtype)
	res.GrokDiagnostics.process(cmd, processErr, processTimedOut)
	return res, combined, runErr
}

func grokPermissionResultSubtype(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unanswered"):
		return "grok_build_permission_unanswered"
	case strings.Contains(msg, "disconnect") || strings.Contains(msg, "crash"):
		return "grok_build_permission_disconnected"
	case strings.Contains(msg, "cancel"):
		return "grok_build_permission_cancelled"
	default:
		return "grok_build_permission_rejected"
	}
}

func runGrokACPClient(ctx context.Context, root string, t *Task, prompt string, stdin *os.File, stdout *os.File) (*claudeResult, string, error) {
	res := &claudeResult{ObservationComplete: true}
	var combined strings.Builder
	var combinedMu sync.Mutex
	var stdinMu sync.Mutex
	enc := json.NewEncoder(stdin)
	enc.SetEscapeHTML(false)
	nextID := 1
	waiters := make(map[string]chan acpJSONRPCMessage)
	var waitMu sync.Mutex
	var permWG sync.WaitGroup
	readerDone := make(chan struct{})
	readCtx, readCancel := context.WithCancel(ctx)
	defer readCancel()

	appendCombined := func(b []byte) {
		combinedMu.Lock()
		combined.Write(b)
		if len(b) == 0 || b[len(b)-1] != '\n' {
			combined.WriteByte('\n')
		}
		combinedMu.Unlock()
	}
	writeMsg := func(v any) error {
		stdinMu.Lock()
		defer stdinMu.Unlock()
		if err := enc.Encode(v); err != nil {
			return err
		}
		b, _ := json.Marshal(v)
		appendCombined(b)
		return nil
	}
	idKey := func(raw json.RawMessage) string {
		return strings.TrimSpace(string(raw))
	}

	go func() {
		defer close(readerDone)
		defer readCancel()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), grokBuildStderrScanMaxToken)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			appendCombined([]byte(line))
			var msg acpJSONRPCMessage
			if json.Unmarshal([]byte(line), &msg) != nil {
				continue
			}
			switch {
			case msg.Method == "session/request_permission":
				permWG.Add(1)
				go func(m acpJSONRPCMessage) {
					defer permWG.Done()
					_ = handleGrokACPPermission(readCtx, root, t, writeMsg, m, res)
				}(msg)
			case msg.Method == "session/update":
				applyGrokACPSessionUpdate(res, msg.Params)
			case len(msg.ID) > 0 && msg.Method == "":
				waitMu.Lock()
				ch := waiters[idKey(msg.ID)]
				waitMu.Unlock()
				if ch != nil {
					select {
					case ch <- msg:
					case <-ctx.Done():
					}
				}
			}
		}
	}()

	sendRequest := func(method string, params any) (json.RawMessage, error) {
		id := nextID
		nextID++
		rawID, _ := json.Marshal(id)
		ch := make(chan acpJSONRPCMessage, 1)
		waitMu.Lock()
		waiters[idKey(rawID)] = ch
		waitMu.Unlock()
		if err := writeMsg(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  method,
			"params":  params,
		}); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return nil, fmt.Errorf("permission denied: grok request_permission unanswered")
			}
			return nil, ctx.Err()
		case <-readerDone:
			return nil, fmt.Errorf("permission denied: grok ACP disconnected before %s completed", method)
		case reply := <-ch:
			if len(reply.Error) > 0 && string(reply.Error) != "null" {
				return nil, fmt.Errorf("Grok ACP %s error: %s", method, string(reply.Error))
			}
			return reply.Result, nil
		}
	}

	if _, err := sendRequest("initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{"readTextFile": false, "writeTextFile": false},
		},
		"clientInfo": map[string]any{"name": "cardex", "version": version},
	}); err != nil {
		return res, combinedSnapshot(&combined, &combinedMu), err
	}

	sessionID := t.SessionID
	var err error
	var result json.RawMessage
	if sessionID != "" {
		result, err = sendRequest("session/load", map[string]any{
			"sessionId":  sessionID,
			"cwd":        t.Dir,
			"mcpServers": []any{},
		})
	} else {
		result, err = sendRequest("session/new", map[string]any{
			"cwd":        t.Dir,
			"mcpServers": []any{},
		})
	}
	if err != nil {
		return res, combinedSnapshot(&combined, &combinedMu), err
	}
	sessionID = grokACPSessionID(result, sessionID)
	if sessionID == "" {
		return res, combinedSnapshot(&combined, &combinedMu), fmt.Errorf("Grok ACP session id missing")
	}
	res.SessionID = sessionID
	t.SessionID = sessionID

	promptResult, err := sendRequest("session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": prompt}},
	})
	permWG.Wait()
	if err != nil {
		if res.IsError && res.Result != "" {
			return res, combinedSnapshot(&combined, &combinedMu), fmt.Errorf("%s", res.Result)
		}
		return res, combinedSnapshot(&combined, &combinedMu), err
	}
	applyGrokACPPromptResult(res, promptResult)
	if res.IsError && res.Result != "" {
		return res, combinedSnapshot(&combined, &combinedMu), fmt.Errorf("%s", res.Result)
	}
	return res, combinedSnapshot(&combined, &combinedMu), nil
}

func combinedSnapshot(b *strings.Builder, mu *sync.Mutex) string {
	mu.Lock()
	defer mu.Unlock()
	return b.String()
}

func grokACPSessionID(result json.RawMessage, fallback string) string {
	var parsed struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(result, &parsed) == nil && strings.TrimSpace(parsed.SessionID) != "" {
		return parsed.SessionID
	}
	return fallback
}

func applyGrokACPSessionUpdate(res *claudeResult, raw json.RawMessage) {
	if res == nil || len(raw) == 0 {
		return
	}
	var params acpSessionUpdateParams
	if json.Unmarshal(raw, &params) != nil {
		return
	}
	switch params.Update.SessionUpdate {
	case "agent_message_chunk":
		if params.Update.Content.Text != "" {
			res.Result += params.Update.Content.Text
			res.SemanticEvents++
			res.ModelEvents++
		}
	case "agent_thought_chunk":
		res.ModelEvents++
	case "tool_call", "tool_call_update":
		res.ToolEvents++
	}
}

func applyGrokACPPromptResult(res *claudeResult, raw json.RawMessage) {
	if res == nil {
		return
	}
	var parsed struct {
		StopReason string `json:"stopReason"`
		SessionID  string `json:"sessionId"`
	}
	if json.Unmarshal(raw, &parsed) == nil {
		if parsed.StopReason != "" {
			res.FinalReason = parsed.StopReason
		}
		if parsed.SessionID != "" {
			res.SessionID = parsed.SessionID
		}
	}
	res.TerminalEvents++
	res.ObservationComplete = true
	if res.NumTurns == 0 {
		res.NumTurns = 1
		res.TurnsPresent = true
	}
}

func handleGrokACPPermission(ctx context.Context, root string, t *Task, writeMsg func(any) error, msg acpJSONRPCMessage, res *claudeResult) error {
	var params acpRequestPermissionParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return fmt.Errorf("permission denied: grok request_permission params invalid")
	}
	toolName := strings.TrimSpace(params.ToolCall.ToolName)
	if toolName == "" {
		toolName = strings.TrimSpace(params.ToolCall.Title)
	}
	if toolName == "" {
		toolName = strings.TrimSpace(params.ToolCall.Kind)
	}
	sessionID := params.SessionID
	if sessionID == "" {
		sessionID = t.SessionID
	}
	ident := grokPermissionIdentity{
		TaskID:           t.ID,
		SessionID:        sessionID,
		AttemptID:        t.ActiveAttemptID,
		ToolCallID:       params.ToolCall.ToolCallID,
		OperationDigest:  grokPermissionOperationDigest(toolName, params.ToolCall.RawInput),
		JSONRPCRequestID: strings.TrimSpace(string(msg.ID)),
	}
	ident.RequestID = newGrokPermissionRequestID(ident)
	pending := grokPermissionPending{
		grokPermissionIdentity: ident,
		ToolName:               toolName,
		RawInput:               params.ToolCall.RawInput,
		Options:                params.Options,
	}
	if err := persistGrokPermissionPending(root, t, pending); err != nil {
		return err
	}
	ans, waitErr := waitGrokPermissionAnswer(ctx, root, pending)
	decision := grokPermissionDecisionCancel
	if waitErr != nil {
		decision = grokPermissionDecisionCancel
		res.IsError = true
		res.Subtype = grokPermissionResultSubtype(waitErr)
		res.Result = waitErr.Error()
	} else {
		decision = ans.Decision
		if decision != grokPermissionDecisionAllow {
			res.IsError = true
			if decision == grokPermissionDecisionCancel {
				res.Subtype = "grok_build_permission_cancelled"
			} else {
				res.Subtype = "grok_build_permission_rejected"
			}
			res.Result = "permission denied: grok request_permission " + decision
		}
	}
	rpc, rpcErr := grokPermissionJSONRPCResult(msg.ID, decision, params.Options)
	if rpcErr != nil {
		rpc = grokPermissionJSONRPCCancelled(jsonRPCIDValue(msg.ID))
		res.IsError = true
		res.Subtype = "grok_build_permission_rejected"
		res.Result = rpcErr.Error()
	}
	if err := writeMsg(rpc); err != nil {
		return err
	}
	if res.IsError {
		return fmt.Errorf("%s", res.Result)
	}
	return nil
}

func jsonRPCIDValue(raw json.RawMessage) any {
	var id any
	if json.Unmarshal(raw, &id) != nil {
		return string(raw)
	}
	return id
}
