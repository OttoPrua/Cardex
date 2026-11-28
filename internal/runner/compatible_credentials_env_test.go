package runner

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/OttoPrua/Cardex/internal/proc"
)

func TestAPIChannelCredentialEnvHonorsSourceAndScope(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "config.json")
	if err := os.WriteFile(file, []byte(`{"engines":{
		"valid-keychain":{"protocol":"anthropic-compatible","credential":{"source":"keychain","name":"KIMI_API_KEY"}},
		"valid-env":{"protocol":"anthropic-compatible","credential":{"source":"env","name":"FIXTURE_KEY"}},
		"ambiguous-env":{"protocol":"anthropic-compatible","credential":{"source":"keychain","name":"ALIAS_A","Source":"env","Name":"ALIAS_B"}},
		"legacy":{"base_url":"https://legacy.example.test","auth_env":"LEGACY_ENGINE"}
	}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=/fixture-bin", "fixture_key=configured", "CARDEX_API_ORPHAN=reserved", "alias_a=duplicate", "ALIAS_B=duplicate",
		"KIMI_API_KEY=unrelated", "LEGACY_ENGINE=unrelated",
	}
	want := []string{"PATH=/fixture-bin", "KIMI_API_KEY=unrelated", "LEGACY_ENGINE=unrelated"}
	ctx := WithCompatibleCredentialEnv(t.Context(), home)
	if out := proc.CompatibleChildEnv(ctx, env); !slices.Equal(out, want) {
		t.Fatal("credential source metadata did not preserve unrelated legacy auth")
	}
	// A rotation cannot erase references from an operation that has already
	// seen them. Do not infer unknown aliases against a different home.
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := proc.CompatibleChildEnv(WithCompatibleCredentialEnv(ctx, home), env); !slices.Equal(out, want) {
		t.Fatal("a config reload dropped the in-flight deny set")
	}
}
