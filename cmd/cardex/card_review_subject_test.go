package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCardAddReviewSubjectHint(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	mustHome(t, home, "project", "add", "--id", "p", "--repo", repo)
	mustHome(t, home, "objective", "add", "--id", "o", "--project", "p", "--title", "o")
	mustHome(t, home, "card", "add", "--id", "dev", "--objective", "o", "--title", "dev", "--kind", "dev")

	code, out, errb := callCLI(t, []string{"--home", home, "--json", "card", "add", "--id", "rv-gone", "--objective", "o", "--title", "alone", "--kind", "review"})
	if code != 0 {
		t.Fatalf("standalone add %d %s", code, errb)
	}
	if !strings.Contains(errb, "rv-gone") || !strings.Contains(errb, "gone") || !strings.Contains(errb, "standalone") || !strings.Contains(errb, "--dep") {
		t.Fatalf("hint %q", errb)
	}
	if strings.Contains(out, "standalone review") {
		t.Fatalf("hint leaked into stdout %s", out)
	}
	var added map[string]any
	if err := json.Unmarshal([]byte(out), &added); err != nil {
		t.Fatal(err)
	}
	devOut := mustHome(t, home, "--json", "card", "add", "--id", "plain", "--objective", "o", "--title", "plain", "--kind", "dev")
	var dev map[string]any
	if err := json.Unmarshal([]byte(devOut), &dev); err != nil {
		t.Fatal(err)
	}
	if len(added) != len(dev) {
		t.Fatalf("json fields %v vs %v", keysOf(added), keysOf(dev))
	}
	for key := range added {
		if _, ok := dev[key]; !ok {
			t.Fatalf("new json field %s", key)
		}
	}
	if added["id"] != "rv-gone" || added["kind"] != "review" {
		t.Fatalf("card %+v", added)
	}

	code, out, errb = callCLI(t, []string{"--home", home, "--json", "card", "add", "--id", "rv-dev", "--objective", "o", "--title", "prefix", "--kind", "review"})
	if code != 0 {
		t.Fatalf("prefix add %d %s", code, errb)
	}
	if strings.Contains(errb, "standalone") {
		t.Fatalf("hint for an existing subject %q", errb)
	}
	if !strings.Contains(out, `"rv-dev"`) {
		t.Fatalf("stdout %s", out)
	}

	code, _, errb = callCLI(t, []string{"--home", home, "card", "add", "--id", "rv-bad", "--objective", "o", "--title", "bad", "--kind", "review", "--dep", "nope"})
	if code == 0 || !strings.Contains(errb, "nope") || !strings.Contains(errb, "does not exist") {
		t.Fatalf("missing dep code %d stderr %q", code, errb)
	}
	list := mustHome(t, home, "card", "list", "--objective", "o")
	if strings.Contains(list, "rv-bad") {
		t.Fatalf("missing dep card was stored\n%s", list)
	}

	code, out, errb = callCLI(t, []string{"--home", home, "--json", "card", "add", "--id", "rv-kept", "--objective", "o", "--title", "kept", "--kind", "review", "--dep", "dev"})
	if code != 0 {
		t.Fatalf("dep add %d %s", code, errb)
	}
	var kept map[string]any
	if err := json.Unmarshal([]byte(out), &kept); err != nil {
		t.Fatal(err)
	}
	deps, _ := kept["deps"].([]any)
	if len(deps) != 1 || deps[0] != "dev" {
		t.Fatalf("deps %+v", kept["deps"])
	}
	if strings.Contains(errb, "standalone") {
		t.Fatalf("hint despite --dep %q", errb)
	}

	code, _, errb = callCLI(t, []string{"--home", home, "card", "add", "--id", "dev-bad-dep", "--objective", "o", "--title", "dev", "--kind", "dev", "--dep", "also-missing"})
	if code != 0 {
		t.Fatalf("non-review missing dep should still add: %d %s", code, errb)
	}

	code, _, errb = callCLI(t, []string{"--home", home, "card", "add", "--id", "rv-probe", "--objective", "o", "--title", "probe", "--kind", "review", "--dep", "", "--dep", "missing"})
	if code == 0 || !strings.Contains(errb, "missing") || !strings.Contains(errb, "does not exist") {
		t.Fatalf("leading blank dep code %d stderr %q", code, errb)
	}
	list = mustHome(t, home, "card", "list", "--objective", "o")
	if strings.Contains(list, "rv-probe") {
		t.Fatalf("leading blank dep stored the card\n%s", list)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}
