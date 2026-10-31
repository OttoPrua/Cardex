package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	hermesInboundSchemaV1 = "cardex.manager_wake.hermes_inbound.v1"
	hermesAckSchemaV1     = "cardex.manager_wake.hermes_ack.v1"
	hermesSessionType     = "hermes_session"
)

type hermesInboundFile struct {
	Schema        string   `json:"schema"`
	Provider      string   `json:"provider"`
	SessionType   string   `json:"session_type"`
	SessionID     string   `json:"session_id"`
	Profile       string   `json:"profile,omitempty"`
	WakeEventIDs  []string `json:"wake_event_ids"`
	Message       string   `json:"message"`
	InboundDigest string   `json:"inbound_digest"`
	TS            string   `json:"ts"`
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

func hermesInboxDir(home, originID string) string {
	return filepath.Join(home, "hermes-inbox", filepath.FromSlash(originID))
}

func hermesInboundPath(home, originID, questionID string) string {
	return filepath.Join(hermesInboxDir(home, originID), safeQuestionName(questionID)+".json")
}

func hermesAckPath(home, originID, questionID string) string {
	return filepath.Join(hermesInboxDir(home, originID), safeQuestionName(questionID)+".ack.json")
}

func inboundDigest(message string) string {
	sum := sha256.Sum256([]byte(message))
	return hex.EncodeToString(sum[:])
}

func writeHermesInbound(home, originID, questionID, message string, now time.Time) (string, error) {
	profile, session := splitHermesID(originID)
	body := hermesInboundFile{
		Schema:        hermesInboundSchemaV1,
		Provider:      "hermes",
		SessionType:   hermesSessionType,
		SessionID:     session,
		Profile:       profile,
		WakeEventIDs:  []string{questionID},
		Message:       message,
		InboundDigest: inboundDigest(message),
		TS:            now.UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", err
	}
	path := hermesInboundPath(home, originID, questionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return "", err
	}
	return body.InboundDigest, nil
}

func hermesAckCovers(home, originID, questionID, digest string) bool {
	raw, err := os.ReadFile(hermesAckPath(home, originID, questionID))
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
		if id == questionID {
			return true
		}
	}
	return false
}

func deliverHermes(home, originID, questionID, message string, now time.Time) error {
	digest, err := writeHermesInbound(home, originID, questionID, message, now)
	if err != nil {
		return err
	}
	if !hermesAckCovers(home, originID, questionID, digest) {
		return fmt.Errorf("hermes_ack_missing")
	}
	return nil
}
