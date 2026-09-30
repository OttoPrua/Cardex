package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const grokCatalogProductPayload = "{\"type\":\"text\",\"data\":\"CATALOG_OK\"}\n{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"sess-g47\",\"num_turns\":1}\n"

// grok47CatalogText is the fixture stdout. Callers pass the same id literal they
// later expect on argv; the test does not parse this text to discover the id.
func grok47CatalogText(defaultID string, extra ...string) string {
	var b strings.Builder
	b.WriteString("\x1b[33mWarning: Default model: grok-4.7-build-fast is listed but is not the default\x1b[0m\n")
	b.WriteString("note: human catalog, not json\n")
	b.WriteString("\x1b[1mDefault model:\x1b[0m " + defaultID + "\n")
	b.WriteString("Available models:\n")
	b.WriteString("  " + defaultID + "\n")
	b.WriteString("  grok-4.7-build-fast\n")
	for _, id := range extra {
		b.WriteString("  " + id + "\n")
	}
	return b.String()
}

func newGrokCatalogFake(t *testing.T, catalogPath string, modelsExit int, modelsSleep bool) (bin, productArgs, modelsArgs string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "grok")
	productArgs = filepath.Join(dir, "product.args")
	modelsArgs = filepath.Join(dir, "models.args")
	payload := filepath.Join(dir, "payload.jsonl")
	if err := os.WriteFile(payload, []byte(grokCatalogProductPayload), 0o644); err != nil {
		t.Fatal(err)
	}
	sleepLine := ""
	if modelsSleep {
		sleepLine = "sleep 30\n"
	}
	script := "#!/bin/sh\nset -e\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = models ]; then\n" +
		"    printf 'x\\n' >> " + shSingleQuote(modelsArgs+".calls") + "\n" +
		"    printf '%s\\n' \"$@\" > " + shSingleQuote(modelsArgs) + "\n" +
		"    printf '%s\\n' 'Warning: probe stderr is not a default' >&2\n" +
		sleepLine +
		"    cat " + shSingleQuote(catalogPath) + "\n" +
		"    exit " + itoaSmall(modelsExit) + "\n" +
		"  fi\n" +
		"done\n" +
		"printf '%s\\n' \"$@\" >> " + shSingleQuote(productArgs) + "\n" +
		"printf '\\n---\\n' >> " + shSingleQuote(productArgs) + "\n" +
		"cat " + shSingleQuote(payload) + "\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, productArgs, modelsArgs
}

func itoaSmall(n int) string { return strconv.Itoa(n) }

