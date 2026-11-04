package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// This is a redacted native session/update from Grok 1.0.40, projected with
// the headless reducer's six-field mapping. Original stdout was not retained.
// The terminal runner's earlier completion is synthetic; its final status and
// rawOutput are followed by the tool layer's content-bearing completion.
func grok140TerminalUpdates(t *testing.T) (string, string, string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/grok-1.0.40-terminal-update.json")
	if err != nil {
		t.Fatal(err)
	}
	var native map[string]any
	if err := json.Unmarshal(raw, &native); err != nil {
		t.Fatal(err)
	}
	projected := map[string]any{
		"type": "tool_call_update", "toolCallId": native["toolCallId"],
		"status": native["status"], "rawOutput": native["rawOutput"],
		"content": native["content"], "locations": []any{},
	}
	final, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	projected["content"] = []any{}
	early, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	call := `{"status":"pending","toolCallId":"call-terminal","toolName":"run_terminal_command","type":"tool_call"}`
	return call, string(early), string(final)
}

func TestGrok140TerminalCompletionMayBeEnriched(t *testing.T) {
	t.Parallel()
	call, early, final := grok140TerminalUpdates(t)
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			stream := call + "\n" + strings.ReplaceAll(early+"\n"+final, `"completed"`, `"`+status+`"`)
			stream += "\n" + `{"type":"text","data":"DONE"}` + "\n" + grok105PublicEnd
			res := parseGrokBuildJSONL(stream)
			if res.IsError || !res.ObservationComplete || res.ToolEvents != 3 || res.TerminalEvents != 1 || res.Result != "DONE" {
				t.Fatalf("valid repeated terminal status rejected: %+v diagnostics=%+v", res, res.GrokDiagnostics)
			}
			if res.GrokDiagnostics.RejectSchema != "none" {
				t.Fatalf("valid update retained a rejection: %+v", res.GrokDiagnostics)
			}
		})
	}
}

func TestGrok140TerminalEnrichmentStillFailsClosed(t *testing.T) {
	t.Parallel()
	call, early, final := grok140TerminalUpdates(t)
	for name, bad := range map[string]string{
		"unknown ID":                strings.ReplaceAll(final, "call-terminal", "unknown-call"),
		"empty ID":                  strings.ReplaceAll(final, "call-terminal", ""),
		"reopen":                    strings.ReplaceAll(final, `"completed"`, `"in_progress"`),
		"contradictory terminal":    strings.ReplaceAll(final, `"completed"`, `"failed"`),
		"unknown status":            strings.ReplaceAll(final, `"completed"`, `"finished"`),
		"null status after closure": strings.ReplaceAll(final, `"completed"`, `null`),
		"wrong ID type":             strings.ReplaceAll(final, `"call-terminal"`, `17`),
		"unknown field":             strings.TrimSuffix(final, "}") + `,"extra":true}`,
		"duplicate call":            call,
	} {
		t.Run(name, func(t *testing.T) {
			res := parseGrokBuildJSONL(call + "\n" + early + "\n" + bad + "\n" + grok105PublicEnd)
			if !res.IsError || res.ObservationComplete {
				t.Fatalf("malformed sequence accepted: %+v", res)
			}
		})
	}
	res := parseGrokBuildJSONL(call + "\n" + early + "\n" + grok105PublicEnd + "\n" + final)
	if !res.IsError || res.ObservationComplete {
		t.Fatalf("post-end tool work accepted: %+v", res)
	}
}
