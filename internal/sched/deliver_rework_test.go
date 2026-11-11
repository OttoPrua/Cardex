package sched

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/notify"
	"github.com/OttoPrua/Cardex/internal/question"
	"github.com/OttoPrua/Cardex/internal/store"
)

const channelAccountLeak = "channel-account-SHOULD-NOT-LEAK"

func TestDeliveryFieldsSameAcrossPaths(t *testing.T) {
	ctx := context.Background()
	const origin = "file:box"
	const fallback = "codex:thread-9"
	home, db, objID, r := seedDeliveryHome(t, origin, "{\"notify\":{\"fallback\":\"codex:thread-9\"}}\n", true)
	logPath := filepath.Join(t.TempDir(), "codex.log")
	plantCodex(t, logPath)
	real := r.mail
	got := map[string]notify.Message{}
	r.mail = captureDeliverer{inner: real, got: got}

	paths := []struct {
		name string
		run  func(t *testing.T) notify.Message
	}{
		{
			name: "design",
			run: func(t *testing.T) notify.Message {
				t.Helper()
				if err := r.productionEngine(ctx, objID); err != nil {
					t.Fatal(err)
				}
				if n := eventCount(t, db, "objective:"+objID, "design_error"); n != 0 {
					t.Fatalf("design_error events %d", n)
				}
				qs, err := db.ListQuestions(ctx, objID)
				if err != nil || len(qs) != 1 {
					t.Fatalf("questions %+v %v", qs, err)
				}
				msg, ok := got[qs[0].ID]
				if !ok {
					t.Fatalf("design delivery missing %+v", got)
				}
				return msg
			},
		},
		{
			name: "question",
			run: func(t *testing.T) notify.Message {
				t.Helper()
				q, err := question.New(db, r.designDeliverer()).Create(ctx, model.Question{
					ID: "q-direct", Objective: objID, Scope: "dir", ScopeKind: "node",
					Kind: model.QuestionCustom, Text: "direct",
				})
				if err != nil {
					t.Fatal(err)
				}
				msg, ok := got[q.ID]
				if !ok {
					t.Fatal("question delivery missing")
				}
				return msg
			},
		},
		{
			name: "scheduler",
			run: func(t *testing.T) notify.Message {
				t.Helper()
				q := model.Question{
					ID: "q-sched", Objective: objID, Scope: "dir", ScopeKind: "node",
					Kind: model.QuestionCustom, Text: "sched",
				}
				if err := db.CreateQuestion(ctx, &q); err != nil {
					t.Fatal(err)
				}
				if err := r.notify(ctx, db); err != nil {
					t.Fatal(err)
				}
				msg, ok := got["q-sched"]
				if !ok {
					t.Fatalf("scheduler delivery missing %+v", got)
				}
				return msg
			},
		},
	}
	var first notify.Message
	for i, tc := range paths {
		i, tc := i, tc
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.run(t)
			assertDeliveryFields(t, msg, home, origin, fallback)
			if i == 0 {
				first = msg
				return
			}
			if msg.Home != first.Home || msg.Origin != first.Origin || msg.Fallback != first.Fallback || msg.SubscriptionID != first.SubscriptionID {
				t.Fatalf("path %s home=%q origin=%q fallback=%q sub=%q, design home=%q origin=%q fallback=%q sub=%q",
					tc.name, msg.Home, msg.Origin, msg.Fallback, msg.SubscriptionID,
					first.Home, first.Origin, first.Fallback, first.SubscriptionID)
			}
		})
	}
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logBody)
	if !strings.Contains(text, "queue") || !strings.Contains(text, "--thread") || !strings.Contains(text, "thread-9") {
		t.Fatalf("codex argv %s", text)
	}
	if strings.Contains(text, channelAccountLeak) {
		t.Fatalf("codex saw channel account %s", text)
	}
	assertTreeLacks(t, filepath.Join(home, "inbox"), channelAccountLeak)
	assertNoHermesAccount(t, home, channelAccountLeak)
}