func writeCatalog(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func productModels(t *testing.T, productArgs string) []string {
	t.Helper()
	raw, err := os.ReadFile(productArgs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var got []string
	for _, chunk := range strings.Split(string(raw), "\n---\n") {
		lines := strings.Split(chunk, "\n")
		for i, line := range lines {
			if line == "--model" && i+1 < len(lines) {
				got = append(got, lines[i+1])
			}
		}
	}
	return got
}

func assertModelsProbeShape(t *testing.T, modelsArgs string) {
	t.Helper()
	raw, err := os.ReadFile(modelsArgs)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if text != "--no-auto-update\nmodels\n" {
		t.Fatalf("catalog probe argv = %q", text)
	}
	if strings.Contains(text, "--json") || strings.Contains(text, "config.toml") {
		t.Fatalf("catalog probe must not request json or toml: %q", text)
	}
}

func catalogRunConfig(t *testing.T, bin string) *Config {
	t.Helper()
	cfg := grokBuildTestConfig(t, bin)
	cfg.OwnerRoutingEnforced = false
	cfg.GrokBuild.Model = grokStableSelector
	cfg.StepTimeoutMin = 1
	return cfg
}

func countFileLines(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func TestGrok47CatalogANSIResolvesStableDefault(t *testing.T) {
	t.Parallel()
	const want = "grok-4.7"
	catalog := filepath.Join(t.TempDir(), "catalog.txt")
	writeCatalog(t, catalog, grok47CatalogText(want))
	bin, productArgs, modelsArgs := newGrokCatalogFake(t, catalog, 0, false)
	root := testRoot(t)
	cfg := catalogRunConfig(t, bin)
	task := newTask(root, cfg, typeSequence, "catalog default", t.TempDir(), []string{"do the thing"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.RunnerExplicit = true
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	assertModelsProbeShape(t, modelsArgs)
	got := productModels(t, productArgs)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("argv model = %q, fixture Default model is %s", got, want)
	}
	if strings.Contains(strings.Join(got, "\n"), grokBuildFastModel) || strings.Contains(strings.Join(got, "\n"), grokStableSelector) {
		t.Fatalf("selector or build-fast leaked into argv: %q", got)
	}
	stored, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.GrokModel != want {
		t.Fatalf("persisted model = %q, fixture Default model is %s", stored.GrokModel, want)
	}
	if stored.LastRouteAttempt == nil || stored.LastRouteAttempt.ActualModel != want || stored.LastRouteAttempt.RequestedModel != want {
		t.Fatalf("readback = %+v", stored.LastRouteAttempt)
	}
}

func TestGrok47MissingDefaultAndProbeFailures(t *testing.T) {
	t.Parallel()
	t.Run("missing default line", func(t *testing.T) {
		t.Parallel()
		catalog := filepath.Join(t.TempDir(), "catalog.txt")
		writeCatalog(t, catalog, "Warning: offline\nAvailable models:\n  grok-4.7\n  grok-4.7-build-fast\n")
		assertCatalogFailure(t, catalog, 0, false, "missing Default model")
	})
	t.Run("nonzero probe", func(t *testing.T) {
		t.Parallel()
		catalog := filepath.Join(t.TempDir(), "catalog.txt")
		writeCatalog(t, catalog, grok47CatalogText("grok-4.7"))
		assertCatalogFailure(t, catalog, 1, false, "catalog probe failed")
	})
	t.Run("timeout", func(t *testing.T) {
		catalog := filepath.Join(t.TempDir(), "catalog.txt")
		writeCatalog(t, catalog, grok47CatalogText("grok-4.7"))
		bin, productArgs, _ := newGrokCatalogFake(t, catalog, 0, true)
		root := testRoot(t)
		cfg := catalogRunConfig(t, bin)
		task := newTask(root, cfg, typeSequence, "timeout", t.TempDir(), []string{"x"}, 1)
		task.PreferRunner = grokBuildRunnerName
		task.RunnerExplicit = true
		if err := saveTask(root, task); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if err := runTaskVia(ctx, root, cfg, task, grokBuildRunnerName); err != nil {
			t.Fatal(err)
		}
		stored, err := loadTask(root, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.GrokModel != "" || stored.Status != statusHeld || !strings.Contains(stored.LastError, "catalog probe failed") {
			t.Fatalf("timeout must hold with no model: status=%s model=%q err=%s", stored.Status, stored.GrokModel, stored.LastError)
		}
		if models := productModels(t, productArgs); len(models) != 0 {
			t.Fatalf("timeout executed a model: %q", models)
		}
	})
	t.Run("offline binary", func(t *testing.T) {
		t.Parallel()
		root := testRoot(t)
		cfg := catalogRunConfig(t, filepath.Join(t.TempDir(), "missing-grok"))
		task := newTask(root, cfg, typeSequence, "offline", t.TempDir(), []string{"x"}, 1)
		task.PreferRunner = grokBuildRunnerName
		task.RunnerExplicit = true
		if err := saveTask(root, task); err != nil {
			t.Fatal(err)
		}
		if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
			t.Fatal(err)
		}
		stored, err := loadTask(root, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.GrokModel != "" || stored.Status != statusHeld || stored.LastError == "" {
			t.Fatalf("offline probe must be visible and store nothing: status=%s model=%q err=%s", stored.Status, stored.GrokModel, stored.LastError)
		}
	})
	t.Run("default line is build-fast", func(t *testing.T) {
		t.Parallel()
		catalog := filepath.Join(t.TempDir(), "catalog.txt")
		writeCatalog(t, catalog, grok47CatalogText(grokBuildFastModel))
		assertCatalogFailure(t, catalog, 0, false, grokBuildFastModel)
	})
}

func assertCatalogFailure(t *testing.T, catalog string, modelsExit int, sleep bool, wantErr string) {
	t.Helper()
	bin, productArgs, _ := newGrokCatalogFake(t, catalog, modelsExit, sleep)
	root := testRoot(t)
	cfg := catalogRunConfig(t, bin)
	task := newTask(root, cfg, typeSequence, "fail", t.TempDir(), []string{"x"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.RunnerExplicit = true
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	stored, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.GrokModel != "" || stored.Status != statusHeld || !strings.Contains(stored.LastError, wantErr) {
		t.Fatalf("probe failure status=%s model=%q err=%s", stored.Status, stored.GrokModel, stored.LastError)
	}
	if models := productModels(t, productArgs); len(models) != 0 {
		t.Fatalf("failed probe still executed: %q", models)
	}
}

func TestGrok47ExplicitPinsBypassCatalog(t *testing.T) {
	t.Parallel()
	const catalogDefault = "grok-4.7"
	catalog := filepath.Join(t.TempDir(), "catalog.txt")
	writeCatalog(t, catalog, grok47CatalogText(catalogDefault, "grok-4.6"))
	bin, productArgs, _ := newGrokCatalogFake(t, catalog, 0, false)
	root := testRoot(t)
	cfg := catalogRunConfig(t, bin)

	pinned := newTask(root, cfg, typeSequence, "pin 4.6", t.TempDir(), []string{"pinned"}, 1)
	pinned.PreferRunner = grokBuildRunnerName
	pinned.RunnerExplicit = true
	pinned.GrokModel = "grok-4.6"
	if err := saveTask(root, pinned); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, pinned, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}

	fast := newTask(root, cfg, typeSequence, "pin fast", t.TempDir(), []string{"fast"}, 1)
	fast.PreferRunner = grokBuildRunnerName
	fast.RunnerExplicit = true
	fast.GrokModel = grokBuildFastModel
	if err := saveTask(root, fast); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, fast, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}

	got := productModels(t, productArgs)
	if len(got) != 2 || got[0] != "grok-4.6" || got[1] != grokBuildFastModel {
		t.Fatalf("pins = %q, want grok-4.6 then %s while catalog Default model is %s", got, grokBuildFastModel, catalogDefault)
	}
}

func TestGrok47RetryKeepsFrozenID(t *testing.T) {
	t.Parallel()
	const first = "grok-4.7"
	const drifted = "grok-4.8"
	catalog := filepath.Join(t.TempDir(), "catalog.txt")
	writeCatalog(t, catalog, grok47CatalogText(first, "grok-4.6"))
	bin, productArgs, modelsArgs := newGrokCatalogFake(t, catalog, 0, false)
	root := testRoot(t)
	cfg := catalogRunConfig(t, bin)
	task := newTask(root, cfg, typeSequence, "freeze", t.TempDir(), []string{"once"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.RunnerExplicit = true
	if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
		t.Fatal(err)
	}
	if task.GrokModel != first {
		t.Fatalf("frozen = %q, fixture Default model is %s", task.GrokModel, first)
	}
	calls := countFileLines(modelsArgs + ".calls")
	writeCatalog(t, catalog, grok47CatalogText(drifted, first))
	if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
		t.Fatal(err)
	}
	if task.GrokModel != first {
		t.Fatalf("retry rewrote frozen id to %q", task.GrokModel)
	}
	if countFileLines(modelsArgs+".calls") != calls {
		t.Fatal("retry/resume probed the catalog again")
	}
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	if err := cmdSetStatus([]string{"-root", root, task.ID}, "retry"); err != nil {
		t.Fatal(err)
	}
	retried, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.GrokModel != first {
		t.Fatalf("retry cleared frozen id %q", retried.GrokModel)
	}
	if err := runTaskVia(context.Background(), root, cfg, retried, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	got := productModels(t, productArgs)
	if len(got) != 2 || got[0] != first || got[1] != first {
		t.Fatalf("retry argv = %q, want frozen %s after catalog moved to %s", got, first, drifted)
	}
	again, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.GrokModel != first {
		t.Fatalf("stored id after retry = %q", again.GrokModel)
	}
}

func TestGrok47EntryPathsUseCatalogID(t *testing.T) {
	t.Parallel()
	const want = "grok-4.7"
	catalog := filepath.Join(t.TempDir(), "catalog.txt")
	writeCatalog(t, catalog, grok47CatalogText(want, "grok-4.6"))
	bin, productArgs, _ := newGrokCatalogFake(t, catalog, 0, false)

	t.Run("automatic route", func(t *testing.T) {
		root := testRoot(t)
		cfg := policyTestConfig()
		cfg.GrokBuildBin = bin
		cfg.GrokBuild.Model = grokStableSelector
		isolateGrokLifecycleHome(t, cfg)
		cfg.StepTimeoutMin = 1
		task := newTask(root, cfg, typeSequence, "auto", t.TempDir(), []string{"implement"}, 1)
		task.Model = "opus"
		task.RouteClass = routeClassGeneral
		task.RiskClass = riskClassOrdinary
		task.PreferRunner = "codex"
		task.FreshSteps = true
		if runner, matched := ownerPrimaryDispatch(root, cfg, task, time.Now()); !matched || runner != grokBuildRunnerName {
			t.Fatalf("automatic route runner=%q matched=%v", runner, matched)
		}
		if task.GrokModel != "" {
			t.Fatalf("automatic route stored %q before catalog resolution", task.GrokModel)
		}
		if err := saveTask(root, task); err != nil {
			t.Fatal(err)
		}
		if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
			t.Fatal(err)
		}
		stored, err := loadTask(root, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.GrokModel != want {
			t.Fatalf("automatic identity = %q", stored.GrokModel)
		}
	})

	t.Run("manual dispatch", func(t *testing.T) {
		cfg := policyTestConfig()
		cfg.GrokBuildBin = bin
		cfg.GrokBuild.Model = grokStableSelector
		task := &Task{Type: typeSequence, Model: "haiku", RouteClass: routeClassGeneral, RiskClass: riskClassOrdinary,
			PreferRunner: "codex", FreshSteps: true, Dir: t.TempDir(), Prompts: []string{"manual"}}
		_, _, command, ok := ownerManualDispatchCommand(cfg, task, "manual")
		if !ok || !strings.Contains(command, "--model "+want) {
			t.Fatalf("manual command ok=%v command=%s", ok, command)
		}
		if strings.Contains(command, "--model "+grokBuildFastModel) || strings.Contains(command, "--model "+grokStableSelector) {
			t.Fatalf("manual command leaked selector or build-fast: %s", command)
		}
	})

	t.Run("goal worker", func(t *testing.T) {
		root := testRoot(t)
		cfg := catalogRunConfig(t, bin)
		task := newTask(root, cfg, typeSequence, "goal worker", t.TempDir(), []string{"work"}, 1)
		task.PreferRunner = grokBuildRunnerName
		task.RunnerExplicit = true
		if err := saveTask(root, task); err != nil {
			t.Fatal(err)
		}
		args, _, err := manualGoalWorkerArgs(context.Background(), root, cfg, task)
		if err != nil {
			t.Fatal(err)
		}
		if model := argValue(args, "--model"); model != want {
			t.Fatalf("goal worker argv model = %q args=%q", model, args)
		}
		stored, err := loadTask(root, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.GrokModel != want {
			t.Fatalf("goal worker persisted %q", stored.GrokModel)
		}
	})

	t.Run("goal reviewer", func(t *testing.T) {
		root := testRoot(t)
		cfg := catalogRunConfig(t, bin)
		task := newTask(root, cfg, typeReview, "goal reviewer", t.TempDir(), []string{"review the frozen candidate"}, 1)
		task.PreferRunner = grokBuildRunnerName
		task.RunnerExplicit = true
		if err := saveTask(root, task); err != nil {
			t.Fatal(err)
		}
		if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
			t.Fatal(err)
		}
		stored, err := loadTask(root, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.GrokModel != want {
			t.Fatalf("goal reviewer identity = %q", stored.GrokModel)
		}
	})

	got := productModels(t, productArgs)
	for _, model := range got {
		if model != want {
			t.Fatalf("entry-path argv = %q, want only %s", got, want)
		}
	}
	if len(got) < 2 {
		t.Fatalf("expected automatic and reviewer product argv, got %q", got)
	}
}

func argValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestGrok47CLIDefaultAndPin(t *testing.T) {
	t.Parallel()
	const want = "grok-4.7"
	catalog := filepath.Join(t.TempDir(), "catalog.txt")
	writeCatalog(t, catalog, grok47CatalogText(want, "grok-4.6"))
	bin, productArgs, _ := newGrokCatalogFake(t, catalog, 0, false)

	t.Run("default", func(t *testing.T) {
		root := testRoot(t)
		cfg := catalogRunConfig(t, bin)
		if err := saveConfig(root, cfg); err != nil {
			t.Fatal(err)
		}
		work := t.TempDir()
		if err := cmdAdd([]string{"-root", root, "-dir", work, "-runner", grokBuildRunnerName, "cli default"}); err != nil {
			t.Fatal(err)
		}
		task := onlyTask(t, root)
		if task.GrokModel != "" {
			t.Fatalf("CLI default stored %q at add", task.GrokModel)
		}
		loaded, err := loadConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		loaded.grokLifecycleHome = cfg.grokLifecycleHome
		if err := runTaskVia(context.Background(), root, loaded, task, grokBuildRunnerName); err != nil {
			t.Fatal(err)
		}
		stored, err := loadTask(root, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.GrokModel != want {
			t.Fatalf("CLI default identity = %q", stored.GrokModel)
		}
	})

	t.Run("pin", func(t *testing.T) {
		root := testRoot(t)
		cfg := catalogRunConfig(t, bin)
		if err := saveConfig(root, cfg); err != nil {
			t.Fatal(err)
		}
		work := t.TempDir()
		if err := cmdAdd([]string{"-root", root, "-dir", work, "-runner", grokBuildRunnerName, "-grok-model", "grok-4.6", "cli pin"}); err != nil {
			t.Fatal(err)
		}
		task := onlyTask(t, root)
		if task.GrokModel != "grok-4.6" {
			t.Fatalf("CLI pin stored %q", task.GrokModel)
		}
		loaded, err := loadConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		loaded.grokLifecycleHome = cfg.grokLifecycleHome
		if err := runTaskVia(context.Background(), root, loaded, task, grokBuildRunnerName); err != nil {
			t.Fatal(err)
		}
	})

	got := productModels(t, productArgs)
	if len(got) != 2 || got[0] != want || got[1] != "grok-4.6" {
		t.Fatalf("CLI argv = %q, want %s then grok-4.6", got, want)
	}
}

func onlyTask(t *testing.T, root string) *Task {
	t.Helper()
	tasks, err := loadTasks(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks=%d", len(tasks))
	}
	return tasks[0]
}

func TestGrok47Labels(t *testing.T) {
	t.Parallel()
	if boardRouteModelName("grok-4.7") != "4.7" {
		t.Fatalf("board label grok-4.7 = %q", boardRouteModelName("grok-4.7"))
	}
	if boardRouteModelName("grok-4.6") != "4.6" {
		t.Fatalf("board label grok-4.6 = %q", boardRouteModelName("grok-4.6"))
	}
	if boardRouteModelName(grokCursorCatalogModel) != "Grok 4.7" {
		t.Fatalf("cursor catalog label = %q", boardRouteModelName(grokCursorCatalogModel))
	}
	if boardRouteModelName("cursor-grok-4.6-xhigh") != "Grok 4.6" {
		t.Fatalf("explicit cursor pin label = %q", boardRouteModelName("cursor-grok-4.6-xhigh"))
	}
	if strings.Contains(boardRouteModelName("grok-4.7-build"), "fast") || strings.Contains(strings.ToLower(boardRouteModelName("grok-4.7-build")), "fast") {
		t.Fatal("internal build suffix must not be labeled Fast")
	}
	app, err := boardWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(app)
	if !strings.Contains(src, "case 'grok-4.7':") || !strings.Contains(src, "return 'Grok 4.7';") {
		t.Fatal("shipped web/app.js does not map grok-4.7 to Grok 4.7")
	}
	if !strings.Contains(src, "case 'grok-4.6':") || !strings.Contains(src, "return 'Grok 4.6';") {
		t.Fatal("shipped web/app.js does not map grok-4.6 to Grok 4.6")
	}
	if !strings.Contains(src, "case 'grok-4.7-xhigh':") || !strings.Contains(src, "return 'Grok 4.7 Extra High';") {
		t.Fatal("shipped web/app.js does not map the current Cursor catalog id grok-4.7-xhigh")
	}
	if !strings.Contains(src, "case 'cursor-grok-4.6-xhigh':") || !strings.Contains(src, "return 'Grok 4.6 Extra High';") {
		t.Fatal("explicit cursor-grok-4.6-xhigh pin must still display as Grok 4.6")
	}
	for _, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, "grok_") && !strings.Contains(line, "kimi_to_grok") && !strings.Contains(line, "fable_to_grok") {
			continue
		}
		if strings.Contains(line, "4.7") || strings.Contains(line, "4.6") {
			t.Fatalf("route reason hardcodes a Grok version: %s", line)
		}
	}
}

func TestGrok47OwnerModelsLoad(t *testing.T) {
	t.Parallel()
	for _, model := range []string{grokCatalogStableID, grokLegacyDefaultModel, grokStableSelector, "grok-4.8", "grok-5"} {
		cfg := policyTestConfig()
		cfg.GrokBuild.Model = model
		prof := cfg.CrossProfiles["fable-dual"]
		prof.A.Model = model
		cfg.CrossProfiles["fable-dual"] = prof
		if err := validateGrokBuild(cfg); err != nil {
			t.Fatalf("%s validateGrokBuild: %v", model, err)
		}
		if err := validateCursor(cfg); err != nil {
			t.Fatalf("%s validateCursor: %v", model, err)
		}
		if err := validateOwnerRoutingPolicy(cfg); err != nil {
			t.Fatalf("%s validateOwnerRoutingPolicy: %v", model, err)
		}
		root := testRoot(t)
		if err := saveConfig(root, cfg); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(configPath(root))
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := loadConfig(root)
		if err != nil {
			t.Fatalf("load %s: %v", model, err)
		}
		if loaded.GrokBuild == nil || loaded.GrokBuild.Model != model {
			t.Fatalf("loaded model = %+v", loaded.GrokBuild)
		}
		after, err := os.ReadFile(configPath(root))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("loadConfig rewrote %s", model)
		}
	}
	rejected := policyTestConfig()
	rejected.GrokBuild.Model = grokBuildFastModel
	if err := validateGrokBuild(rejected); err == nil || !strings.Contains(err.Error(), grokBuildFastModel) {
		t.Fatalf("build-fast policy model must be rejected, got %v", err)
	}
	rejected.GrokBuild.Model = ""
	if err := validateGrokBuild(rejected); err == nil {
		t.Fatal("empty grok_build.model must be rejected")
	}
	for _, model := range []string{"grok-4.8-build-fast", "grok-4.7-preview", "grok-4.7-build", "not-a-grok"} {
		rejected.GrokBuild.Model = model
		if err := validateGrokBuild(rejected); err == nil || !strings.Contains(err.Error(), model) {
			t.Fatalf("policy model %s must be rejected, got %v", model, err)
		}
		if _, err := parseGrokCatalogDefault("Default model: " + model + "\nAvailable models:\n  " + model + "\n"); err == nil {
			t.Fatalf("catalog default %s must be rejected", model)
		}
	}
	if got, err := parseGrokCatalogDefault("Default model: grok-4.8\nAvailable models:\n  grok-4.8\n  grok-4.8-build-fast\n"); err != nil || got != "grok-4.8" {
		t.Fatalf("future stable default = %q err=%v", got, err)
	}
}

func TestGrok47CursorCatalogSlug(t *testing.T) {
	t.Parallel()
	cfg := policyTestConfig()
	for _, model := range []string{grokCursorCatalogModel, "grok-4.7-high", "grok-4.7-medium", "grok-4.7-low", grokLegacyCursorDefault} {
		cfg.CursorModel = model
		if err := validateCursor(cfg); err != nil {
			t.Fatalf("cursor model %s must load: %v", model, err)
		}
	}
	for _, model := range []string{"cursor-grok-4.7-xhigh", "grok-4.7-fast", "grok-4.7-xhigh-fast"} {
		cfg.CursorModel = model
		if err := validateCursor(cfg); err == nil {
			t.Fatalf("cursor model %s must be rejected", model)
		}
	}
	pinned := &Task{CursorModel: grokLegacyCursorDefault}
	cfg.CursorModel = grokCursorCatalogModel
	if got := resolveCursorModel(cfg, pinned); got != grokLegacyCursorDefault {
		t.Fatalf("explicit historical cursor pin resolved to %q", got)
	}
	if got := resolveCursorModel(cfg, &Task{}); got != grokCursorCatalogModel {
		t.Fatalf("current cursor default resolved to %q", got)
	}
	for _, model := range []string{"grok-4.7-high", "grok-4.7-medium", "grok-4.7-low"} {
		if boardRouteModelName(model) != "Grok 4.7" {
			t.Fatalf("board label %s = %q", model, boardRouteModelName(model))
		}
	}
	app, err := boardWeb.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(app)
	for _, snippet := range []string{
		"case 'grok-4.7-high':", "return 'Grok 4.7 High';",
		"case 'grok-4.7-medium':", "return 'Grok 4.7 Medium';",
		"case 'grok-4.7-low':", "return 'Grok 4.7 Low';",
	} {
		if !strings.Contains(src, snippet) {
			t.Fatalf("shipped web/app.js missing %s", snippet)
		}
	}
}

func TestGrok47MigrationRollback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	original := []byte(`{
  "grok_build_bin": "/usr/bin/true",
  "cursor_model": "cursor-grok-4.6-xhigh",
  "note": "history says grok-4.6 stays in prose",
  "grok_build": {"enabled": true, "model": "grok-4.6", "effort": "xhigh"},
  "engines": {"demo": {"default_model": "grok-4.7", "model": "gpt-5.6-sol"}},
  "cross_profiles": {
    "fable-dual": {"a": {"kind": "grok-build", "model": "grok-4.6", "effort": "xhigh"}},
    "old-cursor": {"a": {"kind": "cursor", "model": "cursor-grok-4.6-xhigh"}}
  }
}
`)
	if err := os.WriteFile(configPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	homeGrok := filepath.Join(dir, ".grok")
	if err := os.MkdirAll(homeGrok, 0o700); err != nil {
		t.Fatal(err)
	}
	tomlPath := filepath.Join(homeGrok, "config.toml")
	tomlBytes := []byte("model = \"grok-4.6\"\n")
	if err := os.WriteFile(tomlPath, tomlBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdMigrateGrokModel([]string{"-config", tomlPath}); err == nil {
		t.Fatal("migration must refuse ~/.grok")
	}
	gotToml, err := os.ReadFile(tomlPath)
	if err != nil || !bytes.Equal(gotToml, tomlBytes) {
		t.Fatalf("toml changed: %q err=%v", gotToml, err)
	}
	notConfig := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notConfig, []byte("grok-4.6\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdMigrateGrokModel([]string{"-config", notConfig}); err == nil {
		t.Fatal("migration must refuse a non-config path")
	}
	taskPath := filepath.Join(dir, "task.json")
	taskBytes := []byte(`{"id":"t1","status":"done","prompts":["x"],"grok_model":"grok-4.6"}`)
	if err := os.WriteFile(taskPath, taskBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdMigrateGrokModel([]string{"-config", taskPath}); err == nil {
		t.Fatal("migration must refuse a task record")
	}
	gotTask, err := os.ReadFile(taskPath)
	if err != nil || !bytes.Equal(gotTask, taskBytes) {
		t.Fatalf("task record changed: %s", gotTask)
	}
	if err := cmdMigrateGrokModel([]string{"-config", configPath}); err != nil {
		t.Fatal(err)
	}
	migrated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(migrated, &obj); err != nil {
		t.Fatal(err)
	}
	grok := obj["grok_build"].(map[string]any)
	if grok["model"] != grokStableSelector || grok["effort"] != "xhigh" {
		t.Fatalf("grok_build = %+v", grok)
	}
	if obj["cursor_model"] != grokCursorCatalogModel || obj["note"] != "history says grok-4.6 stays in prose" {
		t.Fatalf("cursor default or prose changed: %+v", obj)
	}
	oldCursor := obj["cross_profiles"].(map[string]any)["old-cursor"].(map[string]any)["a"].(map[string]any)
	if oldCursor["model"] != grokLegacyCursorDefault {
		t.Fatalf("explicit historical cursor pin was rewritten: %+v", oldCursor)
	}
	engines := obj["engines"].(map[string]any)["demo"].(map[string]any)
	if engines["default_model"] != "grok-4.7" || engines["model"] != "gpt-5.6-sol" {
		t.Fatalf("non-legacy model fields changed: %+v", engines)
	}
	cross := obj["cross_profiles"].(map[string]any)["fable-dual"].(map[string]any)["a"].(map[string]any)
	if cross["model"] != grokStableSelector || cross["effort"] != "xhigh" {
		t.Fatalf("cross model = %+v", cross)
	}
	if err := cmdMigrateGrokModel([]string{"-rollback", "-config", configPath}); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("rollback bytes = %s", restored)
	}
	if gotToml, err = os.ReadFile(tomlPath); err != nil || !bytes.Equal(gotToml, tomlBytes) {
		t.Fatalf("toml changed after rollback: %q", gotToml)
	}
}

func TestGrok47OwnerStableFreezeReadbackAndRetry(t *testing.T) {
	t.Parallel()
	const first = "grok-4.7"
	const drifted = "grok-4.8"
	catalog := filepath.Join(t.TempDir(), "catalog.txt")
	writeCatalog(t, catalog, grok47CatalogText(first, "grok-4.6"))
	bin, productArgs, _ := newGrokCatalogFake(t, catalog, 0, false)
	root := testRoot(t)
	cfg := policyTestConfig()
	cfg.GrokBuildBin = bin
	cfg.GrokBuild.Model = grokStableSelector
	isolateGrokLifecycleHome(t, cfg)
	cfg.StepTimeoutMin = 1
	task := newTask(root, cfg, typeSequence, "owner freeze", t.TempDir(), []string{"implement"}, 1)
	task.Model = "opus"
	task.RouteClass = routeClassGeneral
	task.RiskClass = riskClassOrdinary
	task.PreferRunner = "codex"
	task.FreshSteps = true
	if runner, matched := ownerPrimaryDispatch(root, cfg, task, time.Now()); !matched || runner != grokBuildRunnerName {
		t.Fatalf("dispatch runner=%q matched=%v", runner, matched)
	}
	if task.GrokModel != "" {
		t.Fatalf("pre-freeze model = %q", task.GrokModel)
	}
	if _, ok := resolveOwnerRouteReadback(cfg, task); ok {
		t.Fatal("unfrozen selector leg must not read back as a concrete identity")
	}
	if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
		t.Fatal(err)
	}
	if task.GrokModel != first {
		t.Fatalf("frozen = %q", task.GrokModel)
	}
	route, ok := resolveOwnerRouteReadback(cfg, task)
	if !ok || route.Name == "" || len(route.Legs) == 0 || route.Legs[0].Model != grokStableSelector {
		t.Fatalf("frozen task lost owner readback: ok=%v route=%+v", ok, route)
	}
	if err := saveTask(root, task); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, task, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	writeCatalog(t, catalog, grok47CatalogText(drifted, first))
	if err := cmdSetStatus([]string{"-root", root, task.ID}, "retry"); err != nil {
		t.Fatal(err)
	}
	retried, err := loadTask(root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	retried.SessionID = "resume-same-attempt"
	if err := saveTask(root, retried); err != nil {
		t.Fatal(err)
	}
	if err := runTaskVia(context.Background(), root, cfg, retried, grokBuildRunnerName); err != nil {
		t.Fatal(err)
	}
	got := productModels(t, productArgs)
	if len(got) != 2 || got[0] != first || got[1] != first {
		t.Fatalf("owner retry/resume argv = %q, want frozen %s after catalog %s", got, first, drifted)
	}
}

func TestGrok47EvidenceDoesNotResolveAndLegacyDefaultCan(t *testing.T) {
	t.Parallel()
	const catalogDefault = "grok-4.7"
	catalog := filepath.Join(t.TempDir(), "catalog.txt")
	writeCatalog(t, catalog, grok47CatalogText(catalogDefault))
	bin, _, modelsArgs := newGrokCatalogFake(t, catalog, 0, false)
	cfg := catalogRunConfig(t, bin)

	t.Run("last route attempt with zero attempts", func(t *testing.T) {
		task := &Task{PreferRunner: grokBuildRunnerName, Attempts: 0, GrokEffort: "xhigh",
			LastRouteAttempt: &RouteAttemptReadback{RequestedModel: "grok-4.6", ActualModel: "grok-4.6"}}
		if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
			t.Fatal(err)
		}
		if task.GrokModel != "grok-4.6" {
			t.Fatalf("evidence restored %q", task.GrokModel)
		}
		if countFileLines(modelsArgs+".calls") != 0 {
			t.Fatal("execution evidence triggered a catalog probe")
		}
	})

	t.Run("session without a model fails closed", func(t *testing.T) {
		before := countFileLines(modelsArgs + ".calls")
		task := &Task{PreferRunner: grokBuildRunnerName, SessionID: "already-started", GrokEffort: "xhigh"}
		err := freezeGrokAttemptModel(context.Background(), cfg, task)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") || task.GrokModel != "" {
			t.Fatalf("err=%v model=%q", err, task.GrokModel)
		}
		if countFileLines(modelsArgs+".calls") != before {
			t.Fatal("ambiguous session probed the catalog")
		}
	})

	t.Run("never-started auto stamp follows catalog", func(t *testing.T) {
		task := &Task{PreferRunner: grokBuildRunnerName, GrokModel: grokLegacyDefaultModel, GrokEffort: "xhigh",
			RouteReason: routeReasonGrokOpusGeneral, Attempts: 0}
		if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
			t.Fatal(err)
		}
		if task.GrokModel != catalogDefault {
			t.Fatalf("never-started legacy default stayed %q", task.GrokModel)
		}
	})

	t.Run("explicit old pin stays", func(t *testing.T) {
		before := countFileLines(modelsArgs + ".calls")
		task := &Task{PreferRunner: grokBuildRunnerName, RunnerExplicit: true, GrokModel: "grok-4.6", GrokEffort: "xhigh",
			RouteReason: routeReasonGrokExplicit}
		if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
			t.Fatal(err)
		}
		if task.GrokModel != "grok-4.6" || countFileLines(modelsArgs+".calls") != before {
			t.Fatalf("explicit pin model=%q", task.GrokModel)
		}
	})

	t.Run("never-started legacy default without route provenance follows catalog", func(t *testing.T) {
		task := &Task{PreferRunner: grokBuildRunnerName, GrokModel: grokLegacyDefaultModel, GrokEffort: "xhigh",
			Status: statusHeld, Attempts: 0}
		if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
			t.Fatal(err)
		}
		if task.GrokModel != catalogDefault {
			t.Fatalf("never-started held legacy default stayed %q", task.GrokModel)
		}
	})

	t.Run("completed history keeps legacy id", func(t *testing.T) {
		before := countFileLines(modelsArgs + ".calls")
		task := &Task{PreferRunner: grokBuildRunnerName, Status: statusDone, GrokModel: grokLegacyDefaultModel, GrokEffort: "xhigh"}
		if err := freezeGrokAttemptModel(context.Background(), cfg, task); err != nil {
			t.Fatal(err)
		}
		if task.GrokModel != grokLegacyDefaultModel || countFileLines(modelsArgs+".calls") != before {
			t.Fatalf("completed history changed to %q", task.GrokModel)
		}
	})

	t.Run("conflicting evidence fails closed", func(t *testing.T) {
		before := countFileLines(modelsArgs + ".calls")
		task := &Task{PreferRunner: grokBuildRunnerName, GrokEffort: "xhigh", Attempts: 0,
			LastRouteAttempt: &RouteAttemptReadback{RequestedModel: "grok-4.6", ActualModel: "grok-4.5"}}
		err := freezeGrokAttemptModel(context.Background(), cfg, task)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") || task.GrokModel != "" {
			t.Fatalf("err=%v model=%q", err, task.GrokModel)
		}
		if countFileLines(modelsArgs+".calls") != before {
			t.Fatal("conflicting evidence probed the catalog")
		}
	})
}

