package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// grokStableSelector is the non-concrete config token meaning "resolve the
// stable catalog default at a new-attempt boundary". It is never an execution
// identity: it must not be stored on a task or passed as --model.
const grokStableSelector = "stable"

// grokLegacyDefaultModel is the hardcoded Grok id that older Cardex configs
// used as the default. A concrete value is an explicit pin at runtime.
// migrate-grok-model rewrites only this exact model-field value to the selector.
const grokLegacyDefaultModel = "grok-4.6"

// grokCatalogStableID is the stable id published by Grok CLI 1.0.40. It is
// accepted as an explicit pin and as the current catalog default. It is not a
// fallback when the catalog probe fails.
const grokCatalogStableID = "grok-4.7"

// grokBuildFastModel is the current fast variant. Any future "*-build-fast"
// id is the same class and must not become the catalog default.
const grokBuildFastModel = "grok-4.7-build-fast"

// grokCursorCatalogModel is the verified current cursor-agent Grok slug at xhigh.
// The catalog lists grok-4.7-low/medium/high/xhigh and does not use a cursor- prefix.
// Fast variants are separate and are not this default.
const grokCursorCatalogModel = "grok-4.7-xhigh"

// grokLegacyCursorDefault is the previous Cardex cursor_model default.
// Migration rewrites that config field only. An explicit pin of this id still runs.
const grokLegacyCursorDefault = "cursor-grok-4.6-xhigh"

// grokStableIDPattern matches a public stable Grok id (grok-4.6, grok-4.7, grok-4.8, grok-5).
// Fast, internal "-build", preview, and non-Grok strings do not match.
var grokStableIDPattern = regexp.MustCompile(`^grok-[0-9]+(?:\.[0-9]+)?$`)

const grokModelBackupSuffix = ".grok-stable-backup"

var grokCatalogANSI = regexp.MustCompile(`\x1b(?:\[[0-9;]*[A-Za-z]|\][^\x07]*(?:\x07|\x1b\\))`)

func grokModelIsConcrete(model string) bool {
	model = strings.TrimSpace(model)
	return model != "" && model != grokStableSelector
}

func grokStandardStableID(model string) bool {
	return grokStableIDPattern.MatchString(strings.TrimSpace(model))
}

func cursorGrokCatalogModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "grok-4.7-xhigh", "grok-4.7-high", "grok-4.7-medium", "grok-4.7-low":
		return true
	default:
		return false
	}
}

func validateCursorGrokSlug(model string) error {
	model = strings.TrimSpace(model)
	if model == "" || !strings.Contains(strings.ToLower(model), "grok") {
		return nil
	}
	lower := strings.ToLower(model)
	if strings.Contains(lower, "fast") {
		return fmt.Errorf("cursor_model %q is a Fast variant and is not the current Cursor Grok default", model)
	}
	if lower == grokLegacyCursorDefault || cursorGrokCatalogModel(model) {
		return nil
	}
	return fmt.Errorf("cursor_model %q is not a current catalog id (grok-4.7-low|medium|high|xhigh) or the historical pin %s", model, grokLegacyCursorDefault)
}

func grokOwnerPolicyModelAllowed(model string) bool {
	model = strings.TrimSpace(model)
	if model == grokStableSelector {
		return true
	}
	return grokStandardStableID(model)
}

// grokReportableAssistantModel is an id the provider may report as the assistant
// model. The internal "-build" suffix is not the fast variant and is not a
// catalog default.
func grokReportableAssistantModel(model string) bool {
	model = strings.TrimSpace(model)
	if grokStandardStableID(model) {
		return true
	}
	for _, suffix := range []string{"-build", "-build-fast"} {
		if strings.HasSuffix(model, suffix) && grokStandardStableID(strings.TrimSuffix(model, suffix)) {
			return true
		}
	}
	return false
}

func grokPublicIDFromInternal(model string) (string, bool) {
	model = strings.TrimSpace(model)
	if strings.HasSuffix(model, "-build-fast") {
		return "", false
	}
	const suffix = "-build"
	if strings.HasSuffix(model, suffix) {
		base := strings.TrimSuffix(model, suffix)
		if grokStandardStableID(base) {
			return base, true
		}
	}
	return "", false
}

func grokFastVariantID(model string) bool {
	model = strings.TrimSpace(model)
	if !strings.HasSuffix(model, "-build-fast") {
		return false
	}
	return grokStandardStableID(strings.TrimSuffix(model, "-build-fast"))
}

func grokExecutionID(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" || model == grokStableSelector {
		return false
	}
	if grokStandardStableID(model) || grokFastVariantID(model) {
		return true
	}
	_, ok := grokPublicIDFromInternal(model)
	return ok
}

