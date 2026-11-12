package notify

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// SkipNoSubscription is the hermes hop skip when no enabled receiver matches.
	SkipNoSubscription = "缺订阅 id"
	// SkipProfileAmbiguous is the hermes hop skip when several profiles match and no session does.
	SkipProfileAmbiguous = "hermes profile 匹配不唯一"
	// SkipSessionAmbiguous is the hermes hop skip when several sessions match.
	SkipSessionAmbiguous = "hermes session 匹配不唯一"
	// inboxAddress is the landing of the home inbox. File origin and fallback
	// hops write that inbox, so they use this address too. A file receiver
	// keeps file:<target>, the path it actually writes.
	inboxAddress = "inbox"
)

// SkipError is a hermes hop that was not delivered. Error text stays the
// historical invalid_subscription_id token so callers can tell it from a failure.
type SkipError struct {
	Reason string
}

func (e *SkipError) Error() string { return "invalid_subscription_id" }

// UnknownAdapterError marks an origin or fallback whose adapter this build
// cannot deliver to (for example cursor:, cutover: or grokbot:). The home inbox
// hop is still written, so callers treat it as a warning, not a failed delivery.
type UnknownAdapterError struct {
	Key  string // origin or fallback
	Kind string
}

func (e *UnknownAdapterError) Error() string {
	return "unknown_adapter:" + e.Kind + " (" + e.Key + "; delivered to the home inbox only)"
}

// SkipReason collects skip reasons from err. An empty result means the caller
// should use SkipNoSubscription when the error is still a subscription skip.
func SkipReason(err error) string {
	if err == nil {
		return ""
	}
	var parts []string
	seen := map[string]bool{}
	var walk func(error)
	walk = func(err error) {
		if err == nil {
			return
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, item := range joined.Unwrap() {
				walk(item)
			}
			return
		}
		var skip *SkipError
		if errorsAsSkip(err, &skip) {
			text := skip.Reason
			if text == "" {
				text = SkipNoSubscription
			}
			if !seen[text] {
				seen[text] = true
				parts = append(parts, text)
			}
		}
	}
	walk(err)
	return strings.Join(parts, "; ")
}

func errorsAsSkip(err error, target **SkipError) bool {
	for err != nil {
		if skip, ok := err.(*SkipError); ok {
			*target = skip
			return true
		}
		unwrap, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrap.Unwrap()
	}
	return false
}

// Hop is one delivery address after origin, scope, fallback, and inbox resolution.
type Hop struct {
	Key     string // completed-map key: origin, fallback, inbox, or receiver:<id>
	Address string
	Kind    string
	ID      string
	Sub     string
	Root    string
}

// Resolve lists delivery hops and hermes skips. The events filter applies only
// to scope-matched receivers. Addresses are deduped; origin wins.
func Resolve(msg Message) (hops []Hop, skips []string, problems []error) {
	seen := map[string]bool{}
	add := func(hop Hop) {
		if hop.Address == "" || seen[hop.Address] {
			return
		}
		seen[hop.Address] = true
		hops = append(hops, hop)
	}
	if kind, id, ok := ParseOrigin(msg.Origin); ok {
		if hop, skip, problem := originHop("origin", kind, id, msg); problem != nil {
			problems = append(problems, problem)
		} else if skip != "" {
			skips = append(skips, skip)
		} else {
			add(hop)
		}
	} else if strings.TrimSpace(msg.Origin) != "" {
		problems = append(problems, fmt.Errorf("notify: origin %q", msg.Origin))
	}
	receivers := append([]Receiver(nil), msg.Receivers...)
	sort.Slice(receivers, func(i, j int) bool { return receivers[i].ID < receivers[j].ID })
	for _, rec := range receivers {
		if !scopeMatches(rec, msg) || !eventsAllow(rec, msg.Kind) {
			continue
		}
		hop, skip := receiverHop(rec)
		if skip != "" {
			skips = append(skips, skip)
			continue
		}
		add(hop)
	}
	if kind, id, ok := ParseOrigin(msg.Fallback); ok && msg.Fallback != msg.Origin {
		if hop, skip, problem := originHop("fallback", kind, id, msg); problem != nil {
			problems = append(problems, problem)
		} else if skip != "" {
			skips = append(skips, skip)
		} else {
			add(hop)
		}
	} else if strings.TrimSpace(msg.Fallback) != "" && msg.Fallback != msg.Origin {
		problems = append(problems, fmt.Errorf("notify: fallback %q", msg.Fallback))
	}
	// The home inbox hop always stays. A file origin or fallback may already
	// use inboxAddress, because that is the place those hops write.
	hops = append(hops, Hop{Key: "inbox", Address: inboxAddress, Kind: "inbox"})
	return hops, skips, problems
}

