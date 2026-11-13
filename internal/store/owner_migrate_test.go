package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestLegacyOwnerQuestionMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cardex.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `[{"id":"old","text":"keep going","type":"otto","status":"open","evidence":"note"},{"id":"fact","text":"look","type":"fact","status":"open","asks":2}]`
	other := `[{"id":"only","text":"plain","type":"decision","status":"answered"}]`
	insertNode(t, db, "n1", legacy)
	insertNode(t, db, "n2", other)

	read, err := db.GetNode(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if len(read.OpenQuestions) != 2 || read.OpenQuestions[0].Type != model.OQOwner || read.OpenQuestions[1].Type != model.OQFact || read.OpenQuestions[1].Asks != 2 {
		t.Fatalf("read before rewrite %+v", read.OpenQuestions)
	}
	if raw := nodeJSON(t, db, "n1"); !strings.Contains(raw, `"otto"`) {
		t.Fatalf("read path rewrote storage early: %s", raw)
	}

	if _, err := db.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>=3`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	raw := nodeJSON(t, reopened, "n1")
	if strings.Contains(raw, `"otto"`) {
		t.Fatalf("migration left the legacy token: %s", raw)
	}
	var stored model.OpenQuestionList
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].Type != model.OQOwner || stored[0].Text != "keep going" || stored[0].Evidence != "note" {
		t.Fatalf("rewritten owner %+v", stored)
	}
	if stored[1].Type != model.OQFact || stored[1].Text != "look" || stored[1].Asks != 2 || stored[1].Status != model.OQOpen {
		t.Fatalf("fact changed %+v", stored[1])
	}
	if got := nodeJSON(t, reopened, "n2"); got != other {
		t.Fatalf("other row changed %s", got)
	}
	if n := versionRows(t, reopened, 3); n != 1 {
		t.Fatalf("version 3 rows %d", n)
	}

	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got := nodeJSON(t, again, "n1"); got != raw {
		t.Fatalf("second open changed JSON\n%s\n%s", got, raw)
	}
	if got := nodeJSON(t, again, "n2"); got != other {
		t.Fatalf("second open changed the other row %s", got)
	}
	if n := versionRows(t, again, 3); n != 1 {
		t.Fatalf("second open version 3 rows %d", n)
	}
	if n := versionRows(t, again, 0); n != 4 {
		t.Fatalf("schema rows %d", n)
	}
}

func insertNode(t *testing.T, db *Store, id, questions string) {
	t.Helper()
	_, err := db.db.ExecContext(context.Background(), `INSERT INTO design_nodes(id, objective_id, parent_id, layer, doc_path, open_questions, status) VALUES (?,?,?,?,?,?,?)`,
		id, "o1", "", "module", "docs/design/"+id+".md", questions, "drafting")
	if err != nil {
		t.Fatal(err)
	}
}

func nodeJSON(t *testing.T, db *Store, id string) string {
	t.Helper()
	var raw string
	if err := db.db.QueryRowContext(context.Background(), `SELECT open_questions FROM design_nodes WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func versionRows(t *testing.T, db *Store, version int) int {
	t.Helper()
	q := `SELECT COUNT(*) FROM schema_migrations`
	args := []any{}
	if version > 0 {
		q += ` WHERE version=?`
		args = append(args, version)
	}
	var n int
	if err := db.db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