func TestCardOriginPreferredOverTarget(t *testing.T) {
	ctx := context.Background()
	const cardOrigin = "file:box"
	const target = "hermes:other/sess"
	cases := []struct {
		name string
		run  func(t *testing.T, db *store.Store, r *Runner, objID string, got map[string]notify.Message) notify.Message
	}{
		{
			name: "question",
			run: func(t *testing.T, db *store.Store, r *Runner, objID string, got map[string]notify.Message) notify.Message {
				t.Helper()
				q, err := question.New(db, r.designDeliverer()).Create(ctx, model.Question{
					ID: "q-card", Objective: objID, Scope: "c", ScopeKind: "card",
					Kind: model.QuestionCustom, Text: "card question",
				})
				if err != nil {
					t.Fatal(err)
				}
				msg, ok := got[q.ID]
				if !ok {
					t.Fatal("question delivery missing")
				}
				return msg
			},
		},
		{
			name: "scheduler",
			run: func(t *testing.T, db *store.Store, r *Runner, objID string, got map[string]notify.Message) notify.Message {
				t.Helper()
				q := model.Question{
					ID: "q-card", Objective: objID, Scope: "c", ScopeKind: "card",
					Kind: model.QuestionCustom, Text: "card question",
				}
				if err := db.CreateQuestion(ctx, &q); err != nil {
					t.Fatal(err)
				}
				if err := r.notify(ctx, db); err != nil {
					t.Fatal(err)
				}
				msg, ok := got["q-card"]
				if !ok {
					t.Fatal("scheduler delivery missing")
				}
				return msg
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			home, db, objID, r := seedDeliveryHome(t, target, "{\"schedules\":[]}\n", false)
			if err := db.CreateCard(ctx, &model.Card{
				ID: "c", Objective: objID, Kind: model.KindDev, Title: "c",
				Tier: model.TierT2, Status: model.CardDone, Channel: "ch",
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.AppendEvent(ctx, &model.Event{
				Entity: "card:c", Type: "origin", Data: []byte(`{"origin":"file:box"}`),
			}); err != nil {
				t.Fatal(err)
			}
			got := map[string]notify.Message{}
			r.mail = captureDeliverer{inner: r.mail, got: got}
			msg := tc.run(t, db, r, objID, got)
			assertDeliveryFields(t, msg, home, cardOrigin, "")
			if msg.Fallback == target || msg.Origin == target {
				t.Fatalf("target origin used as delivery field %+v", msg)
			}
			body := inboxText(t, home)
			if !strings.Contains(body, cardOrigin) || strings.Contains(body, target) || strings.Contains(body, channelAccountLeak) {
				t.Fatalf("inbox %s", body)
			}
			assertNoHermesAccount(t, home, channelAccountLeak)
			if n := eventCount(t, db, "config:notify", "notify_warning"); n != 0 {
				t.Fatalf("notify_warning events %d", n)
			}
		})
	}
}

func TestHermesSkipAndFallback(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		config   string
		wantHop  bool
		wantWarn bool
	}{
		{name: "configured", config: "{\"notify\":{\"fallback\":\"codex:thread-9\"}}\n", wantHop: true},
		{name: "absent", config: "{\"schedules\":[]}\n"},
		{name: "invalid", config: "{\"notify\":{\"fallback\":\"nope\"}}\n", wantWarn: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			home, db, objID, r := seedDeliveryHome(t, "hermes:prof/sess", tc.config, true)
			logPath := filepath.Join(t.TempDir(), "codex.log")
			plantCodex(t, logPath)
			if err := r.productionEngine(ctx, objID); err != nil {
				t.Fatal(err)
			}
			qs, err := db.ListQuestions(ctx, objID)
			if err != nil || len(qs) != 1 {
				t.Fatalf("questions %+v %v", qs, err)
			}
			q := qs[0]
			if n := eventCount(t, db, "objective:"+objID, "design_error"); n != 0 {
				t.Fatalf("design_error events %d", n)
			}
			if n := eventCount(t, db, "question:"+q.ID, "delivery_error"); n != 0 {
				t.Fatalf("delivery_error events %d", n)
			}
			skip := eventText(t, db, "question:"+q.ID, "delivery_skip")
			if !strings.Contains(skip, "缺订阅 id") {
				t.Fatalf("skip events %s", skip)
			}
			body := inboxText(t, home)
			if !strings.Contains(body, q.ID) || !strings.Contains(body, "hermes:prof/sess") || strings.Contains(body, channelAccountLeak) {
				t.Fatalf("inbox %s", body)
			}
			assertNoHermesAccount(t, home, channelAccountLeak)
			logBody, readErr := os.ReadFile(logPath)
			if tc.wantHop {
				if readErr != nil {
					t.Fatal(readErr)
				}
				text := string(logBody)
				if !strings.Contains(text, "queue") || !strings.Contains(text, "--thread") || !strings.Contains(text, "thread-9") {
					t.Fatalf("codex argv %s", text)
				}
				if strings.Contains(text, channelAccountLeak) {
					t.Fatalf("codex saw channel account %s", text)
				}
			} else if readErr == nil && len(strings.TrimSpace(string(logBody))) != 0 {
				t.Fatalf("unexpected codex argv %s", logBody)
			}
			warns := eventCount(t, db, "config:notify", "notify_warning")
			if tc.wantWarn && warns != 1 {
				t.Fatalf("notify_warning events %d", warns)
			}
			if !tc.wantWarn && warns != 0 {
				t.Fatalf("notify_warning events %d", warns)
			}
		})
	}
}