func originHop(key, kind, id string, msg Message) (Hop, string, error) {
	hop := Hop{Key: key, Kind: kind, ID: id}
	if kind == "hermes" {
		sub, root, reason := resolveHermes(id, msg)
		if reason != "" {
			return Hop{}, reason, nil
		}
		hop.Sub = sub
		hop.Root = root
		hop.Address = hermesAddress(root, sub)
		return hop, "", nil
	}
	if !knownAdapter(kind) {
		return Hop{}, "", &UnknownAdapterError{Key: key, Kind: kind}
	}
	// A file origin or fallback writes the home inbox, not id as a path.
	// file:<id> would match a receiver target this hop never writes.
	if kind == "file" {
		hop.Address = inboxAddress
		return hop, "", nil
	}
	hop.Address = kind + ":" + id
	return hop, "", nil
}

func receiverHop(rec Receiver) (Hop, string) {
	hop := Hop{
		Key:  "receiver:" + rec.ID,
		Kind: rec.Adapter,
		ID:   rec.Target,
	}
	switch rec.Adapter {
	case "hermes":
		if !validSubscriptionID(rec.ID) {
			return Hop{}, SkipNoSubscription
		}
		hop.Sub = rec.ID
		hop.Root = rec.Root
		hop.Address = hermesAddress(rec.Root, rec.ID)
	case "codex", "lark", "file":
		hop.Address = rec.Adapter + ":" + rec.Target
	default:
		return Hop{}, ""
	}
	return hop, ""
}

func hermesAddress(root, sub string) string {
	if strings.TrimSpace(root) == "" {
		root = "home"
	}
	return "hermes:" + root + "|" + sub
}

// resolveHermes picks a subscription id for origin id profile/session.
// A caller-supplied SubscriptionID is used as-is and is not looked up.
func resolveHermes(originID string, msg Message) (sub, root, reason string) {
	if strings.TrimSpace(msg.SubscriptionID) != "" {
		return msg.SubscriptionID, "", ""
	}
	profile, session := hermesParts(originID)
	var sessions, profiles []Receiver
	for _, rec := range msg.Receivers {
		if !rec.Enabled || rec.Adapter != "hermes" || !validSubscriptionID(rec.ID) {
			continue
		}
		rp, rs := hermesTargetParts(rec.Target)
		if rs != "" && rs == session && (profile == "" || rp == "" || rp == profile) {
			sessions = append(sessions, rec)
			continue
		}
		if rp != "" && profile != "" && rp == profile {
			profiles = append(profiles, rec)
		}
	}
	switch {
	case len(sessions) == 1:
		return sessions[0].ID, sessions[0].Root, ""
	case len(sessions) > 1:
		return "", "", SkipSessionAmbiguous
	case len(profiles) == 1:
		return profiles[0].ID, profiles[0].Root, ""
	case len(profiles) > 1:
		return "", "", SkipProfileAmbiguous
	default:
		return "", "", SkipNoSubscription
	}
}

func hermesParts(id string) (profile, session string) {
	profile, session, ok := strings.Cut(id, "/")
	if !ok {
		return "", id
	}
	return profile, session
}

func hermesTargetParts(target string) (profile, session string) {
	profile, session, ok := strings.Cut(target, "/")
	if !ok {
		return strings.TrimSpace(target), ""
	}
	return strings.TrimSpace(profile), strings.TrimSpace(session)
}

func scopeMatches(rec Receiver, msg Message) bool {
	if !rec.Enabled {
		return false
	}
	if rec.Scope.All {
		return true
	}
	if len(rec.Scope.Projects) == 0 && len(rec.Scope.Objectives) == 0 && len(rec.Scope.DirPrefixes) == 0 {
		return false
	}
	for _, project := range rec.Scope.Projects {
		if projectMatches(project, msg.Project) {
			return true
		}
	}
	for _, objective := range rec.Scope.Objectives {
		if objective == msg.Objective && objective != "" {
			return true
		}
	}
	if strings.TrimSpace(msg.Dir) == "" {
		return false
	}
	dir := filepath.Clean(msg.Dir)
	for _, prefix := range rec.Scope.DirPrefixes {
		if prefix == "" {
			continue
		}
		cleaned := filepath.Clean(prefix)
		if dir == cleaned || strings.HasPrefix(dir, cleaned+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// projectMatches keeps a stored v1 project name equal to that name, and also
// equal to the project id import-legacy stores for it. importer.projectID is
// the generator; notify cannot import importer.
func projectMatches(name, delivered string) bool {
	if name == "" || delivered == "" {
		return false
	}
	return name == delivered || delivered == legacyProjectID(name)
}

func legacyProjectID(project string) string {
	return "v1proj-" + legacySanitize(project)
}

func legacySanitize(s string) string {
	if legacyPreserved(s) {
		return s
	}
	return "g" + hex.EncodeToString([]byte(s))
}

func legacyPreserved(s string) bool {
	if s == "" || legacyGenerated(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func legacyGenerated(s string) bool {
	if len(s) == 0 || s[0] != 'g' {
		return false
	}
	rest := s[1:]
	if len(rest)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}

func eventsAllow(rec Receiver, kind string) bool {
	if len(rec.Events) == 0 {
		return true
	}
	for _, ev := range rec.Events {
		if ev == kind && kind != "" {
			return true
		}
	}
	return false
}
