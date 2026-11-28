package model

import (
	"errors"
	"strings"
)

// TierConvention identifies T1-strongest records outside of the SQLite schema.
const TierConvention = "t1-strongest-v1"

// LegacyTierWire identifies emitted fields that retain the old wire spelling.
const LegacyTierWire = "legacy-strength-compatible-v1"

// TranslateLegacyTier preserves capability while renumbering a legacy label.
// Empty is valid only where the caller supports an absent label.
func TranslateLegacyTier(old Tier) (Tier, error) {
	if old == TierNone {
		return TierNone, nil
	}
	canonical, ok := CanonicalTier(old)
	if !ok {
		return TierNone, errors.New("unknown tier; expected T1, T2, or T3")
	}
	switch canonical {
	case TierT1:
		return TierT3, nil
	case TierT3:
		return TierT1, nil
	default:
		return canonical, nil
	}
}

// TierFromConvention interprets a persisted configuration label. Unversioned
// home files retain their original meanings until the owner explicitly
// updates the labels and adds TierConvention to the document.
func TierFromConvention(t Tier, convention string) (Tier, error) {
	switch convention {
	case "", LegacyTierWire:
		return TranslateLegacyTier(t)
	case TierConvention:
		if t == TierNone {
			return t, nil
		}
		if canonical, ok := CanonicalTier(t); ok {
			return canonical, nil
		}
		return TierNone, errors.New("unknown tier; expected T1, T2, or T3")
	default:
		return TierNone, errors.New("unknown tier convention")
	}
}

// ReviewHoldMarker identifies the machine-read tier and optional pin in a
// review body. Review prose is not a tier field.
const ReviewHoldMarker = "<!-- cardex-review-hold -->"

// ParseReviewHold reads a hold without rewriting old immutable prose. A hold
// without its own convention is legacy even after the database migrates.
func ParseReviewHold(body string) (tier Tier, channel, name string, found bool, err error) {
	idx := strings.Index(body, ReviewHoldMarker)
	if idx < 0 {
		return TierNone, "", "", false, nil
	}
	found = true
	var raw Tier
	var convention string
	for _, line := range strings.Split(body[idx+len(ReviewHoldMarker):], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if raw != TierNone {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "<!--") {
			break
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			break
		}
		switch strings.TrimSpace(key) {
		case "tier":
			raw = Tier(strings.TrimSpace(val))
		case "tier_convention":
			convention = strings.TrimSpace(val)
		case "channel":
			channel = strings.TrimSpace(val)
		case "model":
			name = strings.TrimSpace(val)
		default:
			goto parsed
		}
	}
parsed:
	if raw == TierNone {
		return TierNone, "", "", true, errors.New("missing tier in review hold")
	}
	tier, err = TierFromConvention(raw, convention)
	return tier, channel, name, true, err
}