func TestGrok47RequestedStableKeepsActualBuildSuffix(t *testing.T) {
	t.Parallel()
	const requested = "grok-4.7"
	const actual = "grok-4.7-build"
	raw := "{\"type\":\"text\",\"data\":\"ok\"}\n" +
		"{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"sess-build\",\"num_turns\":1," +
		"\"requestId\":\"req-build\",\"model_id\":\"" + requested + "\"," +
		"\"chat_history\":[{\"role\":\"user\",\"model_id\":\"ignored\"}," +
		"{\"role\":\"assistant\",\"model_id\":\"" + actual + "\",\"reasoning_effort\":\"xhigh\"}]," +
		"\"modelUsage\":{\"" + actual + "\":{\"input_tokens\":1,\"output_tokens\":1}}}\n"
	res := parseGrokBuildJSONL(raw)
	if res == nil || res.IsError || res.ObservedAssistantModel != actual {
		t.Fatalf("parse = %+v", res)
	}
	if strings.Contains(res.ObservedAssistantModel, "fast") {
		t.Fatalf("internal build id labeled fast: %s", res.ObservedAssistantModel)
	}
	root := testRoot(t)
	cfg := catalogRunConfig(t, "/usr/bin/true")
	task := newTask(root, cfg, typeSequence, "actual", t.TempDir(), []string{"x"}, 1)
	task.PreferRunner = grokBuildRunnerName
	task.Runner = grokBuildRunnerName
	task.RunnerExplicit = true
	task.GrokModel = requested
	task.GrokEffort = "xhigh"
	beginRouteAttemptReadback(cfg, task, false)
	recordRouteAttemptObservation(task, res)
	if task.GrokModel != requested || task.GrokEffort != "xhigh" ||
		task.LastRouteAttempt.RequestedModel != requested || task.LastRouteAttempt.ActualModel != actual ||
		task.LastRouteAttempt.RequestedEffort != "xhigh" || task.LastRouteAttempt.ActualEffort != "xhigh" {
		t.Fatalf("requested/actual drifted: task=%s/%s readback=%+v", task.GrokModel, task.GrokEffort, task.LastRouteAttempt)
	}
	if strings.Contains(task.LastRouteAttempt.ActualModel, "fast") {
		t.Fatalf("actual=%s", task.LastRouteAttempt.ActualModel)
	}
	resume := &Task{PreferRunner: grokBuildRunnerName, GrokEffort: "xhigh", Attempts: 0,
		LastRouteAttempt: &RouteAttemptReadback{RequestedModel: requested, ActualModel: actual, RequestedEffort: "xhigh", ActualEffort: "xhigh"}}
	if err := freezeGrokAttemptModel(context.Background(), cfg, resume); err != nil {
		t.Fatal(err)
	}
	if resume.GrokModel != requested {
		t.Fatalf("internal build suffix drifted the next request to %q", resume.GrokModel)
	}
}