func concreteGrokPin(model string) string {
	model = strings.TrimSpace(model)
	if grokModelIsConcrete(model) {
		return model
	}
	return ""
}

// parseGrokCatalogDefault reads Grok CLI 1.0.40 human text. The execution id
// is the Default model line only. ANSI, warnings, and the available list
// (including grok-4.7-build-fast) do not choose the id.
func parseGrokCatalogDefault(text string) (string, error) {
	clean := grokCatalogANSI.ReplaceAllString(text, "")
	var found string
	saw := false
	for _, line := range strings.Split(clean, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || grokCatalogNoiseLine(trimmed) {
			continue
		}
		const prefix = "Default model:"
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		id := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		if fields := strings.Fields(id); len(fields) > 0 {
			id = fields[0]
		} else {
			id = ""
		}
		if saw {
			return "", fmt.Errorf("Grok catalog probe failed: multiple Default model lines")
		}
		saw = true
		found = id
	}
	if !saw || found == "" {
		return "", fmt.Errorf("Grok catalog probe failed: missing Default model")
	}
	if !grokStandardStableID(found) {
		return "", fmt.Errorf("Grok catalog probe failed: refusing non-stable default %s", found)
	}
	return found, nil
}

func grokCatalogNoiseLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.HasPrefix(lower, "warning"),
		strings.HasPrefix(lower, "warn:"),
		strings.HasPrefix(lower, "error:"),
		strings.HasPrefix(lower, "note:"):
		return true
	default:
		return false
	}
}

