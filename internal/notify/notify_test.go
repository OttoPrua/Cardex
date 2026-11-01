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
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
	wantLark := []string{"lark-cli", "im", "+messages-send", "--chat-id", "chat-b", "--text", "need a decision"}
	if !reflect.DeepEqual(calls[1], wantLark) {
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

func TestPartialFailureKeepsDedupeAndBackoff(t *testing.T) {
	cases := []struct {
		name         string
		failOrigin   bool
		failFallback bool
		failInbox    bool
	}{
		{name: "origin fails fallback and file succeed", failOrigin: true},
		{name: "origin succeeds fallback fails", failFallback: true},
		{name: "external succeeds inbox fails", failInbox: true},
		{name: "full success dedupes until gap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CARDEX_HOME", home)
			var trace []string
			origWrite := inboxWrite
			t.Cleanup(func() { inboxWrite = origWrite })
			inboxWrite = func(name string, data []byte, perm os.FileMode) error {
				trace = append(trace, "inbox")
				if tc.failInbox {
					return errors.New("inbox down")
				}
				return origWrite(name, data, perm)
			}
			exec := func(_ context.Context, argv []string) error {
				if len(argv) == 0 {
					t.Fatal("empty argv")
				}
				if argv[0] == "lark-cli" {
					want := []string{"lark-cli", "im", "+messages-send", "--chat-id", "chat-b", "--text", "still open"}
					if !reflect.DeepEqual(argv, want) {
						t.Fatalf("lark argv %#v", argv)
					}
				}
				trace = append(trace, argv[0])
				if tc.failOrigin && argv[0] == "codex" {
					return errors.New("origin down")
				}
				if tc.failFallback && argv[0] == "lark-cli" {
					return errors.New("fallback down")
				}
				return nil
			}
			cur := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
			start := cur
			svc := New(exec, func() time.Time { return cur })
			msg := Message{
				QuestionID: "q-partial",
				Objective:  "obj",
				Origin:     "codex:thread-a",
				Fallback:   "lark:chat-b",
				Text:       "still open",
				Home:       home,
			}
			err := svc.Deliver(context.Background(), msg)
			partial := tc.failOrigin || tc.failFallback || tc.failInbox
			if partial && err == nil {
				t.Fatal("partial failure was ignored")
			}
			if !partial && err != nil {
				t.Fatal(err)
			}
			wantFirst := []string{"codex", "lark-cli", "inbox"}
			if !reflect.DeepEqual(trace, wantFirst) {
				t.Fatalf("order %v", trace)
			}
			st := svc.sent[msg.QuestionID]
			if st.completed["origin"] != !tc.failOrigin || st.completed["fallback"] != !tc.failFallback || st.completed["inbox"] != !tc.failInbox {
				t.Fatalf("completed %+v", st.completed)
			}
			if st.gap != ReminderGap(0) {
				t.Fatalf("gap %s", st.gap)
			}
			first := append([]string{}, trace...)
			g1 := st.gap
			cur = start.Add(g1 / 2)
			if err := svc.Deliver(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(trace, first) {
				t.Fatalf("pre-gap retried successes or failures: %v", trace)
			}
			if !reflect.DeepEqual(svc.sent[msg.QuestionID].completed, st.completed) {
				t.Fatalf("pre-gap dropped completed %+v", svc.sent[msg.QuestionID].completed)
			}

			cur = start.Add(g1 + time.Nanosecond)
			remindErr := svc.Deliver(context.Background(), msg)
			if partial && remindErr == nil {
				t.Fatal("reminder lost the failing destination")
			}
			if !partial && remindErr != nil {
				t.Fatal(remindErr)
			}
			if len(trace) <= len(first) {
				t.Fatalf("reminder did not send %v", trace)
			}
			reminderAt := cur
			after := len(trace)
			g2 := svc.sent[msg.QuestionID].gap
			if g2 <= g1 {
				t.Fatalf("following gap %s is not longer than %s", g2, g1)
			}
			cur = reminderAt.Add(g1 + time.Nanosecond)
			if err := svc.Deliver(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if len(trace) != after {
				t.Fatalf("following gap was not longer, trace %v", trace)
			}
			cur = reminderAt.Add(g2 + time.Nanosecond)
			laterErr := svc.Deliver(context.Background(), msg)
			if partial && laterErr == nil {
				t.Fatal("later reminder lost the failing destination")
			}
			if !partial && laterErr != nil {
				t.Fatal(laterErr)
			}
			if len(trace) <= after {
				t.Fatalf("longer gap did not send %v", trace)
			}
		})
	}
}

func TestHermesReceiverAckInAttempt(t *testing.T) {
	const (
		sub     = "mgr-r3d"
		profile = "profile"
		session = "session"
		rawText = "RAW-QUESTION-NOT-COMPACT"
	)
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	cur := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	svc := New(func(context.Context, []string) error {
		t.Fatal("hermes executed a command")
		return nil
	}, func() time.Time { return cur })
	msg := Message{
		QuestionID:     "q-hermes",
		Objective:      "obj",
		Origin:         "hermes:" + profile + "/" + session,
		Text:           rawText,
		Home:           home,
		SubscriptionID: sub,
	}

	t.Run("no-ack-fails", func(t *testing.T) {
		isolated := t.TempDir()
		t.Setenv("CARDEX_HOME", isolated)
		bare := New(svc.exec, svc.now)
		bareMsg := msg
		bareMsg.Home = isolated
		bareMsg.QuestionID = "q-no-ack"
		if err := bare.Deliver(context.Background(), bareMsg); err == nil {
			t.Fatal("hermes without ack succeeded")
		}
		if acks := ackPaths(isolated); len(acks) != 0 {
			t.Fatalf("unexpected ack %v", acks)
		}
	})

	svc.receiveHermes = func(home, subscriptionID string) error {
		return simulateHermesReceive(home, subscriptionID, profile, session)
	}
	if err := svc.Deliver(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	inboundPath, raw := readOnlyInbound(t, hermesInboxDir(home, sub))
	var inbound hermesInboundFile
	if err := json.Unmarshal(raw, &inbound); err != nil {
		t.Fatal(err)
	}
	if inbound.Schema != hermesInboundSchemaV1 || inbound.SubscriptionID != sub || inbound.Provider != "hermes" {
		t.Fatalf("inbound %+v", inbound)
	}
	if inbound.Profile != profile || inbound.SessionID != session {
		t.Fatalf("identity %+v", inbound)
	}
	if inbound.SessionType != "hermes" && inbound.SessionType != "hermes_session" {
		t.Fatalf("session_type %s", inbound.SessionType)
	}
	if inbound.Message == rawText || strings.Contains(inbound.Message, rawText) {
		t.Fatalf("message is raw question text %q", inbound.Message)
	}
	if inbound.InboundDigest != inboundDigest(inbound.Message) {
		t.Fatalf("digest %s", inbound.InboundDigest)
	}
	headerSub, highWater, rows := mustParseCompact(t, inbound.Message)
	if headerSub != sub || highWater < 0 {
		t.Fatalf("header sub %s high_water %d", headerSub, highWater)
	}
	wakes := make([]string, len(rows))
	for i, row := range rows {
		wakes[i] = row.wake
		if !strings.HasPrefix(row.wake, row.task+":") {
			t.Fatalf("wake %s task %s", row.wake, row.task)
		}
	}
	if !reflect.DeepEqual(inbound.WakeEventIDs, wakes) {
		t.Fatalf("wake ids %+v rows %+v", inbound.WakeEventIDs, wakes)
	}
	stem := strings.TrimSuffix(filepath.Base(inboundPath), ".json")
	if strings.Contains(stem, "/") || strings.Contains(stem, "..") || !containsString(wakes, stem) {
		t.Fatalf("stem %s wakes %v", stem, wakes)
	}
	if filepath.Dir(inboundPath) != hermesInboxDir(home, sub) {
		t.Fatalf("dir %s", inboundPath)
	}
	oldDir := filepath.Join(home, "hermes-inbox", profile, session)
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old inbox %v", err)
	}
	ackRaw, err := os.ReadFile(hermesAckPath(home, sub, stem))
	if err != nil {
		t.Fatal(err)
	}
	var ack hermesAckFile
	if err := json.Unmarshal(ackRaw, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Schema != hermesAckSchemaV1 || ack.InboundDigest != inbound.InboundDigest || !reflect.DeepEqual(ack.WakeEventIDs, wakes) {
		t.Fatalf("ack %+v", ack)
	}
	inboxRaw, err := os.ReadFile(filepath.Join(home, "inbox", "q-hermes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inboxRaw), rawText) {
		t.Fatalf("inbox lost question text %s", inboxRaw)
	}

	t.Run("missing-subscription", func(t *testing.T) {
		h := t.TempDir()
		t.Setenv("CARDEX_HOME", h)
		miss := New(func(context.Context, []string) error { return nil }, func() time.Time { return cur })
		miss.receiveHermes = svc.receiveHermes
		bad := msg
		bad.Home = h
		bad.QuestionID = "q-missing-sub"
		bad.SubscriptionID = ""
		if err := miss.Deliver(context.Background(), bad); err == nil {
			t.Fatal("missing subscription succeeded")
		}
		if acks := ackPaths(h); len(acks) != 0 {
			t.Fatalf("ack %v", acks)
		}
		if _, err := os.Stat(filepath.Join(h, "control", "manager-wake", "hermes-inbox")); !os.IsNotExist(err) {
			t.Fatalf("wrote receiver dir without subscription: %v", err)
		}
	})

	variants := []struct {
		name   string
		mutate func(home, sub string)
	}{
		{name: "missing-subscription-field", mutate: func(home, sub string) {
			rewriteInbound(t, home, sub, func(body map[string]any) { delete(body, "subscription_id") })
		}},
		{name: "raw-message", mutate: func(home, sub string) {
			rewriteInbound(t, home, sub, func(body map[string]any) {
				body["message"] = rawText
				body["inbound_digest"] = inboundDigest(rawText)
			})
		}},
		{name: "digest-mismatch", mutate: func(home, sub string) {
			rewriteInbound(t, home, sub, func(body map[string]any) {
				body["inbound_digest"] = strings.Repeat("ab", 32)
			})
		}},
		{name: "outside-directory", mutate: func(home, sub string) {
			dir := hermesInboxDir(home, sub)
			old := filepath.Join(home, "hermes-inbox", profile, session)
			if err := os.MkdirAll(old, 0o755); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".ack.json") {
					continue
				}
				if err := os.Rename(filepath.Join(dir, entry.Name()), filepath.Join(old, entry.Name())); err != nil {
					t.Fatal(err)
				}
			}
		}},
	}
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			h := t.TempDir()
			t.Setenv("CARDEX_HOME", h)
			attempt := New(func(context.Context, []string) error { return nil }, func() time.Time { return cur })
			attempt.receiveHermes = func(home, subscriptionID string) error {
				tc.mutate(home, subscriptionID)
				return simulateHermesReceive(home, subscriptionID, profile, session)
			}
			variant := msg
			variant.Home = h
			variant.QuestionID = "q-" + tc.name
			if err := attempt.Deliver(context.Background(), variant); err == nil {
				t.Fatal("invalid inbound succeeded")
			}
			if acks := ackPaths(h); len(acks) != 0 {
				t.Fatalf("ack %v", acks)
			}
		})
	}
}

