package route

import (
	"encoding/json"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// CardUserChannel is the card-level limit, not the objective channel.
// A channel written by the latest auto_route is that event's pin.
// A channel stored for a same-channel retry is the pin recorded on that
// retry, which is empty when the card was not limited.
func CardUserChannel(card model.Card, events []model.Event) string {
	if channel, pin, ok := latestAutoRoute(events, card.ID); ok && card.Channel == channel {
		return pin
	}
	if pin, ok := retryUserPin(events, card.ID, card.Channel); ok {
		return pin
	}
	return card.Channel
}

// ChannelScope is the one channel a retry may not leave.
// The card-level user channel wins; otherwise the objective channel.
// An empty scope keeps today's automatic switch.
func ChannelScope(card model.Card, objective model.Objective, events []model.Event) string {
	if user := CardUserChannel(card, events); user != "" {
		return user
	}
	return objective.Channel
}

// RestartTarget is the channel and model an automatic switch may return to.
// The card must still sit on autoChannel. A different card-level pin wins
// and clears the model. An empty card-level pin uses the objective channel
// and model. The automatic channel itself is not a target, and a non-empty
// card pin is not replaced by the objective. ok is false when neither level
// names another channel, which leaves the current lock in place.
func RestartTarget(card model.Card, objective model.Objective, events []model.Event, autoChannel string) (channel, modelName string, ok bool) {
	if autoChannel == "" || card.Channel != autoChannel {
		return "", "", false
	}
	user := CardUserChannel(card, events)
	if user != "" {
		if user == autoChannel {
			return "", "", false
		}
		return user, "", true
	}
	scope := ChannelScope(card, objective, events)
	if scope == "" || scope == autoChannel {
		return "", "", false
	}
	return scope, objective.Model, true
}

// AutoSelection identifies the current scheduler-generated route.
type AutoSelection struct {
	EventID string
	Channel string
	At      time.Time
}

// LatestAutoSelection returns the active automatic switch generation.
func LatestAutoSelection(events []model.Event, cardID string) AutoSelection {
	var choice AutoSelection
	for _, event := range events {
		if event.Type != "auto_route" || event.Entity != "card:"+cardID {
			continue
		}
		var data struct {
			Channel string `json:"channel"`
		}
		if json.Unmarshal(event.Data, &data) == nil {
			choice = AutoSelection{EventID: event.ID, Channel: data.Channel, At: event.TS}
		}
	}
	return choice
}

func latestAutoRoute(events []model.Event, cardID string) (channel, pin string, ok bool) {
	for _, event := range events {
		if event.Type != "auto_route" || event.Entity != "card:"+cardID {
			continue
		}
		var data struct {
			Channel string `json:"channel"`
			Pin     string `json:"pin"`
		}
		if json.Unmarshal(event.Data, &data) != nil {
			continue
		}
		channel, pin, ok = data.Channel, data.Pin, true
	}
	return channel, pin, ok
}

// retryUserPin is the user channel captured before a same-channel retry
// wrote the attempt channel onto the card. Events without a pin key are
// older records and do not count. The earliest matching record wins.
func retryUserPin(events []model.Event, cardID, cardChannel string) (string, bool) {
	if cardChannel == "" {
		return "", false
	}
	found := false
	pin := ""
	for _, event := range events {
		if event.Type != "retry_backoff" || event.Entity != "card:"+cardID {
			continue
		}
		var data struct {
			Channel string  `json:"channel"`
			Pin     *string `json:"pin"`
		}
		if json.Unmarshal(event.Data, &data) != nil || data.Pin == nil || data.Channel != cardChannel {
			continue
		}
		if !found {
			pin = *data.Pin
			found = true
		}
	}
	return pin, found
}

// RecoveryAuthorization ties the one allowed target to the answered question,
// latest settled attempt, and exact auto-route generation. Only the trusted
// operator-retry transaction writes it.
type RecoveryAuthorization struct {
	Route     model.Route `json:"route"`
	Question  string      `json:"question"`
	Attempt   string      `json:"attempt"`
	AutoRoute string      `json:"auto_route"`
}

func RecoveryPinEvent(cardID string, authorization RecoveryAuthorization) (model.Event, error) {
	raw, err := json.Marshal(authorization)
	return model.Event{Entity: "card:" + cardID, Type: "repair_recovered", Data: raw}, err
}

// RecoveryPin returns the last authorized target while the card still carries
// that exact pin and the switch generation is current.
func RecoveryPin(card model.Card, events []model.Event) (model.Route, bool) {
	auto := LatestAutoSelection(events, card.ID)
	if auto.EventID == "" {
		return model.Route{}, false
	}
	var valid RecoveryAuthorization
	for _, event := range events {
		if event.Type != "repair_recovered" || event.Entity != "card:"+card.ID {
			continue
		}
		var pin RecoveryAuthorization
		if json.Unmarshal(event.Data, &pin) == nil && pin.AutoRoute == auto.EventID &&
			pin.Question != "" && pin.Attempt != "" && !event.TS.Before(auto.At) {
			valid = pin
		}
	}
	target := valid.Route
	return target, target.Channel != "" && card.Channel == target.Channel && card.Model == target.Model
}

// FallbackRetryBlock marks a current automatic route that stopped for a
// typed operator retry choice. Pausing or resuming the card cannot retire it.
type FallbackRetryBlock struct {
	Question  string `json:"question"`
	Attempt   string `json:"attempt"`
	AutoRoute string `json:"auto_route"`
}

const fallbackRetryOffered = "automatic_recovery_offered"

func FallbackRetryOffer(cardID string, retry FallbackRetryBlock) (model.Event, error) {
	raw, err := json.Marshal(retry)
	return model.Event{Entity: "card:" + cardID, Type: fallbackRetryOffered, Data: raw}, err
}

func FallbackRetryBlockFor(card model.Card, events []model.Event) (FallbackRetryBlock, bool) {
	auto := LatestAutoSelection(events, card.ID)
	if auto.EventID == "" || auto.Channel != card.Channel {
		return FallbackRetryBlock{}, false
	}
	var block FallbackRetryBlock
	for _, event := range events {
		if event.Type != fallbackRetryOffered || event.Entity != "card:"+card.ID {
			continue
		}
		var offered FallbackRetryBlock
		if json.Unmarshal(event.Data, &offered) == nil && offered.AutoRoute == auto.EventID &&
			offered.Attempt != "" && offered.Question != "" {
			block = offered
		}
	}
	return block, block.Attempt != ""
}

const interruptedRecoveryHeld = "automatic_recovery_held"

// HoldInterruptedRecovery is written before a manual pause/cancel would erase
// the needs-input linkage. Legacy state may not have a typed retry offer.
func HoldInterruptedRecovery(cardID, autoRouteID string) (model.Event, error) {
	raw, err := json.Marshal(struct {
		AutoRoute string `json:"auto_route"`
	}{AutoRoute: autoRouteID})
	return model.Event{Entity: "card:" + cardID, Type: interruptedRecoveryHeld, Data: raw}, err
}

func InterruptedRecoveryHeld(card model.Card, events []model.Event) bool {
	auto := LatestAutoSelection(events, card.ID)
	if auto.EventID == "" || auto.Channel != card.Channel {
		return false
	}
	for _, event := range events {
		if event.Type != interruptedRecoveryHeld || event.Entity != "card:"+card.ID {
			continue
		}
		var hold struct {
			AutoRoute string `json:"auto_route"`
		}
		if json.Unmarshal(event.Data, &hold) == nil && hold.AutoRoute == auto.EventID {
			return true
		}
	}
	return false
}
