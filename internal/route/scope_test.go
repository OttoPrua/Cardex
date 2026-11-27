package route

import (
	"encoding/json"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestChannelScope(t *testing.T) {
	auto := func(channel, pin string) model.Event {
		raw, _ := json.Marshal(map[string]string{"channel": channel, "pin": pin})
		return model.Event{Entity: "card:c", Type: "auto_route", Data: raw}
	}
	retry := func(channel, pin string) model.Event {
		raw, _ := json.Marshal(map[string]string{"channel": channel, "pin": pin})
		return model.Event{Entity: "card:c", Type: "retry_backoff", Data: raw}
	}
	card := model.Card{ID: "c", Channel: "auto"}
	obj := model.Objective{Channel: "objective"}
	if got := ChannelScope(card, obj, []model.Event{auto("auto", "user")}); got != "user" {
		t.Fatalf("auto pin %q", got)
	}
	if got := CardUserChannel(card, []model.Event{auto("auto", "user")}); got != "user" {
		t.Fatalf("card user %q", got)
	}
	card.Channel = "user"
	if got := ChannelScope(card, obj, nil); got != "user" {
		t.Fatalf("card channel %q", got)
	}
	card.Channel = ""
	if got := CardUserChannel(card, nil); got != "" {
		t.Fatalf("empty card copied objective %q", got)
	}
	if got := ChannelScope(card, obj, nil); got != "objective" {
		t.Fatalf("objective scope %q", got)
	}
	card.Channel = "tried"
	if got := CardUserChannel(card, []model.Event{retry("tried", "")}); got != "" {
		t.Fatalf("retry lock became a pin %q", got)
	}
	if got := ChannelScope(card, model.Objective{}, []model.Event{retry("tried", "")}); got != "" {
		t.Fatalf("unpinned retry scope %q", got)
	}
	if got := CardUserChannel(card, []model.Event{retry("tried", "user")}); got != "user" {
		t.Fatalf("retry kept user %q", got)
	}
}

func TestRestartTargetUsesObjectiveOnlyWhenCardPinIsEmpty(t *testing.T) {
	auto := func(channel, pin string) model.Event {
		raw, _ := json.Marshal(map[string]string{"channel": channel, "pin": pin})
		return model.Event{Entity: "card:c", Type: "auto_route", Data: raw}
	}
	card := model.Card{ID: "c", Channel: "B", Model: "model-b"}
	obj := model.Objective{Channel: "A", Model: "model-a"}
	events := []model.Event{auto("B", "")}
	channel, modelName, ok := RestartTarget(card, obj, events, "B")
	if !ok || channel != "A" || modelName != "model-a" {
		t.Fatalf("objective fallback %q %q %v", channel, modelName, ok)
	}
	events = []model.Event{auto("B", "card-pin")}
	channel, modelName, ok = RestartTarget(card, obj, events, "B")
	if !ok || channel != "card-pin" || modelName != "" {
		t.Fatalf("card pin %q %q %v", channel, modelName, ok)
	}
	channel, modelName, ok = RestartTarget(card, model.Objective{}, []model.Event{auto("B", "")}, "B")
	if ok || channel != "" || modelName != "" {
		t.Fatalf("both empty %q %q %v", channel, modelName, ok)
	}
	channel, modelName, ok = RestartTarget(card, obj, []model.Event{auto("B", "B")}, "B")
	if ok || channel != "" || modelName != "" {
		t.Fatalf("pin equal to auto channel fell through %q %q %v", channel, modelName, ok)
	}
	same := model.Objective{Channel: "B", Model: "model-a"}
	channel, modelName, ok = RestartTarget(card, same, []model.Event{auto("B", "")}, "B")
	if ok || channel != "" || modelName != "" {
		t.Fatalf("objective channel equal to auto channel %q %q %v", channel, modelName, ok)
	}
}
