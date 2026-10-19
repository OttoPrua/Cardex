package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func kimiCLITestConfig(t *testing.T, bin string) *Config {
	t.Helper()
	cfg := defaultConfig("")
	cfg.DefaultRunner = "codex"
	cfg.CodexBin = "/usr/bin/true"
	cfg.KimiCLIBin = bin
	cfg.KimiCLIHome = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.KimiCLIHome, "config.toml"), []byte("default_model = \"kimi-code/k3\"\n\n[providers.\"managed:kimi-code\"]\ntype = \"kimi\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.KimiCLIHome, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.KimiCLIModel = "kimi-code/k3"
	cfg.KimiCLIEffort = "max"
	cfg.StepTimeoutMin = 1
	cfg.CooldownMarginSec = 0
	cfg.KimiCLIOpus = &KimiCLIOpusRoute{
		Enabled: true, ExcludeBackend: true, Model: "kimi-code/k3", Effort: "max", LimitFallbackMin: 180,
	}
	cfg.GrokBuildBin = "/usr/bin/true"
	cfg.GrokBuild = &GrokBuildRoute{
		Enabled: true, Model: "grok-4.6", Effort: "xhigh", KimiOpusFallback: true,
		CodexFallbackModel: "gpt-5.6-sol", CodexFallbackEffort: "xhigh",
		ReviewCodexModel: "gpt-5.6-sol", ReviewCodexEffort: "max", OpusAdversarialReview: true,
		TierRoutes: map[string]GrokTierRoute{
			"opus_backend": {Effort: "xhigh", CodexFallbackModel: "gpt-5.6-sol", CodexFallbackEffort: "max"},
			"sonnet":       {Effort: "high", CodexFallbackModel: "gpt-5.6-luna", CodexFallbackEffort: "max"},
			"haiku":        {Effort: "high", CodexFallbackModel: "gpt-5.6-luna", CodexFallbackEffort: "xhigh"},
		},
	}
	return cfg
}

