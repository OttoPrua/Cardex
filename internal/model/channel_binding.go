package model

import (
	"fmt"
	"slices"
	"strings"
)

const CompatibleEngineProtocol = "anthropic-compatible"
const CompatibleEnginePrefix = CompatibleEngineProtocol + ":"

// CompatibleRuntimeHold is a staging-only capability boundary, independent of
// stored health or a local flag. A healthy row from an older version cannot
// enable a provider request in this build.
const CompatibleRuntimeHold = "compatible API runtime is disabled; use a legacy channel"

func (ch Channel) CompatibleRuntimeHeld() bool { return HasCompatibleEngineBinding(ch.EngineProfile) }

// ValidateCompatiblePositioning prevents either writer from assigning a
// different family to a model that is already bound to a compatible channel.
func ValidateCompatiblePositioning(ch Channel, rows []Positioning) error {
	if !HasCompatibleEngineBinding(ch.EngineProfile) {
		return nil
	}
	for _, row := range rows {
		if row.Family != "" && row.Family != ch.Family && slices.Contains(ch.Models, row.Model) {
			return fmt.Errorf("routing conflict: positioning %s gives model %q family %q, but API channel %s is bound to family %q",
				row.ID, row.Model, row.Family, ch.ID, ch.Family)
		}
	}
	return nil
}

// HasCompatibleEngineBinding identifies the persisted API protocol marker.
// It recognizes damaged tagged references too, so legacy paths fail closed
// rather than acting on the assumption that an API channel lost its binding.
func HasCompatibleEngineBinding(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), CompatibleEnginePrefix)
}

// CompatibleHistoricalRecoveryHold identifies a card-only park: this build
// cannot resume the staged route and does not know what the old attempt did.
const CompatibleHistoricalRecoveryHold = "compatible API runtime is disabled; inspect the historical attempt before retrying"

const LegacyClaudeBuildHold = "legacy Claude profile is blocked locally; check channel configuration and credentials before retrying"

const (
	EventLegacyClaudeBuildHeld   = "legacy_claude_profile_build_held"
	EventAttemptPrepared         = "scheduler_attempt_prepared"
	EventCompatibleAttemptHeld   = "compatible_runtime_attempt_held"
	EventCompatibleRefusedNoWire = "compatible_runtime_refused_no_wire"
)

// HistoricalCompatibleHoldQuarantine recognizes the old terminal hold shape.
// Older writers sometimes did not save a binding or typed event. Suppress
// generic disposition on the exact shape, but never use this as evidence of
// the historical transport, actual dispatch, or whether a process was started.
func (a Attempt) HistoricalCompatibleHoldQuarantine() bool {
	return a.Outcome == OutcomeNoOutput && !a.EndedAt.IsZero() && a.ErrorExcerpt == CompatibleRuntimeHold
}

// EventHostDependencyDeferred is used at several D1 admission stages. Only
// a freshly written, identity-bound probe record is known to precede effects.
const EventHostDependencyDeferred = "host_dependency_deferred"
const HostDependencyPreclaimProbe = "preclaim_probe"

type HostDependencyDeferral struct {
	Card    string `json:"card"`
	Attempt string `json:"attempt,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Reason  string `json:"reason"`
}
