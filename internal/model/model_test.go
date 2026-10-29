package model

import (
	"reflect"
	"testing"
)

func TestClosedSetsAccepted(t *testing.T) {
	for _, k := range AllCardKinds() {
		if !k.Valid() {
			t.Fatalf("card kind %q rejected", k)
		}
	}
	if CardKind("held").Valid() {
		t.Fatal("held is not a card kind")
	}
	for _, tier := range append(AllTiers(), TierNone) {
		if !tier.Valid() {
			t.Fatalf("tier %q rejected", tier)
		}
	}
	for _, o := range AllOutcomes() {
		if !o.Valid() {
			t.Fatalf("outcome %q rejected", o)
		}
		if !ErrorClass(o).Valid() {
			t.Fatalf("error class %q rejected", o)
		}
	}
	for _, set := range []struct {
		name string
		ok   bool
	}{
		{"safe", FreezeSafe.Valid()},
		{"fast", FreezeFast.Valid()},
		{"direction", LayerDirection.Valid()},
		{"module", LayerModule.Valid()},
		{"detail", LayerDetail.Valid()},
		{"auto", ModeAuto.Valid()},
		{"manual", ModeManual.Valid()},
	} {
		if !set.ok {
			t.Fatalf("%s rejected", set.name)
		}
	}
	for _, s := range AllObjectiveStatuses() {
		if !s.Valid() {
			t.Fatalf("objective status %q", s)
		}
	}
	for _, s := range AllNodeStatuses() {
		if !s.Valid() {
			t.Fatalf("node status %q", s)
		}
	}
	for _, s := range AllCardStatuses() {
		if !s.Valid() {
			t.Fatalf("card status %q", s)
		}
	}
	if CardStatus("held").Valid() {
		t.Fatal("held is not a card status")
	}
	for _, h := range AllHealth() {
		if !h.Valid() {
			t.Fatalf("health %q", h)
		}
	}
	for _, r := range AllRunners() {
		if !r.Valid() {
			t.Fatalf("runner %q", r)
		}
	}
	for _, k := range AllQuestionKinds() {
		if !k.Valid() {
			t.Fatalf("question kind %q", k)
		}
	}
}

func TestSection3Fields(t *testing.T) {
	checks := []struct {
		value any
		tags  []string
	}{
		{Project{}, []string{"id", "repo_path", "worktree_root", "test_cmd", "design_dir", "forbidden_paths"}},
		{Objective{}, []string{"id", "project", "title", "acceptance", "budget", "priority", "mode", "freeze_mode", "origin", "status", "integration_branch"}},
		{Acceptance{}, []string{"commands", "checklist"}},
		{Budget{}, []string{"soft_tokens", "hard_tokens"}},
		{DesignNode{}, []string{"id", "objective", "parent", "layer", "doc_path", "open_questions", "status"}},
		{Card{}, []string{"id", "objective", "node", "kind", "title", "body", "tier", "requires", "claims", "deps", "acceptance", "base", "status", "rework_count", "legacy"}},
		{Claim{}, []string{"path", "symbol"}},
		{Attempt{}, []string{"id", "card", "route", "route_reason", "worktree", "branch", "base_commit", "head_commit", "pid", "started_at", "ended_at", "exit", "usage", "outcome", "error_class", "error_excerpt"}},
		{Usage{}, []string{"tokens", "source", "confidence"}},
		{Route{}, []string{"channel", "model"}},
		{Channel{}, []string{"id", "runner", "engine_profile", "source_group", "models", "concurrency", "quota_windows", "redline", "health"}},
		{Positioning{}, []string{"model", "family", "tiers", "capabilities", "preference", "version"}},
		{Question{}, []string{"id", "objective", "scope", "kind", "text", "options", "answer", "deliveries"}},
		{Event{}, []string{"ts", "entity", "type", "data"}},
		{Legacy{}, []string{"status", "source_path"}},
	}
	for _, check := range checks {
		got := jsonTags(reflect.TypeOf(check.value))
		for _, tag := range check.tags {
			if _, ok := got[tag]; !ok {
				t.Errorf("%T missing json tag %q", check.value, tag)
			}
		}
	}
}

func jsonTags(typ reflect.Type) map[string]struct{} {
	out := map[string]struct{}{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" {
			panic(typ.Name() + "." + f.Name + " has no json tag")
		}
		name := tag
		for j, c := range tag {
			if c == ',' {
				name = tag[:j]
				break
			}
		}
		out[name] = struct{}{}
	}
	return out
}