// probeGrokCatalogDefault runs the configured grok_build_bin with the existing
// human catalog shape: `grok --no-auto-update models`. It does not pass --json
// and does not read or write ~/.grok/config.toml. A non-zero exit, timeout, or
// missing Default model line returns an error and no model id.
func probeGrokCatalogDefault(ctx context.Context, cfg *Config) (string, error) {
	if cfg == nil || strings.TrimSpace(cfg.GrokBuildBin) == "" {
		return "", fmt.Errorf("Grok catalog probe failed: grok_build_bin unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, grokBuildAuthProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, cfg.GrokBuildBin, "--no-auto-update", "models")
	home, _ := resolveGrokLifecycleHome(cfg)
	cmd.Env = providerChildEnv(home, nil)
	setupProcGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := runCmdRegistered(cmd)
	if probeCtx.Err() != nil {
		return "", fmt.Errorf("Grok catalog probe failed: %w", probeCtx.Err())
	}
	if runErr != nil {
		return "", fmt.Errorf("Grok catalog probe failed: %s", safeGrokBuildProbeDiagnostic(stderr.String(), runErr))
	}
	return parseGrokCatalogDefault(stdout.String() + "\n" + stderr.String())
}

// freezeGrokAttemptModel stores one concrete id on the task before argv is
// built. A concrete task pin or a concrete grok_build.model pin is kept and
// does not probe. The selector and an empty model probe once. A later call
// sees the stored id and does not probe again.
func freezeGrokAttemptModel(ctx context.Context, cfg *Config, t *Task) error {
	if t == nil {
		return fmt.Errorf("Grok catalog probe failed: no task")
	}
	if grokHasExecutionEvidence(t) && !grokModelIsConcrete(t.GrokModel) {
		id, ok, ambiguous := grokEvidenceExecutionID(t)
		if ambiguous || !ok {
			return fmt.Errorf("Grok attempt identity is ambiguous; refusing to resolve a new catalog model")
		}
		t.GrokModel = id
		return nil
	}
	if grokModelIsConcrete(t.GrokModel) {
		if grokShouldReresolveLegacyDefault(cfg, t) {
			t.GrokModel = ""
		} else if grokAmbiguousLegacyIdentity(cfg, t) {
			return fmt.Errorf("Grok attempt identity is ambiguous; refusing to resolve a new catalog model")
		} else {
			return nil
		}
	} else {
		t.GrokModel = ""
	}
	// A prior attempt with no recoverable Grok identity must not inherit a new
	// config pin or catalog id. A different provider's last route is a new leg.
	if t.Attempts > 0 && !grokModelIsConcrete(t.GrokModel) && !grokOtherProviderNewLeg(t) {
		return fmt.Errorf("Grok attempt identity is ambiguous; refusing to resolve a new catalog model")
	}
	if cfg != nil && cfg.GrokBuild != nil {
		if model := concreteGrokPin(cfg.GrokBuild.Model); model != "" {
			t.GrokModel = model
			return nil
		}
	}
	id, err := probeGrokCatalogDefault(ctx, cfg)
	if err != nil {
		t.GrokModel = ""
		return err
	}
	if !grokStandardStableID(id) {
		t.GrokModel = ""
		return fmt.Errorf("Grok catalog probe failed: refusing non-stable id %q", id)
	}
	t.GrokModel = id
	return nil
}

func grokRouteRunner(r *RouteAttemptReadback) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(firstNonBlank(r.ActualRunner, r.RequestedRunner))
}

func grokRouteAttemptIsOtherProvider(r *RouteAttemptReadback) bool {
	runner := grokRouteRunner(r)
	return runner != "" && runner != grokBuildRunnerName
}

func grokOtherProviderNewLeg(t *Task) bool {
	return t != nil && t.LastRouteAttempt != nil && grokRouteAttemptIsOtherProvider(t.LastRouteAttempt)
}

func grokRouteAttemptIsGrok(r *RouteAttemptReadback) bool {
	if r == nil || grokRouteAttemptIsOtherProvider(r) {
		return false
	}
	if grokRouteRunner(r) == grokBuildRunnerName {
		return true
	}
	return grokExecutionID(r.RequestedModel) || grokExecutionID(r.ActualModel)
}

func grokHasExecutionEvidence(t *Task) bool {
	if t == nil {
		return false
	}
	if t.LastRouteAttempt != nil && grokRouteAttemptIsOtherProvider(t.LastRouteAttempt) {
		return false
	}
	if strings.TrimSpace(t.SessionID) != "" || t.MidStep || strings.TrimSpace(t.ActiveAttemptID) != "" {
		return true
	}
	return grokRouteAttemptIsGrok(t.LastRouteAttempt)
}

func grokEvidenceExecutionID(t *Task) (string, bool, bool) {
	if t == nil || !grokHasExecutionEvidence(t) {
		return "", false, false
	}
	requested, actual := "", ""
	if grokRouteAttemptIsGrok(t.LastRouteAttempt) {
		requested = strings.TrimSpace(t.LastRouteAttempt.RequestedModel)
		actual = strings.TrimSpace(t.LastRouteAttempt.ActualModel)
	}
	if requested == "" && actual == "" {
		return "", false, true
	}
	if requested != "" && actual != "" && requested != actual {
		if grokFastVariantID(actual) || !grokExecutionID(requested) {
			return "", false, true
		}
		base, ok := grokPublicIDFromInternal(actual)
		if !ok || base != requested {
			return "", false, true
		}
	}
	if grokExecutionID(requested) {
		return requested, true, false
	}
	if grokFastVariantID(actual) {
		return actual, true, false
	}
	if base, ok := grokPublicIDFromInternal(actual); ok {
		return base, true, false
	}
	if grokExecutionID(actual) {
		return actual, true, false
	}
	return "", false, true
}

func grokExplicitTaskPin(t *Task) bool {
	if t == nil || !grokModelIsConcrete(t.GrokModel) {
		return false
	}
	if t.RouteReason == routeReasonGrokExplicit {
		return true
	}
	return t.RunnerExplicit && t.PreferRunner == grokBuildRunnerName
}

// grokShouldReresolveLegacyDefault is true only for a never-started task whose
// grok-4.6 id was stamped from the old hardcoded default. Attempts>0 is prior
// work even when session and readback are missing. Explicit pins and Grok
// execution evidence stay.
func grokShouldReresolveLegacyDefault(cfg *Config, t *Task) bool {
	if t == nil || t.terminal() || t.Attempts > 0 || grokHasExecutionEvidence(t) || grokExplicitTaskPin(t) {
		return false
	}
	if strings.TrimSpace(t.GrokModel) != grokLegacyDefaultModel {
		return false
	}
	if cfg == nil || cfg.GrokBuild == nil {
		return false
	}
	return !grokModelIsConcrete(cfg.GrokBuild.Model)
}

func grokAmbiguousLegacyIdentity(cfg *Config, t *Task) bool {
	if t == nil || t.terminal() || grokHasExecutionEvidence(t) || grokExplicitTaskPin(t) {
		return false
	}
	if t.Attempts > 0 && grokExecutionID(t.GrokModel) {
		return false
	}
	if strings.TrimSpace(t.GrokModel) != grokLegacyDefaultModel {
		return false
	}
	if cfg != nil && cfg.GrokBuild != nil && concreteGrokPin(cfg.GrokBuild.Model) == grokLegacyDefaultModel {
		return false
	}
	return true
}

// grokManualHintApplies is true only for a genuinely new, still-unfrozen task.
// A stored model, Grok execution evidence, or an unidentified prior attempt
// keeps its own identity. A concrete policy-leg hint must not fill those.
func grokManualHintApplies(t *Task) bool {
	if t == nil || t.terminal() || grokModelIsConcrete(t.GrokModel) || grokHasExecutionEvidence(t) {
		return false
	}
	if t.Attempts > 0 && !grokOtherProviderNewLeg(t) {
		return false
	}
	return true
}

// grokDispatchExecutionModel is the id a manual command may pass as --model.
// It copies the task and reuses freeze so the caller's task is unchanged.
// Frozen identity wins. A hint fills only a new unfrozen task, on the copy.
func grokDispatchExecutionModel(ctx context.Context, cfg *Config, t *Task, hinted string) (string, error) {
	if t == nil {
		if model := concreteGrokPin(hinted); model != "" {
			return model, nil
		}
		return probeGrokCatalogDefault(ctx, cfg)
	}
	local := *t
	if model := concreteGrokPin(hinted); model != "" && grokManualHintApplies(&local) {
		local.GrokModel = model
		// The hint is this command's explicit id. Freeze must not treat a
		// hinted grok-4.6 as the historical auto-stamped default.
		local.RouteReason = routeReasonGrokExplicit
	}
	if err := freezeGrokAttemptModel(ctx, cfg, &local); err != nil {
		return "", err
	}
	if model := concreteGrokPin(local.GrokModel); model != "" {
		return model, nil
	}
	return "", fmt.Errorf("Grok attempt identity is ambiguous; refusing to resolve a new catalog model")
}

func grokDispatchModelPending(cfg *Config, t *Task) bool {
	if grokModelIsConcrete(resolveGrokBuildModel(cfg, t)) {
		return true
	}
	if cfg == nil || strings.TrimSpace(cfg.GrokBuildBin) == "" || cfg.GrokBuild == nil {
		return false
	}
	switch strings.TrimSpace(cfg.GrokBuild.Model) {
	case "", grokStableSelector:
		return true
	default:
		return false
	}
}

// freezeManualGoalModel stores one concrete id before admission binds a session.
// SessionID counts as execution evidence, so a probe after binding would treat
// a new Goal as an unidentified resume and refuse the catalog id.
func freezeManualGoalModel(ctx context.Context, root string, cfg *Config, t *Task) error {
	if err := freezeGrokAttemptModel(ctx, cfg, t); err != nil {
		return err
	}
	if root != "" && t != nil && strings.TrimSpace(t.ID) != "" {
		return saveTask(root, t)
	}
	return nil
}

// manualGoalCommandArgs builds the final Goal argv from the id already stored
// on the task. It does not probe. Call it after admission has bound SessionID.
func manualGoalCommandArgs(cfg *Config, t *Task) ([]string, string, error) {
	model := resolveGrokBuildModel(cfg, t)
	effort := resolveGrokBuildEffort(cfg, t)
	if !grokModelIsConcrete(model) || strings.TrimSpace(effort) == "" {
		return nil, "", fmt.Errorf("unresolved Grok model/effort")
	}
	sandbox, permission, err := resolveManualGrokTuple(cfg, t)
	if err != nil {
		return nil, "", err
	}
	grokHome := defaultGrokHome()
	if t != nil && t.Goal != nil && strings.TrimSpace(t.Goal.GrokHome) != "" {
		grokHome = strings.TrimSpace(t.Goal.GrokHome)
	}
	if t != nil && t.Goal != nil {
		t.Goal.GrokHome = grokHome
	}
	return manualGrokGoalArgs(cfg, t, model, effort, sandbox, permission, grokHome), grokHome, nil
}

func manualGoalWorkerArgs(ctx context.Context, root string, cfg *Config, t *Task) ([]string, string, error) {
	if err := freezeManualGoalModel(ctx, root, cfg, t); err != nil {
		return nil, "", err
	}
	return manualGoalCommandArgs(cfg, t)
}

func grokModelBackupPath(configPath string) string {
	return configPath + grokModelBackupSuffix
}

func pathHasGrokDir(path string) bool {
	cleaned := filepath.Clean(path)
	for _, part := range strings.Split(cleaned, string(os.PathSeparator)) {
		if part == ".grok" {
			return true
		}
	}
	return false
}

func cmdMigrateGrokModel(args []string) error {
	fs := flag.NewFlagSet("migrate-grok-model", flag.ExitOnError)
	configPath := fs.String("config", "", "caller-supplied Cardex config.json")
	rollback := fs.Bool("rollback", false, "restore that file's pre-migration bytes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" {
		return fmt.Errorf("migrate-grok-model requires -config")
	}
	if *rollback {
		return rollbackGrokStableModel(*configPath)
	}
	n, err := migrateGrokStableModel(*configPath)
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Printf("migrate-grok-model: %s has no legacy grok-4.6 model fields\n", *configPath)
		return nil
	}
	fmt.Printf("migrate-grok-model: rewrote %d legacy default field(s) in %s\n", n, *configPath)
	fmt.Printf("rollback: cardex migrate-grok-model -rollback -config %s\n", *configPath)
	return nil
}

func migrateGrokStableModel(configPath string) (int, error) {
	raw, err := readCardexConfigFile(configPath)
	if err != nil {
		return 0, err
	}
	obj, err := decodeCardexConfigObject(raw)
	if err != nil {
		return 0, err
	}
	n := rewriteLegacyGrokModelFields(obj)
	if n == 0 {
		return 0, nil
	}
	backup := grokModelBackupPath(configPath)
	if _, err := os.Lstat(backup); err == nil {
		return 0, fmt.Errorf("migrate-grok-model: backup %s already exists; rollback before migrating again", backup)
	}
	encoded, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return 0, err
	}
	encoded = append(encoded, '\n')
	if err := writeExclusiveFile(backup, raw); err != nil {
		return 0, fmt.Errorf("migrate-grok-model: write backup: %w", err)
	}
	if err := atomicWrite(configPath, encoded); err != nil {
		return 0, err
	}
	return n, nil
}

