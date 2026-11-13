package loop

import (
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestDecideUnavailableMovesWithoutRework(t *testing.T) {
	card := model.Card{ID: "c", Kind: model.KindReview, ReworkCount: 2}
	d := Decide(card, model.OutcomeUnavailable, "model gemini-3.7-flash-high is not recognized", 2, 2, true, History{NoOutput: 1})
	if d.Action != ActionUnavailableMove || !d.OtherChannel || !d.Notify || d.NeedsInput || d.SetRework {
		t.Fatalf("decision %+v", d)
	}
}

func TestNoOutputQuestionNamesEachCause(t *testing.T) {
	earlier := model.Attempt{
		ID: "a1", Index: 0, Route: model.Route{Channel: "grok", Model: "grok-4.6"},
		Outcome: model.OutcomeNoOutput, ErrorClass: model.ErrorPermissionDenied,
		ErrorExcerpt: "sandbox initialization failed:\n Operation not permitted",
	}
	passed := model.Attempt{ID: "a0", Index: 0, Outcome: model.OutcomePassed, Route: model.Route{Channel: "kimi"}}
	cur := model.Attempt{
		ID: "a2", Index: 1, Route: model.Route{Channel: "agy", Model: "gemini-3.7-flash-high"},
		Outcome: model.OutcomeNoOutput, ErrorExcerpt: "error: " + strings.Repeat("x", 400),
	}
	got := noOutputDetail([]model.Attempt{passed, earlier, cur}, cur)
	for _, want := range []string{
		noOutputQuestion,
		"attempt a1 on grok/grok-4.6: permission_denied: sandbox initialization failed: Operation not permitted",
		"attempt a2 on agy/gemini-3.7-flash-high: unclassified: error: xxx",
		"…",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "a0") || strings.Count(got, "attempt a2") != 1 {
		t.Fatalf("wrong attempts listed:\n%s", got)
	}
}