func TestParseKimiCLIJSONL(t *testing.T) {
	t.Parallel()
	raw := `{"role":"meta","type":"system.version","version":"0.35.0"}` + "\n" +
		`{"role":"assistant","content":"OK"}` + "\n" +
		`{"role":"meta","type":"session.resume_hint","session_id":"session-1"}`
	res := parseKimiCLIJSONL(raw)
	if res.Result != "OK" || res.SessionID != "session-1" || res.NumTurns != 1 || res.IsError {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestParseKimiCLIJSONLUnknownVersionRecordsResult(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"0.42.0", "9.9.9"} {
		raw := `{"role":"meta","type":"system.version","version":"` + version + `"}` + "\n" +
			`{"role":"assistant","content":"OK"}`
		res := parseKimiCLIJSONL(raw)
		if res.Result != "OK" {
			t.Fatalf("%s assistant text must land in Result: %+v", version, res)
		}
		if res.NativeVersion != version {
			t.Fatalf("%s must be recorded, got %q", version, res.NativeVersion)
		}
		if res.Subtype == kimiCLISubtypeInvalidPostamble {
			t.Fatalf("%s must not be invalid_completion_postamble: %+v", version, res)
		}
		if !runnerNativeTerminalValid(kimiCLIRunnerName, res, nil) {
			t.Fatalf("%s complete stream must be a valid terminal: %+v", version, res)
		}
	}
	missing := parseKimiCLIJSONL(`{"role":"assistant","content":"OK"}`)
	if missing.Result != "OK" || missing.Subtype != kimiCLISubtypeProtocolIncomplete {
		t.Fatalf("missing version + assistant must be protocol_incomplete with Result: %+v", missing)
	}
	if missing.Subtype == kimiCLISubtypeInvalidPostamble {
		t.Fatal("versionCount!=1 must not hard-invalidate nonempty Result as invalid_completion_postamble")
	}
}

func TestParseKimiCLIJSONLUnknownMetadataFailsProofClosed(t *testing.T) {
	t.Parallel()
	res := parseKimiCLIJSONL(`{"role":"meta","type":"model.started"}`)
	if res.ObservationComplete {
		t.Fatalf("unknown metadata must not be assumed presemantic: %+v", res)
	}
}

func TestKimiCLIOpusSecondLegAvailabilityAndBackendExclusion(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	cfg := kimiCLITestConfig(t, "/usr/bin/true")
	for _, hour := range []int{0, 3, 12, 23} {
		task := &Task{Model: "opus", PreferRunner: "codex", Type: typeSequence, Dir: t.TempDir(), Prompts: []string{"实现前端页面"}}
		now := time.Date(2026, 8, 13, hour, 0, 0, 0, time.Local)
		if !kimiCLIOpusEligible(root, cfg, task, now) {
			t.Fatalf("%02d:00 非后端 Opus 的 Kimi 第二腿应可用", hour)
		}
	}

	for name, task := range map[string]*Task{
		"显式后端": {Model: "opus", PreferRunner: "codex", Type: typeSequence, RouteClass: routeClassBackend, Prompts: []string{"实现功能"}},
		"存量词项": {Model: "opus", PreferRunner: "codex", Type: typeSequence, Prompts: []string{"实现数据库迁移和 API endpoint"}},
		"人工消歧": {Model: "opus", PreferRunner: "codex", Type: typeSequence, RouteClass: routeClassGeneral, Prompts: []string{"评估后端方案但只改文档"}},
	} {
		t.Run(name, func(t *testing.T) {
			excluded := kimiCLIBackendExcluded(cfg, task)
			if name == "人工消歧" && excluded {
				t.Fatal("route_class=general 必须覆盖文本误命中")
			}
			if name != "人工消歧" && !excluded {
				t.Fatal("后端开发必须排除 Kimi 自动路由")
			}
		})
	}

	sonnet := &Task{Model: "sonnet", PreferRunner: "codex", Type: typeSequence, Prompts: []string{"实现前端"}}
	if kimiCLIOpusEligible(root, cfg, sonnet, time.Now()) {
		t.Fatal("Sonnet 不得进入 Opus Kimi 路由")
	}
}

func fakeKimiCLI(t *testing.T, payload string, exitCode int) (bin, argsDump, envDump string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "kimi")
	argsDump = filepath.Join(dir, "args.dump")
	envDump = filepath.Join(dir, "env.dump")
	payloadPath := filepath.Join(dir, "result.jsonl")
	if err := os.WriteFile(payloadPath, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + shSingleQuote(argsDump) + "\n" +
		"printf '%s\\n' \"$KIMI_MODEL_THINKING_EFFORT\" \"$KIMI_CODE_NO_AUTO_UPDATE\" \"$KIMI_CODE_HOME\" > " + shSingleQuote(envDump) + "\n" +
		"cat " + shSingleQuote(payloadPath) + "\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsDump, envDump
}

func TestInvokeKimiCLIUsesK3MaxAndVersionCompatibleFlags(t *testing.T) {
	t.Parallel()
	payload := `{"role":"meta","type":"system.version","version":"0.35.0"}` + "\n" +
		`{"role":"assistant","content":"OK"}` + "\n" +
		`{"role":"meta","type":"session.resume_hint","session_id":"session-ok"}`
	bin, argsDump, envDump := fakeKimiCLI(t, payload, 0)
	cfg := kimiCLITestConfig(t, bin)
	task := &Task{ID: "kimi-k3-max", Model: "opus", PreferRunner: "codex", Effort: "xhigh", Type: typeSequence, Dir: t.TempDir()}
	root := testRoot(t)
	admitDirectInvoke(t, root, task)
	res, combined, err := invokeKimiCLI(context.Background(), root, cfg, task, "p")
	if err != nil || res == nil || res.Result != "OK" || res.SessionID != "session-ok" {
		t.Fatalf("invoke failed: res=%+v err=%v", res, err)
	}
	if strings.Contains(combined, "COLD-START TRANSPORT RETRY") {
		t.Fatalf("0.35 semantic stream must not be replayed:\n%s", combined)
	}
	args, _ := os.ReadFile(argsDump)
	got := string(args)
	for _, want := range []string{"--model\nkimi-code/k3\n", "--prompt\np\n", "--output-format\nstream-json\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Kimi CLI argv 缺 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--auto\n") || strings.Contains(got, "--yolo\n") || strings.Contains(got, "--plan\n") {
		t.Fatalf("Kimi CLI 0.35.0 禁止 --prompt 与 --auto/--yolo/--plan 同用:\n%s", got)
	}
	env, _ := os.ReadFile(envDump)
	envLines := strings.Split(strings.TrimSpace(string(env)), "\n")
	executeHome := filepath.Join(root, "kimi-cli-home", "execute")
	if len(envLines) != 3 || envLines[0] != "max" || envLines[1] != "1" || envLines[2] != executeHome {
		t.Fatalf("K3 max 必须通过官方环境变量注入, env=%q", env)
	}
	runtimeConfig, err := os.ReadFile(filepath.Join(executeHome, "config.toml"))
	if err != nil || !strings.Contains(string(runtimeConfig), `default_permission_mode = "auto"`) ||
		!strings.Contains(string(runtimeConfig), `default_plan_mode = false`) {
		t.Fatalf("Cardex 隔离配置必须启用 prompt 模式自动权限: err=%v config=%q", err, runtimeConfig)
	}
}

func TestInvokeKimiCLIReviewUsesIsolatedConfiguredPlanMode(t *testing.T) {
	t.Parallel()
	payload := `{"role":"assistant","content":"REVIEW_OK"}`
	bin, argsDump, envDump := fakeKimiCLI(t, payload, 0)
	cfg := kimiCLITestConfig(t, bin)
	task := &Task{ID: "kimi-review", Model: "opus", Type: "review", Dir: t.TempDir()}
	root := testRoot(t)
	admitDirectInvoke(t, root, task)
	res, _, err := invokeKimiCLI(context.Background(), root, cfg, task, "review")
	if err != nil || res == nil || res.Result != "REVIEW_OK" {
		t.Fatalf("review invoke failed: res=%+v err=%v", res, err)
	}
	args, _ := os.ReadFile(argsDump)
	if got := string(args); strings.Contains(got, "--plan\n") || strings.Contains(got, "--auto\n") || strings.Contains(got, "--yolo\n") {
		t.Fatalf("prompt mode must not receive incompatible permission flags:\n%s", got)
	}
	reviewHome := filepath.Join(root, "kimi-cli-home", "review")
	env, _ := os.ReadFile(envDump)
	if !strings.HasSuffix(strings.TrimSpace(string(env)), reviewHome) {
		t.Fatalf("review must use its isolated runtime home: %q", env)
	}
	runtimeConfig, err := os.ReadFile(filepath.Join(reviewHome, "config.toml"))
	if err != nil || !strings.Contains(string(runtimeConfig), `default_permission_mode = "auto"`) ||
		!strings.Contains(string(runtimeConfig), `default_plan_mode = true`) {
		t.Fatalf("review runtime must enable configured plan mode: err=%v config=%q", err, runtimeConfig)
	}
	executeConfig := filepath.Join(root, "kimi-cli-home", "execute", "config.toml")
	if _, err := os.Stat(executeConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("review must not create or mutate the execute runtime: %v", err)
	}
}

func TestInvokeKimiCLI0361DoesNotReplayMetadataOnlyColdStart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "kimi")
	countPath := filepath.Join(dir, "count")
	script := "#!/bin/sh\n" +
		"count=0\n" +
		"if [ -f " + shSingleQuote(countPath) + " ]; then count=$(cat " + shSingleQuote(countPath) + "); fi\n" +
		"count=$((count + 1))\n" +
		"printf '%s' \"$count\" > " + shSingleQuote(countPath) + "\n" +
		"printf '%s\\n' '{\"role\":\"meta\",\"type\":\"system.version\",\"version\":\"0.36.1\"}'\n" +
		"if [ \"$count\" -eq 1 ]; then exit 1; fi\n" +
		"printf '%s\\n' '{\"role\":\"assistant\",\"content\":\"KIMI_0361_OK\"}'\n" +
		"printf '%s\\n' '{\"role\":\"meta\",\"type\":\"session.resume_hint\",\"session_id\":\"session-0361\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := kimiCLITestConfig(t, bin)
	task := &Task{ID: "kimi-0361-cold-start", Model: "opus", Type: typeSequence, Dir: t.TempDir()}
	root := admitDirectInvoke(t, "", task)
	res, combined, err := invokeKimiCLI(context.Background(), root, cfg, task, "read only")
	if err == nil || res == nil || res.TerminalEvents != 0 || strings.Contains(combined, "KIMI_0361_OK") {
		t.Fatalf("metadata-only child failure must stay incomplete, with no transport restart: res=%+v err=%v", res, err)
	}
	count, err := os.ReadFile(countPath)
	if err != nil || string(count) != "1" {
		t.Fatalf("expected one child only: count=%q err=%v", count, err)
	}

}

