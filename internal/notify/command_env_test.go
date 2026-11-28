package notify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/store"
)

func TestNotifySubprocessScrubsCompatibleCredentialEnv(t *testing.T) {
	home := t.TempDir()
	config := `{"engines":{
	  "gateway":{"protocol":"anthropic-compatible","base_url":"https://gateway.example.test","credential":{"source":"env","name":"FIXTURE_PROVIDER_ONLY"}},
	  "keychain":{"protocol":"anthropic-compatible","base_url":"https://keychain.example.test","credential":{"source":"keychain","name":"FIXTURE_NOTIFY_LEGACY"}}
	}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARDEX_HOME", t.TempDir()) // not the service home of any fixture
	t.Setenv("CARDEX_NOTIFY_ENV_FIXTURE", "1")
	t.Setenv("fixture_provider_only", "synthetic-configured")
	t.Setenv("CARDEX_API_NOTIFY_ORPHAN", "synthetic-reserved")
	t.Setenv("FIXTURE_NOTIFY_LEGACY", "synthetic-ordinary")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executed := 0
	localOnly := func(ctx context.Context, argv []string) error {
		t.Helper()
		if len(argv) == 0 || (argv[0] != "codex" && argv[0] != "lark-cli") {
			t.Fatal("unexpected test adapter")
		}
		executed++
		// Run the real process launcher against this local test binary only.
		return CommandExec(ctx, []string{bin, "-test.run=^TestNotifyCompatibleEnvFixtureChild$"})
	}
	ctx := t.Context()
	for _, origin := range []string{"codex:fixture-thread", "lark:oc_fixture"} {
		if err := New(localOnly, nil).Deliver(ctx, Message{
			QuestionID: "fixture-" + origin, Objective: "fixture", Origin: origin,
			Text: "fixture notice", Home: home,
		}); err != nil {
			t.Fatalf("synthetic delivery %q: %v", origin, err)
		}
	}
	if err := TestDeliver(ctx, localOnly, home, Receiver{
		ID: "fixture-receiver", Adapter: "codex", Target: "fixture-thread",
	}); err != nil {
		t.Fatalf("synthetic notify test: %v", err)
	}
	old := lifecycleExec
	lifecycleExec = localOnly
	t.Cleanup(func() { lifecycleExec = old })
	if err := deliverOriginEvent(ctx, home, "fixture-lifecycle", store.LifecycleDelivery{
		Objective: "fixture", Origin: "codex:fixture-thread", Payload: "fixture lifecycle",
	}); err != nil {
		t.Fatalf("synthetic lifecycle: %v", err)
	}
	if executed != 4 {
		t.Fatalf("checked %d local child launches; wanted 4", executed)
	}
}

func TestNotifyCompatibleEnvFixtureChild(t *testing.T) {
	if os.Getenv("CARDEX_NOTIFY_ENV_FIXTURE") != "1" {
		return
	}
	for _, key := range []string{"fixture_provider_only", "CARDEX_API_NOTIFY_ORPHAN"} {
		if _, exists := os.LookupEnv(key); exists {
			t.Fatal("compatible API credential reached a notification child")
		}
	}
	if os.Getenv("FIXTURE_NOTIFY_LEGACY") != "synthetic-ordinary" {
		t.Fatal("unrelated notification environment did not survive")
	}
}
