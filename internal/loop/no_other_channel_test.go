package loop

import (
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestNoOtherChannelText(t *testing.T) {
	chs := []model.Channel{{ID: "cx", Runner: model.RunnerCodex}}
	plain := noOtherChannelText(model.Attempt{Outcome: model.OutcomeQuotaHit, Route: model.Route{Channel: "cx"}}, chs)
	if plain != "no other channel is available" {
		t.Fatalf("plain %q", plain)
	}
	got := noOtherChannelText(model.Attempt{Outcome: model.OutcomeAuthFailed, Route: model.Route{Channel: "cx"}}, chs)
	for _, want := range []string{"no other channel is available (", "channel cx is not signed in", "codex login", "channel set -id cx -health ok"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
