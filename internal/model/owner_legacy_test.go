package model

import (
	"bytes"
	"testing"
)

func TestLegacyOwnerTypeReadsAsOwner(t *testing.T) {
	for _, raw := range []OpenQuestionType{"otto", "OTTO", " otto "} {
		if got := CanonicalOpenQuestionType(raw); got != OQOwner {
			t.Fatalf("%q canonical %q", raw, got)
		}
	}
	if got := CanonicalOpenQuestionType(" owner "); got != OQOwner {
		t.Fatalf("owner canonical %q", got)
	}
	if got := CanonicalOpenQuestionType("fact"); got != OQFact {
		t.Fatalf("fact canonical %q", got)
	}
	if got := CanonicalOpenQuestionType("experiment"); got != OQExperiment {
		t.Fatalf("experiment canonical %q", got)
	}
	if got := CanonicalOpenQuestionType("decision"); got != OQDecision {
		t.Fatalf("decision canonical %q", got)
	}

	raw := []byte(`[{"id":"old","text":"keep going","type":"otto","status":"open","evidence":"note"},{"id":"fact","text":"look","type":"fact","status":"open","asks":2}]`)
	qs, err := ParseOpenQuestions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0].Type != OQOwner || qs[0].Text != "keep going" || qs[0].Evidence != "note" {
		t.Fatalf("read %+v", qs)
	}
	if qs[1].Type != OQFact || qs[1].Text != "look" || qs[1].Asks != 2 {
		t.Fatalf("fact %+v", qs[1])
	}

	next, changed, err := RewriteStoredOwnerQuestionTypes(raw)
	if err != nil || !changed {
		t.Fatalf("rewrite changed %v err %v", changed, err)
	}
	if bytes.Contains(next, []byte(`"otto"`)) || !bytes.Contains(next, []byte(`"owner"`)) {
		t.Fatalf("stored %s", next)
	}
	again, changed, err := RewriteStoredOwnerQuestionTypes(next)
	if err != nil || changed || !bytes.Equal(again, next) {
		t.Fatalf("second rewrite changed %v equal %v err %v", changed, bytes.Equal(again, next), err)
	}
	rewritten, err := ParseOpenQuestions(next)
	if err != nil {
		t.Fatal(err)
	}
	if rewritten[0].Type != OQOwner || rewritten[0].Evidence != "note" || rewritten[1].Type != OQFact || rewritten[1].Asks != 2 {
		t.Fatalf("rewritten values %+v", rewritten)
	}
	untouched := []byte(`[{"id":"only","text":"plain","type":"decision","status":"answered"}]`)
	same, changed, err := RewriteStoredOwnerQuestionTypes(untouched)
	if err != nil || changed || !bytes.Equal(same, untouched) {
		t.Fatalf("other row changed %v %s", changed, same)
	}
}
