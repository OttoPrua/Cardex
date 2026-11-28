package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/board"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func TestStrongResearchReportCLIReadOnly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict O1 read unsupported on this platform")
	}
	home := t.TempDir()
	path := filepath.Join(home, "cardex.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InstallStrongWorkflowPrototype(t.Context()); err != nil {
		t.Fatal(err)
	}
	plan := model.StrongPlan{Run: "sensitive-run-canary", Revision: 1, Version: model.StrongPlanVersion, Template: model.StrongTemplateLight,
		Project: "private-project-canary", Scope: "private-prompt-canary", TaskSHA: strings.Repeat("a", 64), EvidenceSHA: strings.Repeat("b", 64), EvidenceCommit: strings.Repeat("c", 40), EvidencePaths: []string{"secret-path-canary.go"},
		GlobalTokens: 100, Seats: 1, Limits: map[string]model.StrongSlotLimit{}}
	route := model.StrongApprovedRoute{Runner: "fake", Channel: "private-channel-canary", Model: "private-model-canary", Family: "private-family-canary"}
	graph, err := plan.Graph()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range graph {
		if s.Phase != model.StrongPhaseCheckpoint {
			plan.Limits[s.Key] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{route}}
		}
	}
	key, err := db.SaveStrongPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--home", home, "research", "report", "--run", key.Run, "--revision", "1", "--sha", key.SHA}
	var stdout, stderr bytes.Buffer
	if code := Main(args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), key.SHA) {
		t.Fatalf("private report returned %d %s %s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Main(append(args, "--redacted"), &stdout, &stderr); code != 0 {
		t.Fatalf("redacted code=%d %s", code, stderr.String())
	}
	var public board.StrongExport
	if err := json.Unmarshal(stdout.Bytes(), &public); err != nil || public.Report.Result != "awaiting_approval" || strings.Contains(stdout.String(), "canary") || strings.Contains(stdout.String(), key.SHA) {
		t.Fatalf("invalid export: %s %v", stdout.String(), err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("report changed DB")
	}
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + side); !os.IsNotExist(err) {
			t.Fatalf("offline report produced %s: %v", side, err)
		}
	}
	missing := filepath.Join(home, "not-created")
	args[1] = missing
	stdout.Reset()
	stderr.Reset()
	if code := Main(append(args, "--redacted"), &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatalf("missing home code=%d %s", code, stdout.String())
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("report created a missing home: %v", err)
	}
}

func TestStrongResearchReportDoesNotEnumerateLegacyTasks(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "external-canary.json"), []byte("NEVER READ THIS"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "tasks")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"--home", home, "research", "report", "--run", "fixture", "--revision", "1", "--sha", strings.Repeat("a", 64), "--redacted"}
	if code := Main(args, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "stable checkpointed") || strings.Contains(stderr.String(), "legacy") {
		t.Fatalf("legacy probing or report leaked: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, "cardex.db")); !os.IsNotExist(err) {
		t.Fatalf("report created DB: %v", err)
	}
}
