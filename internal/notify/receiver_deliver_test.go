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

func TestTwoHermesHopsAckIndependently(t *testing.T) {
	home := t.TempDir()
	earlierRoot := t.TempDir()
	laterRoot := t.TempDir()
	cur := time.Date(2026, 11, 8, 1, 0, 0, 0, time.UTC)
	var execs int
	svc := New(func(context.Context, []string) error {
		execs++
		return nil
	}, func() time.Time { return cur })
	msg := Message{
		QuestionID: "q-two",
		Objective:  "obj",
		Origin:     "codex:thread-0001",
		Text:       "need a decision",
		Home:       home,
		Kind:       "freeze",
		Project:    "p",
		Receivers: []Receiver{
			{ID: "thread-0001", Adapter: "hermes", Target: "profile-a/thread-0001", Root: laterRoot, Enabled: true, Scope: Scope{All: true}},
			{ID: "example-manager", Adapter: "hermes", Target: "profile-a/example-manager", Root: earlierRoot, Enabled: true, Scope: Scope{All: true}},
		},
	}
	if err := svc.Deliver(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "hermes_ack_missing") {
		t.Fatalf("first %v", err)
	}
	if execs != 1 {
		t.Fatalf("origin execs %d", execs)
	}
	before, found, err := loadReminder(home, msg.QuestionID)
	if err != nil || !found {
		t.Fatal(err)
	}
	earlier, ok := hopByKey(before, "receiver:example-manager")
	later, okLater := hopByKey(before, "receiver:thread-0001")
	if !ok || !okLater {
		t.Fatalf("wakes %+v", before.wakes)
	}
	if earlier.Root != earlierRoot || later.Root != laterRoot || earlier.Sub == later.Sub || earlier.Digest == "" || later.Digest == "" {
		t.Fatalf("earlier %+v later %+v", earlier, later)
	}
	earlierPath := hermesInboundPath(earlier.Root, earlier.Sub, earlier.WakeID)
	laterPath := hermesInboundPath(later.Root, later.Sub, later.WakeID)
	if earlierPath == laterPath {
		t.Fatal("hops share one wake file")
	}
	earlierBytes := mustReadFile(t, earlierPath)
	laterBytes := mustReadFile(t, laterPath)
	if _, err := os.Stat(hermesAckPath(earlier.Root, earlier.Sub, earlier.WakeID)); !os.IsNotExist(err) {
		t.Fatal("earlier ack already present")
	}
	writeTestAck(t, earlier.Root, earlier.Sub, earlier.WakeID, earlier.Digest, cur)
	if err := svc.Deliver(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "hermes_ack_missing") {
		t.Fatalf("second %v", err)
	}
	if execs != 1 {
		t.Fatalf("origin resent %d", execs)
	}
	after := svc.sent[msg.QuestionID]
	if after.wakeSeq != before.wakeSeq || after.gap != before.gap || !after.at.Equal(before.at) {
		t.Fatalf("gap moved before %+v after %+v", before, after)
	}
	if !after.completed["receiver:example-manager"] || after.completed["receiver:thread-0001"] {
		t.Fatalf("completed %+v", after.completed)
	}
	if got := mustReadFile(t, earlierPath); string(got) != string(earlierBytes) {
		t.Fatal("acked hop was rewritten")
	}
	if got := mustReadFile(t, laterPath); string(got) != string(laterBytes) {
		t.Fatal("unacked hop left its wake")
	}
	if wakes := nonAckJSON(t, later.Root, later.Sub); len(wakes) != 1 || wakes[0] != wakeFileStem(later.WakeID) {
		t.Fatalf("later wakes %v", wakes)
	}
	if _, err := os.Stat(hermesAckPath(later.Root, later.Sub, later.WakeID)); !os.IsNotExist(err) {
		t.Fatal("later hop was acked")
	}
	reloaded, found, err := loadReminder(home, msg.QuestionID)
	if err != nil || !found || !reloaded.completed["receiver:example-manager"] || reloaded.completed["receiver:thread-0001"] {
		t.Fatalf("reloaded %+v found %v err %v", reloaded, found, err)
	}
	if _, ok := hopByKey(reloaded, "receiver:example-manager"); !ok {
		t.Fatal("reloaded dropped the earlier hop")
	}
	if _, ok := hopByKey(reloaded, "receiver:thread-0001"); !ok {
		t.Fatal("reloaded dropped the later hop")
	}
}

func hopByKey(st deliveryState, key string) (hermesHopWake, bool) {
	for _, hop := range st.wakes {
		if hop.Key == key {
			return hop, true
		}
	}
	return hermesHopWake{}, false
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func nonAckJSON(t *testing.T, root, sub string) []string {
	t.Helper()
	entries, err := os.ReadDir(hermesInboxDir(root, sub))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".ack.json") {
			names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
		}
	}
	return names
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
