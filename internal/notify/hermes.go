package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	hermesInboundSchemaV1 = "cardex.manager_wake.hermes_inbound.v1"
	hermesAckSchemaV1     = "cardex.manager_wake.hermes_ack.v1"
	hermesSessionType     = "hermes_session"
)

// subscriptionIDPattern matches the receiver's closed subscription token.
var subscriptionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type hermesInboundFile struct {
	Schema         string   `json:"schema"`
	SubscriptionID string   `json:"subscription_id"`
	Provider       string   `json:"provider"`
	SessionType    string   `json:"session_type"`
	SessionID      string   `json:"session_id"`
	Profile        string   `json:"profile,omitempty"`
	WakeEventIDs   []string `json:"wake_event_ids"`
	Message        string   `json:"message"`
	InboundDigest  string   `json:"inbound_digest"`
	TS             string   `json:"ts"`
}

type hermesAckFile struct {
	Schema        string   `json:"schema"`
	WakeEventIDs  []string `json:"wake_event_ids"`
	InboundDigest string   `json:"inbound_digest"`
	ReceivedAt    string   `json:"received_at"`
}

func splitHermesID(id string) (profile, session string) {
	profile, session, ok := strings.Cut(id, "/")
	if !ok {
		return "", id
	}
	return profile, session
}

func hermesInboxDir(home, subscriptionID string) string {
	return filepath.Join(home, "control", "manager-wake", "hermes-inbox", subscriptionID)
}

func hermesInboundPath(home, subscriptionID, wakeID string) string {
	return filepath.Join(hermesInboxDir(home, subscriptionID), wakeID+".json")
}

func hermesAckPath(home, subscriptionID, wakeID string) string {
	return filepath.Join(hermesInboxDir(home, subscriptionID), wakeID+".ack.json")
}

func inboundDigest(message string) string {
	sum := sha256.Sum256([]byte(message))
	return hex.EncodeToString(sum[:])
}

func validSubscriptionID(id string) bool {
	return subscriptionIDPattern.MatchString(id)
}

// compactQuestionWake builds one receiver-valid cardex-wake v1 body.
// The question text stays in the file inbox; this body is not that text.
func compactQuestionWake(subscriptionID, questionID, objective string, seq int) (string, []string) {
	task := compactTaskID(questionID)
	if seq < 1 {
		seq = 1
	}
	wake := fmt.Sprintf("%s:%d", task, seq)
	var b strings.Builder
	fmt.Fprintf(&b, "cardex-wake v1 n=1 high_water=%d sub=%s\n", seq, subscriptionID)
	fmt.Fprintf(&b, "%s type=needs_owner project=%s status=open reason=question transition=- wake=%s\n",
		task, compactProject(objective), wake)
	return b.String(), []string{wake}
}

const (
	deliveryIDMax   = 80
	deliveryDigestN = 16
)

// deliveryIdentity is the receiver token for one question id.
// Every id is mixed with a stable digest of that exact raw id.
// A receiver-legal id is not returned unchanged, so it cannot share
// a token, wake, ack path, or inbox name with a different raw id.
func deliveryIdentity(id string) string {
	suffix := "-" + questionDigest(id)
	prefix := compactPrefix(id, deliveryIDMax-len(suffix))
	token := prefix + suffix
	if prefix == "" {
		token = "q" + suffix
	}
	if !deliveryIDOK(token) {
		token = "q" + suffix
	}
	return token
}

func questionDigest(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:deliveryDigestN]
}

func deliveryIDOK(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > deliveryIDMax {
		return false
	}
	return isTaskID(s)
}

func compactPrefix(id string, max int) string {
	var b strings.Builder
	for _, r := range id {
		if isCompactRune(r) {
			b.WriteRune(r)
		}
	}
	s := b.String()
	for strings.Contains(s, "..") {
		s = strings.ReplaceAll(s, "..", ".")
	}
	s = strings.Trim(s, ".:-")
	if s == "" || max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) > max {
		runes = runes[:max]
	}
	s = strings.TrimRight(string(runes), ".:-")
	if s == "" || !isTaskID(s) {
		return ""
	}
	return s
}

func compactTaskID(id string) string {
	return deliveryIdentity(id)
}

func compactProject(objective string) string {
	if objective == "" || strings.Contains(objective, "/") || strings.Contains(objective, `\`) || strings.Contains(objective, "..") {
		return "-"
	}
	for _, r := range objective {
		if !isCompactRune(r) {
			return "-"
		}
	}
	return objective
}

func isCompactRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == ':' || r == '-'
}

func isTaskID(s string) bool {
	if s == "" || strings.Contains(s, "..") || strings.Contains(s, "/") {
		return false
	}
	for i, r := range s {
		if i == 0 && !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
		if !isCompactRune(r) {
			return false
		}
	}
	return true
}

func writeHermesInbound(home, originID string, msg Message, now time.Time, seq int) (wakeID, digest string, err error) {
	if !validSubscriptionID(msg.SubscriptionID) {
		return "", "", fmt.Errorf("invalid_subscription_id")
	}
	profile, session := splitHermesID(originID)
	message, wakeIDs := compactQuestionWake(msg.SubscriptionID, msg.QuestionID, msg.Objective, seq)
	wakeID = wakeIDs[0]
	if wakeID == "" || strings.Contains(wakeID, "/") || strings.Contains(wakeID, "..") {
		return "", "", fmt.Errorf("invalid_wake_id")
	}
	digest = inboundDigest(message)
	body := hermesInboundFile{
		Schema:         hermesInboundSchemaV1,
		SubscriptionID: msg.SubscriptionID,
		Provider:       "hermes",
		SessionType:    hermesSessionType,
		SessionID:      session,
		Profile:        profile,
		WakeEventIDs:   wakeIDs,
		Message:        message,
		InboundDigest:  digest,
		TS:             now.UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", "", err
	}
	path := hermesInboundPath(home, msg.SubscriptionID, wakeID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return "", "", err
	}
	return wakeID, digest, nil
}

func hermesAckCovers(home, subscriptionID, wakeID, digest string) bool {
	raw, err := os.ReadFile(hermesAckPath(home, subscriptionID, wakeID))
	if err != nil {
		return false
	}
	var ack hermesAckFile
	if json.Unmarshal(raw, &ack) != nil {
		return false
	}
	if ack.Schema != hermesAckSchemaV1 || ack.InboundDigest != digest {
		return false
	}
	for _, id := range ack.WakeEventIDs {
		if id == wakeID {
			return true
		}
	}
	return false
}

func (s *Service) deliverHermes(root, originID string, msg Message, now time.Time, seq int, key string) error {
	if strings.TrimSpace(root) == "" {
		root = homeOf(msg)
	}
	wakeID, digest, err := writeHermesInbound(root, originID, msg, now, seq)
	if err != nil {
		return err
	}
	s.hermesWrote = true
	s.hermesWake = wakeID
	s.hermesDigest = digest
	s.hermesRoot = root
	s.hermesSub = msg.SubscriptionID
	s.hermesKey = key
	if s.receiveHermes != nil {
		if err := s.receiveHermes(root, msg.SubscriptionID); err != nil {
			return err
		}
	}
	if !hermesAckCovers(root, msg.SubscriptionID, wakeID, digest) {
		return fmt.Errorf("hermes_ack_missing")
	}
	return nil
}
