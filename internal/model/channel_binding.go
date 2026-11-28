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
