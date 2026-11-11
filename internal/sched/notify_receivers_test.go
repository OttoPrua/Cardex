package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/notify"
)

func TestHermesProfileAmbiguousSkipsAndInboxes(t *testing.T) {
	ctx := context.Background()
	cfg := receiverConfig(t, "",
		notify.Receiver{ID: "one", Adapter: "hermes", Target: "profile-a", Enabled: true},
		notify.Receiver{ID: "two", Adapter: "hermes", Target: "profile-a", Enabled: true},
	)
	home, db, objID, r := seedDeliveryHome(t, "hermes:profile-a/example-manager", cfg, true)
	if err := r.productionEngine(ctx, objID); err != nil {
		t.Fatal(err)
	}
	qs, err := db.ListQuestions(ctx, objID)
	if err != nil || len(qs) != 1 || qs[0].Answer != "" {
		t.Fatalf("questions %+v %v", qs, err)
	}
	skip := eventText(t, db, "question:"+qs[0].ID, "delivery_skip")
	if !strings.Contains(skip, "不唯一") {
		t.Fatalf("skip %s", skip)
	}
	if n := eventCount(t, db, "question:"+qs[0].ID, "delivery_error"); n != 0 {
		t.Fatalf("delivery_error %d", n)
	}
	body := inboxText(t, home)
	if !strings.Contains(body, qs[0].ID) {
		t.Fatalf("inbox %s", body)
	}
	if _, err := os.Stat(filepath.Join(home, "control", "manager-wake")); !os.IsNotExist(err) {
		t.Fatalf("hermes inbox %v", err)
	}
}

func TestHermesSessionMatchWritesWakeUnderRoot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := receiverConfig(t, "", notify.Receiver{
		ID: "example-manager", Adapter: "hermes", Target: "profile-a/example-manager",
		Root: root, Enabled: true,
	})
	home, db, objID, r := seedDeliveryHome(t, "hermes:profile-a/example-manager", cfg, true)
	if err := r.productionEngine(ctx, objID); err != nil {
		t.Fatal(err)
	}
	qs, err := db.ListQuestions(ctx, objID)
	if err != nil || len(qs) != 1 {
		t.Fatalf("questions %+v %v", qs, err)
	}
	skip := eventText(t, db, "question:"+qs[0].ID, "delivery_skip")
	if strings.Contains(skip, "缺订阅 id") || strings.Contains(skip, "不唯一") {
		t.Fatalf("skip %s", skip)
	}
	dir := filepath.Join(root, "control", "manager-wake", "hermes-inbox", "example-manager")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	wake := ""
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".ack.json") {
			wake = filepath.Join(dir, entry.Name())
		}
	}
	if wake == "" {
		t.Fatalf("no wake under %s", dir)
	}
	if _, err := os.Stat(filepath.Join(home, "control", "manager-wake")); !os.IsNotExist(err) {
		t.Fatalf("wake written in home %v", err)
	}
	body := inboxText(t, home)
	if !strings.Contains(body, qs[0].ID) {
		t.Fatalf("inbox %s", body)
	}
}

func receiverConfig(t *testing.T, fallback string, receivers ...notify.Receiver) string {
	t.Helper()
	type notifyBody struct {
		Fallback  string            `json:"fallback,omitempty"`
		Receivers []notify.Receiver `json:"receivers"`
	}
	doc := map[string]any{
		"schedules": []any{},
		"notify": notifyBody{
			Fallback:  fallback,
			Receivers: receivers,
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
