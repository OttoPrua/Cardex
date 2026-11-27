package model

import "strings"

const CompatibleEngineProtocol = "anthropic-compatible"
const CompatibleEnginePrefix = CompatibleEngineProtocol + ":"

// HasCompatibleEngineBinding identifies the persisted API protocol marker.
// It recognizes damaged tagged references too, so legacy paths fail closed
// rather than acting on the assumption that an API channel lost its binding.
func HasCompatibleEngineBinding(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), CompatibleEnginePrefix)
}