func TestValidateKimiCLIOpenFileLimit(t *testing.T) {
	t.Parallel()
	if err := validateKimiCLIOpenFileLimit(256); err == nil ||
		!strings.Contains(err.Error(), "EMFILE") || !strings.Contains(err.Error(), "65536") {
		t.Fatalf("low launchd ceiling must fail with actionable EMFILE diagnosis: %v", err)
	}
	if err := validateKimiCLIOpenFileLimit(65536); err != nil {
		t.Fatalf("recommended ceiling must pass: %v", err)
	}
}

func TestKimiCLIEMFILEStderrOverridesVersionMetadata(t *testing.T) {
	t.Parallel()
	stdout := `{"role":"meta","type":"system.version","version":"0.36.1"}`
	stderr := "[unexpected] Error: EMFILE: too many open files, watch\n"
	runErr := errors.New("exit status 1")
	res := parseKimiCLIJSONL(stdout)
	preserveKimiCLIProcessError(res, stderr, runErr)
	got := errorSummary(res, stdout+"\n"+stderr, runErr)
	if !strings.Contains(got, "EMFILE: too many open files, watch") {
		t.Fatalf("last_error must surface concrete stderr root cause, got %q", got)
	}
	if strings.Contains(got, "system.version") {
		t.Fatalf("system.version is metadata and must not mask root cause, got %q", got)
	}
}