type captureDeliverer struct {
	inner notify.Deliverer
	got   map[string]notify.Message
}

func (c captureDeliverer) Deliver(ctx context.Context, msg notify.Message) error {
	if c.got != nil {
		c.got[msg.QuestionID] = msg
	}
	if c.inner == nil {
		return nil
	}
	return c.inner.Deliver(ctx, msg)
}

func seedDeliveryHome(t *testing.T, origin, config string, freezable bool) (string, *store.Store, string, *Runner) {
	t.Helper()
	ctx := context.Background()
	t.Setenv("GROK_HOME", t.TempDir())
	home, db, repo := newHome(t)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	_, objID := seedRepo(t, db, repo, "true")
	if err := db.CreateChannel(ctx, &model.Channel{
		ID: "ch", Runner: model.RunnerFake, Family: "fam", SourceGroup: channelAccountLeak,
		Health: model.HealthOK, Models: []string{"m"},
	}); err != nil {
		t.Fatal(err)
	}
	obj, err := db.GetObjective(ctx, objID)
	if err != nil {
		t.Fatal(err)
	}
	obj.Origin = origin
	obj.Channel = "ch"
	obj.Mode = model.ModeAuto
	if err := db.UpdateObjective(ctx, &obj); err != nil {
		t.Fatal(err)
	}
	if freezable {
		node := model.DesignNode{
			ID: "dir", Objective: objID, Layer: model.LayerDirection,
			Status: model.NodeFreezable, OpenQuestions: model.OpenQuestionList{},
		}
		if err := db.CreateNode(ctx, &node); err != nil {
			t.Fatal(err)
		}
	}
	return home, db, objID, New(home)
}

func plantCodex(t *testing.T, logPath string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$CARDEX_CODEX_LOG\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARDEX_CODEX_LOG", logPath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GROK_HOME", t.TempDir())
}

func assertDeliveryFields(t *testing.T, msg notify.Message, home, origin, fallback string) {
	t.Helper()
	if msg.Home != home || msg.Origin != origin || msg.Fallback != fallback || msg.SubscriptionID != "" {
		t.Fatalf("home=%q origin=%q fallback=%q sub=%q", msg.Home, msg.Origin, msg.Fallback, msg.SubscriptionID)
	}
	blob := strings.Join([]string{msg.Home, msg.Origin, msg.Fallback, msg.SubscriptionID, msg.Text}, "\n")
	if strings.Contains(blob, channelAccountLeak) {
		t.Fatalf("channel account in message %s", blob)
	}
}

func eventCount(t *testing.T, db *store.Store, entity, typ string) int {
	t.Helper()
	events, err := db.ListEvents(context.Background(), entity)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, event := range events {
		if event.Type == typ {
			n++
		}
	}
	return n
}

func eventText(t *testing.T, db *store.Store, entity, typ string) string {
	t.Helper()
	events, err := db.ListEvents(context.Background(), entity)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, event := range events {
		if event.Type == typ {
			b.Write(event.Data)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func inboxText(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, "inbox")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(body)
	}
	return b.String()
}

func assertTreeLacks(t *testing.T, root, needle string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(body), needle) {
			t.Fatalf("%s contains %s", path, needle)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertNoHermesAccount(t *testing.T, home, needle string) {
	t.Helper()
	root := filepath.Join(home, "control", "manager-wake", "hermes-inbox")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("hermes inbox %+v", entries)
	}
	assertTreeLacks(t, root, needle)
}
