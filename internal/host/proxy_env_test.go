package host_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/model"
)

// The channel route reaches the executor even for an engine profile that
// replaces the whole environment, and "direct" clears an inherited proxy.
func TestRunAppliesChannelProxy(t *testing.T) {
	for _, tc := range []struct {
		name, route, want string
	}{
		{"default", "", "HTTPS=http://127.0.0.1:6152|NO=127.0.0.1,localhost"},
		{"direct", `,"channels":{"ch1":"direct"}`, "HTTPS=|NO=|"},
		{"override", `,"channels":{"ch1":"http://10.0.0.9:7890"}`, "HTTPS=http://10.0.0.9:7890|"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			stub := filepath.Join(binDir, "claude")
			body := "#!/bin/sh\nprintf 'HTTPS=%s|NO=%s|\\n' \"$https_proxy\" \"$NO_PROXY\"\n"
			if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("https_proxy", "http://inherited:1")
			t.Setenv("NO_PROXY", "inherited")
			t.Setenv("KIMI_API_KEY", "k")

			home, id, _ := seedPrepared(t, t.TempDir(), model.RunnerClaude, "kimi", 1, nil)
			cfg := `{"proxy":{"url":"http://127.0.0.1:6152","no_proxy":["127.0.0.1","localhost"]` + tc.route + `}}`
			if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := host.Run(context.Background(), home, id); err != nil {
				t.Fatal(err)
			}
			logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(logb), tc.want) {
				t.Fatalf("want %q in\n%s", tc.want, logb)
			}
		})
	}
}
