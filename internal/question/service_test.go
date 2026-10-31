package question

import (
	"context"
	"errors"
	"testing"

	"cardex/internal/model"
	"cardex/internal/notify"
	"cardex/internal/store"
)

type deliverBox struct {
	msgs []notify.Message
	fail error
}

func (b *deliverBox) Deliver(_ context.Context, msg notify.Message) error {
	b.msgs = append(b.msgs, msg)
	return b.fail
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/cardex.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestCreateKeepsScopeAndKindAndDelivers(t *testing.T) {
	st := openStore(t)
	ctx := t.Context()
	obj := &model.Objective{
		Title: "范围", Mode: model.ModeAuto, FreezeMode: model.FreezeSafe,
		Status: model.ObjectiveActive, Origin: "codex:thread",
	}
	if err := st.CreateObjective(ctx, obj); err != nil {
		t.Fatal(err)
	}
	box := &deliverBox{}
	svc := New(st, box)
	nodeQ, err := svc.Create(ctx, model.Question{
		Objective: obj.ID, Scope: "node-a", ScopeKind: "node",
		Kind: model.QuestionFreeze, Text: "冻结方向？", Options: []string{"同意", "拒绝"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cardQ, err := svc.Create(ctx, model.Question{
		Objective: obj.ID, Scope: "card-a", ScopeKind: "card",
		Kind: model.QuestionCustom, Text: "这张卡？", Options: []string{"是"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if nodeQ.Kind != model.QuestionFreeze || nodeQ.Scope != "node-a" || nodeQ.ScopeKind != "node" {
		t.Fatalf("node question %+v", nodeQ)
	}
	if cardQ.Kind != model.QuestionCustom || cardQ.Scope != "card-a" || cardQ.ScopeKind != "card" {
		t.Fatalf("card question %+v", cardQ)
	}
	if len(box.msgs) != 2 || box.msgs[0].QuestionID != nodeQ.ID || box.msgs[0].Origin != "codex:thread" || box.msgs[0].Text != "冻结方向？" {
		t.Fatalf("deliveries %+v", box.msgs)
	}
	if box.msgs[1].QuestionID != cardQ.ID || box.msgs[1].Objective != obj.ID {
		t.Fatalf("card delivery %+v", box.msgs[1])
	}

	pending, err := svc.Pending(ctx, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != nodeQ.ID {
		t.Fatalf("pending node %+v", pending)
	}
	blocked, err := svc.Blocked(ctx, "node-a")
	if err != nil || !blocked {
		t.Fatalf("node blocked %v %v", blocked, err)
	}
	for _, scope := range []string{"node-b", "card-b", ""} {
		on, err := svc.Blocked(ctx, scope)
		if err != nil || on {
			t.Fatalf("scope %q blocked %v %v", scope, on, err)
		}
	}
	if on, err := svc.Blocked(ctx, "card-a"); err != nil || !on {
		t.Fatalf("card scope %v %v", on, err)
	}

	kept, err := svc.Answer(ctx, nodeQ.ID, "")
	if !errors.Is(err, errEmptyAnswer) {
		t.Fatalf("empty answer err %v", err)
	}
	if kept.Answer != "" {
		t.Fatalf("empty answer stored %q", kept.Answer)
	}
	if on, err := svc.Blocked(ctx, "node-a"); err != nil || !on {
		t.Fatalf("still blocked after empty %v %v", on, err)
	}

	got, err := svc.Answer(ctx, nodeQ.ID, nodeQ.Options[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.Answer != nodeQ.Options[0] {
		t.Fatalf("answer %q", got.Answer)
	}
	if on, err := svc.Blocked(ctx, "node-a"); err != nil || on {
		t.Fatalf("blocked after answer %v %v", on, err)
	}
	pending, err = svc.Pending(ctx, "node-a")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after answer %+v %v", pending, err)
	}
	if on, err := svc.Blocked(ctx, "card-a"); err != nil || !on {
		t.Fatalf("other scope cleared %v %v", on, err)
	}
}
