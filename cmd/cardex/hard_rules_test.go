package main

import (
	"encoding/json"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestHardRulesCLIReplaceAndClear(t *testing.T) {
	home := t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		code, out, err := runMain(t, append([]string{"--home", home, "--json"}, args...)...)
		if code != 0 {
			t.Fatalf("%v returned %d: %s", args, code, err)
		}
		return []byte(out)
	}
	run("project", "add", "--id", "proj", "--repo", t.TempDir(), "--hard-rule", " No publishing ", "--hard-rule", "No publishing")
	run("objective", "add", "--id", "obj", "--project", "proj", "--title", "Synthetic rules", "--hard-rule", "Use generated fixtures")
	run("objective", "set", "--id", "obj", "--priority", "2")
	var p model.Project
	var o model.Objective
	if err := json.Unmarshal(run("project", "show", "proj"), &p); err != nil || len(p.HardRules) != 1 || p.HardRules[0] != "No publishing" {
		t.Fatalf("project rules %v: %v", p.HardRules, err)
	}
	if err := json.Unmarshal(run("objective", "show", "obj"), &o); err != nil || len(o.HardRules) != 1 || o.HardRules[0] != "Use generated fixtures" {
		t.Fatalf("objective rules %v: %v", o.HardRules, err)
	}
	run("project", "set", "--id", "proj", "--hard-rule", "Project replacement")
	run("objective", "set", "--id", "obj", "--hard-rule=")
	p, o = model.Project{}, model.Objective{}
	if err := json.Unmarshal(run("project", "show", "proj"), &p); err != nil || len(p.HardRules) != 1 || p.HardRules[0] != "Project replacement" {
		t.Fatalf("project replacement %v: %v", p.HardRules, err)
	}
	if err := json.Unmarshal(run("objective", "show", "obj"), &o); err != nil || len(o.HardRules) != 0 {
		t.Fatalf("objective clear %v: %v", o.HardRules, err)
	}
}
