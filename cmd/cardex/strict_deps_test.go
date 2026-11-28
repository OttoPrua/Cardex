package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/store"
)

func TestStrictDepsCLI(t *testing.T) {
	home := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p", "--repo", t.TempDir())
	mustHome(t, home, "objective", "add", "--id", "o", "--project", "p", "--title", "o")
	add := func(id string, args ...string) map[string]json.RawMessage {
		t.Helper()
		base := []string{"--json", "card", "add", "--id", id, "--objective", "o", "--title", id}
		out := mustHome(t, home, append(base, args...)...)
		var row map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &row); err != nil {
			t.Fatalf("decode %s: %v", out, err)
		}
		return row
	}
	add("source", "--status", "failed")
	add("old-gate", "--status", "canceled")
	add("gate", "--status", "done")
	depsAre := func(row map[string]json.RawMessage, want ...string) bool {
		var got []string
		return json.Unmarshal(row["deps"], &got) == nil && slices.Equal(got, want)
	}
	legacy := add("legacy", "--dep", "old-gate")
	if _, ok := legacy["strict_deps"]; ok || !depsAre(legacy, "old-gate") {
		t.Fatalf("changed legacy JSON %+v", legacy)
	}
	got := add("strict", "--strict-deps", "--dep", "old-gate", "--dep", "gate")
	if string(got["strict_deps"]) != "true" || !depsAre(got, "old-gate", "gate") {
		t.Fatalf("strict JSON %+v", got)
	}
	add("review", "--kind", "review", "--strict-deps", "--dep", "source", "--dep", "gate")
	add("review-upstream", "--kind", "review", "--status", "done", "--dep", "source")
	add("depend-on-review", "--strict-deps", "--dep", "review-upstream")
	add("missing", "--strict-deps", "--dep", "unknown")

	show := func(id string, brief bool, reason string) {
		t.Helper()
		for _, asJSON := range []bool{false, true} {
			var args []string
			if asJSON {
				args = append(args, "--json")
			}
			args = append(args, "card", "show", id)
			if brief {
				args = append(args, "--brief")
			}
			out := mustHome(t, home, args...)
			if asJSON {
				var row map[string]json.RawMessage
				if err := json.Unmarshal([]byte(out), &row); err != nil {
					t.Fatal(err)
				}
				if reason == "" {
					if _, ok := row["dependency_blocker"]; ok {
						t.Fatalf("unexpected reason in %s", out)
					}
				} else {
					var actual string
					if err := json.Unmarshal(row["dependency_blocker"], &actual); err != nil || actual != reason {
						t.Fatalf("reason %q want %q err %v in %s", actual, reason, err, out)
					}
				}
			} else if reason != "" && !strings.Contains(out, reason) {
				t.Fatalf("missing blocker %q in %s", reason, out)
			}
		}
	}
	show("legacy", true, "")
	show("strict", true, "strict dependency old-gate is canceled; requires done or merged")
	show("strict", false, "strict dependency old-gate is canceled; requires done or merged")
	show("depend-on-review", true, "strict dependency review-upstream is a review card; depend on the merged subject instead")
	show("missing", true, "strict dependency unknown does not exist")
	show("review", true, "") // Failed review subject is not an execution gate.
	var blockers []blockerRow
	out := mustHome(t, home, "--json", "blockers", "--objective", "o")
	if err := json.Unmarshal([]byte(out), &blockers); err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, row := range blockers {
		reasons[row.ID] = row.Reason
	}
	if reasons["legacy"] != "" || reasons["review"] != "" ||
		reasons["strict"] != "strict dependency old-gate is canceled; requires done or merged" ||
		reasons["depend-on-review"] != "strict dependency review-upstream is a review card; depend on the merged subject instead" ||
		reasons["missing"] != "strict dependency unknown does not exist" {
		t.Fatalf("strict blockers did not match the scheduler and card view: %v", reasons)
	}

	mustHome(t, home, "card", "retry", "gate")
	show("review", true, "strict dependency gate is ready; requires done or merged")
}

func TestStrictDepsLegacyImportKeepsCanceledAsSatisfied(t *testing.T) {
	home, v1 := t.TempDir(), t.TempDir()
	writeLegacyFile(t, v1, "archive/canceled.json", map[string]any{
		"id": "upstream", "title": "Upstream", "status": "canceled", "project": "test",
	})
	writeLegacyFile(t, v1, "tasks/downstream.json", map[string]any{
		"id": "downstream", "title": "Downstream", "status": "queued", "project": "test",
		"prompts": []string{"Synthetic test"}, "depends_on": []string{"upstream"},
	})
	mustHome(t, home, "import-legacy", v1)
	withDB(t, home, func(db *store.Store) {
		ctx := t.Context()
		card, err := db.GetCard(ctx, "v1-downstream")
		if err != nil || card.StrictDeps || !slices.Equal(card.Deps, []string{"v1-upstream"}) {
			t.Fatalf("imported card %+v, err %v", card, err)
		}
		if reason, err := db.ExecutionDependencyBlocker(ctx, card); err != nil || reason != "" {
			t.Fatalf("legacy card blocked by %q: %v", reason, err)
		}
	})
}
