package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
)

func cmdGrokPermission(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("用法: cardex grok-permission list|answer [-root ROOT] ...")
	}
	switch args[0] {
	case "list":
		return cmdGrokPermissionList(args[1:])
	case "answer":
		return cmdGrokPermissionAnswer(args[1:])
	default:
		return fmt.Errorf("用法: cardex grok-permission list|answer [-root ROOT] ...")
	}
}

func cmdGrokPermissionList(args []string) error {
	fs := flag.NewFlagSet("grok-permission list", flag.ExitOnError)
	rootFlag := fs.String("root", "", "数据目录")
	asJSON := fs.Bool("json", false, "JSON 输出")
	_ = fs.Parse(args)
	root := resolveRoot(*rootFlag)
	taskID := ""
	if fs.NArg() >= 1 {
		taskID = strings.TrimSpace(fs.Arg(0))
	}
	pending, err := listGrokPermissionPending(root, taskID)
	if err != nil {
		return err
	}
	if *asJSON {
		data, err := json.MarshalIndent(pending, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if len(pending) == 0 {
		fmt.Println("no pending Grok request_permission")
		return nil
	}
	for _, p := range pending {
		fmt.Printf("%s task=%s session=%s attempt=%s tool=%s digest=%s jsonrpc=%s\n",
			p.RequestID, p.TaskID, p.SessionID, p.AttemptID, p.ToolCallID, p.OperationDigest, p.JSONRPCRequestID)
	}
	return nil
}

func cmdGrokPermissionAnswer(args []string) error {
	fs := flag.NewFlagSet("grok-permission answer", flag.ExitOnError)
	rootFlag := fs.String("root", "", "数据目录")
	requestID := fs.String("request", "", "pending request id")
	decision := fs.String("decision", "", "allow-once, reject, or cancel")
	sessionID := fs.String("session", "", "optional session identity check")
	attemptID := fs.String("attempt", "", "optional attempt identity check")
	toolCallID := fs.String("tool-call", "", "optional tool-call identity check")
	digest := fs.String("digest", "", "optional operation digest check")
	_ = fs.Parse(args)
	if fs.NArg() < 1 || strings.TrimSpace(*requestID) == "" || strings.TrimSpace(*decision) == "" {
		return fmt.Errorf("用法: cardex grok-permission answer -request ID -decision allow-once|reject|cancel [-root ROOT] TASK")
	}
	root := resolveRoot(*rootFlag)
	want := grokPermissionIdentity{
		TaskID:          strings.TrimSpace(fs.Arg(0)),
		RequestID:       strings.TrimSpace(*requestID),
		SessionID:       strings.TrimSpace(*sessionID),
		AttemptID:       strings.TrimSpace(*attemptID),
		ToolCallID:      strings.TrimSpace(*toolCallID),
		OperationDigest: strings.TrimSpace(*digest),
	}
	if err := submitGrokPermissionAnswer(root, want, strings.TrimSpace(*decision)); err != nil {
		return err
	}
	fmt.Printf("answered %s %s\n", want.RequestID, *decision)
	return nil
}
