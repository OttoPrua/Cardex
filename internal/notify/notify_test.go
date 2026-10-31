package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOriginFlagBeatsEnvAndSlashID(t *testing.T) {
	env := func(string) string { return "hermes:profile/session" }
	kind, id, ok := OriginFromArgs([]string{"cardex", "--origin", "codex:thread-1"}, env)
	if !ok || kind != "codex" || id != "thread-1" {
		t.Fatalf("flag origin %s %s %v", kind, id, ok)
	}
	kind, id, ok = OriginFromArgs([]string{"cardex", "--origin=hermes:alt/sess"}, env)
	if !ok || kind != "hermes" || id != "alt/sess" {
		t.Fatalf("equals origin %s %s %v", kind, id, ok)
	}
	kind, id, ok = OriginFromArgs([]string{"cardex", "question", "list"}, env)
	if !ok || kind != "hermes" || id != "profile/session" || !strings.Contains(id, "/") {
		t.Fatalf("env origin %s %s %v", kind, id, ok)
	}
	kind, id, ok = ParseOrigin("codex:org/thread:more")
	if !ok || kind != "codex" || id != "org/thread:more" {
		t.Fatalf("split %s %s %v", kind, id, ok)
	}
}

func TestDeliverOrderInboxWhenOriginFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	var calls [][]string
	exec := func(ctx context.Context, argv []string) error {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		calls = append(calls, append([]string{}, argv...))
		if len(argv) > 0 && argv[0] == "codex" {
			return errors.New("origin down")
		}
		return nil
	}
	svc := New(exec, func() time.Time { return time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC) })
	msg := Message{
		QuestionID: "q-order",
		Objective:  "obj",
		Origin:     "codex:thread-a",
		Fallback:   "lark:chat-b",
		Text:       "need a decision",
	}
	err := svc.Deliver(context.Background(), msg)
	if err == nil {
		t.Fatal("origin error was ignored")
	}
	if len(calls) != 2 {
		t.Fatalf("calls %#v", calls)
	}
	wantCodex := []string{"codex", "queue", "--thread", "thread-a", "--message", "need a decision"}
	if !reflect.DeepEqual(calls[0], wantCodex) {
		t.Fatalf("codex argv %#v", calls[0])
	}
	for _, arg := range calls[0] {
		if strings.Contains(arg, "idempotency") || arg == "--id" || arg == "--dedupe-key" {
			t.Fatalf("unexpected flag %q", arg)
		}
	}
	if calls[1][0] != "lark-cli" {
		t.Fatalf("lark argv %#v", calls[1])
	}
	inbox := filepath.Join(home, "inbox", "q-order.txt")
	raw, err := os.ReadFile(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "need a decision") {
		t.Fatalf("inbox %s", raw)
	}
}

func TestDeliverDedupeAndIncreasingBackoff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	cur := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	var calls int
	exec := func(context.Context, []string) error {
		calls++
		return nil
	}
	svc := New(exec, func() time.Time { return cur })
	msg := Message{QuestionID: "q-remind", Origin: "codex:thread-a", Text: "still open"}
	ctx := context.Background()
	if err := svc.Deliver(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if err := svc.Deliver(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("duplicate delivery calls=%d", calls)
	}

	g1 := ReminderGap(0)
	if g1 <= 0 {
		t.Fatal(g1)
	}
	cur = cur.Add(g1)
	if err := svc.Deliver(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("gap not yet past, calls=%d", calls)
	}
	cur = cur.Add(time.Nanosecond)
	if err := svc.Deliver(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("first reminder calls=%d", calls)
	}

	g2 := ReminderGap(g1)
	if g2 <= g1 {
		t.Fatalf("backoff did not increase: %s then %s", g1, g2)
	}
	reminderAt := cur
	cur = reminderAt.Add(g1)
	if err := svc.Deliver(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("second gap must be longer than %s, calls=%d", g1, calls)
	}
	cur = reminderAt.Add(g2 + time.Nanosecond)
	if err := svc.Deliver(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("second reminder calls=%d", calls)
	}
}

func TestHermesAckAndLarkNotAProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	cur := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	var calls [][]string
	svc := New(func(_ context.Context, argv []string) error {
		calls = append(calls, append([]string{}, argv...))
		return nil
	}, func() time.Time { return cur })
	ctx := context.Background()
	msg := Message{
		QuestionID: "q-hermes",
		Origin:     "hermes:profile/session",
		Text:       "wake text",
	}
	if err := svc.Deliver(ctx, msg); err == nil {
		t.Fatal("hermes without ack succeeded")
	}
	if len(calls) != 0 {
		t.Fatalf("hermes exec %#v", calls)
	}
	inboundPath := hermesInboundPath(home, "profile/session", msg.QuestionID)
	raw, err := os.ReadFile(inboundPath)
	if err != nil {
		t.Fatal(err)
	}
	var inbound hermesInboundFile
	if err := json.Unmarshal(raw, &inbound); err != nil {
		t.Fatal(err)
	}
	if inbound.Schema != "cardex.manager_wake.hermes_inbound.v1" || inbound.Profile != "profile" || inbound.SessionID != "session" {
		t.Fatalf("inbound %+v", inbound)
	}
	sum := sha256.Sum256([]byte(msg.Text))
	if inbound.Message != msg.Text || inbound.InboundDigest != hex.EncodeToString(sum[:]) {
		t.Fatalf("inbound digest %+v", inbound)
	}
	if len(inbound.WakeEventIDs) != 1 || inbound.WakeEventIDs[0] != msg.QuestionID {
		t.Fatalf("inbound ids %+v", inbound)
	}
	if _, err := os.Stat(filepath.Join(home, "inbox", "q-hermes.txt")); err != nil {
		t.Fatal(err)
	}

	bad := msg
	bad.QuestionID = "q-bad-ack"
	writeAck(t, home, "profile/session", bad.QuestionID, "not-the-digest", []string{bad.QuestionID})
	if err := svc.Deliver(ctx, bad); err == nil {
		t.Fatal("mismatched ack succeeded")
	}

	writeAck(t, home, "profile/session", msg.QuestionID, inbound.InboundDigest, inbound.WakeEventIDs)
	if err := svc.Deliver(ctx, msg); err != nil {
		t.Fatal(err)
	}

	lark := Message{QuestionID: "q-lark", Origin: "lark:oc_chat", Text: "ping"}
	if err := svc.Deliver(ctx, lark); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0][0] != "lark-cli" {
		t.Fatalf("lark calls %#v", calls)
	}
}

func TestFileAdapterWritesInbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	svc := New(func(context.Context, []string) error {
		t.Fatal("file adapter executed a command")
		return nil
	}, func() time.Time { return time.Unix(0, 0).UTC() })
	err := svc.Deliver(context.Background(), Message{
		QuestionID: "q-file",
		Origin:     "file:inbox",
		Text:       "on disk",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "inbox", "q-file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "on disk") {
		t.Fatalf("inbox %s", raw)
	}
}

func writeAck(t *testing.T, home, originID, questionID, digest string, ids []string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"schema":         "cardex.manager_wake.hermes_ack.v1",
		"wake_event_ids": ids,
		"inbound_digest": digest,
		"received_at":    "2026-10-31T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := hermesAckPath(home, originID, questionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