func TestGrok47ManualResumeAndOtherRunnerEvidence(t *testing.T) {
	t.Parallel()
	const resumed = "grok-4.6"
	const catalogDefault = "grok-4.7"
	catalog := filepath.Join(t.TempDir(), "catalog.txt")
	writeCatalog(t, catalog, grok47CatalogText(catalogDefault))
	bin, _, modelsArgs := newGrokCatalogFake(t, catalog, 0, false)
	cfg := catalogRunConfig(t, bin)
	dir := t.TempDir()
	snap := func(task *Task) string {
		t.Helper()
		raw, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	requireUnchanged := func(task *Task, before string) {
		t.Helper()
		if snap(task) != before {
			t.Fatalf("manual command mutated caller task:\n%s", snap(task))
		}
	}

	resumedTask := &Task{Type: typeSequence, Dir: dir, PreferRunner: grokBuildRunnerName, GrokEffort: "xhigh",
		LastRouteAttempt: &RouteAttemptReadback{
			RequestedRunner: grokBuildRunnerName, ActualRunner: grokBuildRunnerName,
			RequestedModel: resumed, ActualModel: resumed,
		}}
	beforeSnap := snap(resumedTask)
	before := countFileLines(modelsArgs + ".calls")
	command, ok := manualDispatchCommandForLeg(cfg, resumedTask, "resume", policyLeg{
		Runner: grokBuildRunnerName, Model: grokStableSelector, Effort: "xhigh",
	})
	requireUnchanged(resumedTask, beforeSnap)
	if !ok || resumedTask.GrokModel != "" || !strings.Contains(command, "--model "+resumed) || strings.Contains(command, "--model "+catalogDefault) {
		t.Fatalf("manual resume ok=%v model=%q command=%s", ok, resumedTask.GrokModel, command)
	}
	if countFileLines(modelsArgs+".calls") != before {
		t.Fatal("manual resume probed the catalog")
	}
	fresh := &Task{Type: typeSequence, Dir: dir, PreferRunner: grokBuildRunnerName}
	freshSnap := snap(fresh)
	hinted, ok := manualDispatchCommandForLeg(cfg, fresh, "new", policyLeg{
		Runner: grokBuildRunnerName, Model: resumed, Effort: "xhigh",
	})
	requireUnchanged(fresh, freshSnap)
	if !ok || !strings.Contains(hinted, "--model "+resumed) || strings.Contains(hinted, "--model "+catalogDefault) {
		t.Fatalf("manual leg pin ok=%v command=%s", ok, hinted)
	}
	if countFileLines(modelsArgs+".calls") != before {
		t.Fatal("concrete manual leg pin probed the catalog")
	}
	frozen := &Task{Type: typeSequence, Dir: dir, PreferRunner: grokBuildRunnerName, GrokModel: resumed,
		SessionID: "already-frozen", GrokEffort: "xhigh",
		LastRouteAttempt: &RouteAttemptReadback{
			RequestedRunner: grokBuildRunnerName, ActualRunner: grokBuildRunnerName,
			RequestedModel: resumed, ActualModel: resumed,
		}}
	frozenSnap := snap(frozen)
	frozenCmd, ok := manualDispatchCommandForLeg(cfg, frozen, "resume-frozen", policyLeg{
		Runner: grokBuildRunnerName, Model: catalogDefault, Effort: "xhigh",
	})
	requireUnchanged(frozen, frozenSnap)
	if !ok || !strings.Contains(frozenCmd, "--model "+resumed) || strings.Contains(frozenCmd, "--model "+catalogDefault) {
		t.Fatalf("hint overwrote frozen command ok=%v command=%s", ok, frozenCmd)
	}
	if countFileLines(modelsArgs+".calls") != before {
		t.Fatal("frozen manual resume probed the catalog")
	}

	legacy := &Task{Type: typeSequence, Dir: dir, PreferRunner: grokBuildRunnerName, GrokModel: resumed, GrokEffort: "xhigh"}
	legacySnap := snap(legacy)
	legacyCmd, ok := manualDispatchCommandForLeg(cfg, legacy, "legacy-new", policyLeg{
		Runner: grokBuildRunnerName, Model: grokStableSelector, Effort: "xhigh",
	})
	requireUnchanged(legacy, legacySnap)
	if !ok || !strings.Contains(legacyCmd, "--model "+catalogDefault) || strings.Contains(legacyCmd, "--model "+resumed) {
		t.Fatalf("never-started legacy manual argv ok=%v command=%s", ok, legacyCmd)
	}
	if countFileLines(modelsArgs+".calls") != before+1 {
		t.Fatal("never-started legacy manual command did not probe once")
	}
	before = countFileLines(modelsArgs + ".calls")

	ambiguous := &Task{Type: typeSequence, Dir: dir, PreferRunner: grokBuildRunnerName, SessionID: "started-without-model"}
	ambiguousSnap := snap(ambiguous)
	if _, ok := manualDispatchCommandForLeg(cfg, ambiguous, "nope", policyLeg{
		Runner: grokBuildRunnerName, Model: grokStableSelector, Effort: "xhigh",
	}); ok {
		t.Fatal("ambiguous manual resume dispatched")
	}
	requireUnchanged(ambiguous, ambiguousSnap)
	if countFileLines(modelsArgs+".calls") != before {
		t.Fatal("ambiguous manual resume probed the catalog")
	}

	pinnedCfg := catalogRunConfig(t, bin)
	pinnedCfg.GrokBuild.Model = catalogDefault
	stale := &Task{Type: typeSequence, Dir: dir, PreferRunner: grokBuildRunnerName, GrokModel: resumed, GrokEffort: "xhigh"}
	staleSnap := snap(stale)
	if _, ok := manualDispatchCommandForLeg(pinnedCfg, stale, "stale", policyLeg{
		Runner: grokBuildRunnerName, Model: catalogDefault, Effort: "xhigh",
	}); ok {
		t.Fatal("ambiguous never-started grok-4.6 dispatched against a concrete newer pin")
	}
	requireUnchanged(stale, staleSnap)
	if countFileLines(modelsArgs+".calls") != before {
		t.Fatal("ambiguous legacy identity probed the catalog")
	}
	prior := &Task{Type: typeSequence, Dir: dir, PreferRunner: grokBuildRunnerName, GrokEffort: "xhigh", Attempts: 2}
	priorSnap := snap(prior)
	if _, ok := manualDispatchCommandForLeg(pinnedCfg, prior, "prior", policyLeg{
		Runner: grokBuildRunnerName, Model: catalogDefault, Effort: "xhigh",
	}); ok {
		t.Fatal("unidentified prior attempt dispatched from a concrete hint")
	}
	requireUnchanged(prior, priorSnap)
	if countFileLines(modelsArgs+".calls") != before {
		t.Fatal("unidentified prior attempt probed the catalog")
	}

	for _, runner := range []string{kimiCLIRunnerName, "codex"} {
		foreignModel := "kimi-code/k3"
		if runner == "codex" {
			foreignModel = "gpt-5.6-sol"
		}
		next := &Task{Type: typeSequence, PreferRunner: grokBuildRunnerName, GrokEffort: "xhigh", Attempts: 2,
			LastRouteAttempt: &RouteAttemptReadback{
				RequestedRunner: runner, ActualRunner: runner,
				RequestedModel: foreignModel, ActualModel: foreignModel,
			}}
		if err := freezeGrokAttemptModel(context.Background(), cfg, next); err != nil {
			t.Fatal(runner, err)
		}
		if next.GrokModel != catalogDefault {
			t.Fatalf("%s evidence became Grok model %q", runner, next.GrokModel)
		}
	}

	kept := &Task{PreferRunner: grokBuildRunnerName, GrokModel: resumed, GrokEffort: "xhigh", Attempts: 3}
	calls := countFileLines(modelsArgs + ".calls")
	if err := freezeGrokAttemptModel(context.Background(), cfg, kept); err != nil {
		t.Fatal(err)
	}
	if kept.GrokModel != resumed || countFileLines(modelsArgs+".calls") != calls {
		t.Fatalf("attempts>0 without session was re-resolved to %q", kept.GrokModel)
	}
	unidentified := &Task{PreferRunner: grokBuildRunnerName, GrokEffort: "xhigh", Attempts: 2}
	if err := freezeGrokAttemptModel(context.Background(), pinnedCfg, unidentified); err == nil || unidentified.GrokModel != "" {
		t.Fatalf("config pin rewrote an unidentified prior attempt: err=%v model=%q", err, unidentified.GrokModel)
	}
	missing := &Task{PreferRunner: grokBuildRunnerName, GrokEffort: "xhigh", Attempts: 2}
	err := freezeGrokAttemptModel(context.Background(), cfg, missing)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || missing.GrokModel != "" || countFileLines(modelsArgs+".calls") != calls {
		t.Fatalf("missing identity after attempts err=%v model=%q", err, missing.GrokModel)
	}
}

func TestGrok47FastActualIsNotStandardAlias(t *testing.T) {
	t.Parallel()
	cfg := catalogRunConfig(t, "/usr/bin/true")
	mismatch := &Task{PreferRunner: grokBuildRunnerName,
		LastRouteAttempt: &RouteAttemptReadback{
			ActualRunner: grokBuildRunnerName, RequestedRunner: grokBuildRunnerName,
			RequestedModel: "grok-4.7", ActualModel: "grok-4.7-build-fast",
		}}
	err := freezeGrokAttemptModel(context.Background(), cfg, mismatch)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || mismatch.GrokModel != "" {
		t.Fatalf("fast mismatch err=%v model=%q", err, mismatch.GrokModel)
	}
	fastOnly := &Task{PreferRunner: grokBuildRunnerName,
		LastRouteAttempt: &RouteAttemptReadback{
			ActualRunner: grokBuildRunnerName, ActualModel: "grok-4.7-build-fast",
		}}
	if err := freezeGrokAttemptModel(context.Background(), cfg, fastOnly); err != nil {
		t.Fatal(err)
	}
	if fastOnly.GrokModel != "grok-4.7-build-fast" {
		t.Fatalf("fast actual recovered as %q", fastOnly.GrokModel)
	}
}

func TestGrok47CorruptBackupRollbackLeavesConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := []byte("{\n  \"grok_build_bin\": \"/usr/bin/true\",\n  \"grok_build\": {\"enabled\": true, \"model\": \"stable\", \"effort\": \"xhigh\"}\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grokModelBackupPath(path), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rollbackGrokStableModel(path); err == nil {
		t.Fatal("corrupt backup was accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("config changed after rejected rollback: %s", got)
	}
}
