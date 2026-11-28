package store

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestStrongRunReportExactRevisionAndNoImplicitInstall(t *testing.T) {
	db := openDB(t)
	ctx := t.Context()
	if _, err := db.ReadStrongRunReport(ctx, StrongRunKey{Run: "fixture", Revision: 1, SHA: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("missing report was accepted")
	}
	var n int
	if err := db.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='strong_run_plans'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("implicit install %d %v", n, err)
	}
	if err := db.InstallStrongWorkflowPrototype(ctx); err != nil {
		t.Fatal(err)
	}
	p := model.StrongPlan{Run: "fixture", Revision: 1, Version: model.StrongPlanVersion, Template: model.StrongTemplateLight, Project: "synthetic-project", Scope: "Fixture task",
		TaskSHA: strings.Repeat("1", 64), EvidenceSHA: strings.Repeat("2", 64), EvidenceCommit: strings.Repeat("3", 40), EvidencePaths: []string{"fact.txt"}, GlobalTokens: 100, Seats: 1,
		Limits: map[string]model.StrongSlotLimit{}}
	g, err := p.Graph()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range g {
		if s.Phase != model.StrongPhaseCheckpoint {
			p.Limits[s.Key] = model.StrongSlotLimit{Tokens: 20, Routes: []model.StrongApprovedRoute{{Runner: "fake", Channel: "fake-channel", Model: "fake-model", Family: "fake-family"}}}
		}
	}
	key, err := db.SaveStrongPlan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	r, err := db.ReadStrongRunReport(ctx, key)
	if err != nil || r.Run.Status != StrongAwaiting || len(r.Stages) != 3 || r.Stages[0].State != "not_materialized" || r.Stages[0].ModelDispatches != nil || !r.BudgetCheck.ChargeReconciles {
		t.Fatalf("awaiting snapshot: %+v %v", r, err)
	}
	key.SHA = strings.Repeat("f", 64)
	if r, err := db.ReadStrongRunReport(ctx, key); err == nil || len(r.Stages) != 0 {
		t.Fatalf("wrong SHA yielded data: %+v %v", r, err)
	}
}

func TestStrongRunReportPinnedReadRejectsLinksAndLiveSidecars(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict O1 Linux boundary")
	}
	ctx := t.Context()
	external := closedSnapshotHome(t)
	sentinelBefore := snapshotManifest(t, filepath.Dir(external))
	key := StrongRunKey{Run: "fixture", Revision: 1, SHA: strings.Repeat("a", 64)}
	for _, side := range []string{"", "-wal", "-shm", "-journal"} {
		t.Run("linked-"+side, func(t *testing.T) {
			path := closedSnapshotHome(t)
			if side == "" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(external, path+side); err != nil {
				t.Fatal(err)
			}
			r, err := ReadPinnedStrongRunReport(ctx, path, key)
			if !errors.Is(err, ErrSnapshotUnavailable) || len(r.Stages) != 0 {
				t.Fatalf("followed linked snapshot path: %+v %v", r, err)
			}
		})
	}
	if !reflect.DeepEqual(sentinelBefore, snapshotManifest(t, filepath.Dir(external))) {
		t.Fatal("external sentinel changed")
	}
	path := closedSnapshotHome(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InstallStrongWorkflowPrototype(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path + "-wal"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path + "-shm"); err != nil {
		t.Fatal(err)
	}
	before := snapshotManifest(t, filepath.Dir(path))
	r, err := ReadPinnedStrongRunReport(ctx, path, key)
	if !errors.Is(err, ErrSnapshotUnavailable) || len(r.Stages) != 0 {
		t.Fatalf("opened native live sidecars: %+v %v", r, err)
	}
	if !reflect.DeepEqual(before, snapshotManifest(t, filepath.Dir(path))) {
		t.Fatal("live WAL/SHM source changed")
	}
}
