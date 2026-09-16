package main

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
)

type hermesInboundFile struct {
	Schema          string   `json:"schema"`
	SubscriptionID  string   `json:"subscription_id"`
	Provider        string   `json:"provider"`
	SessionType     string   `json:"session_type"`
	SessionID       string   `json:"session_id"`
	Profile         string   `json:"profile,omitempty"`
	WakeEventIDs    []string `json:"wake_event_ids"`
	TaskIDs         []string `json:"task_ids,omitempty"`
	Message         string   `json:"message"`
	InboundDigest   string   `json:"inbound_digest"`
	TS              string   `json:"ts"`
}

type hermesAckFile struct {
	Schema         string   `json:"schema"`
	WakeEventIDs   []string `json:"wake_event_ids"`
	InboundDigest  string   `json:"inbound_digest"`
	ReceivedAt     string   `json:"received_at"`
}

func isHermesWake(sub ManagerWakeSubscription) bool {
	p := strings.ToLower(strings.TrimSpace(sub.Provider))
	st := strings.ToLower(strings.TrimSpace(sub.SessionType))
	return p == "hermes" || st == sessionTypeHermes
}

func managerWakeHermesInboxDir(root, subID string) string {
	return filepath.Join(managerWakeDir(root), "hermes-inbox", subID)
}

func hermesInboundPath(root, subID, wakeID string) string {
	return filepath.Join(managerWakeHermesInboxDir(root, subID), wakeID+".json")
}

func hermesAckPath(root, subID, wakeID string) string {
	return filepath.Join(managerWakeHermesInboxDir(root, subID), wakeID+".ack.json")
}

func deliverHermesWake(root string, sub ManagerWakeSubscription, rows []managerWakeOutboxRow, message string) error {
	if !isHermesWake(sub) {
		return fmt.Errorf("invalid_wake_provider")
	}
	if !wakeMessageCarriesEventIDs(message) {
		return fmt.Errorf("queue_idempotency_unsupported")
	}
	ids := make([]string, 0, len(rows))
	taskIDs := make([]string, 0, len(rows))
	seenTask := map[string]bool{}
	for _, row := range rows {
		if row.WakeEventID != "" {
			ids = append(ids, row.WakeEventID)
		}
		if row.TaskID != "" && !seenTask[row.TaskID] {
			seenTask[row.TaskID] = true
			taskIDs = append(taskIDs, row.TaskID)
		}
	}
	if len(ids) == 0 {
		return fmt.Errorf("queue_idempotency_unsupported")
	}
	if err := os.MkdirAll(managerWakeHermesInboxDir(root, sub.ID), 0o755); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(message))
	digest := hex.EncodeToString(sum[:])
	body := hermesInboundFile{
		Schema:         hermesInboundSchemaV1,
		SubscriptionID: sub.ID,
		Provider:       "hermes",
		SessionType:    sessionTypeHermes,
		SessionID:      sub.ThreadID,
		Profile:        sub.Profile,
		WakeEventIDs:   ids,
		TaskIDs:        taskIDs,
		Message:        message,
		InboundDigest:  digest,
		TS:             time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := atomicWriteSync(hermesInboundPath(root, sub.ID, id), append(raw, '\n')); err != nil {
			return err
		}
	}
	if hook := managerWakeHermesAckHook; hook != nil {
		if err := hook(root, sub, ids); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(managerWakeQueueTimeout)
	for time.Now().Before(deadline) {
		if hermesAcksCover(root, sub.ID, ids, digest) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hermesAcksCover(root, sub.ID, ids, digest) {
		return nil
	}
	return fmt.Errorf("hermes_ack_missing")
}

func hermesAcksCover(root, subID string, ids []string, digest string) bool {
	for _, id := range ids {
		raw, err := os.ReadFile(hermesAckPath(root, subID, id))
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
		seen := map[string]bool{}
		for _, got := range ack.WakeEventIDs {
			seen[got] = true
		}
		if !seen[id] {
			return false
		}
	}
	return true
}

func writeHermesAckForTest(root, subID, wakeID, digest string, ids []string) error {
	ack := hermesAckFile{
		Schema:        hermesAckSchemaV1,
		WakeEventIDs:  ids,
		InboundDigest: digest,
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(managerWakeHermesInboxDir(root, subID), 0o755); err != nil {
		return err
	}
	return os.WriteFile(hermesAckPath(root, subID, wakeID), append(raw, '\n'), 0o644)
}