func rollbackGrokStableModel(configPath string) error {
	if err := rejectGrokMigrationPath(configPath); err != nil {
		return err
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		return fmt.Errorf("migrate-grok-model: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("migrate-grok-model: %s is not a Cardex config file", configPath)
	}
	backup := grokModelBackupPath(configPath)
	prior, err := os.ReadFile(backup)
	if err != nil {
		return fmt.Errorf("migrate-grok-model: rollback backup missing: %w", err)
	}
	if _, err := decodeCardexConfigObject(prior); err != nil {
		return fmt.Errorf("migrate-grok-model: rollback backup is corrupt: %w", err)
	}
	if err := atomicWrite(configPath, prior); err != nil {
		return err
	}
	return nil
}

func readCardexConfigFile(configPath string) ([]byte, error) {
	if err := rejectGrokMigrationPath(configPath); err != nil {
		return nil, err
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		return nil, fmt.Errorf("migrate-grok-model: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("migrate-grok-model: %s is not a Cardex config file", configPath)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	if _, err := decodeCardexConfigObject(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func rejectGrokMigrationPath(configPath string) error {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return fmt.Errorf("migrate-grok-model requires a Cardex config path")
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	if pathHasGrokDir(abs) || strings.HasSuffix(strings.ToLower(filepath.Base(abs)), ".toml") {
		return fmt.Errorf("migrate-grok-model refuses %s: not a Cardex config (does not touch ~/.grok)", configPath)
	}
	return nil
}

func decodeCardexConfigObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("migrate-grok-model: not a Cardex config: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("migrate-grok-model: not a Cardex config: trailing data")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("migrate-grok-model: not a Cardex config")
	}
	if _, taskPrompts := obj["prompts"]; taskPrompts {
		if _, taskStatus := obj["status"]; taskStatus {
			return nil, fmt.Errorf("migrate-grok-model: refusing a task record")
		}
	}
	for _, key := range []string{"grok_build", "grok_build_bin", "claude_bin", "codex_bin", "poll_interval_sec", "type_defaults", "default_runner"} {
		if _, ok := obj[key]; ok {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("migrate-grok-model: not a Cardex config")
}

func rewriteLegacyGrokModelFields(v any) int {
	switch node := v.(type) {
	case map[string]any:
		n := 0
		for key, child := range node {
			if key == "model" || key == "grok_model" || key == "default_model" {
				if text, ok := child.(string); ok && text == grokLegacyDefaultModel {
					node[key] = grokStableSelector
					n++
					continue
				}
			}
			if key == "cursor_model" {
				if text, ok := child.(string); ok && text == grokLegacyCursorDefault {
					node[key] = grokCursorCatalogModel
					n++
					continue
				}
			}
			n += rewriteLegacyGrokModelFields(child)
		}
		return n
	case []any:
		n := 0
		for _, child := range node {
			n += rewriteLegacyGrokModelFields(child)
		}
		return n
	default:
		return 0
	}
}

func writeExclusiveFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = f.Close()
			_ = os.Remove(path)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	cleanup = false
	return nil
}
