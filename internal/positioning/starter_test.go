package positioning

import (
	"context"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestEnsureStarterFromChannels(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	chans := []model.Channel{
		{ID: "grok", Runner: model.RunnerGrok, Models: []string{"grok-4.7"}},
		{ID: "kimi", Runner: model.RunnerKimi, Models: []string{"kimi-code/k3.1-pro", "grok-4.7"}},
	}
	p, created, ok, err := st.EnsureStarter(ctx, chans)
	if err != nil || !ok || !created {
		t.Fatalf("first EnsureStarter ok=%v created=%v err=%v", ok, created, err)
	}
	if len(p.Entries) != 2 || p.Entries[0].Family != "grok" || p.Entries[1].Family != "kimi" || len(p.Entries[0].Tiers) != 3 {
		t.Fatalf("entries %+v", p.Entries)
	}
	again, created, ok, err := st.EnsureStarter(ctx, chans)
	if err != nil || !ok || created || again.ID != p.ID {
		t.Fatalf("second EnsureStarter id=%s created=%v err=%v", again.ID, created, err)
	}
	if approved, _ := st.Approved(ctx); approved {
		t.Fatal("approved before approve")
	}
	if err := st.Approve(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if approved, _ := st.Approved(ctx); !approved {
		t.Fatal("not approved after approve")
	}
	if pending, _ := st.Pending(); len(pending) != 0 {
		t.Fatalf("pending after approve %+v", pending)
	}
	if _, created, ok, err := st.EnsureStarter(ctx, chans); err != nil || ok || created {
		t.Fatalf("EnsureStarter after approval ok=%v created=%v err=%v", ok, created, err)
	}
}

func TestEnsureStarterNothingToPropose(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, created, ok, err := st.EnsureStarter(context.Background(), nil); err != nil || ok || created {
		t.Fatalf("ok=%v created=%v err=%v", ok, created, err)
	}
}

func TestFamilyOf(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"gpt-6.2-sol", "codex"}: "gpt", {"claude-opus-5", "claude"}: "claude", {"kimi-code/k3.1-pro", "kimi"}: "kimi",
		{"gemini-3-pro", "agy"}: "gemini", {"auto", "codex"}: "gpt", {"composer-2", "cursor"}: "cursor",
	} {
		if got := FamilyOf(in[0], in[1]); got != want {
			t.Fatalf("FamilyOf(%q,%q)=%q want %q", in[0], in[1], got, want)
		}
	}
}
