package notify

import (
	"os"
	"strings"
)

// OriginFromArgs reads --origin <kind>:<id>. A present flag wins over CARDEX_ORIGIN.
// The id is everything after the first colon, so hermes:<profile>/<session> keeps the slash.
// getenv may be nil; then os.Getenv is used.
func OriginFromArgs(args []string, getenv func(string) string) (kind, id string, ok bool) {
	if value, present := originFlag(args); present && strings.TrimSpace(value) != "" {
		return splitOrigin(value)
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	return splitOrigin(getenv("CARDEX_ORIGIN"))
}

// ParseOrigin splits a kind:id string. The id keeps every character after the first colon.
func ParseOrigin(raw string) (kind, id string, ok bool) {
	return splitOrigin(raw)
}

func originFlag(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--origin" && i+1 < len(args) {
			return args[i+1], true
		}
		if rest, found := strings.CutPrefix(arg, "--origin="); found {
			return rest, true
		}
	}
	return "", false
}

func splitOrigin(raw string) (kind, id string, ok bool) {
	raw = strings.TrimSpace(raw)
	kind, id, found := strings.Cut(raw, ":")
	if !found || kind == "" || id == "" {
		return "", "", false
	}
	return kind, id, true
}