func TestInvokeKimiCLI0361PreservesEMFILEStderr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "kimi")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"role\":\"meta\",\"type\":\"system.version\",\"version\":\"0.36.1\"}'\n" +
		"printf '%s\\n' '[unexpected] Error: EMFILE: too many open files, watch' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := kimiCLITestConfig(t, bin)
	task := &Task{ID: "kimi-0361-emfile", Model: "opus", Type: typeSequence, Dir: dir}
	root := admitDirectInvoke(t, "", task)
	res, combined, runErr := invokeKimiCLI(context.Background(), root, cfg, task, "read only")
	if runErr == nil || res == nil || !res.IsError ||
		!strings.Contains(errorSummary(res, combined, runErr), "EMFILE: too many open files, watch") {
		t.Fatalf("EMFILE stderr must survive version metadata: res=%+v err=%v\n%s", res, runErr, combined)
	}
	if strings.Contains(combined, "COLD-START TRANSPORT RETRY") {
		t.Fatalf("concrete stderr must not trigger metadata-only retry:\n%s", combined)
	}
}

func TestRunTaskKimiCLILimitHoldsWithoutGlobalSolFallback(t *testing.T) {
	t.Parallel()
	root := testRoot(t)
	payload := `{"role":"meta","type":"error","content":"HTTP 429: usage limit reached"}`
	bin, _, _ := fakeKimiCLI(t, payload, 1)
	cfg := kimiCLITestConfig(t, bin)
	cfg.OwnerRoutingEnforced = true
	task := newTask(root, cfg, typeSequence, "kimi limit", t.TempDir(), []string{"p"}, 1)
	task.Model = "opus"
	task.RouteClass = routeClassGeneral
	route, ok := resolveOwnerRoute(cfg, task)
	if !ok || !pinOwnerPrimaryRoute(task, route) {
		t.Fatal("无法钉定 Grok 第一腿")
	}
	task.Runner = grokBuildRunnerName
	if err := queuePolicyFallback(cfg, task, fallbackTransport, v3ProofAuthorization()); err != nil {
		t.Fatal(err)
	}
	task.Runner = kimiCLIRunnerName
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, kimiCLIRunnerName); err != nil {
		t.Fatal(err)
	}
	got, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != statusHeld || got.RouteReason != routeReasonGrokToKimi ||
		got.PreferRunner != kimiCLIRunnerName || got.CodexModel != "" || got.OwnerRouteLeg != 2 ||
		got.SessionID != "" || got.Attempts != 0 || !strings.Contains(got.LastError, "global Codex fallback disabled") {
		t.Fatalf("Kimi 安全限额必须保持第二腿并关闭全局 Sol 回退: %+v", got)
	}
	cd := loadEngineCooldown(root, kimiCLICooldownName)
	if cd == nil || !cd.active(time.Now()) || cd.UntilEpoch < time.Now().Add(179*time.Minute).Unix() {
		t.Fatalf("应写独立 Kimi CLI 车道冷却约 180 分钟: %+v", cd)
	}
	if _, err := os.Stat(cooldownPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Kimi CLI 限额不得写 Claude 全局冷却: %v", err)
	}
	events := readAllEventsRaw(t, root, task.ID)
	last := events[len(events)-1]
	if last.Type != evHeld || last.Actor != "runner:policy-fallback" ||
		last.Detail["reason"] != "no_resolver_proven_next_leg" ||
		last.Detail["workspace_fingerprint_before"] == "" ||
		last.Detail["workspace_fingerprint_before"] != last.Detail["workspace_fingerprint_after"] {
		t.Fatalf("应留下 Kimi CLI 无解析下一腿的三证持有事件: %+v", last)
	}
}

func TestKimiCLIBoardShowsActualModelEffortAndRoute(t *testing.T) {
	t.Parallel()
	cfg := kimiCLITestConfig(t, "/usr/bin/true")
	now := time.Now()
	queued := &Task{ID: "queued", Status: statusQueued, PreferRunner: "codex", Model: "opus", Effort: "xhigh", Type: typeSequence, Prompts: []string{"前端实现"}}
	brief := toBrief(cfg, queued, now)
	if brief.Runner != grokBuildRunnerName || brief.RunnerSource != "route_policy" ||
		brief.Model != "grok-4.6" || brief.ModelSource != "grok_model" ||
		brief.Effort != "xhigh" || brief.EffortSource != "grok_effort" || brief.RouteReason != routeReasonGrokOpusGeneral ||
		!strings.Contains(brief.ModelRoute, "Grok Build 4.6/xhigh →（仅安全失败）Kimi CLI K3/max") {
		t.Fatalf("未派发 Opus/general 卡必须显示 Grok→Kimi 开头的全路由: %+v", brief)
	}

	backend := &Task{ID: "backend", Status: statusQueued, PreferRunner: "codex", Model: "opus", Effort: "xhigh", Type: typeSequence, RouteClass: routeClassBackend, Prompts: []string{"后端实现"}}
	brief = toBrief(cfg, backend, now)
	if brief.Runner != grokBuildRunnerName || brief.Model != "grok-4.6" || brief.Effort != "xhigh" || brief.RouteReason != routeReasonGrokOpusBackend {
		t.Fatalf("后端 Opus 必须显示 Grok/xhigh 主腿: %+v", brief)
	}
}