// consumingReceiver acks a wake id once. A later file with that id is skipped,
// the same way the real receiver leaves a settled inbound alone.
type consumingReceiver struct {
	profile  string
	session  string
	consumed map[string]bool
	skips    int
	ackNew   bool
}

func (r *consumingReceiver) receive(home, subscriptionID string) error {
	dir := hermesInboxDir(home, subscriptionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".ack.json") || strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		ack, ok := validateHermesInbound(path, subscriptionID, r.profile, r.session)
		if !ok {
			continue
		}
		raw, err := json.MarshalIndent(ack, "", "  ")
		if err != nil {
			return err
		}
		for _, id := range ack.WakeEventIDs {
			if r.consumed[id] {
				r.skips++
				continue
			}
			if !r.ackNew {
				continue
			}
			r.consumed[id] = true
			if err := os.WriteFile(hermesAckPath(home, subscriptionID, id), append(raw, '\n'), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func hermesInboundFiles(t *testing.T, home, sub string) map[string]hermesInboundFile {
	t.Helper()
	dir := hermesInboxDir(home, sub)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]hermesInboundFile{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".ack.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var body hermesInboundFile
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		stem := strings.TrimSuffix(name, ".json")
		out[stem] = body
	}
	return out
}

func TestDueHermesReminderIsConsumableAgain(t *testing.T) {
	const (
		profile = "profile"
		session = "session"
		sub     = "mgr-r3d"
	)
	hermesID := "hermes:" + profile + "/" + session
	cases := []struct {
		name     string
		origin   string
		fallback string
	}{
		{name: "hermes-origin", origin: hermesID},
		{name: "hermes-fallback", origin: "codex:thread-a", fallback: hermesID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CARDEX_HOME", home)
			cur := time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC)
			var execs int
			svc := New(func(_ context.Context, argv []string) error {
				execs++
				if len(argv) == 0 || argv[0] == "hermes" {
					t.Fatalf("argv %v", argv)
				}
				return nil
			}, func() time.Time { return cur })
			recv := &consumingReceiver{
				profile: profile, session: session,
				consumed: map[string]bool{},
				ackNew:   true,
			}
			svc.receiveHermes = recv.receive
			msg := Message{
				QuestionID:     "q-" + tc.name,
				Objective:      "obj",
				Origin:         tc.origin,
				Fallback:       tc.fallback,
				Text:           "still open",
				Home:           home,
				SubscriptionID: sub,
			}
			if err := svc.Deliver(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			first := hermesInboundFiles(t, home, sub)
			if len(first) != 1 || len(recv.consumed) != 1 {
				t.Fatalf("first files %d consumed %d", len(first), len(recv.consumed))
			}
			var wake1 string
			var body1 hermesInboundFile
			for id, body := range first {
				wake1, body1 = id, body
			}
			headerSub, highWater, rows := mustParseCompact(t, body1.Message)
			if headerSub != sub || highWater != 1 || len(rows) != 1 || rows[0].wake != rows[0].task+":1" || wake1 != rows[0].wake {
				t.Fatalf("first wake %s high %d rows %+v", wake1, highWater, rows)
			}
			digest1 := inboundDigest(body1.Message)
			if body1.InboundDigest != digest1 || !recv.consumed[wake1] || !strings.HasPrefix(body1.Message, "cardex-wake v1 ") {
				t.Fatalf("first body digest %s", body1.InboundDigest)
			}
			task := rows[0].task

			ackPath := hermesAckPath(home, sub, wake1)
			ackRaw, err := os.ReadFile(ackPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(ackPath); err != nil {
				t.Fatal(err)
			}
			before := svc.sent[msg.QuestionID]
			skipsBefore := recv.skips
			execsBefore := execs
			if !cur.Before(before.at.Add(before.gap + time.Nanosecond)) {
				t.Fatal("clock already opened the next reminder")
			}
			if err := svc.Deliver(context.Background(), msg); err == nil {
				t.Fatal("same-delivery retry succeeded without its ack")
			}
			after := svc.sent[msg.QuestionID]
			if after.wakeSeq != before.wakeSeq || after.gap != before.gap || !after.at.Equal(before.at) || !reflect.DeepEqual(after.completed, before.completed) {
				t.Fatalf("retry opened a new reminder")
			}
			if execs != execsBefore || recv.skips <= skipsBefore || len(recv.consumed) != 1 {
				t.Fatalf("retry execs %d skips %d consumed %+v", execs, recv.skips, recv.consumed)
			}
			retryFiles := hermesInboundFiles(t, home, sub)
			retry, ok := retryFiles[wake1]
			if !ok || len(retryFiles) != 1 || retry.Message != body1.Message || retry.InboundDigest != digest1 {
				t.Fatalf("retry wake changed: %v", retryFiles)
			}
			if err := os.WriteFile(ackPath, ackRaw, 0o644); err != nil {
				t.Fatal(err)
			}

			cur = before.at.Add(before.gap + time.Nanosecond)
			if err := svc.Deliver(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			files := hermesInboundFiles(t, home, sub)
			if len(files) != 2 {
				t.Fatalf("reminder files %d", len(files))
			}
			var wake2 string
			var body2 hermesInboundFile
			for id, body := range files {
				if id == wake1 {
					continue
				}
				wake2, body2 = id, body
			}
			headerSub, highWater, rows = mustParseCompact(t, body2.Message)
			digest2 := inboundDigest(body2.Message)
			if headerSub != sub || highWater != 2 || wake2 != task+":2" || rows[0].wake != wake2 || digest2 == digest1 || body2.InboundDigest != digest2 {
				t.Fatalf("second wake %s digest %s rows %+v", wake2, digest2, rows)
			}
			if !strings.HasPrefix(body2.Message, "cardex-wake v1 ") || !recv.consumed[wake1] || !recv.consumed[wake2] {
				t.Fatalf("consumed %+v", recv.consumed)
			}
			if tc.fallback == "" && execs != 0 {
				t.Fatalf("origin row execs %d", execs)
			}
			if tc.fallback != "" && execs != 2 {
				t.Fatalf("fallback row execs %d", execs)
			}

			homeD := t.TempDir()
			t.Setenv("CARDEX_HOME", homeD)
			curD := time.Date(2026, 10, 31, 5, 0, 0, 0, time.UTC)
			recvD := &consumingReceiver{
				profile: profile, session: session,
				consumed: map[string]bool{},
				ackNew:   true,
			}
			svcD := New(func(context.Context, []string) error { return nil }, func() time.Time { return curD })
			svcD.receiveHermes = recvD.receive
			msgD := msg
			msgD.Home = homeD
			msgD.QuestionID = msg.QuestionID + "-ack"
			if err := svcD.Deliver(context.Background(), msgD); err != nil {
				t.Fatal(err)
			}
			planted := hermesInboundFiles(t, homeD, sub)
			if len(planted) != 1 {
				t.Fatalf("planted %d", len(planted))
			}
			var oldWake string
			var oldBody hermesInboundFile
			for id, body := range planted {
				oldWake, oldBody = id, body
			}
			if !hermesAckCovers(homeD, sub, oldWake, oldBody.InboundDigest) {
				t.Fatal("first ack missing")
			}
			st := svcD.sent[msgD.QuestionID]
			curD = st.at.Add(st.gap + time.Nanosecond)
			recvD.ackNew = false
			if err := svcD.Deliver(context.Background(), msgD); err == nil {
				t.Fatal("previous ack confirmed the later reminder")
			}
			later := hermesInboundFiles(t, homeD, sub)
			var newWake string
			var newBody hermesInboundFile
			for id, body := range later {
				if id == oldWake {
					continue
				}
				newWake, newBody = id, body
			}
			if newWake == "" || newWake == oldWake || newBody.InboundDigest == oldBody.InboundDigest {
				t.Fatalf("later wake %s", newWake)
			}
			if !hermesAckCovers(homeD, sub, oldWake, oldBody.InboundDigest) || hermesAckCovers(homeD, sub, newWake, newBody.InboundDigest) {
				t.Fatal("ack coverage does not follow the delivery")
			}
			recvD.ackNew = true
			if err := svcD.Deliver(context.Background(), msgD); err != nil {
				t.Fatal(err)
			}
			confirmed := hermesInboundFiles(t, homeD, sub)[newWake]
			if !recvD.consumed[newWake] || confirmed.WakeEventIDs[0] != newWake || confirmed.InboundDigest != newBody.InboundDigest {
				t.Fatalf("new reminder confirmed as %v", confirmed.WakeEventIDs)
			}
			if svcD.sent[msgD.QuestionID].wakeSeq != 2 || !hermesAckCovers(homeD, sub, newWake, newBody.InboundDigest) {
				t.Fatalf("seq %d", svcD.sent[msgD.QuestionID].wakeSeq)
			}
		})
	}
}

func TestLarkArgvIsNotAProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARDEX_HOME", home)
	cur := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	var calls [][]string
	svc := New(func(_ context.Context, argv []string) error {
		calls = append(calls, append([]string{}, argv...))
		return nil
	}, func() time.Time { return cur })
	lark := Message{QuestionID: "q-lark", Origin: "lark:oc_chat", Text: "ping", Home: home}
	if err := svc.Deliver(context.Background(), lark); err != nil {
		t.Fatal(err)
	}
	want := []string{"lark-cli", "im", "+messages-send", "--chat-id", "oc_chat", "--text", "ping"}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
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

func TestReminderStateSurvivesNewDeliverer(t *testing.T) {
	t.Run("partial", func(t *testing.T) {
		home := t.TempDir()
		cur := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
		var calls int
		exec := func(_ context.Context, argv []string) error {
			calls++
			if len(argv) > 0 && argv[0] == "codex" {
				return errors.New("origin down")
			}
			return nil
		}
		now := func() time.Time { return cur }
		msg := Message{
			QuestionID: "q-restart",
			Objective:  "obj",
			Origin:     "codex:thread-a",
			Fallback:   "lark:chat-b",
			Text:       "still open",
			Home:       home,
		}
		other := msg
		other.QuestionID = "q-other"
		svc := New(exec, now)
		if err := svc.Deliver(context.Background(), msg); err == nil {
			t.Fatal("partial failure was ignored")
		}
		if err := svc.Deliver(context.Background(), other); err == nil {
			t.Fatal("second partial failure was ignored")
		}
		if calls != 4 {
			t.Fatalf("calls %d", calls)
		}
		st := svc.sent[msg.QuestionID]
		held := calls
		again := New(exec, now)
		if err := again.Deliver(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		if err := again.Deliver(context.Background(), other); err != nil {
			t.Fatal(err)
		}
		if calls != held {
			t.Fatalf("restart resent before the gap, calls %d", calls)
		}
		got := again.sent[msg.QuestionID]
		if !got.at.Equal(st.at) || got.gap != st.gap || !reflect.DeepEqual(got.completed, st.completed) {
			t.Fatalf("restored %+v want %+v", got, st)
		}
		cur = st.at.Add(st.gap)
		if err := New(exec, now).Deliver(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		if calls != held {
			t.Fatalf("gap boundary resent, calls %d", calls)
		}
		cur = st.at.Add(st.gap + time.Nanosecond)
		if err := New(exec, now).Deliver(context.Background(), msg); err == nil {
			t.Fatal("reminder lost the failing origin")
		}
		if calls == held {
			t.Fatal("restored gap did not open the next reminder")
		}
	})

	t.Run("unacked-hermes", func(t *testing.T) {
		const (
			profile = "profile"
			session = "session"
			sub     = "mgr-r3d"
		)
		home := t.TempDir()
		cur := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
		var inboxN int
		origWrite := inboxWrite
		t.Cleanup(func() { inboxWrite = origWrite })
		inboxWrite = func(name string, data []byte, perm os.FileMode) error {
			inboxN++
			return origWrite(name, data, perm)
		}
		now := func() time.Time { return cur }
		ack := func(home, subscriptionID string) error {
			return simulateHermesReceive(home, subscriptionID, profile, session)
		}
		msg := Message{
			QuestionID:     "q-hermes-restart",
			Objective:      "obj",
			Origin:         "hermes:" + profile + "/" + session,
			Text:           "still open",
			Home:           home,
			SubscriptionID: sub,
		}
		svc := New(func(context.Context, []string) error { return nil }, now)
		svc.receiveHermes = ack
		if err := svc.Deliver(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		first := svc.sent[msg.QuestionID]
		svc.receiveHermes = nil
		cur = first.at.Add(first.gap + time.Nanosecond)
		if err := svc.Deliver(context.Background(), msg); err == nil {
			t.Fatal("second delivery acked itself")
		}
		second := svc.sent[msg.QuestionID]
		if second.wakeSeq != 2 || second.wakeID == "" || second.wakeID == first.wakeID || second.gap <= first.gap || second.digest == "" {
			t.Fatalf("second identity %+v first %+v", second, first)
		}
		if len(hermesInboundFiles(t, home, sub)) != 2 {
			t.Fatal("second delivery did not open a wake")
		}
		inboxAfter := inboxN
		restart := New(func(context.Context, []string) error { return nil }, now)
		if err := restart.Deliver(context.Background(), msg); err == nil {
			t.Fatal("unacked restart started a clean delivery")
		}
		if inboxN != inboxAfter {
			t.Fatal("unacked restart wrote the inbox again")
		}
		got := restart.sent[msg.QuestionID]
		if !got.at.Equal(second.at) || got.gap != second.gap || got.wakeSeq != second.wakeSeq || got.wakeID != second.wakeID || got.digest != second.digest {
			t.Fatalf("restored %+v want %+v", got, second)
		}
		if len(hermesInboundFiles(t, home, sub)) != 2 {
			t.Fatal("unacked restart opened a new sequence")
		}
		cur = second.at.Add(first.gap + time.Nanosecond)
		if err := New(func(context.Context, []string) error { return nil }, now).Deliver(context.Background(), msg); err == nil {
			t.Fatal("shorter previous gap reopened the reminder")
		}
		if len(hermesInboundFiles(t, home, sub)) != 2 || inboxN != inboxAfter {
			t.Fatal("reminder fired inside the restored gap")
		}
		cur = second.at.Add(second.gap + time.Nanosecond)
		opened := New(func(context.Context, []string) error { return nil }, now)
		opened.receiveHermes = ack
		if err := opened.Deliver(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		if opened.sent[msg.QuestionID].wakeSeq != 3 || opened.sent[msg.QuestionID].wakeID == second.wakeID {
			t.Fatalf("seq after restored gap %+v", opened.sent[msg.QuestionID])
		}
		if len(hermesInboundFiles(t, home, sub)) != 3 {
			t.Fatal("restored gap did not open a new wake")
		}
	})
}

func TestQuestionIdentityStaysDistinct(t *testing.T) {
	longPrefix := strings.Repeat("a", 80)
	pairs := [][2]string{
		{"q/a", "qa"},
		{longPrefix + "xxx", longPrefix + "yyy"},
		{"q/a", "q_a"},
		{"dir/item", "item"},
		{"a..b", "a.b"},
		{"!!!", "@@@"},
	}
	for _, pair := range pairs {
		t.Run(pair[0]+" vs "+pair[1], func(t *testing.T) {
			left := deliverQuestionIdentity(t, pair[0])
			right := deliverQuestionIdentity(t, pair[1])
			again := deliverQuestionIdentity(t, pair[0])
			if left != again {
				t.Fatalf("unstable %q %+v %+v", pair[0], left, again)
			}
			if left.task == right.task || left.wake == right.wake || left.inbox == right.inbox {
				t.Fatalf("collision %+v %+v", left, right)
			}
			checkQuestionIdentity(t, pair[0], left)
			checkQuestionIdentity(t, pair[1], right)
		})
	}
}

type questionIdentity struct {
	task, wake, inbox string
}

func deliverQuestionIdentity(t *testing.T, id string) questionIdentity {
	t.Helper()
	home := t.TempDir()
	cur := time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)
	svc := New(func(context.Context, []string) error { return nil }, func() time.Time { return cur })
	svc.receiveHermes = func(home, subscriptionID string) error {
		return simulateHermesReceive(home, subscriptionID, "profile", "session")
	}
	if err := svc.Deliver(context.Background(), Message{
		QuestionID:     id,
		Objective:      "obj",
		Origin:         "hermes:profile/session",
		Text:           "body",
		Home:           home,
		SubscriptionID: "mgr-r3d",
	}); err != nil {
		t.Fatal(err)
	}
	_, raw := readOnlyInbound(t, hermesInboxDir(home, "mgr-r3d"))
	var inbound hermesInboundFile
	if err := json.Unmarshal(raw, &inbound); err != nil {
		t.Fatal(err)
	}
	_, _, rows := mustParseCompact(t, inbound.Message)
	if len(rows) != 1 || rows[0].wake != rows[0].task+":1" || inbound.WakeEventIDs[0] != rows[0].wake {
		t.Fatalf("wake %+v ids %+v", rows, inbound.WakeEventIDs)
	}
	entries, err := os.ReadDir(filepath.Join(home, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".txt") {
		t.Fatalf("inbox %#v", entries)
	}
	return questionIdentity{
		task:  rows[0].task,
		wake:  rows[0].wake,
		inbox: strings.TrimSuffix(entries[0].Name(), ".txt"),
	}
}

func checkQuestionIdentity(t *testing.T, id string, got questionIdentity) {
	t.Helper()
	assertReceiverToken(t, got.task)
	assertReceiverToken(t, got.inbox)
	if got.wake != got.task+":1" {
		t.Fatalf("wake %q task %q", got.wake, got.task)
	}
	assertReceiverToken(t, strings.TrimSuffix(got.wake, ":1"))
	if strings.Contains(got.wake, "/") || strings.Contains(got.wake, "..") {
		t.Fatalf("wake %q", got.wake)
	}
	for i, r := range got.wake {
		if i == 0 && !receiverAlnum(r) {
			t.Fatalf("wake %q", got.wake)
		}
		if !isCompactRune(r) {
			t.Fatalf("wake rune %q", r)
		}
	}
	sum := sha256.Sum256([]byte(id))
	digest := hex.EncodeToString(sum[:])[:16]
	if got.task != id && !strings.Contains(got.task, digest) {
		t.Fatalf("task %q lost digest of %q", got.task, id)
	}
	if got.inbox != id && !strings.Contains(got.inbox, digest) {
		t.Fatalf("inbox %q lost digest of %q", got.inbox, id)
	}
	if got.task != got.inbox {
		t.Fatalf("hermes %q inbox %q", got.task, got.inbox)
	}
}

func assertReceiverToken(t *testing.T, id string) {
	t.Helper()
	if id == "" || utf8.RuneCountInString(id) > 80 {
		t.Fatalf("len %d %q", utf8.RuneCountInString(id), id)
	}
	if strings.Contains(id, "/") || strings.Contains(id, "..") {
		t.Fatalf("separators %q", id)
	}
	for i, r := range id {
		if i == 0 && !receiverAlnum(r) {
			t.Fatalf("start %q", id)
		}
		if !isCompactRune(r) {
			t.Fatalf("rune %q in %q", r, id)
		}
	}
}

func receiverAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

type compactRow struct {
	task, typ, project, status, reason, transition, wake string
}

func mustParseCompact(t *testing.T, message string) (string, int, []compactRow) {
	t.Helper()
	sub, highWater, rows, ok := parseCompactWake(message)
	if !ok {
		t.Fatalf("not compact %q", message)
	}
	return sub, highWater, rows
}

func parseCompactWake(message string) (sub string, highWater int, rows []compactRow, ok bool) {
	lines := strings.Split(strings.TrimRight(message, "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "cardex-wake v1 ") {
		return "", 0, nil, false
	}
	fields := map[string]string{}
	for _, token := range strings.Fields(lines[0][len("cardex-wake v1 "):]) {
		key, val, found := strings.Cut(token, "=")
		if !found {
			return "", 0, nil, false
		}
		fields[key] = val
	}
	nRaw, hasN := fields["n"]
	hwRaw, hasHW := fields["high_water"]
	sub, hasSub := fields["sub"]
	if !hasN || !hasHW || !hasSub {
		return "", 0, nil, false
	}
	n, errN := strconv.Atoi(nRaw)
	hw, errH := strconv.Atoi(hwRaw)
	if errN != nil || errH != nil || n < 0 || hw < 0 || strings.ContainsAny(nRaw, "+-.") || strings.ContainsAny(hwRaw, "+-.") {
		return "", 0, nil, false
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		row, rowOK := parseWakeRow(line)
		if !rowOK {
			return "", 0, nil, false
		}
		rows = append(rows, row)
	}
	if n != len(rows) || len(rows) == 0 {
		return "", 0, nil, false
	}
	return sub, hw, rows, true
}

func parseWakeRow(line string) (compactRow, bool) {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return compactRow{}, false
	}
	row := compactRow{task: parts[0]}
	got := map[string]bool{}
	for _, token := range parts[1:] {
		key, val, found := strings.Cut(token, "=")
		if !found {
			return compactRow{}, false
		}
		got[key] = true
		switch key {
		case "type":
			row.typ = val
		case "project":
			row.project = val
		case "status":
			row.status = val
		case "reason":
			row.reason = val
		case "transition":
			row.transition = val
		case "wake":
			row.wake = val
		}
	}
	for _, key := range []string{"type", "project", "status", "reason", "transition", "wake"} {
		if !got[key] {
			return compactRow{}, false
		}
	}
	if row.task == "" || row.wake == "" || !strings.HasPrefix(row.wake, row.task+":") {
		return compactRow{}, false
	}
	if strings.Contains(row.wake, "/") || strings.Contains(row.wake, "..") {
		return compactRow{}, false
	}
	return row, true
}

func simulateHermesReceive(home, subscriptionID, profile, sessionID string) error {
	if !validSubscriptionID(subscriptionID) {
		return nil
	}
	dir := hermesInboxDir(home, subscriptionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".ack.json") || strings.HasPrefix(name, ".") {
			continue
		}
		if strings.Contains(name, "/") || strings.Contains(name, "..") {
			continue
		}
		path := filepath.Join(dir, name)
		if filepath.Dir(path) != dir {
			continue
		}
		ack, ok := validateHermesInbound(path, subscriptionID, profile, sessionID)
		if !ok {
			continue
		}
		raw, err := json.MarshalIndent(ack, "", "  ")
		if err != nil {
			return err
		}
		for _, id := range ack.WakeEventIDs {
			if id == "" || strings.Contains(id, "/") || strings.Contains(id, "..") {
				continue
			}
			if err := os.WriteFile(hermesAckPath(home, subscriptionID, id), append(raw, '\n'), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateHermesInbound(path, subscriptionID, profile, sessionID string) (hermesAckFile, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return hermesAckFile{}, false
	}
	var body hermesInboundFile
	if json.Unmarshal(raw, &body) != nil || body.Schema != hermesInboundSchemaV1 {
		return hermesAckFile{}, false
	}
	if body.SubscriptionID == "" || body.SubscriptionID != subscriptionID || body.Provider != "hermes" {
		return hermesAckFile{}, false
	}
	if body.Profile != profile || body.SessionID != sessionID {
		return hermesAckFile{}, false
	}
	if body.SessionType != "hermes" && body.SessionType != "hermes_session" {
		return hermesAckFile{}, false
	}
	sub, _, rows, ok := parseCompactWake(body.Message)
	if !ok || sub != subscriptionID {
		return hermesAckFile{}, false
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		if row.typ == "" || row.project == "" || row.status == "" || row.reason == "" || row.transition == "" || row.wake == "" {
			return hermesAckFile{}, false
		}
		ids[i] = row.wake
	}
	if !reflect.DeepEqual(body.WakeEventIDs, ids) {
		return hermesAckFile{}, false
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".json")
	if stem == "" || strings.Contains(stem, "/") || strings.Contains(stem, "..") || !containsString(ids, stem) {
		return hermesAckFile{}, false
	}
	if body.InboundDigest != inboundDigest(body.Message) || len(body.InboundDigest) != 64 {
		return hermesAckFile{}, false
	}
	return hermesAckFile{
		Schema:        hermesAckSchemaV1,
		WakeEventIDs:  append([]string{}, ids...),
		InboundDigest: body.InboundDigest,
		ReceivedAt:    "2026-10-31T00:00:00Z",
	}, true
}

func rewriteInbound(t *testing.T, home, sub string, mutate func(map[string]any)) {
	t.Helper()
	dir := hermesInboxDir(home, sub)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".ack.json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		mutate(body)
		rewritten, err := json.MarshalIndent(body, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(rewritten, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no inbound to rewrite")
	}
}

func readOnlyInbound(t *testing.T, dir string) (string, []byte) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".ack.json") {
			continue
		}
		if path != "" {
			t.Fatalf("extra inbound %s", entry.Name())
		}
		path = filepath.Join(dir, entry.Name())
	}
	if path == "" {
		t.Fatal("no inbound")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, raw
}

func ackPaths(home string) []string {
	var out []string
	_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(path, ".ack.json") {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
