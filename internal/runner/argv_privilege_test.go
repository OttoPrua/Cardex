package runner

import "testing"

func TestPrivilegeFlagPositionStillRejects(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		cursor bool
		want   bool
	}{
		{name: "grok_model_yolo", args: []string{"grok", "--model", "--yolo", "--permission-mode", "auto"}, want: false},
		{name: "grok_flag_yolo", args: []string{"grok", "--yolo", "--model", "m"}, want: true},
		{name: "grok_flag_always_approve", args: []string{"grok", "--always-approve"}, want: true},
		{name: "grok_flag_skip", args: []string{"grok", "--skip-permissions"}, want: true},
		{name: "grok_flag_dangerously", args: []string{"grok", "--dangerously-skip-permissions"}, want: true},
		{name: "grok_permission_yolo", args: []string{"grok", "--permission-mode", "yolo"}, want: true},
		{name: "grok_permission_bypass", args: []string{"grok", "--permission-mode", "bypassPermissions"}, want: true},
		{name: "grok_permission_dont_ask", args: []string{"grok", "--permission-mode", "dontAsk"}, want: true},
		{name: "grok_permission_always", args: []string{"grok", "--permission-mode", "always-approve"}, want: true},
		{name: "grok_permission_auto", args: []string{"grok", "--permission-mode", "auto"}, want: false},
		{name: "grok_prompt_file_yolo", args: []string{"grok", "--prompt-file", "--yolo"}, want: false},
		{name: "grok_cwd_force", args: []string{"grok", "--cwd", "--force"}, want: false},
		{name: "grok_sandbox_yolo", args: []string{"grok", "--sandbox", "--yolo"}, want: false},
		{name: "cursor_body_force", args: []string{"cursor-agent", "--model", "m", "--sandbox", "enabled", "--force"}, cursor: true, want: false},
		{name: "cursor_flag_force", args: []string{"cursor-agent", "--force", "--model", "m", "body"}, cursor: true, want: true},
		{name: "cursor_flag_yolo", args: []string{"cursor-agent", "--yolo", "body"}, cursor: true, want: true},
		{name: "cursor_flag_auto_review", args: []string{"cursor-agent", "--auto-review", "body"}, cursor: true, want: true},
		{name: "cursor_model_yolo", args: []string{"cursor-agent", "--model", "--yolo", "body"}, cursor: true, want: false},
		{name: "cursor_workspace_force", args: []string{"cursor-agent", "--workspace", "--force", "body"}, cursor: true, want: false},
		{name: "cursor_output_format_yolo", args: []string{"cursor-agent", "--output-format", "--yolo", "body"}, cursor: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := grokPrivilegeWidening(tc.args)
			if tc.cursor {
				got = cursorPrivilegeWidening(tc.args)
			}
			if got != tc.want {
				t.Fatalf("widening %v want %v for %q", got, tc.want, tc.args)
			}
		})
	}
}