func TestValidateKimiCLIConfig(t *testing.T) {
	t.Parallel()
	cfg := kimiCLITestConfig(t, "/usr/bin/true")
	cfg.KimiCLIRuntimeHome = "  /tmp/kimi-runtime-parent  "
	if err := validateKimiCLI(cfg); err != nil {
		t.Fatalf("合法 Kimi CLI 策略被拒: %v", err)
	}
	if cfg.KimiCLIRuntimeHome != "/tmp/kimi-runtime-parent" {
		t.Fatalf("kimi_cli_runtime_home 应去掉空白, got %q", cfg.KimiCLIRuntimeHome)
	}
	missingGrok := kimiCLITestConfig(t, "/usr/bin/true")
	missingGrok.GrokBuild = nil
	if err := validateKimiCLI(missingGrok); err == nil {
		t.Fatal("Kimi 自动路由缺 Grok 下一腿必须 fail-fast")
	}
	cfg.KimiCLIOpus.Effort = "ultra"
	if err := validateKimiCLI(cfg); err == nil {
		t.Fatal("非法 Kimi effort 必须 fail-fast")
	}
}

func TestKimiCLIRealCanary(t *testing.T) {
	t.Parallel()
	if os.Getenv("CARDEX_KIMI_REAL_CANARY") != "1" {
		t.Skip("set CARDEX_KIMI_REAL_CANARY=1 for the authenticated integration canary")
	}
	bin := strings.TrimSpace(os.Getenv("CARDEX_KIMI_BIN"))
	home := strings.TrimSpace(os.Getenv("CARDEX_KIMI_HOME"))
	if bin == "" || home == "" {
		t.Fatal("CARDEX_KIMI_BIN and CARDEX_KIMI_HOME are required")
	}
	cfg := defaultConfig("")
	cfg.KimiCLIBin = bin
	cfg.KimiCLIHome = home
	cfg.KimiCLIModel = "kimi-code/k3"
	cfg.KimiCLIEffort = "max"
	cfg.StepTimeoutMin = 2
	task := &Task{
		ID: "kimi-real-canary", Type: typeSequence, Dir: t.TempDir(), Model: "opus",
		Prompts: []string{"run pwd"}, PreferRunner: "codex",
	}
	root := admitDirectInvoke(t, "", task)
	res, _, err := invokeKimiCLI(context.Background(), root, cfg, task,
		"这是 Cardex Kimi CLI 原生执行器测试。运行 pwd，不做任何写入，然后只回复 KIMI_CARDEX_NATIVE_OK。")
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !strings.Contains(res.Result, "KIMI_CARDEX_NATIVE_OK") {
		t.Fatalf("unexpected Kimi result: %+v", res)
	}
}

func TestKimiCLIRealReviewCanary(t *testing.T) {
	t.Parallel()
	if os.Getenv("CARDEX_KIMI_REAL_REVIEW_CANARY") != "1" {
		t.Skip("set CARDEX_KIMI_REAL_REVIEW_CANARY=1 for the authenticated review canary")
	}
	bin := strings.TrimSpace(os.Getenv("CARDEX_KIMI_BIN"))
	home := strings.TrimSpace(os.Getenv("CARDEX_KIMI_HOME"))
	if bin == "" || home == "" {
		t.Fatal("CARDEX_KIMI_BIN and CARDEX_KIMI_HOME are required")
	}
	cfg := defaultConfig("")
	cfg.KimiCLIBin = bin
	cfg.KimiCLIHome = home
	cfg.KimiCLIModel = "kimi-code/k3"
	cfg.KimiCLIEffort = "max"
	cfg.StepTimeoutMin = 2
	task := &Task{
		ID: "kimi-real-review-canary", Type: "review", Dir: t.TempDir(), Model: "opus",
		Prompts: []string{"review pwd"}, PreferRunner: "codex",
	}
	root := testRoot(t)
	admitDirectInvoke(t, root, task)
	res, combined, err := invokeKimiCLI(context.Background(), root, cfg, task,
		"这是 Cardex Kimi CLI 只读复盘执行器测试。运行 pwd，不做任何写入，然后只回复 KIMI_CARDEX_REVIEW_OK。")
	if err != nil {
		t.Fatalf("review canary failed: %v\n%s", err, combined)
	}
	if res == nil || !strings.Contains(res.Result, "KIMI_CARDEX_REVIEW_OK") {
		t.Fatalf("unexpected Kimi review result: %+v", res)
	}
	runtimeConfig, err := os.ReadFile(filepath.Join(root, "kimi-cli-home", "review", "config.toml"))
	if err != nil || !strings.Contains(string(runtimeConfig), `default_plan_mode = true`) {
		t.Fatalf("real review canary did not use configured plan mode: err=%v config=%q", err, runtimeConfig)
	}
}

func kimiCLIIsolationPayload() string {
	return `{"role":"meta","type":"system.version","version":"0.35.0"}` + "\n" +
		`{"role":"assistant","content":"ISOLATION_OK"}` + "\n" +
		`{"role":"meta","type":"session.resume_hint","session_id":"session-iso"}`
}

