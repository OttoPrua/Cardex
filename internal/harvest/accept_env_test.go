package harvest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestHarvestAcceptanceDropsServeEnv(t *testing.T) {
	dir, head := newRepo(t)
	t.Setenv("CARDEX_SCHED_HOST", "1")
	t.Setenv("CARDEX_SCHED_ATTEMPT", "att-probe")
	t.Setenv("CURSOR_CONFIG_DIR", t.TempDir())
	t.Setenv("KIMI_CODE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("OPENCODE_DB", filepath.Join(t.TempDir(), "opencode.db"))
	t.Setenv("GROK_LOG_FILE", filepath.Join(t.TempDir(), "grok.log"))
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	t.Setenv("GIT_EXEC_PATH", t.TempDir())
	t.Setenv("GIT_TEMPLATE_DIR", t.TempDir())
	res, err := Harvest(context.Background(), harvestInput(dir, head, model.Card{
		ID: "c", Kind: model.KindDev,
		Acceptance: []string{serveEnvProbe},
	}, 0, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome == model.OutcomeTestsFailed {
		t.Fatalf("acceptance saw serve environment: %s", res.Evidence)
	}
}

const serveEnvProbe = `
for key in CARDEX_SCHED_HOST CARDEX_SCHED_ATTEMPT CURSOR_CONFIG_DIR KIMI_CODE_HOME \
	CLAUDE_CONFIG_DIR OPENCODE_DB GROK_LOG_FILE GEMINI_CLI_HOME GIT_EXEC_PATH GIT_TEMPLATE_DIR
do
	eval "v=\${$key-}"
	if [ -n "$v" ]; then
		echo "leaked $key" >&2
		exit 1
	fi
done
test -n "$PATH" && test -n "$HOME"
`
