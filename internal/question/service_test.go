package question

import (
	"context"
	"encoding/json"
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

func TestCreateReusesStoredQuestionAndCardOrigin(t *testing.T) {
	ctx := t.Context()
	for _, kind := range []model.QuestionKind{model.QuestionReopen, model.QuestionFreeze} {
		t.Run(string(kind), func(t *testing.T) {
			st := openStore(t)
			obj := &model.Objective{
				Title: "重试", Mode: model.ModeAuto, FreezeMode: model.FreezeSafe,
				Status: model.ObjectiveActive, Origin: "codex:goal",
			}
			if err := st.CreateObjective(ctx, obj); err != nil {
				t.Fatal(err)
			}
			box := &deliverBox{fail: errors.New("notify down")}
			svc := New(st, box)
			q := model.Question{
				Objective: obj.ID, Scope: "node-1", ScopeKind: "node",
				Kind: kind, Text: "接口不一致", Options: []string{"同意", "拒绝"},
			}
			first, err := svc.Create(ctx, q)
			if err == nil || first.ID == "" {
				t.Fatalf("stored %s err %v", first.ID, err)
			}
			again, err := svc.Create(ctx, q)
			if err == nil {
				t.Fatal("delivery still down")
			}
			if again.ID != first.ID {
				t.Fatalf("second row %s %s", first.ID, again.ID)
			}
			rows, err := st.ListQuestions(ctx, obj.ID)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows %+v %v", rows, err)
			}
			if len(box.msgs) != 2 || box.msgs[0].QuestionID != first.ID || box.msgs[1].QuestionID != first.ID {
				t.Fatalf("deliveries %+v", box.msgs)
			}
			box.fail = nil
			got, err := svc.Create(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != first.ID {
				t.Fatalf("id %s want %s", got.ID, first.ID)
			}
			rows, err = st.ListQuestions(ctx, obj.ID)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows after success %+v %v", rows, err)
			}
			if len(box.msgs) != 3 || box.msgs[2].Origin != "codex:goal" {
				t.Fatalf("success delivery %+v", box.msgs)
			}
			later, err := svc.Create(ctx, q)
			if err != nil || later.ID != first.ID {
				t.Fatalf("later %+v %v", later, err)
			}
			rows, err = st.ListQuestions(ctx, obj.ID)
			if err != nil || len(rows) != 1 || len(box.msgs) != 3 {
				t.Fatalf("duplicate rows %d msgs %d err %v", len(rows), len(box.msgs), err)
			}
			other := q
			other.Text = "另一处设计问题"
			second, err := svc.Create(ctx, other)
			if err != nil || second.ID == first.ID {
				t.Fatalf("different text %+v %v", second, err)
			}
			rows, err = st.ListQuestions(ctx, obj.ID)
			if err != nil || len(rows) != 2 {
				t.Fatalf("rows %+v %v", rows, err)
			}
		})
	}

	originCases := []struct {
		name       string
		scopeKind  string
		cardOrigin string
		want       string
	}{
		{name: "card-origin", scopeKind: "card", cardOrigin: "hermes:card", want: "hermes:card"},
		{name: "card-without-origin", scopeKind: "card", want: "codex:goal"},
		{name: "node-ignores-card-origin", scopeKind: "node", cardOrigin: "hermes:card", want: "codex:goal"},
	}
	for _, tc := range originCases {
		t.Run(tc.name, func(t *testing.T) {
			st := openStore(t)
			obj := &model.Objective{
				Title: "来源", Mode: model.ModeManual, FreezeMode: model.FreezeSafe,
				Status: model.ObjectiveActive, Origin: "codex:goal",
			}
			if err := st.CreateObjective(ctx, obj); err != nil {
				t.Fatal(err)
			}
			card := &model.Card{
				Objective: obj.ID, Kind: model.KindDev, Title: "卡", Body: "任务\n",
				Tier: model.TierT1, Status: model.CardRunning,
			}
			if err := st.CreateCard(ctx, card); err != nil {
				t.Fatal(err)
			}
			if tc.cardOrigin != "" {
				raw, err := json.Marshal(map[string]string{"origin": tc.cardOrigin})
				if err != nil {
					t.Fatal(err)
				}
				if err := st.AppendEvent(ctx, &model.Event{Entity: "card:" + card.ID, Type: "origin", Data: raw}); err != nil {
					t.Fatal(err)
				}
			}
			box := &deliverBox{}
			got, err := New(st, box).Create(ctx, model.Question{
				Objective: obj.ID, Scope: card.ID, ScopeKind: tc.scopeKind,
				Kind: model.QuestionCustom, Text: "这张卡怎么处理", Options: []string{"是"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(box.msgs) != 1 || box.msgs[0].QuestionID != got.ID || box.msgs[0].Origin != tc.want {
				t.Fatalf("origin %q deliveries %+v", tc.want, box.msgs)
			}
		})
	}
}
