package notify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiverRootAckMarksDeliveredWithoutNewGap(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	cur := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	svc := New(func(context.Context, []string) error {
		t.Fatal("hermes ran a command")
		return nil
	}, func() time.Time { return cur })
	msg := rootMessage(home, root)
	if err := svc.Deliver(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "hermes_ack_missing") {
		t.Fatalf("first %v", err)
	}
	if _, err := os.Stat(hermesInboxDir(home, "example-manager")); !os.IsNotExist(err) {
		t.Fatalf("wake written under home: %v", err)
	}
	before, found, err := loadReminder(home, msg.QuestionID)
	if err != nil || !found || before.completed["origin"] || before.wakeRoot != root || before.wakeKey != "origin" || before.wakeSub != "example-manager" {
		t.Fatalf("reminder %+v found %v err %v", before, found, err)
	}
	wakePath := hermesInboundPath(root, "example-manager", before.wakeID)
	if _, err := os.Stat(wakePath); err != nil {
		t.Fatal(err)
	}
	writeTestAck(t, root, "example-manager", before.wakeID, before.digest, cur)
	if err := svc.Deliver(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	after, found, err := loadReminder(home, msg.QuestionID)
	if err != nil || !found || !after.completed["origin"] {
		t.Fatalf("delivered %+v found %v err %v", after, found, err)
	}
	if after.wakeSeq != before.wakeSeq || after.gap != before.gap || !after.at.Equal(before.at) {
		t.Fatalf("gap moved before %+v after %+v", before, after)
	}
}

func TestReceiverRootNoAckKeepsReminder(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	cur := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	svc := New(func(context.Context, []string) error {
		t.Fatal("hermes ran a command")
		return nil
	}, func() time.Time { return cur })
	msg := rootMessage(home, root)
	if err := svc.Deliver(context.Background(), msg); err == nil {
		t.Fatal("missing ack succeeded")
	}
	before, found, err := loadReminder(home, msg.QuestionID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if err := svc.Deliver(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "hermes_ack_missing") {
		t.Fatalf("rewrite %v", err)
	}
	after, found, err := loadReminder(home, msg.QuestionID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if after.wakeSeq != before.wakeSeq || after.gap != before.gap || !after.at.Equal(before.at) || after.completed["origin"] {
		t.Fatalf("reminder changed before %+v after %+v", before, after)
	}
	entries, err := os.ReadDir(hermesInboxDir(root, "example-manager"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".ack.json") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("wake files %d", n)
	}
}

func TestAmbiguousHermesSkipsWithoutInboxDir(t *testing.T) {
	home := t.TempDir()
	cur := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	var argv []string
	svc := New(func(_ context.Context, got []string) error {
		argv = append(argv, strings.Join(got, " "))
		return nil
	}, func() time.Time { return cur })
	msg := Message{
		QuestionID: "q-skip",
		Objective:  "obj",
		Origin:     "hermes:profile-a/example-manager",
		Fallback:   "codex:thread-0001",
		Text:       "need a decision",
		Home:       home,
		Kind:       "freeze",
		Receivers: []Receiver{
			{ID: "one", Adapter: "hermes", Target: "profile-a", Enabled: true},
			{ID: "two", Adapter: "hermes", Target: "profile-a", Enabled: true},
		},
	}
	err := svc.Deliver(context.Background(), msg)
	if err == nil || SkipReason(err) != SkipProfileAmbiguous {
		t.Fatalf("err %v reason %q", err, SkipReason(err))
	}
	if _, statErr := os.Stat(filepath.Join(home, "control", "manager-wake")); !os.IsNotExist(statErr) {
		t.Fatalf("hermes dir %v", statErr)
	}
	if _, statErr := os.Stat(inboxPath(home, msg.QuestionID)); statErr != nil {
		t.Fatal(statErr)
	}
	if len(argv) != 1 || !strings.Contains(argv[0], "thread-0001") {
		t.Fatalf("fallback argv %v", argv)
	}
}

func TestEventsFilterDoesNotDropOriginCommand(t *testing.T) {
	home := t.TempDir()
	cur := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	var argv [][]string
	svc := New(func(_ context.Context, got []string) error {
		argv = append(argv, append([]string(nil), got...))
		return nil
	}, func() time.Time { return cur })
	msg := Message{
		QuestionID: "q-filter",
		Objective:  "obj",
		Origin:     "codex:thread-0001",
		Text:       "need a decision",
		Home:       home,
		Kind:       "freeze",
		Receivers: []Receiver{{
			ID: "done-only", Adapter: "codex", Target: "thread-9", Enabled: true,
			Scope: Scope{All: true}, Events: []string{"done"},
		}},
	}
	if err := svc.Deliver(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(argv) != 1 || !containsString(argv[0], "thread-0001") || containsString(argv[0], "thread-9") {
		t.Fatalf("argv %v", argv)
	}
}

func rootMessage(home, root string) Message {
	return Message{
		QuestionID: "q-root",
		Objective:  "obj",
		Origin:     "hermes:profile-a/example-manager",
		Text:       "need a decision",
		Home:       home,
		Kind:       "freeze",
		Project:    "p",
		Receivers: []Receiver{{
			ID:      "example-manager",
			Adapter: "hermes",
			Target:  "profile-a/example-manager",
			Root:    root,
			Enabled: true,
		}},
	}
}

func writeTestAck(t *testing.T, root, sub, wakeID, digest string, now time.Time) {
	t.Helper()
	body, err := json.Marshal(hermesAckFile{
		Schema:        hermesAckSchemaV1,
		WakeEventIDs:  []string{wakeID},
		InboundDigest: digest,
		ReceivedAt:    now.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hermesAckPath(root, sub, wakeID), append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
