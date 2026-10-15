package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestConsumeGrokFrozenAllowRules(t *testing.T) {
	t.Parallel()
	frozen := []string{"Read", "Grep", "Glob", "Bash(git log:*)", "Bash(git diff:*)", "Bash(git show:*)", "Bash(git status:*)", "Bash(ls:*)"}
	policy, err := consumeGrokFrozenAllowRules(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(policy.Requested, ",") != strings.Join(frozen, ",") {
		t.Fatalf("requested=%v", policy.Requested)
	}
	if strings.Join(policy.Consumed, ",") != strings.Join(frozen, ",") {
		t.Fatalf("consumed=%v", policy.Consumed)
	}
	subset, err := consumeGrokFrozenAllowRules([]string{"Read", "Bash(git status:*)"})
	if err != nil || len(subset.Consumed) != 2 {
		t.Fatalf("subset: %+v err=%v", subset, err)
	}
	if _, err := consumeGrokFrozenAllowRules([]string{"Read", "Bash(git *)"}); err == nil {
		t.Fatal("broad git prefix must fail closed")
	}
	if _, err := consumeGrokFrozenAllowRules([]string{"Bash(git rev-parse:*)"}); err == nil {
		t.Fatal("rev-parse is outside frozen rules and must fail closed")
	}
	if _, err := consumeGrokFrozenAllowRules([]string{" Read"}); err == nil {
		t.Fatal("whitespace-altered grammar must fail closed")
	}
}

func TestInvokeGrokBuildForwardsFrozenAllowRules(t *testing.T) {
	t.Parallel()
	frozen := []string{"Read", "Grep", "Glob", "Bash(git log:*)", "Bash(git diff:*)", "Bash(git show:*)", "Bash(git status:*)", "Bash(ls:*)"}
	payload := `{"type":"text","data":"GROK_OK"}` + "\n" +
		`{"type":"end","stopReason":"end_turn","sessionId":"session-grok","num_turns":1}`
	for n := 0; n < 2; n++ {
		t.Run("pass", func(t *testing.T) {
			t.Parallel()
			bin, argsDump, _ := fakeGrokBuild(t, payload, "", 0)
			cfg := grokBuildTestConfig(t, bin)
			task := &Task{
				ID: uniqueTaskID("grok-allow-forward"), Type: typeReview, Dir: t.TempDir(),
				PreferRunner: grokBuildRunnerName, AllowedTools: append([]string(nil), frozen...),
			}
			root := admitDirectInvoke(t, "", task)
			task.LastProviderPreflight = &ProviderPreflightReadback{Runner: grokBuildRunnerName, State: providerReady}
			res, _, err := invokeGrokBuild(context.Background(), root, cfg, task, "harmless prompt")
			if err != nil || res == nil || res.Result != "GROK_OK" {
				t.Fatalf("invoke failed: res=%+v err=%v", res, err)
			}
			if res.GrokAllowPolicy == nil || strings.Join(res.GrokAllowPolicy.Consumed, ",") != strings.Join(frozen, ",") {
				t.Fatalf("requested-vs-consumed missing: %+v", res.GrokAllowPolicy)
			}
			args := string(mustRead(t, argsDump))
			argv := strings.Split(strings.TrimSuffix(args, "\n"), "\n")
			if grokBuildArgvHasPrivilegeWidening(argv) {
				t.Fatalf("widened privilege:\n%s", args)
			}
			if !strings.Contains(args, "--sandbox\nread-only\n") || !strings.Contains(args, "--permission-mode\nplan\n") {
				t.Fatalf("read-only plan missing:\n%s", args)
			}
			if grokBuildArgIndex(argv, "stdio") < 0 {
				t.Fatalf("expected ACP stdio:\n%s", args)
			}
			for _, rule := range frozen {
				if !strings.Contains(args, "--allow\n"+rule+"\n") && !strings.HasSuffix(args, "--allow\n"+rule) {
					t.Fatalf("missing --allow %s:\n%s", rule, args)
				}
			}
			if strings.Contains(args, "Bash(git *)") || strings.Contains(args, "rev-parse") || strings.Contains(args, "cat-file") || strings.Contains(args, "merge-base") {
				t.Fatalf("broad or held-review git grant leaked:\n%s", args)
			}
			if grokBuildArgIndex(argv, "--always-approve") >= 0 || grokBuildArgIndex(argv, "--yolo") >= 0 {
				t.Fatalf("auto-approve present:\n%s", args)
			}
		})
	}
}

func TestInvokeGrokBuildUnknownAllowGrammarFailsClosed(t *testing.T) {
	t.Parallel()
	bin, productCalls := fakeGrokBuildCounted(t, `{"type":"end","stopReason":"end_turn"}`, "", 0)
	cfg := grokBuildTestConfig(t, bin)
	task := &Task{
		ID: uniqueTaskID("grok-allow-unknown"), Type: typeReview, Dir: t.TempDir(),
		PreferRunner: grokBuildRunnerName, AllowedTools: []string{"Read", "Bash(git rev-parse:*)"},
	}
	root := admitDirectInvoke(t, "", task)
	task.LastProviderPreflight = &ProviderPreflightReadback{Runner: grokBuildRunnerName, State: providerReady}
	_, _, err := invokeGrokBuild(context.Background(), root, cfg, task, "harmless prompt")
	if err == nil || !strings.Contains(err.Error(), "fail closed") {
		t.Fatalf("want fail closed, got %v", err)
	}
	if n := countProductCalls(t, productCalls); n != 0 {
		t.Fatalf("product started after unknown grammar: %d", n)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGrokBuildProductArgsOtherRunnersUntouched(t *testing.T) {
	t.Parallel()
	cfg := grokBuildTestConfig(t, "/usr/bin/true")
	seq := &Task{Type: typeSequence, SessionID: "s1"}
	args, acp, _, err := grokBuildProductArgs(cfg, seq, "grok-4.6", "xhigh", "/tmp/p")
	if err != nil || acp {
		t.Fatalf("sequence must stay headless: acp=%v err=%v", acp, err)
	}
	joined := strings.Join(args, "\n")
	if !strings.Contains(joined, "--prompt-file") || strings.Contains(joined, "stdio") {
		t.Fatalf("sequence argv: %v", args)
	}
}