func kimiCLIEnvHome(t *testing.T, envDump string) string {
	t.Helper()
	env, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(env)), "\n")
	if len(lines) == 0 {
		t.Fatalf("empty kimi env dump: %q", env)
	}
	return lines[len(lines)-1]
}

func kimiCLIWriteSourceToken(t *testing.T, sourceHome, secret string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(sourceHome, "credentials", "token"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
}

func kimiCLIAssertCredentialReference(t *testing.T, runtimeHome, sourceHome, secret string) {
	t.Helper()
	sourceCred := filepath.Join(sourceHome, "credentials")
	destCred := filepath.Join(runtimeHome, "credentials")
	info, err := os.Lstat(destCred)
	if err != nil {
		t.Fatalf("runtime credentials missing: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("credentials must be a directory symlink, mode=%v", info.Mode())
	}
	target, err := os.Readlink(destCred)
	if err != nil || target != sourceCred {
		t.Fatalf("credentials symlink target=%q want=%q err=%v", target, sourceCred, err)
	}
	err = filepath.WalkDir(runtimeHome, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), secret) {
			t.Fatalf("OAuth token material copied into runtime file %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestKimiCLIIsolationRuntimeParentResolution(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := t.TempDir()
	cfg := &Config{}
	got, err := resolveKimiCLIRuntimeHome(root, cfg, false)
	if err != nil || got != filepath.Join(root, "kimi-cli-home", "execute") {
		t.Fatalf("unset execute: got=%q err=%v", got, err)
	}
	got, err = resolveKimiCLIRuntimeHome(root, cfg, true)
	if err != nil || got != filepath.Join(root, "kimi-cli-home", "review") {
		t.Fatalf("unset review: got=%q err=%v", got, err)
	}
	cfg.KimiCLIRuntimeHome = parent
	got, err = resolveKimiCLIRuntimeHome(root, cfg, false)
	if err != nil || got != filepath.Join(parent, "execute") {
		t.Fatalf("configured execute: got=%q err=%v", got, err)
	}
	if got == filepath.Join(root, "kimi-cli-home", "execute") {
		t.Fatal("configured parent must not fall back to cardex-root isolation")
	}
	got, err = resolveKimiCLIRuntimeHome(root, cfg, true)
	if err != nil || got != filepath.Join(parent, "review") {
		t.Fatalf("configured review: got=%q err=%v", got, err)
	}
	missing := filepath.Join(t.TempDir(), "absent-mount")
	cfg.KimiCLIRuntimeHome = missing
	if _, err := resolveKimiCLIRuntimeHome(root, cfg, false); err == nil ||
		!strings.Contains(err.Error(), "隔离运行父目录不可用") {
		t.Fatalf("missing configured parent must fail closed: %v", err)
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing mount must not be manufactured: %v", statErr)
	}
	fileParent := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.KimiCLIRuntimeHome = fileParent
	if _, err := resolveKimiCLIRuntimeHome(root, cfg, false); err == nil ||
		!strings.Contains(err.Error(), "不是目录") {
		t.Fatalf("file parent must fail closed: %v", err)
	}
}

func TestKimiCLIIsolationConfiguredParentInvoke(t *testing.T) {
	t.Parallel()
	payload := kimiCLIIsolationPayload()
	bin, argsDump, envDump := fakeKimiCLI(t, payload, 0)
	cfg := kimiCLITestConfig(t, bin)
	secret := "kimi-iso-secret-not-for-copy"
	kimiCLIWriteSourceToken(t, cfg.KimiCLIHome, secret)
	parent := t.TempDir()
	cfg.KimiCLIRuntimeHome = parent
	sourceSnapshot, err := os.ReadFile(filepath.Join(cfg.KimiCLIHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	task := &Task{ID: "kimi-iso-exec", Model: "opus", PreferRunner: "codex", Type: typeSequence, Dir: t.TempDir()}
	root := testRoot(t)
	admitDirectInvoke(t, root, task)
	res, _, err := invokeKimiCLI(context.Background(), root, cfg, task, "p")
	if err != nil || res == nil || res.Result != "ISOLATION_OK" || res.SessionID != "session-iso" {
		t.Fatalf("configured invoke failed: res=%+v err=%v", res, err)
	}
	runtimeHome := filepath.Join(parent, "execute")
	if got := kimiCLIEnvHome(t, envDump); got != runtimeHome {
		t.Fatalf("KIMI_CODE_HOME=%q want runtime %q", got, runtimeHome)
	}
	if got := kimiCLIEnvHome(t, envDump); got == cfg.KimiCLIHome {
		t.Fatal("KIMI_CODE_HOME must not be the authenticated source home")
	}
	if _, err := os.Stat(filepath.Join(root, "kimi-cli-home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configured parent must not create cardex-root isolation: %v", err)
	}
	runtimeConfig, err := os.ReadFile(filepath.Join(runtimeHome, "config.toml"))
	if err != nil || !strings.Contains(string(runtimeConfig), `default_permission_mode = "auto"`) ||
		!strings.Contains(string(runtimeConfig), `default_plan_mode = false`) ||
		!strings.Contains(string(runtimeConfig), `default_model = "kimi-code/k3"`) {
		t.Fatalf("runtime config must be sourced from authenticated home: err=%v config=%q", err, runtimeConfig)
	}
	afterSource, err := os.ReadFile(filepath.Join(cfg.KimiCLIHome, "config.toml"))
	if err != nil || string(afterSource) != string(sourceSnapshot) {
		t.Fatalf("source config must stay unmodified")
	}
	kimiCLIAssertCredentialReference(t, runtimeHome, cfg.KimiCLIHome, secret)
	if _, err := os.Stat(filepath.Join(parent, "review")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("execute must not create review: %v", err)
	}
	args, _ := os.ReadFile(argsDump)
	if !strings.Contains(string(args), "--model\nkimi-code/k3\n") {
		t.Fatalf("configured invoke must still pass model flags:\n%s", args)
	}
}

func TestKimiCLIIsolationDefaultInvokeSourceDistinct(t *testing.T) {
	t.Parallel()
	bin, _, envDump := fakeKimiCLI(t, kimiCLIIsolationPayload(), 0)
	cfg := kimiCLITestConfig(t, bin)
	secret := "kimi-default-secret-not-for-copy"
	kimiCLIWriteSourceToken(t, cfg.KimiCLIHome, secret)
	task := &Task{ID: "kimi-iso-default", Model: "opus", Type: typeSequence, Dir: t.TempDir()}
	root := testRoot(t)
	admitDirectInvoke(t, root, task)
	res, _, err := invokeKimiCLI(context.Background(), root, cfg, task, "p")
	if err != nil || res == nil || res.Result != "ISOLATION_OK" {
		t.Fatalf("default invoke failed: res=%+v err=%v", res, err)
	}
	runtimeHome := filepath.Join(root, "kimi-cli-home", "execute")
	if got := kimiCLIEnvHome(t, envDump); got != runtimeHome {
		t.Fatalf("unset isolation KIMI_CODE_HOME=%q want %q", got, runtimeHome)
	}
	if got := kimiCLIEnvHome(t, envDump); got == cfg.KimiCLIHome {
		t.Fatal("runtime home must stay distinct from source home")
	}
	kimiCLIAssertCredentialReference(t, runtimeHome, cfg.KimiCLIHome, secret)
}

func TestKimiCLIIsolationFailClosed(t *testing.T) {
	t.Parallel()
	bin, _, envDump := fakeKimiCLI(t, kimiCLIIsolationPayload(), 0)
	root := testRoot(t)

	t.Run("missing-source-config", func(t *testing.T) {
		cfg := kimiCLITestConfig(t, bin)
		if err := os.Remove(filepath.Join(cfg.KimiCLIHome, "config.toml")); err != nil {
			t.Fatal(err)
		}
		task := &Task{ID: "kimi-iso-miss-src", Model: "opus", Type: typeSequence, Dir: t.TempDir()}
		admitDirectInvoke(t, root, task)
		res, _, err := invokeKimiCLI(context.Background(), root, cfg, task, "p")
		if err == nil || res != nil || !strings.Contains(err.Error(), "读取 Kimi CLI 配置失败") {
			t.Fatalf("missing source config must fail closed: res=%+v err=%v", res, err)
		}
		if _, statErr := os.Stat(envDump); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal("child must not start when source config is missing")
		}
	})

	t.Run("missing-source-credentials", func(t *testing.T) {
		cfg := kimiCLITestConfig(t, bin)
		if err := os.RemoveAll(filepath.Join(cfg.KimiCLIHome, "credentials")); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareKimiCLIHome(root, cfg, false); err == nil ||
			!strings.Contains(err.Error(), "OAuth 凭据目录不可用") {
			t.Fatalf("missing source credentials must fail closed: %v", err)
		}
	})

	t.Run("missing-configured-parent", func(t *testing.T) {
		cfg := kimiCLITestConfig(t, bin)
		missing := filepath.Join(t.TempDir(), "no-such-mount")
		cfg.KimiCLIRuntimeHome = missing
		task := &Task{ID: "kimi-iso-miss-parent", Model: "opus", Type: typeSequence, Dir: t.TempDir()}
		localRoot := testRoot(t)
		admitDirectInvoke(t, localRoot, task)
		res, _, err := invokeKimiCLI(context.Background(), localRoot, cfg, task, "p")
		if err == nil || res != nil || !strings.Contains(err.Error(), "隔离运行父目录不可用") {
			t.Fatalf("missing configured parent must fail closed: res=%+v err=%v", res, err)
		}
		if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("absent external mount must not be created: %v", statErr)
		}
		if _, statErr := os.Stat(filepath.Join(localRoot, "kimi-cli-home")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("must not silently fall back to cardex-root isolation: %v", statErr)
		}
	})

	t.Run("credential-regular-file", func(t *testing.T) {
		cfg := kimiCLITestConfig(t, bin)
		runtimeHome := filepath.Join(root, "kimi-cli-home", "execute")
		if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runtimeHome, "credentials"), []byte("not-a-symlink"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareKimiCLIHome(root, cfg, false); err == nil ||
			!strings.Contains(err.Error(), "不是软链接") {
			t.Fatalf("regular credentials path must fail closed: %v", err)
		}
	})

	t.Run("credential-symlink-mismatch", func(t *testing.T) {
		cfg := kimiCLITestConfig(t, bin)
		localRoot := testRoot(t)
		runtimeHome := filepath.Join(localRoot, "kimi-cli-home", "execute")
		if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "other-credentials"), filepath.Join(runtimeHome, "credentials")); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareKimiCLIHome(localRoot, cfg, false); err == nil ||
			!strings.Contains(err.Error(), "软链接目标不一致") {
			t.Fatalf("mismatched credentials symlink must fail closed: %v", err)
		}
	})
}

func TestKimiCLIIsolationExecuteReviewAndPersistence(t *testing.T) {
	t.Parallel()
	bin, argsDump, envDump := fakeKimiCLI(t, kimiCLIIsolationPayload(), 0)
	cfg := kimiCLITestConfig(t, bin)
	secret := "kimi-persist-secret-not-for-copy"
	kimiCLIWriteSourceToken(t, cfg.KimiCLIHome, secret)
	parent := t.TempDir()
	cfg.KimiCLIRuntimeHome = parent
	root := testRoot(t)

	execTask := &Task{ID: "kimi-iso-persist-exec", Model: "opus", Type: typeSequence, Dir: t.TempDir()}
	admitDirectInvoke(t, root, execTask)
	res, _, err := invokeKimiCLI(context.Background(), root, cfg, execTask, "exec-one")
	if err != nil || res == nil || res.SessionID != "session-iso" {
		t.Fatalf("execute invoke failed: res=%+v err=%v", res, err)
	}
	executeHome := filepath.Join(parent, "execute")
	if kimiCLIEnvHome(t, envDump) != executeHome {
		t.Fatalf("execute KIMI_CODE_HOME=%q", kimiCLIEnvHome(t, envDump))
	}
	firstConfig, err := os.ReadFile(filepath.Join(executeHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, "review")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("execute must not create review: %v", err)
	}

	execTask.SessionID = "session-iso"
	res, _, err = invokeKimiCLI(context.Background(), root, cfg, execTask, "exec-two")
	if err != nil || res == nil || res.Result != "ISOLATION_OK" {
		t.Fatalf("second execute invoke failed: res=%+v err=%v", res, err)
	}
	if kimiCLIEnvHome(t, envDump) != executeHome {
		t.Fatalf("session resume must keep the same runtime home: %q", kimiCLIEnvHome(t, envDump))
	}
	args, _ := os.ReadFile(argsDump)
	if !strings.Contains(string(args), "--session\nsession-iso\n") {
		t.Fatalf("resume must pass session_id from the card:\n%s", args)
	}
	secondConfig, err := os.ReadFile(filepath.Join(executeHome, "config.toml"))
	if err != nil || string(secondConfig) != string(firstConfig) {
		t.Fatalf("runtime config must persist across invokes")
	}
	kimiCLIAssertCredentialReference(t, executeHome, cfg.KimiCLIHome, secret)

	reviewTask := &Task{ID: "kimi-iso-persist-review", Model: "opus", Type: "review", Dir: t.TempDir()}
	admitDirectInvoke(t, root, reviewTask)
	res, _, err = invokeKimiCLI(context.Background(), root, cfg, reviewTask, "review")
	if err != nil || res == nil || res.Result != "ISOLATION_OK" {
		t.Fatalf("review invoke failed: res=%+v err=%v", res, err)
	}
	reviewHome := filepath.Join(parent, "review")
	if kimiCLIEnvHome(t, envDump) != reviewHome {
		t.Fatalf("review KIMI_CODE_HOME=%q want %q", kimiCLIEnvHome(t, envDump), reviewHome)
	}
	reviewConfig, err := os.ReadFile(filepath.Join(reviewHome, "config.toml"))
	if err != nil || !strings.Contains(string(reviewConfig), `default_plan_mode = true`) {
		t.Fatalf("plan-mode review must land on review runtime: err=%v config=%q", err, reviewConfig)
	}
	afterExec, err := os.ReadFile(filepath.Join(executeHome, "config.toml"))
	if err != nil || string(afterExec) != string(firstConfig) {
		t.Fatalf("review must not mutate execute runtime")
	}
	if _, err := os.Stat(filepath.Join(root, "kimi-cli-home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configured isolation must not also populate cardex-root homes: %v", err)
	}
	kimiCLIAssertCredentialReference(t, reviewHome, cfg.KimiCLIHome, secret)
}
