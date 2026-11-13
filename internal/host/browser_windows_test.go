//go:build windows

package host

import (
	"strings"
	"testing"
)

func TestBrowserEnvWindowsKeepsSystemVars(t *testing.T) {
	got := strings.Join(browserEnv([]string{
		"Path=C:\\Windows", "SystemRoot=C:\\Windows", "LOCALAPPDATA=C:\\x",
		"CARDEX_SECRET_FOR_TEST=1", "OPENAI_API_KEY=k",
	}), "\n")
	for _, want := range []string{"Path=", "SystemRoot=", "LOCALAPPDATA="} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %q", want, got)
		}
	}
	if strings.Contains(got, "SECRET") || strings.Contains(got, "API_KEY") {
		t.Fatalf("secret leaked: %q", got)
	}
}
