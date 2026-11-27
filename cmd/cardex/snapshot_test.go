package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

func snapshotMain(args ...string) (int, string, string) {
	var out, errBuf bytes.Buffer
	status := Main(args, &out, &errBuf)
	return status, out.String(), errBuf.String()
}

func TestSnapshotCompactAllowlistAtCLI(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	card := model.Card{ID: "sample", Kind: model.KindDev, Status: model.CardDone,
		Title: "SYNTHETIC_TITLE", Body: "SYNTHETIC_BODY " + strings.Repeat("x", 8000)}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	attempt := model.Attempt{ID: "a-sample", Index: 1, Card: card.ID,
		StartedAt: base, EndedAt: base.Add(time.Second), Outcome: model.OutcomePassed,
		Route:        model.Route{Channel: "channel-a", Model: "model-a"},
		ErrorExcerpt: "SYNTHETIC_EXCERPT", PromptPath: "SYNTHETIC_PROMPT"}
	if err := db.CreateAttempt(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		event := model.Event{Entity: "card:" + card.ID, Type: "review_verdict", ID: fmt.Sprintf("event-%02d", i),
			TS: base.Add(time.Duration(i) * 100 * time.Millisecond), Data: json.RawMessage(`{"marker":"SYNTHETIC_EVENT_PAYLOAD"}`)}
		if err := db.AppendEvent(ctx, &event); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AppendEvent(ctx, &model.Event{Entity: "card:" + card.ID, Type: "SYNTHETIC_NON_ALLOWLIST",
		TS: base.Add(time.Hour), Data: json.RawMessage(`{"extra":"SYNTHETIC_REDACT"}`)}); err != nil {
		t.Fatal(err)
	}

	// Inject a malformed proxy; successful snapshot never uses that config.
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"proxy":{"http":"SYNTHETIC_PROXY"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	code, out, errText := snapshotMain("--home", home, "snapshot", "--json", "--checkpointed-copy", "--card", card.ID)
	if code != 0 || errText != "" || strings.Contains(out, "SYNTHETIC") || len(out) > 1400 {
		t.Fatalf("unexpected snapshot CLI result: %d %q %q", code, out, errText)
	}
	var doc struct {
		Version  string `json:"version"`
		Revision string `json:"revision"`
		Serve    string `json:"serve"`
		store.Snapshot
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Card == nil || doc.Card.ID != "sample" || doc.Card.Kind != model.KindDev || doc.Card.Status != model.CardDone ||
		doc.Attempt == nil || doc.Attempt.ID != "a-sample" || doc.Attempt.Status != "passed" ||
		doc.Route != "channel-a/model-a" || doc.Schema <= 0 || len(doc.Events) != 8 || doc.Serve != "unknown" ||
		doc.Version == "" || doc.Revision == "" {
		t.Fatalf("unexpected snapshot: %+v", doc)
	}
	for i, e := range doc.Events {
		if e.Type != "review_verdict" || !e.TS.Equal(base.Add(time.Duration(11-i)*100*time.Millisecond)) {
			t.Fatalf("wrong bounded event order: %v", doc.Events)
		}
	}
}

func TestSnapshotHelpInvalidAndMissingHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-created")
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"snapshot", "--help"}, 0},
		{[]string{"snapshot"}, 2},
		{[]string{"snapshot", "--json", "--card="}, 2},
		{[]string{"snapshot", "--json", "--secret-marker"}, 2},
		{[]string{"snapshot", "--json"}, 1},
	} {
		code, out, stderr := snapshotMain(append([]string{"--home", home}, test.args...)...)
		if code != test.code || strings.Contains(out+stderr, "secret-marker") || (code != 0 && out != "") {
			t.Fatalf("%v: %d %q %q", test.args, code, out, stderr)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatalf("%v created a home: %v", test.args, err)
		}
	}
}

func TestSnapshotCheckpointedCopyRequiresExplicitHome(t *testing.T) {
	code, out, stderr := snapshotMain("snapshot", "--json", "--checkpointed-copy")
	if code != 2 || out != "" || !strings.Contains(stderr, "--home") {
		t.Fatalf("copy mode accepted default original home: %d %q %q", code, out, stderr)
	}
}
